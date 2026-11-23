#include "../xhttpd.h"
#include <assert.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#ifndef _WIN32
#include <arpa/inet.h>
#include <fcntl.h>
#include <poll.h>
#include <netinet/in.h>
#include <pthread.h>
#include <signal.h>
#include <sys/socket.h>
#include <unistd.h>
#endif

static int g_pass = 0;
static int g_fail = 0;

static void check(const char *name, int cond) {
    if (cond) {
        g_pass++;
        printf("PASS: %s\n", name);
    } else {
        g_fail++;
        printf("FAIL: %s\n", name);
    }
}

static int feed_http_req(const char *raw, size_t len, http_req_t *req, conn_reader_t *cr_io) {
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) return -99;
    ssize_t w = send(sv[1], raw, len, MSG_NOSIGNAL);
    (void)w;
    shutdown(sv[1], SHUT_WR);
    conn_reader_t local_cr;
    conn_reader_t *cr = cr_io ? cr_io : &local_cr;
    if (!cr_io) {
        memset(cr, 0, sizeof(*cr));
        cr->fd = sv[0];
    }
    int rc = read_http_request(cr, req);
    close(sv[0]);
    close(sv[1]);
    return rc;
}

static const fp_cred_t *find_cred(const xhttpd_config_t *cfg, const char *client_id) {
    for (int i = 0; i < cfg->num_fp_creds; i++) {
        if (strcmp(cfg->fp_creds[i].client_id, client_id) == 0) {
            return &cfg->fp_creds[i];
        }
    }
    return NULL;
}

static void make_test_v2_secret(int idx, uint8_t out[32]) {
    for (int i = 0; i < 32; i++) {
        out[i] = (uint8_t)(0xA5U ^ (uint8_t)((idx + 1) * 29 + i * 7));
    }
}

static void add_cfg_client(xhttpd_config_t *cfg, int idx, const char *uuid_str) {
    vless_client_t *c = &cfg->clients[idx];
    parse_uuid_str(uuid_str, c->uuid);
    format_uuid_canonical(c->uuid, c->uuid_str);
    bytes_to_hex(c->uuid, 16, c->hex_no_dash);
    uint8_t sec[32];
    make_test_v2_secret(idx, sec);
    (void)add_explicit_fp_cred(cfg, c->uuid_str, sec);
}

static void test_a1_a9_credentials(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.num_clients = 1;
    parse_uuid_str("b831381d-6324-4d53-ad4f-8cda48b30811", cfg.clients[0].uuid);

    unsetenv("FP_ENABLE_DIAG_CRED");
    unsetenv("FP_SECRET_HEX");
    check("A1: rebuild_fp_credentials succeeds with 1 VLESS client and 0 explicit FP creds",
          rebuild_fp_credentials(&cfg) == 0);
    check("A1: VLESS UUID alone does NOT auto-derive any FP credentials",
          cfg.num_fp_creds == 0 &&
          find_cred(&cfg, "b831381d-6324-4d53-ad4f-8cda48b30811") == NULL &&
          find_cred(&cfg, "b831381d63244d53ad4f8cda48b30811") == NULL);

    /* Verify all UUID-derived 32-byte key variants are rejected by add_explicit_fp_cred */
    uint8_t u_v1[32], u_sha[32];
    const char *canon = "b831381d-6324-4d53-ad4f-8cda48b30811";
    char v1_pre[64];
    int v1_len = snprintf(v1_pre, sizeof(v1_pre), "awgm-fp-v1:%s", canon);
    sha256_hash((const uint8_t *)v1_pre, (size_t)v1_len, u_v1);
    sha256_hash(cfg.clients[0].uuid, 16, u_sha);
    check("A1: rejects SHA256(awgm-fp-v1:<uuid>) secret",
          add_explicit_fp_cred(&cfg, "fp_0102030405060708090a0b0c0d0e0f10", u_v1) < 0);
    check("A1: rejects SHA256(uuid_bytes) secret",
          add_explicit_fp_cred(&cfg, "fp_0102030405060708090a0b0c0d0e0f10", u_sha) < 0);
    check("A1: rejects 32-byte hex_no_dash UUID secret",
          add_explicit_fp_cred(&cfg, "fp_0102030405060708090a0b0c0d0e0f10",
                               (const uint8_t *)"b831381d63244d53ad4f8cda48b30811") < 0);

    uint8_t valid_sec[32];
    make_test_v2_secret(0, valid_sec);
    check("A1: accepts independent 32-byte CSPRNG secret",
          add_explicit_fp_cred(&cfg, "fp_0102030405060708090a0b0c0d0e0f10", valid_sec) == 0 &&
          rebuild_fp_credentials(&cfg) == 0 &&
          find_cred(&cfg, "fp_0102030405060708090a0b0c0d0e0f10") != NULL);
    check("A1: stage2probe credential disabled by default", find_cred(&cfg, DEFAULT_STAGE2_ID) == NULL);

    setenv("FP_ENABLE_DIAG_CRED", "1", 1);
    check("A1: stage2probe still disabled when FP_SECRET_HEX missing",
          rebuild_fp_credentials(&cfg) == 0 && find_cred(&cfg, DEFAULT_STAGE2_ID) == NULL);

    /* Unified 64-hex (32-byte) contract for FP_SECRET_HEX: 32-hex (16-byte) must be rejected */
    setenv("FP_SECRET_HEX", "00112233445566778899aabbccddeeff", 1);
    check("A1: stage2probe rejects 32-hex (16-byte) FP_SECRET_HEX under strict 64-hex contract",
          rebuild_fp_credentials(&cfg) == 0 && find_cred(&cfg, DEFAULT_STAGE2_ID) == NULL);

    setenv("FP_SECRET_HEX", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", 1);
    check("A1: stage2probe enabled when both FP_ENABLE_DIAG_CRED=1 and valid 64-hex FP_SECRET_HEX set",
          rebuild_fp_credentials(&cfg) == 0 && find_cred(&cfg, DEFAULT_STAGE2_ID) != NULL);
    unsetenv("FP_ENABLE_DIAG_CRED");
    unsetenv("FP_SECRET_HEX");

    /* A9: MAX_CLIENTS (64) must fit in MAX_FP_CREDS without truncation */
    check("A9: MAX_FP_CREDS >= MAX_CLIENTS * 8 + 16", MAX_FP_CREDS >= MAX_CLIENTS * 8 + 16);
    memset(&cfg, 0, sizeof(cfg));
    cfg.num_clients = MAX_CLIENTS;
    for (int i = 0; i < MAX_CLIENTS; i++) {
        char ustr[40];
        snprintf(ustr, sizeof(ustr), "00000000-0000-4000-8000-%012d", i + 1);
        add_cfg_client(&cfg, i, ustr);
    }
    check("A9: rebuild_fp_credentials succeeds for all 64 MAX_CLIENTS",
          rebuild_fp_credentials(&cfg) == 0);
    check("A9: 64th client credential is present and not truncated",
          find_cred(&cfg, cfg.clients[MAX_CLIENTS - 1].uuid_str) != NULL);

    cfg.num_clients = MAX_CLIENTS + 1;
    check("A9: rebuild_fp_credentials rejects num_clients > MAX_CLIENTS",
          rebuild_fp_credentials(&cfg) == -1);
}

static void test_a2_http_parser(void) {
    http_req_t req;

    const char *bad_hdr = "GET / HTTP/1.1\r\nHost: localhost\r\nBadHeaderWithoutColon\r\n\r\n";
    check("A2: rejects header line without colon",
          feed_http_req(bad_hdr, strlen(bad_hdr), &req, NULL) < 0);

    const char *dup_cl = "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 4\r\nContent-Length: 4\r\n\r\nABCD";
    check("A2: rejects duplicate Content-Length",
          feed_http_req(dup_cl, strlen(dup_cl), &req, NULL) < 0);

    const char *te_cl = "POST / HTTP/1.1\r\nHost: localhost\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n4\r\nABCD\r\n0\r\n\r\n";
    check("A2: rejects conflicting Transfer-Encoding + Content-Length",
          feed_http_req(te_cl, strlen(te_cl), &req, NULL) < 0);

    const char *bad_te = "POST / HTTP/1.1\r\nHost: localhost\r\nTransfer-Encoding: gzip\r\n\r\n";
    check("A2: rejects unsupported Transfer-Encoding",
          feed_http_req(bad_te, strlen(bad_te), &req, NULL) < 0);

    const char *bad_cl = "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 4abc\r\n\r\nABCD";
    check("A2: rejects non-numeric Content-Length",
          feed_http_req(bad_cl, strlen(bad_cl), &req, NULL) < 0);

    const char *ovf_chunk = "POST / HTTP/1.1\r\nHost: localhost\r\nTransfer-Encoding: chunked\r\n\r\nFFFFFFFFFFFFFFFF\r\nABCD\r\n0\r\n\r\n";
    check("A2: rejects overflowing chunk size",
          feed_http_req(ovf_chunk, strlen(ovf_chunk), &req, NULL) < 0);

    /* Chunked request with trailers followed by a second pipelined request on the same keep-alive stream */
    const char *pipelined =
        "POST /up HTTP/1.1\r\nHost: localhost\r\nTransfer-Encoding: chunked\r\n\r\n"
        "5\r\nHELLO\r\n0\r\nX-Trailer-One: 1\r\nX-Trailer-Two: 2\r\n\r\n"
        "GET /.xhttpd/health HTTP/1.1\r\nHost: localhost\r\n\r\n";
    int sv[2];
    check("A2: socketpair for pipelined chunked+trailer test", socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0);
    send(sv[1], pipelined, strlen(pipelined), MSG_NOSIGNAL);
    shutdown(sv[1], SHUT_WR);

    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = sv[0];
    http_req_t req1, req2;
    int rc1 = read_http_request(&cr, &req1);
    check("A2: first chunked request with trailers parses cleanly",
          rc1 == 0 && req1.body_len == 5 && memcmp(req1.body, "HELLO", 5) == 0);
    free(req1.body);

    int rc2 = read_http_request(&cr, &req2);
    check("A2: second pipelined request after trailers parses cleanly",
          rc2 == 0 && strcmp(req2.method, "GET") == 0 && strcmp(req2.path, "/.xhttpd/health") == 0);
    free(req2.body);
    close(sv[0]);
    close(sv[1]);
}

static void *echo_server_thread(void *arg) {
    int lfd = *(int *)arg;
    free(arg);
    struct sockaddr_in cli;
    socklen_t clen = sizeof(cli);
    int cfd = accept(lfd, (struct sockaddr *)&cli, &clen);
    close(lfd);
    if (cfd < 0) return NULL;
    uint8_t buf[256];
    for (;;) {
        ssize_t r = recv(cfd, buf, sizeof(buf), 0);
        if (r <= 0) break;
        send(cfd, buf, (size_t)r, MSG_NOSIGNAL);
    }
    close(cfd);
    return NULL;
}

static void put_be64_t(uint8_t *p, uint64_t v) {
    for (int i = 0; i < 8; i++) p[i] = (uint8_t)(v >> (56 - 8 * i));
}

static void put_be32_t(uint8_t *p, uint32_t v) {
    for (int i = 0; i < 4; i++) p[i] = (uint8_t)(v >> (24 - 8 * i));
}

static uint64_t get_be64_t(const uint8_t *p) {
    uint64_t v = 0;
    for (int i = 0; i < 8; i++) v = (v << 8) | p[i];
    return v;
}

static uint32_t get_be32_t(const uint8_t *p) {
    uint32_t v = 0;
    for (int i = 0; i < 4; i++) v = (v << 8) | p[i];
    return v;
}

static void build_signed_fp_http_req_ex(http_req_t *req, const xhttpd_config_t *cfg,
                                        const char *client_id, uint8_t op, const uint8_t sid[16],
                                        uint64_t up_off, uint64_t ack_down_off,
                                        uint64_t want_down_off, uint32_t max_down_bytes,
                                        const uint8_t *payload, size_t plen,
                                        uint32_t nonce_seq) {
    memset(req, 0, sizeof(*req));
    snprintf(req->method, sizeof(req->method), "%s", "POST");
    snprintf(req->path, sizeof(req->path), "%s", "/api/v1/upload");
    snprintf(req->fp_version, sizeof(req->fp_version), "%s", FP_PROTO_VERSION);

    req->body_len = FP_REQ_HEADER_SIZE + plen;
    req->body = calloc(1, req->body_len);
    memcpy(req->body, FP_PROTO_MAGIC, 4);
    req->body[4] = op;
    req->body[5] = 0;
    memcpy(req->body + 6, sid, 16);
    put_be64_t(req->body + 22, up_off);
    put_be64_t(req->body + 30, ack_down_off);
    put_be64_t(req->body + 38, want_down_off);
    put_be32_t(req->body + 46, max_down_bytes);
    put_be32_t(req->body + 50, (uint32_t)plen);
    if (plen > 0 && payload) {
        memcpy(req->body + FP_REQ_HEADER_SIZE, payload, plen);
    }

    const fp_cred_t *cred = find_cred(cfg, client_id);
    assert(cred != NULL);

    char ts_str[32], nonce_hex[33], sig_hex[65];
    snprintf(ts_str, sizeof(ts_str), "%llu", (unsigned long long)epoch_ms());
    snprintf(nonce_hex, sizeof(nonce_hex), "00112233445566778899aabb%08x", nonce_seq);

    uint8_t body_sha[32];
    sha256_hash(req->body, req->body_len, body_sha);

    uint8_t msg[1024];
    size_t off = 0;
    const char *parts[6] = { FP_PROTO_VERSION, req->method, req->path, client_id, ts_str, nonce_hex };
    for (int i = 0; i < 6; i++) {
        size_t l = strlen(parts[i]);
        memcpy(msg + off, parts[i], l);
        off += l;
        msg[off++] = 0;
    }
    memcpy(msg + off, body_sha, 32);
    off += 32;

    uint8_t sig[32];
    hmac_sha256(cred->secret, cred->secret_len, msg, off, sig);
    bytes_to_hex(sig, 32, sig_hex);
    snprintf(req->fp_auth, sizeof(req->fp_auth), "%s:%s:%s:%s", client_id, ts_str, nonce_hex, sig_hex);
}

static void build_signed_fp_http_req(http_req_t *req, const xhttpd_config_t *cfg,
                                     const char *client_id, uint8_t op, const uint8_t sid[16],
                                     uint64_t up_off, const uint8_t *payload, size_t plen,
                                     uint32_t nonce_seq) {
    build_signed_fp_http_req_ex(req, cfg, client_id, op, sid, up_off, 0, 0, 65536, payload, plen, nonce_seq);
}

static void test_a3_a4_a5_finiteproto(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true; /* allow loopback echo server for session test */
    cfg.num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(&cfg, 0, uuid);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    /* A3: VLESS MUX command (0x03) must be rejected with 400 Bad Request */
    uint8_t vless_mux[22];
    vless_mux[0] = 0x00; /* version */
    memcpy(vless_mux + 1, cfg.clients[0].uuid, 16);
    vless_mux[17] = 0x00; /* addons len */
    vless_mux[18] = VLESS_CMD_MUX;
    vless_mux[19] = 0x00; vless_mux[20] = 53;
    vless_mux[21] = VLESS_ADDR_IPV4;

    uint8_t open_sid[16] = { 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16 };
    http_req_t req;
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_OPEN, open_sid, 0, vless_mux, sizeof(vless_mux), 1);
    int sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    char resp[512] = {0};
    recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    check("A3: handle_fp_open rejects VLESS MUX command with 400 Bad Request",
          strstr(resp, "HTTP/1.1 400 ") != NULL);

    /* A3: VLESS UDP QUIC (port 443) must be rejected with 403 Forbidden */
    uint8_t vless_quic[26];
    vless_quic[0] = 0x00;
    memcpy(vless_quic + 1, cfg.clients[0].uuid, 16);
    vless_quic[17] = 0x00;
    vless_quic[18] = VLESS_CMD_UDP;
    vless_quic[19] = 0x01; vless_quic[20] = 0xBB; /* port 443 */
    vless_quic[21] = VLESS_ADDR_IPV4;
    vless_quic[22] = 8; vless_quic[23] = 8; vless_quic[24] = 8; vless_quic[25] = 8;

    uint8_t open_sid_quic[16] = { 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 17 };
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_OPEN, open_sid_quic, 0, vless_quic, sizeof(vless_quic), 10);
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    memset(resp, 0, sizeof(resp));
    recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    check("A3: handle_fp_open rejects VLESS UDP port 443 (QUIC) with 403 Forbidden",
          strstr(resp, "HTTP/1.1 403 ") != NULL);

    /* Start loopback TCP echo server for A3/A4/A5 session lifecycle & OOM test */
    int lfd = socket(AF_INET, SOCK_STREAM, 0);
    int one = 1;
    setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    struct sockaddr_in sin;
    memset(&sin, 0, sizeof(sin));
    sin.sin_family = AF_INET;
    sin.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    sin.sin_port = 0;
    bind(lfd, (struct sockaddr *)&sin, sizeof(sin));
    listen(lfd, 4);
    socklen_t slen = sizeof(sin);
    getsockname(lfd, (struct sockaddr *)&sin, &slen);
    uint16_t echo_port = ntohs(sin.sin_port);

    int *lfd_p = malloc(sizeof(int));
    *lfd_p = lfd;
    pthread_t eth;
    pthread_create(&eth, NULL, echo_server_thread, lfd_p);
    pthread_detach(eth);

    uint8_t vless_tcp[26];
    vless_tcp[0] = 0x00;
    memcpy(vless_tcp + 1, cfg.clients[0].uuid, 16);
    vless_tcp[17] = 0x00;
    vless_tcp[18] = VLESS_CMD_TCP;
    vless_tcp[19] = (uint8_t)(echo_port >> 8);
    vless_tcp[20] = (uint8_t)(echo_port & 0xFF);
    vless_tcp[21] = VLESS_ADDR_IPV4;
    vless_tcp[22] = 127; vless_tcp[23] = 0; vless_tcp[24] = 0; vless_tcp[25] = 1;

    uint8_t open_sid2[16] = { 0x11, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16 };
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_OPEN, open_sid2, 0, vless_tcp, sizeof(vless_tcp), 2);
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    uint8_t raw_resp[1024] = {0};
    ssize_t rn = recv(sv[1], raw_resp, sizeof(raw_resp) - 1, 0);
    close(sv[1]);
    check("A3: handle_fp_open TCP session succeeds with 200 OK",
          rn > FP_RESP_HEADER_SIZE && strstr((char *)raw_resp, "HTTP/1.1 200 ") != NULL);

    /* Extract assigned 16-byte session ID from FP01 response body (after \r\n\r\n + 6) */
    uint8_t sess_sid[16] = {0};
    char *hdr_end = strstr((char *)raw_resp, "\r\n\r\n");
    if (hdr_end) {
        const uint8_t *fp_resp = (const uint8_t *)(hdr_end + 4);
        memcpy(sess_sid, fp_resp + 6, 16);
    }

    /* A4: Simulate allocation failure in handle_fp_up — must return 503 and NOT advance next_up_offset */
    const uint8_t payload[] = "PING-ECHO";
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_UP, sess_sid, 0, payload, sizeof(payload) - 1, 3);
    fp_test_set_alloc_fail_after(0);
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    fp_test_set_alloc_fail_after(-1);
    memset(resp, 0, sizeof(resp));
    recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    check("A4: handle_fp_up OOM returns 503 Service Unavailable",
          strstr(resp, "HTTP/1.1 503 ") != NULL);

    /* Retry with the SAME offset=0 after clearing OOM: must succeed because offset was not advanced! */
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_UP, sess_sid, 0, payload, sizeof(payload) - 1, 4);
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    memset(resp, 0, sizeof(resp));
    recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    check("A4: retry at offset=0 after OOM succeeds with 200 OK",
          strstr(resp, "HTTP/1.1 200 ") != NULL);

    /* Close session and verify global buffer accounting returns to 0 */
    build_signed_fp_http_req(&req, &cfg, uuid, FP_OP_CLOSE, sess_sid, 0, NULL, 0, 5);
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, &cfg);
    close(sv[0]);
    free(req.body);
    close(sv[1]);
    usleep(50000);
    check("A5: global buffer bytes returns to 0 after session close",
          fp_engine_get_global_buf_bytes() == 0);
}

static void test_a6_acl_and_defaults(void) {
    char tmp_cfg[] = "/tmp/xhttpd-cfg-XXXXXX";
    int fd = mkstemp(tmp_cfg);
    const char *json = "{\"listen\":\"127.0.0.1:18080\",\"clients\":[\"b831381d-6324-4d53-ad4f-8cda48b30811\"]}";
    ssize_t w1 = write(fd, json, strlen(json));
    (void)w1;
    close(fd);

    xhttpd_config_t cfg;
    check("A6: load_config_file succeeds", load_config_file(tmp_cfg, &cfg) == 0);
    unlink(tmp_cfg);
    check("A6: allow_private defaults to false when omitted from config", cfg.allow_private == false);

    check("A6: blocks IPv4 loopback 127.0.0.1", !check_destination_acl_ex("127.0.0.1", false, false));
    check("A6: blocks RFC1918 10.0.0.1", !check_destination_acl_ex("10.0.0.1", false, false));
    check("A6: blocks CGNAT 100.64.1.1", !check_destination_acl_ex("100.64.1.1", false, false));
    check("A6: blocks link-local 169.254.169.254", !check_destination_acl_ex("169.254.169.254", false, false));
    check("A6: blocks 198.18.0.25 when FakeIP exception is false",
          !check_destination_acl_ex("198.18.0.25", false, false));
    check("A6: allows 198.18.0.25 ONLY when FakeIP exception is true",
          check_destination_acl_ex("198.18.0.25", false, true));
    check("A6: allows public IPv4 1.1.1.1", check_destination_acl_ex("1.1.1.1", false, false));

    check("A6: blocks IPv6 loopback ::1", !check_destination_acl_ex("::1", false, false));
    check("A6: blocks IPv6 link-local fe80::1", !check_destination_acl_ex("fe80::1", false, false));
    check("A6: blocks IPv6 ULA fd00::1", !check_destination_acl_ex("fd00::1", false, false));
    check("A6: blocks IPv4-mapped IPv6 ::ffff:192.168.10.1", !check_destination_acl_ex("::ffff:192.168.10.1", false, false));
    check("A6: blocks IPv6 documentation range 2001:db8::1", !check_destination_acl_ex("2001:db8::1", false, false));
    check("A6: allows public IPv6 2606:4700:4700::1111", check_destination_acl_ex("2606:4700:4700::1111", false, false));

    check("A6: blocks localhost domain", !check_destination_acl_ex("localhost", false, false));
    check("A6: blocks .local domain", !check_destination_acl_ex("printer.local", false, false));
    check("A6: blocks .lan domain", !check_destination_acl_ex("router.lan", false, false));
    check("A6: blocks .home.arpa domain", !check_destination_acl_ex("nas.home.arpa", false, false));
    check("A6: allows public domain example.com", check_destination_acl_ex("example.com", false, false));
}

static void test_a7_fakeip_dnat(void) {
    char script_path[] = "/tmp/xhttpd-ipt-XXXXXX";
    char log_path[] = "/tmp/xhttpd-ipt-log-XXXXXX";
    int sfd = mkstemp(script_path);
    int lfd = mkstemp(log_path);
    close(lfd);

    char script[1024];
    snprintf(script, sizeof(script),
             "#!/bin/sh\n"
             "echo \"$*\" >> %s\n"
             "if [ \"$4\" = \"-S\" ] && [ \"$5\" = \"FIRC_DNAT\" ]; then\n"
             "  echo \"-N FIRC_DNAT\"\n"
             "  echo \"-A FIRC_DNAT -d 198.18.0.42/32 -p tcp -j DNAT --to-destination 93.184.216.34\"\n"
             "  exit 0\n"
             "fi\n"
             "if [ \"$4\" = \"-S\" ] && [ \"$5\" = \"FEXT_DNAT\" ]; then\n"
             "  echo \"-N FEXT_DNAT\"\n"
             "  exit 0\n"
             "fi\n"
             "exit 0\n",
             log_path);
    ssize_t w2 = write(sfd, script, strlen(script));
    (void)w2;
    fchmod(sfd, 0755);
    close(sfd);

    setenv("XHTTPD_IPTABLES_BIN", script_path, 1);
    dnat_bump_generation();
    check("A7: sync_fakeip_dnat returns 0 on success", sync_fakeip_dnat("198.18.0.42") == 0);

    FILE *lf = fopen(log_path, "r");
    char log_buf[2048] = {0};
    if (lf) {
        size_t r1 = fread(log_buf, 1, sizeof(log_buf) - 1, lf);
        (void)r1;
        fclose(lf);
    }
    check("A7: sync_fakeip_dnat queries FIRC_DNAT and inserts rule at FEXT_DNAT 1",
          strstr(log_buf, "-w -t nat -S FIRC_DNAT") != NULL &&
          strstr(log_buf, "-w -t nat -I FEXT_DNAT 1 -d 198.18.0.42/32 -p tcp -m mark --mark 0x40000000/0x40000000 -j DNAT --to-destination 93.184.216.34") != NULL);

    /* Second call within same generation should hit cache (no new log lines) */
    int trc = truncate(log_path, 0);
    (void)trc;
    sync_fakeip_dnat("198.18.0.42");
    memset(log_buf, 0, sizeof(log_buf));
    lf = fopen(log_path, "r");
    if (lf) {
        size_t r2 = fread(log_buf, 1, sizeof(log_buf) - 1, lf);
        (void)r2;
        fclose(lf);
    }
    check("A7: second sync_fakeip_dnat call within same generation hits cache", log_buf[0] == '\0');

    /* Bumping generation invalidates cache and re-syncs */
    dnat_bump_generation();
    sync_fakeip_dnat("198.18.0.42");
    lf = fopen(log_path, "r");
    if (lf) {
        size_t r3 = fread(log_buf, 1, sizeof(log_buf) - 1, lf);
        (void)r3;
        fclose(lf);
    }
    check("A7: dnat_bump_generation invalidates cache and forces re-sync",
          strstr(log_buf, "-I FEXT_DNAT 1 -d 198.18.0.42/32") != NULL);

    unsetenv("XHTTPD_IPTABLES_BIN");
    unlink(script_path);
    unlink(log_path);
}

typedef struct {
    int lfd;
    int cfd;
} accept_arg_t;

static void *accept_one_thread(void *arg) {
    accept_arg_t *a = (accept_arg_t *)arg;
    struct pollfd pfd = { .fd = a->lfd, .events = POLLIN };
    if (poll(&pfd, 1, 3000) <= 0) {
        a->cfd = -1;
        return NULL;
    }
    struct sockaddr_in cli;
    socklen_t clen = sizeof(cli);
    a->cfd = accept(a->lfd, (struct sockaddr *)&cli, &clen);
    return NULL;
}

static int create_loopback_listener(uint16_t *out_port) {
    int lfd = socket(AF_INET, SOCK_STREAM, 0);
    int one = 1;
    setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    struct sockaddr_in sin;
    memset(&sin, 0, sizeof(sin));
    sin.sin_family = AF_INET;
    sin.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    sin.sin_port = 0;
    bind(lfd, (struct sockaddr *)&sin, sizeof(sin));
    listen(lfd, 4);
    socklen_t slen = sizeof(sin);
    getsockname(lfd, (struct sockaddr *)&sin, &slen);
    *out_port = ntohs(sin.sin_port);
    return lfd;
}

static int open_plain_fp_session(const xhttpd_config_t *cfg, const char *uuid,
                                 const uint8_t open_sid[16], uint32_t nonce_seq,
                                 uint8_t out_sid[16], int *out_peer_fd) {
    uint16_t port = 0;
    int lfd = create_loopback_listener(&port);
    accept_arg_t acc = { .lfd = lfd, .cfd = -1 };
    pthread_t th;
    pthread_create(&th, NULL, accept_one_thread, &acc);

    char target_str[64];
    snprintf(target_str, sizeof(target_str), "127.0.0.1:%u", (unsigned)port);

    http_req_t req;
    build_signed_fp_http_req(&req, cfg, uuid, FP_OP_OPEN, open_sid, 0,
                             (const uint8_t *)target_str, strlen(target_str), nonce_seq);
    int sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, cfg);
    close(sv[0]);
    free(req.body);

    uint8_t raw_resp[1024] = {0};
    ssize_t rn = recv(sv[1], raw_resp, sizeof(raw_resp) - 1, 0);
    close(sv[1]);

    if (rn <= FP_RESP_HEADER_SIZE || strstr((char *)raw_resp, "HTTP/1.1 200 ") == NULL) {
        int dummy = socket(AF_INET, SOCK_STREAM, 0);
        if (dummy >= 0) {
            struct sockaddr_in dsin;
            memset(&dsin, 0, sizeof(dsin));
            dsin.sin_family = AF_INET;
            dsin.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
            dsin.sin_port = htons(port);
            connect(dummy, (struct sockaddr *)&dsin, sizeof(dsin));
            close(dummy);
        }
        pthread_join(th, NULL);
        close(lfd);
        if (acc.cfd >= 0) close(acc.cfd);
        return -1;
    }
    pthread_join(th, NULL);
    close(lfd);
    char *hdr_end = strstr((char *)raw_resp, "\r\n\r\n");
    if (!hdr_end) {
        if (acc.cfd >= 0) close(acc.cfd);
        return -1;
    }
    const uint8_t *fp_resp = (const uint8_t *)(hdr_end + 4);
    memcpy(out_sid, fp_resp + 6, 16);
    *out_peer_fd = acc.cfd;
    return 0;
}

static int exec_fp_down(const xhttpd_config_t *cfg, const char *uuid,
                        const uint8_t sid[16], uint64_t ack_off, uint64_t want_off,
                        uint32_t max_bytes, uint32_t nonce_seq,
                        uint8_t *out_status, uint8_t *out_flags,
                        uint64_t *out_start, uint64_t *out_end,
                        uint8_t *out_buf, uint32_t *out_len) {
    http_req_t req;
    build_signed_fp_http_req_ex(&req, cfg, uuid, FP_OP_DOWN, sid, 0,
                                ack_off, want_off, max_bytes, NULL, 0, nonce_seq);
    int sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, cfg);
    close(sv[0]);
    free(req.body);

    size_t cap = 131072;
    uint8_t *raw = malloc(cap);
    size_t got = 0;
    while (got < cap) {
        ssize_t r = recv(sv[1], raw + got, cap - got, 0);
        if (r <= 0) break;
        got += (size_t)r;
    }
    close(sv[1]);

    char *hdr_end = (char *)memmem(raw, got, "\r\n\r\n", 4);
    if (!hdr_end) {
        free(raw);
        return -1;
    }
    size_t hdr_bytes = (size_t)(hdr_end + 4 - (char *)raw);
    if (got < hdr_bytes + FP_RESP_HEADER_SIZE) {
        free(raw);
        return -1;
    }
    const uint8_t *fp = raw + hdr_bytes;
    *out_status = fp[4];
    *out_flags  = fp[5];
    *out_start  = get_be64_t(fp + 30);
    *out_end    = get_be64_t(fp + 38);
    uint32_t plen = get_be32_t(fp + 54);
    if (got < hdr_bytes + FP_RESP_HEADER_SIZE + plen) {
        free(raw);
        return -1;
    }
    if (plen > 0 && out_buf) {
        memcpy(out_buf, fp + FP_RESP_HEADER_SIZE, plen);
    }
    *out_len = plen;
    free(raw);
    return 0;
}

static uint8_t r1_pattern_byte(int sess_idx, size_t offset) {
    return (uint8_t)(((unsigned)sess_idx * 41U + (unsigned)(offset & 0xFFFFU) * 17U + (unsigned)(offset >> 8) * 3U) & 0xFFU);
}

typedef struct {
    int fd;
    int sess_idx;
    size_t start_off;
    size_t len;
} r1_writer_arg_t;

static void *r1_writer_thread(void *arg) {
    r1_writer_arg_t *w = (r1_writer_arg_t *)arg;
    uint8_t *buf = malloc(w->len);
    for (size_t i = 0; i < w->len; i++) {
        buf[i] = r1_pattern_byte(w->sess_idx, w->start_off + i);
    }
    write_all(w->fd, buf, w->len);
    free(buf);
    return NULL;
}

static void test_r1_global_budget_ack_and_concurrent_growth(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(&cfg, 0, uuid);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    /*
     * Subtest 1:
     * Fill 100% of global budget (256 KiB = 4 * 64 KiB) across 4 active sessions,
     * ACK all 4 sessions completely WITHOUT closing them, verify global budget drops to 0,
     * then send a second 64 KiB wave across all 4 sessions and verify full progress,
     * byte-for-byte equality across all 128 KiB per session, and global budget compliance.
     */
    const size_t chunk_sz = 65536;
    const size_t global_cap_1 = 4 * chunk_sz;
    fp_test_set_max_global_buf(global_cap_1);

    uint8_t sids[4][16];
    int peer_fds[4] = {-1, -1, -1, -1};
    uint32_t nseq = 100;
    int open_ok = 1;
    for (int i = 0; i < 4; i++) {
        uint8_t osid[16] = { (uint8_t)(0x40 + i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
        if (open_plain_fp_session(&cfg, uuid, osid, nseq++, sids[i], &peer_fds[i]) != 0) {
            open_ok = 0;
        }
    }
    check("R1: open 4 concurrent sessions for global budget fill test", open_ok);

    /* Wave 1: write 64 KiB to each of the 4 upstream sockets */
    pthread_t wth[4];
    r1_writer_arg_t warg[4];
    for (int i = 0; i < 4; i++) {
        warg[i].fd = peer_fds[i];
        warg[i].sess_idx = i;
        warg[i].start_off = 0;
        warg[i].len = chunk_sz;
        pthread_create(&wth[i], NULL, r1_writer_thread, &warg[i]);
    }
    for (int i = 0; i < 4; i++) {
        pthread_join(wth[i], NULL);
    }

    uint8_t *rx_buf[4];
    for (int i = 0; i < 4; i++) rx_buf[i] = calloc(1, 2 * chunk_sz);

    /* Read all 64 KiB on each session WITHOUT ACKing yet (ack_off = 0) so global budget stays 100% full */
    int wave1_read_ok = 1;
    int budget_bounded_1 = 1;
    for (int i = 0; i < 4; i++) {
        uint64_t cur_off = 0;
        int tries = 0;
        while (cur_off < chunk_sz && tries++ < 40) {
            uint8_t st = 0, fl = 0;
            uint64_t dstart = 0, dend = 0;
            uint32_t dlen = 0;
            if (exec_fp_down(&cfg, uuid, sids[i], 0, cur_off, 65536, nseq++,
                             &st, &fl, &dstart, &dend, rx_buf[i] + cur_off, &dlen) != 0 || st != FP_STATUS_OK) {
                wave1_read_ok = 0;
                break;
            }
            cur_off = dend;
            if (fp_engine_get_global_buf_bytes() > global_cap_1) {
                budget_bounded_1 = 0;
            }
        }
        if (cur_off != chunk_sz) wave1_read_ok = 0;
    }
    check("R1: all 4 sessions buffered and delivered 64 KiB each (256 KiB total)", wave1_read_ok);
    check("R1: global buffer budget reached 256 KiB ceiling without exceeding it",
          budget_bounded_1 && fp_engine_get_global_buf_bytes() == global_cap_1);

    /* Now ACK all 64 KiB on all 4 sessions WITHOUT sending FP_OP_CLOSE */
    for (int i = 0; i < 4; i++) {
        uint8_t st = 0, fl = 0;
        uint64_t dstart = 0, dend = 0;
        uint32_t dlen = 0;
        exec_fp_down(&cfg, uuid, sids[i], chunk_sz, chunk_sz, 0, nseq++,
                     &st, &fl, &dstart, &dend, NULL, &dlen);
    }
    check("R1: ACK without CLOSE releases 100% of global buffer reservation back to 0",
          fp_engine_get_global_buf_bytes() == 0);

    /* Wave 2: send another 64 KiB on all 4 open sessions after full ACK */
    for (int i = 0; i < 4; i++) {
        warg[i].fd = peer_fds[i];
        warg[i].sess_idx = i;
        warg[i].start_off = chunk_sz;
        warg[i].len = chunk_sz;
        pthread_create(&wth[i], NULL, r1_writer_thread, &warg[i]);
    }
    for (int i = 0; i < 4; i++) {
        pthread_join(wth[i], NULL);
        close(peer_fds[i]);
    }

    int wave2_read_ok = 1;
    int wave2_data_match = 1;
    for (int i = 0; i < 4; i++) {
        uint64_t cur_off = chunk_sz;
        int tries = 0;
        while (cur_off < 2 * chunk_sz && tries++ < 40) {
            uint8_t st = 0, fl = 0;
            uint64_t dstart = 0, dend = 0;
            uint32_t dlen = 0;
            if (exec_fp_down(&cfg, uuid, sids[i], cur_off, cur_off, 65536, nseq++,
                             &st, &fl, &dstart, &dend, rx_buf[i] + cur_off, &dlen) != 0 || st != FP_STATUS_OK) {
                wave2_read_ok = 0;
                break;
            }
            cur_off = dend;
            if (fp_engine_get_global_buf_bytes() > global_cap_1) {
                budget_bounded_1 = 0;
            }
        }
        /* Final ACK for wave 2 */
        uint8_t st = 0, fl = 0;
        uint64_t dstart = 0, dend = 0;
        uint32_t dlen = 0;
        exec_fp_down(&cfg, uuid, sids[i], 2 * chunk_sz, 2 * chunk_sz, 0, nseq++,
                     &st, &fl, &dstart, &dend, NULL, &dlen);
        if (cur_off != 2 * chunk_sz) wave2_read_ok = 0;
        for (size_t b = 0; b < 2 * chunk_sz; b++) {
            if (rx_buf[i][b] != r1_pattern_byte(i, b)) {
                wave2_data_match = 0;
                break;
            }
        }
    }
    check("R1: all 4 sessions make full progress on second 64 KiB wave after ACK", wave2_read_ok);
    check("R1: byte-for-byte equality across all 128 KiB on all 4 sessions", wave2_data_match);
    check("R1: global buffer budget returns to 0 after second wave ACK",
          budget_bounded_1 && fp_engine_get_global_buf_bytes() == 0);

    for (int i = 0; i < 4; i++) {
        free(rx_buf[i]);
        http_req_t creq;
        build_signed_fp_http_req(&creq, &cfg, uuid, FP_OP_CLOSE, sids[i], 0, NULL, 0, nseq++);
        int sv[2];
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        handle_fp_http_request(sv[0], &creq, &cfg);
        close(sv[0]);
        close(sv[1]);
        free(creq.body);
    }

    /*
     * Subtest 2:
     * Simultaneous growth of two session buffers near the global limit (64 KiB total cap,
     * 2 sessions each receiving 48 KiB = 96 KiB total concurrently).
     * Verifies backpressure (no TARGET_RESET), strict adherence to global cap (<= 64 KiB),
     * and byte-for-byte delivery as ACKs free budget.
     */
    const size_t global_cap_2 = 65536;
    const size_t per_sess_2 = 49152; /* 48 KiB each -> 96 KiB total > 64 KiB cap */
    fp_test_set_max_global_buf(global_cap_2);

    uint8_t sid2[2][16];
    int pfd2[2] = {-1, -1};
    for (int i = 0; i < 2; i++) {
        uint8_t osid[16] = { (uint8_t)(0x70 + i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
        open_plain_fp_session(&cfg, uuid, osid, nseq++, sid2[i], &pfd2[i]);
    }

    pthread_t cth[2];
    r1_writer_arg_t carg[2];
    for (int i = 0; i < 2; i++) {
        carg[i].fd = pfd2[i];
        carg[i].sess_idx = 10 + i;
        carg[i].start_off = 0;
        carg[i].len = per_sess_2;
        pthread_create(&cth[i], NULL, r1_writer_thread, &carg[i]);
    }

    /* Allow both readers to contend against the 64 KiB global cap simultaneously */
    usleep(80000);
    int cap2_respected = (fp_engine_get_global_buf_bytes() <= global_cap_2);

    uint8_t *rx2[2];
    rx2[0] = calloc(1, per_sess_2);
    rx2[1] = calloc(1, per_sess_2);
    uint64_t off2[2] = {0, 0};
    int no_reset = 1;
    int rounds = 0;
    while ((off2[0] < per_sess_2 || off2[1] < per_sess_2) && rounds++ < 60) {
        for (int i = 0; i < 2; i++) {
            if (off2[i] >= per_sess_2) continue;
            uint8_t st = 0, fl = 0;
            uint64_t dstart = 0, dend = 0;
            uint32_t dlen = 0;
            if (exec_fp_down(&cfg, uuid, sid2[i], off2[i], off2[i], 16384, nseq++,
                             &st, &fl, &dstart, &dend, rx2[i] + off2[i], &dlen) == 0 && st == FP_STATUS_OK) {
                if (fl & FP_RESP_FLAG_TARGET_RESET) no_reset = 0;
                off2[i] = dend;
            }
            if (fp_engine_get_global_buf_bytes() > global_cap_2) {
                cap2_respected = 0;
            }
        }
    }

    for (int i = 0; i < 2; i++) {
        pthread_join(cth[i], NULL);
        close(pfd2[i]);
        uint8_t st = 0, fl = 0;
        uint64_t dstart = 0, dend = 0;
        uint32_t dlen = 0;
        exec_fp_down(&cfg, uuid, sid2[i], per_sess_2, per_sess_2, 0, nseq++,
                     &st, &fl, &dstart, &dend, NULL, &dlen);
    }

    int match2 = (off2[0] == per_sess_2 && off2[1] == per_sess_2);
    for (int i = 0; i < 2 && match2; i++) {
        for (size_t b = 0; b < per_sess_2; b++) {
            if (rx2[i][b] != r1_pattern_byte(10 + i, b)) {
                match2 = 0;
                break;
            }
        }
    }
    check("R1: simultaneous buffer growth near global limit respects <= 64 KiB cap", cap2_respected);
    check("R1: simultaneous buffer growth applies backpressure without TARGET_RESET and delivers all 96 KiB",
          no_reset && match2);

    for (int i = 0; i < 2; i++) {
        free(rx2[i]);
        http_req_t creq;
        build_signed_fp_http_req(&creq, &cfg, uuid, FP_OP_CLOSE, sid2[i], 0, NULL, 0, nseq++);
        int sv[2];
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        handle_fp_http_request(sv[0], &creq, &cfg);
        close(sv[0]);
        close(sv[1]);
        free(creq.body);
    }
    fp_test_set_max_global_buf(FP_MAX_GLOBAL_BUF);
}

static void test_r2_http_absolute_deadline_drip_feed(void) {
    /* Set total HTTP request deadline to 160 ms */
    xhttpd_set_http_req_timeout_ms(160);

    /* Phase 1: Drip-feed 1 byte every 25 ms in request line without '\n' */
    int sv[2];
    check("R2: socketpair for request-line drip-feed test", socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0);
    check("R2: spawn HTTP worker for request-line drip-feed", xhttpd_spawn_client_worker_for_test(sv[0]) == 0);

    uint64_t t0 = now_ms();
    int peer_closed = 0;
    for (int i = 0; i < 30; i++) {
        usleep(25000); /* 25 ms per byte — well under 160 ms per-step, but exceeds 160 ms total after ~7 bytes */
        char ch = 'G';
        ssize_t w = send(sv[1], &ch, 1, MSG_NOSIGNAL);
        char rbuf[16];
        ssize_t r = recv(sv[1], rbuf, sizeof(rbuf), MSG_DONTWAIT);
        if (w <= 0 || r == 0 || (r < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR)) {
            peer_closed = 1;
            break;
        }
    }
    uint64_t elapsed_req_line = now_ms() - t0;
    close(sv[1]);

    for (int i = 0; i < 20 && xhttpd_get_active_http_workers() > 0; i++) {
        usleep(10000);
    }

    check("R2: drip-feed request line without '\\n' terminated by total deadline (not per-byte timeout)",
          peer_closed && elapsed_req_line >= 120 && elapsed_req_line <= 550);
    check("R2: active HTTP worker count returns to 0 after request-line deadline",
          xhttpd_get_active_http_workers() == 0);

    /* Phase 2: Send request line quickly, then drip-feed header line 1 byte every 25 ms without '\n' */
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    xhttpd_spawn_client_worker_for_test(sv[0]);
    const char *hdr_prefix = "GET /.xhttpd/health HTTP/1.1\r\nX-Drip: ";
    send(sv[1], hdr_prefix, strlen(hdr_prefix), MSG_NOSIGNAL);

    t0 = now_ms();
    peer_closed = 0;
    for (int i = 0; i < 30; i++) {
        usleep(25000);
        char ch = 'x';
        ssize_t w = send(sv[1], &ch, 1, MSG_NOSIGNAL);
        char rbuf[16];
        ssize_t r = recv(sv[1], rbuf, sizeof(rbuf), MSG_DONTWAIT);
        if (w <= 0 || r == 0 || (r < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR)) {
            peer_closed = 1;
            break;
        }
    }
    uint64_t elapsed_hdr_line = now_ms() - t0;
    close(sv[1]);

    for (int i = 0; i < 20 && xhttpd_get_active_http_workers() > 0; i++) {
        usleep(10000);
    }

    check("R2: drip-feed header line without '\\n' terminated by total deadline",
          peer_closed && elapsed_hdr_line >= 120 && elapsed_hdr_line <= 550);
    check("R2: active HTTP worker count returns to 0 after header-line deadline",
          xhttpd_get_active_http_workers() == 0);

    xhttpd_set_http_req_timeout_ms(30000);
}

static pthread_mutex_t g_r3_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t  g_r3_cond = PTHREAD_COND_INITIALIZER;
static int g_r3_entered = 0;
static int g_r3_release = 0;

static void r3_reader_exit_barrier(void) {
    pthread_mutex_lock(&g_r3_mu);
    g_r3_entered++;
    pthread_cond_broadcast(&g_r3_cond);
    while (!g_r3_release) {
        pthread_cond_wait(&g_r3_cond, &g_r3_mu);
    }
    pthread_mutex_unlock(&g_r3_mu);
}

static void test_r3_reader_running_barrier_and_slot_reuse(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(&cfg, 0, uuid);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    pthread_mutex_lock(&g_r3_mu);
    g_r3_entered = 0;
    g_r3_release = 0;
    pthread_mutex_unlock(&g_r3_mu);
    fp_test_set_reader_exit_hook(r3_reader_exit_barrier);

    /* 1. Open Session A -> occupies slot 0 */
    uint8_t osid_a[16] = { 0xA1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t sid_a[16] = {0};
    int pfd_a = -1;
    check("R3: open Session A succeeds",
          open_plain_fp_session(&cfg, uuid, osid_a, 500, sid_a, &pfd_a) == 0);
    check("R3: Session A is allocated in slot 0",
          fp_test_find_session_slot(sid_a) == 0);

    /* 2. Close Session A -> sets active=false, closed=true, ref_count=0, while reader thread enters exit barrier */
    http_req_t creq;
    build_signed_fp_http_req(&creq, &cfg, uuid, FP_OP_CLOSE, sid_a, 0, NULL, 0, 501);
    int sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &creq, &cfg);
    close(sv[0]);
    close(sv[1]);
    free(creq.body);
    if (pfd_a >= 0) close(pfd_a);

    /* Wait until Session A's reader thread is paused inside r3_reader_exit_barrier (reader_running is still true!) */
    pthread_mutex_lock(&g_r3_mu);
    while (g_r3_entered == 0) {
        pthread_cond_wait(&g_r3_cond, &g_r3_mu);
    }
    pthread_mutex_unlock(&g_r3_mu);

    /* 3. Open Session B while Session A's reader_running is still true -> MUST NOT reuse slot 0 */
    uint8_t osid_b[16] = { 0xB2, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t sid_b[16] = {0};
    int pfd_b = -1;
    check("R3: open Session B while Session A reader is at exit barrier succeeds",
          open_plain_fp_session(&cfg, uuid, osid_b, 502, sid_b, &pfd_b) == 0);
    check("R3: slot 0 is NOT reused while reader_running == true (Session B placed in slot 1)",
          fp_test_find_session_slot(sid_b) == 1);

    /* 4. Release barrier so Session A's reader sets reader_running = false under sess->mu */
    fp_test_set_reader_exit_hook(NULL);
    pthread_mutex_lock(&g_r3_mu);
    g_r3_release = 1;
    pthread_cond_broadcast(&g_r3_cond);
    pthread_mutex_unlock(&g_r3_mu);
    usleep(30000);

    /* 5. Open Session C -> now slot 0 has reader_running == false and ref_count == 0, so slot 0 MUST be reused */
    uint8_t osid_c[16] = { 0xC3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t sid_c[16] = {0};
    int pfd_c = -1;
    check("R3: open Session C after Session A reader exits succeeds",
          open_plain_fp_session(&cfg, uuid, osid_c, 503, sid_c, &pfd_c) == 0);
    check("R3: slot 0 IS reused once reader_running == false and ref_count == 0",
          fp_test_find_session_slot(sid_c) == 0);

    /* Cleanup Sessions B and C */
    if (pfd_b >= 0) close(pfd_b);
    if (pfd_c >= 0) close(pfd_c);
    fp_engine_close_all();
    usleep(30000);
}

static void build_custom_signed_fp_http_req(http_req_t *req, const char *client_id,
                                            const uint8_t *secret, size_t secret_len,
                                            uint8_t op, const uint8_t sid[16],
                                            const uint8_t *payload, size_t plen,
                                            uint32_t nonce_seq, bool tamper_sig) {
    memset(req, 0, sizeof(*req));
    snprintf(req->method, sizeof(req->method), "%s", "POST");
    snprintf(req->path, sizeof(req->path), "%s", "/api/v1/upload");
    snprintf(req->fp_version, sizeof(req->fp_version), "%s", FP_PROTO_VERSION);

    req->body_len = FP_REQ_HEADER_SIZE + plen;
    req->body = calloc(1, req->body_len);
    memcpy(req->body, FP_PROTO_MAGIC, 4);
    req->body[4] = op;
    req->body[5] = 0;
    memcpy(req->body + 6, sid, 16);
    put_be32_t(req->body + 46, 65536);
    put_be32_t(req->body + 50, (uint32_t)plen);
    if (plen > 0 && payload) {
        memcpy(req->body + FP_REQ_HEADER_SIZE, payload, plen);
    }

    char ts_str[32], nonce_hex[33], sig_hex[65];
    snprintf(ts_str, sizeof(ts_str), "%llu", (unsigned long long)epoch_ms());
    snprintf(nonce_hex, sizeof(nonce_hex), "aabbccddeeff001122334455%08x", nonce_seq);

    uint8_t body_sha[32];
    sha256_hash(req->body, req->body_len, body_sha);

    uint8_t msg[1024];
    size_t off = 0;
    const char *parts[6] = { FP_PROTO_VERSION, req->method, req->path, client_id, ts_str, nonce_hex };
    for (int i = 0; i < 6; i++) {
        size_t l = strlen(parts[i]);
        memcpy(msg + off, parts[i], l);
        off += l;
        msg[off++] = 0;
    }
    memcpy(msg + off, body_sha, 32);
    off += 32;

    uint8_t sig[32];
    hmac_sha256(secret, secret_len, msg, off, sig);
    if (tamper_sig) {
        sig[0] ^= 0xFF;
    }
    bytes_to_hex(sig, 32, sig_hex);
    snprintf(req->fp_auth, sizeof(req->fp_auth), "%s:%s:%s:%s", client_id, ts_str, nonce_hex, sig_hex);
}

static int exec_raw_fp_req_status(const xhttpd_config_t *cfg, http_req_t *req) {
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) return -1;
    handle_fp_http_request(sv[0], req, cfg);
    close(sv[0]);
    free(req->body);
    req->body = NULL;
    char resp[512] = {0};
    recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    int code = 0;
    sscanf(resp, "HTTP/%*s %d", &code);
    return code;
}

static void test_fp_v2_auth_and_revocation(void) {
    fp_engine_init();

    const char *uuid_str = "b831381d-6324-4d53-ad4f-8cda48b30811";
    const char *uuid_nodash = "b831381d63244d53ad4f8cda48b30811";
    const char *fp_id = "fp_9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d";
    uint8_t sec1[32], sec2[32], wrong_sec[32];
    make_test_v2_secret(10, sec1);
    make_test_v2_secret(20, sec2);
    make_test_v2_secret(30, wrong_sec);
    char sec1_hex[65], sec2_hex[65];
    bytes_to_hex(sec1, 32, sec1_hex);
    bytes_to_hex(sec2, 32, sec2_hex);

    char tmp_cfg[] = "/tmp/xhttpd-fpv2-XXXXXX";
    int cfd = mkstemp(tmp_cfg);
    char cfg_text[512];
    int clen = snprintf(cfg_text, sizeof(cfg_text),
                        "mode=direct\nallow_private=1\nclient=%s\nfp_cred=%s:%s\n",
                        uuid_str, fp_id, sec1_hex);
    ssize_t w = write(cfd, cfg_text, (size_t)clen);
    (void)w;
    close(cfd);

    xhttpd_config_t cfg;
    check("FPv2: load_config_file parses fp_cred=<id>:<64hex>", load_config_file(tmp_cfg, &cfg) == 0);
    check("FPv2: explicit fp_cred loaded and VLESS UUID not auto-enrolled",
          find_cred(&cfg, fp_id) != NULL && find_cred(&cfg, uuid_str) == NULL);

    /* Verify all 4 legacy UUID-derived keys, wrong key, and tampered signature return 401 */
    uint8_t u_bytes[16], u_v1[32], u_sha[32];
    parse_uuid_str(uuid_str, u_bytes);
    char v1_pre[64];
    int v1_len = snprintf(v1_pre, sizeof(v1_pre), "awgm-fp-v1:%s", uuid_str);
    sha256_hash((const uint8_t *)v1_pre, (size_t)v1_len, u_v1);
    sha256_hash(u_bytes, 16, u_sha);

    uint8_t osid[16] = { 0x55, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    const uint8_t target[] = "127.0.0.1:9";
    http_req_t req;

    build_custom_signed_fp_http_req(&req, uuid_str, u_v1, 32, FP_OP_OPEN, osid, target, sizeof(target) - 1, 701, false);
    check("FPv2: rejects legacy SHA256(awgm-fp-v1:<uuid>) signature with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    build_custom_signed_fp_http_req(&req, uuid_nodash, u_sha, 32, FP_OP_OPEN, osid, target, sizeof(target) - 1, 702, false);
    check("FPv2: rejects legacy SHA256(uuid_bytes) signature with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    build_custom_signed_fp_http_req(&req, uuid_nodash, (const uint8_t *)uuid_nodash, 32, FP_OP_OPEN, osid, target, sizeof(target) - 1, 703, false);
    check("FPv2: rejects legacy hex_no_dash signature with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    build_custom_signed_fp_http_req(&req, uuid_str, u_bytes, 16, FP_OP_OPEN, osid, target, sizeof(target) - 1, 704, false);
    check("FPv2: rejects legacy 16-byte raw UUID signature with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    build_custom_signed_fp_http_req(&req, fp_id, wrong_sec, 32, FP_OP_OPEN, osid, target, sizeof(target) - 1, 705, false);
    check("FPv2: rejects valid fp_id signed with wrong 32-byte key with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    build_custom_signed_fp_http_req(&req, fp_id, sec1, 32, FP_OP_OPEN, osid, target, sizeof(target) - 1, 706, true);
    check("FPv2: rejects tampered HMAC signature with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    /* Open an active session with (fp_id, sec1) */
    uint8_t sid1[16] = {0};
    int pfd1 = -1;
    check("FPv2: open active session with (fp_id, sec1) succeeds",
          open_plain_fp_session(&cfg, fp_id, osid, 710, sid1, &pfd1) == 0);
    check("FPv2: active session is present before credential rotation",
          fp_test_find_session_slot(sid1) >= 0);

    /* Rotate fp_id secret to sec2 via load_config_file -> must immediately close sid1 */
    cfd = open(tmp_cfg, O_WRONLY | O_TRUNC);
    clen = snprintf(cfg_text, sizeof(cfg_text),
                    "mode=direct\nallow_private=1\nclient=%s\nfp_cred=%s:%s\n",
                    uuid_str, fp_id, sec2_hex);
    w = write(cfd, cfg_text, (size_t)clen);
    (void)w;
    close(cfd);
    check("FPv2: reload config with rotated secret sec2 succeeds",
          load_config_file(tmp_cfg, &cfg) == 0);
    check("FPv2: already-open session sid1 is immediately closed upon secret rotation",
          fp_test_find_session_slot(sid1) < 0);

    /* Old secret sec1 must be rejected with 401 */
    build_custom_signed_fp_http_req(&req, fp_id, sec1, 32, FP_OP_DOWN, sid1, NULL, 0, 711, false);
    check("FPv2: old secret sec1 rejected with 401 after rotation",
          exec_raw_fp_req_status(&cfg, &req) == 401);

    /* New secret sec2 on old sid1 must get 410 Gone (session was closed) */
    build_custom_signed_fp_http_req(&req, fp_id, sec2, 32, FP_OP_DOWN, sid1, NULL, 0, 712, false);
    check("FPv2: new secret sec2 on pre-rotation session sid1 gets 410 Gone",
          exec_raw_fp_req_status(&cfg, &req) == 410);
    if (pfd1 >= 0) close(pfd1);

    /* Open new session with (fp_id, sec2), then revoke fp_id completely */
    uint8_t osid2[16] = { 0x66, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t sid2[16] = {0};
    int pfd2 = -1;
    check("FPv2: open new session with rotated (fp_id, sec2) succeeds",
          open_plain_fp_session(&cfg, fp_id, osid2, 713, sid2, &pfd2) == 0);
    check("FPv2: session sid2 is active before revocation",
          fp_test_find_session_slot(sid2) >= 0);

    cfd = open(tmp_cfg, O_WRONLY | O_TRUNC);
    clen = snprintf(cfg_text, sizeof(cfg_text), "mode=direct\nallow_private=1\nclient=%s\n", uuid_str);
    w = write(cfd, cfg_text, (size_t)clen);
    (void)w;
    close(cfd);
    check("FPv2: reload config with revoked fp_cred succeeds",
          load_config_file(tmp_cfg, &cfg) == 0);
    check("FPv2: active session sid2 is immediately closed upon credential revocation",
          fp_test_find_session_slot(sid2) < 0);

    build_custom_signed_fp_http_req(&req, fp_id, sec2, 32, FP_OP_DOWN, sid2, NULL, 0, 714, false);
    check("FPv2: revoked credential rejected with 401",
          exec_raw_fp_req_status(&cfg, &req) == 401);
    if (pfd2 >= 0) close(pfd2);
    unlink(tmp_cfg);
    fp_engine_close_all();
    usleep(30000);
}

static pthread_mutex_t g_barrier_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_barrier_cond = PTHREAD_COND_INITIALIZER;
static bool g_barrier_reached = false;
static bool g_barrier_allow = false;

static void post_dial_barrier_hook(void) {
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_reached = true;
    pthread_cond_broadcast(&g_barrier_cond);
    while (!g_barrier_allow) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);
}

static void pre_dial_barrier_hook(void) {
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_reached = true;
    pthread_cond_broadcast(&g_barrier_cond);
    while (!g_barrier_allow) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);
}

static void pre_auth_barrier_hook(void) {
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_reached = true;
    pthread_cond_broadcast(&g_barrier_cond);
    while (!g_barrier_allow) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);
}

static void write_barrier_hook(void) {
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_reached = true;
    pthread_cond_broadcast(&g_barrier_cond);
    while (!g_barrier_allow) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);
}

static void slow_write_progress_hook(void) {
    usleep(25000); /* 25 ms per 4 KiB chunk (< 50 ms poll slice, so poll never times out) */
}

static bool verify_socket_closed_or_reset(int fd, int timeout_ms) {
    if (fd < 0) return false;
    uint64_t deadline = now_ms() + (uint64_t)timeout_ms;
    uint8_t drain[4096];
    for (;;) {
        uint64_t now = now_ms();
        if (now >= deadline) return false; /* Poll timeout must NEVER pass */
        int wait_ms = (int)(deadline - now);
        if (wait_ms > 50) wait_ms = 50;
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        if (pr == 0) {
            continue;
        }
        ssize_t rn = recv(fd, (char *)drain, sizeof(drain), 0);
        if (rn == 0) {
            return true; /* Clean EOF after peer close/shutdown */
        }
        if (rn < 0) {
#ifdef _WIN32
            int werr = WSAGetLastError();
            if (werr == WSAECONNRESET || werr == WSAECONNABORTED || werr == WSAESHUTDOWN) return true;
#endif
            if (errno == ECONNRESET || errno == ECONNABORTED || errno == EPIPE) return true;
            return false;
        }
        /* rn > 0: drained buffered payload bytes; keep reading until EOF/reset or deadline */
    }
}

typedef struct {
    const xhttpd_config_t *cfg;
    const char *client_id;
    uint8_t open_sid[16];
    uint32_t nonce_seq;
    uint16_t target_port;
    const uint8_t *vless_initial_payload;
    size_t vless_initial_payload_len;
    int resp_code;
    uint8_t fp_status;
    uint8_t assigned_sid[16];
    volatile int done;
} async_fp_open_t;

static bool wait_async_fp_open_done(async_fp_open_t *a, int timeout_ms) {
    uint64_t deadline = now_ms() + (uint64_t)timeout_ms;
    while (__atomic_load_n(&a->done, __ATOMIC_ACQUIRE) == 0) {
        if (now_ms() >= deadline) return false;
        usleep(2000);
    }
    return true;
}

static void *async_fp_open_thread(void *arg) {
    async_fp_open_t *a = (async_fp_open_t *)arg;
    http_req_t req;

    if (a->vless_initial_payload && a->vless_initial_payload_len > 0) {
        size_t vless_len = 26 + a->vless_initial_payload_len;
        uint8_t *vless_buf = (uint8_t *)malloc(vless_len);
        memset(vless_buf, 0, vless_len);
        vless_buf[0] = 0x00;
        if (a->cfg && a->cfg->num_clients > 0) {
            memcpy(vless_buf + 1, a->cfg->clients[0].uuid, 16);
        }
        vless_buf[17] = 0x00;
        vless_buf[18] = 0x01; /* VLESS_CMD_TCP */
        vless_buf[19] = (uint8_t)((a->target_port >> 8) & 0xff);
        vless_buf[20] = (uint8_t)(a->target_port & 0xff);
        vless_buf[21] = 0x01; /* VLESS_ADDR_IPV4 */
        vless_buf[22] = 127; vless_buf[23] = 0; vless_buf[24] = 0; vless_buf[25] = 1;
        memcpy(vless_buf + 26, a->vless_initial_payload, a->vless_initial_payload_len);
        build_signed_fp_http_req(&req, a->cfg, a->client_id, FP_OP_OPEN, a->open_sid, 0,
                                 vless_buf, vless_len, a->nonce_seq);
        free(vless_buf);
    } else {
        char target_str[64];
        snprintf(target_str, sizeof(target_str), "127.0.0.1:%u", (unsigned)a->target_port);
        build_signed_fp_http_req(&req, a->cfg, a->client_id, FP_OP_OPEN, a->open_sid, 0,
                                 (const uint8_t *)target_str, strlen(target_str), a->nonce_seq);
    }

    int sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
    handle_fp_http_request(sv[0], &req, a->cfg);
    close(sv[0]);
    free(req.body);

    char resp[1024] = {0};
    ssize_t rn = recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    if (rn > 0) {
        if (strstr(resp, "HTTP/1.1 200 ")) a->resp_code = 200;
        else if (strstr(resp, "HTTP/1.1 401 ")) a->resp_code = 401;
        else if (strstr(resp, "HTTP/1.1 502 ")) a->resp_code = 502;
        else a->resp_code = -1;

        char *hdr_end = strstr(resp, "\r\n\r\n");
        if (hdr_end && (size_t)rn >= (size_t)(hdr_end + 4 - resp) + FP_RESP_HEADER_SIZE) {
            const uint8_t *fpr = (const uint8_t *)(hdr_end + 4);
            a->fp_status = fpr[4];
            memcpy(a->assigned_sid, fpr + 6, 16);
        }
    } else {
        a->resp_code = -1;
    }
    __atomic_store_n(&a->done, 1, __ATOMIC_RELEASE);
    return NULL;
}

static void test_fp_v2_barrier_races(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;

    const char *uuid_str = "b831381d-6324-4d53-ad4f-8cda48b30811";
    parse_uuid_str(uuid_str, cfg.clients[0].uuid);
    format_uuid_canonical(cfg.clients[0].uuid, cfg.clients[0].uuid_str);
    bytes_to_hex(cfg.clients[0].uuid, 16, cfg.clients[0].hex_no_dash);

    const char *fp_id = "fp_0102030405060708090a0b0c0d0e0f10";
    uint8_t sec1[32], sec2[32];
    for (int i = 0; i < 32; i++) {
        sec1[i] = (uint8_t)(0x11 ^ (i * 5));
        sec2[i] = (uint8_t)(0x22 ^ (i * 7));
    }
    char sec1_hex[65], sec2_hex[65];
    bytes_to_hex(sec1, 32, sec1_hex);
    bytes_to_hex(sec2, 32, sec2_hex);

    add_explicit_fp_cred(&cfg, fp_id, sec1);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    /* Test 1: Post-dial barrier race */
    uint16_t lport = 0;
    int lfd = create_loopback_listener(&lport);
    accept_arg_t acc = { .lfd = lfd, .cfd = -1 };
    pthread_t acc_th;
    pthread_create(&acc_th, NULL, accept_one_thread, &acc);

    g_barrier_reached = false;
    g_barrier_allow = false;
    fp_test_set_dial_hooks(NULL, post_dial_barrier_hook);

    async_fp_open_t arg1;
    memset(&arg1, 0, sizeof(arg1));
    arg1.cfg = &cfg;
    arg1.client_id = fp_id;
    arg1.open_sid[0] = 0xE1;
    arg1.nonce_seq = 801;
    arg1.target_port = lport;

    pthread_t open_th;
    pthread_create(&open_th, NULL, async_fp_open_thread, &arg1);

    pthread_mutex_lock(&g_barrier_mu);
    while (!g_barrier_reached) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(acc_th, NULL);
    check("FPv2 barrier: backend TCP accepted connection during dial", acc.cfd >= 0);

    /* While worker is paused in post-dial hook, rotate credentials to sec2 */
    xhttpd_config_t cfg2 = cfg;
    cfg2.num_cfg_fp_creds = 0;
    cfg2.num_fp_creds = 0;
    add_explicit_fp_cred(&cfg2, fp_id, sec2);
    rebuild_fp_credentials(&cfg2);
    fp_engine_revoke_unauthorised(&cfg2);

    /* Unblock post-dial hook */
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_allow = true;
    pthread_cond_broadcast(&g_barrier_cond);
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(open_th, NULL);
    fp_test_set_dial_hooks(NULL, NULL);

    check("FPv2 barrier: worker gets 401 Unauthorized when credential rotated during dial",
          arg1.resp_code == 401);
    check("FPv2 barrier: no live session slot allocated for rotated session",
          fp_test_find_session_slot(arg1.assigned_sid) < 0);

    /* Verify backend connection was closed immediately by reading EOF / reset (never poll timeout) */
    check("FPv2 barrier: dialed backend socket was closed immediately upon revocation",
          verify_socket_closed_or_reset(acc.cfd, 500));
    if (acc.cfd >= 0) close(acc.cfd);
    close(lfd);

    /* Test 2: Pre-dial barrier race */
    lport = 0;
    lfd = create_loopback_listener(&lport);
    g_barrier_reached = false;
    g_barrier_allow = false;
    fp_test_set_dial_hooks(pre_dial_barrier_hook, NULL);

    /* Configure back to sec1 for pre-dial test */
    xhttpd_config_t cfg3 = cfg;
    cfg3.num_cfg_fp_creds = 0;
    cfg3.num_fp_creds = 0;
    add_explicit_fp_cred(&cfg3, fp_id, sec1);
    rebuild_fp_credentials(&cfg3);
    fp_engine_revoke_unauthorised(&cfg3);

    async_fp_open_t arg2;
    memset(&arg2, 0, sizeof(arg2));
    arg2.cfg = &cfg3;
    arg2.client_id = fp_id;
    arg2.open_sid[0] = 0xE2;
    arg2.nonce_seq = 802;
    arg2.target_port = lport;

    pthread_create(&open_th, NULL, async_fp_open_thread, &arg2);

    pthread_mutex_lock(&g_barrier_mu);
    while (!g_barrier_reached) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);

    /* Rotate to sec2 while paused before dial */
    cfg2.num_cfg_fp_creds = 0;
    cfg2.num_fp_creds = 0;
    add_explicit_fp_cred(&cfg2, fp_id, sec2);
    rebuild_fp_credentials(&cfg2);
    fp_engine_revoke_unauthorised(&cfg2);

    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_allow = true;
    pthread_cond_broadcast(&g_barrier_cond);
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(open_th, NULL);
    fp_test_set_dial_hooks(NULL, NULL);

    struct pollfd lpfd = { .fd = lfd, .events = POLLIN };
    check("FPv2 barrier: pre-dial revoked session never dials backend (0 connections accepted)",
          poll(&lpfd, 1, 100) == 0);
    close(lfd);

    check("FPv2 barrier: worker gets 401 when credential rotated before dial",
          arg2.resp_code == 401);
    check("FPv2 barrier: no session slot allocated for pre-dial rotated session",
          fp_test_find_session_slot(arg2.assigned_sid) < 0);

    /* Test 3: New OPEN with rotated key sec2 succeeds cleanly */
    uint8_t sid_ok[16];
    int pfd_ok = -1;
    uint8_t osid_ok[16] = { 0xE3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    check("FPv2 barrier: OPEN with active rotated key sec2 succeeds with 200 OK",
          open_plain_fp_session(&cfg2, fp_id, osid_ok, 803, sid_ok, &pfd_ok) == 0);
    check("FPv2 barrier: session for sec2 is active in session table",
          fp_test_find_session_slot(sid_ok) >= 0);
    if (pfd_ok >= 0) close(pfd_ok);

    fp_engine_close_all();
    usleep(20000);
}

static void test_fp_v2_pre_auth_revocation_barrier(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;

    const char *uuid_str = "b831381d-6324-4d53-ad4f-8cda48b30811";
    parse_uuid_str(uuid_str, cfg.clients[0].uuid);
    format_uuid_canonical(cfg.clients[0].uuid, cfg.clients[0].uuid_str);
    bytes_to_hex(cfg.clients[0].uuid, 16, cfg.clients[0].hex_no_dash);

    const char *fp_id = "fp_0102030405060708090a0b0c0d0e0f10";
    uint8_t sec1[32];
    for (int i = 0; i < 32; i++) {
        sec1[i] = (uint8_t)(0x11 ^ (i * 5));
    }
    add_explicit_fp_cred(&cfg, fp_id, sec1);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    check("FPv2 pre-auth: active cred count is 1 before test",
          fp_test_get_active_creds_count() == 1);

    uint16_t lport = 0;
    int lfd = create_loopback_listener(&lport);

    g_barrier_reached = false;
    g_barrier_allow = false;
    fp_test_set_pre_auth_hook(pre_auth_barrier_hook);

    async_fp_open_t arg;
    memset(&arg, 0, sizeof(arg));
    arg.cfg = &cfg; /* Worker holds pointer/snapshot with key K1 */
    arg.client_id = fp_id;
    arg.open_sid[0] = 0xAA;
    arg.nonce_seq = 901;
    arg.target_port = lport;

    pthread_t open_th;
    pthread_create(&open_th, NULL, async_fp_open_thread, &arg);

    /* Wait until worker enters pre_auth_hook (before acquiring g_fp_table_mu) */
    pthread_mutex_lock(&g_barrier_mu);
    while (!g_barrier_reached) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);

    /* While worker is waiting at barrier holding stale snapshot with K1, revoke all credentials */
    xhttpd_config_t empty_cfg = cfg;
    empty_cfg.num_cfg_fp_creds = 0;
    empty_cfg.num_fp_creds = 0;
    rebuild_fp_credentials(&empty_cfg);
    fp_engine_revoke_unauthorised(&empty_cfg);

    check("FPv2 pre-auth: active cred count is 0 after revocation",
          fp_test_get_active_creds_count() == 0);

    /* Unblock worker: worker enters g_fp_table_mu */
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_allow = true;
    pthread_cond_broadcast(&g_barrier_cond);
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(open_th, NULL);
    fp_test_set_pre_auth_hook(NULL);

    check("FPv2 pre-auth: worker returns 401 Unauthorized against stale snapshot",
          arg.resp_code == 401);
    check("FPv2 pre-auth: authoritative creds remain 0 (no stale snapshot restoration)",
          fp_test_get_active_creds_count() == 0);
    check("FPv2 pre-auth: no session allocated in table",
          fp_test_find_session_slot(arg.assigned_sid) < 0);

    struct pollfd lpfd = { .fd = lfd, .events = POLLIN };
    check("FPv2 pre-auth: 0 backend connections accepted",
          poll(&lpfd, 1, 100) == 0);
    close(lfd);
    fp_engine_close_all();
    usleep(20000);
}

static void test_fp_v2_open_initial_write_cancellation_and_table_availability(void) {
    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 2;

    const char *uuid1_str = "b831381d-6324-4d53-ad4f-8cda48b30811";
    parse_uuid_str(uuid1_str, cfg.clients[0].uuid);
    format_uuid_canonical(cfg.clients[0].uuid, cfg.clients[0].uuid_str);
    bytes_to_hex(cfg.clients[0].uuid, 16, cfg.clients[0].hex_no_dash);

    const char *uuid2_str = "c942492e-7435-4e64-be50-9deb59c41922";
    parse_uuid_str(uuid2_str, cfg.clients[1].uuid);
    format_uuid_canonical(cfg.clients[1].uuid, cfg.clients[1].uuid_str);
    bytes_to_hex(cfg.clients[1].uuid, 16, cfg.clients[1].hex_no_dash);

    const char *fp_id_1 = "fp_client_one_0102030405060708090a";
    const char *fp_id_2 = "fp_client_two_0102030405060708090b";
    uint8_t sec1[32], sec2[32];
    for (int i = 0; i < 32; i++) {
        sec1[i] = (uint8_t)(0x11 ^ (i * 5));
        sec2[i] = (uint8_t)(0x22 ^ (i * 7));
    }
    add_explicit_fp_cred(&cfg, fp_id_1, sec1);
    add_explicit_fp_cred(&cfg, fp_id_2, sec2);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    check("Write cancel: both clients authorized in table",
          fp_test_get_active_creds_count() == 2);

    /* 1. Target 1 listener for Client 1 */
    uint16_t lport1 = 0;
    int lfd1 = create_loopback_listener(&lport1);
    accept_arg_t acc1 = { .lfd = lfd1, .cfd = -1 };
    pthread_t acc_th1;
    pthread_create(&acc_th1, NULL, accept_one_thread, &acc1);

    /* Prepare 1024-byte initial upload payload (VLESS framed) */
    uint8_t init_payload[1024];
    memset(init_payload, 0x5a, sizeof(init_payload));

    g_barrier_reached = false;
    g_barrier_allow = false;
    fp_test_set_write_hook(write_barrier_hook);

    async_fp_open_t arg1;
    memset(&arg1, 0, sizeof(arg1));
    arg1.cfg = &cfg;
    arg1.client_id = fp_id_1;
    arg1.open_sid[0] = 0xAA;
    arg1.nonce_seq = 961;
    arg1.target_port = lport1;
    arg1.vless_initial_payload = init_payload;
    arg1.vless_initial_payload_len = sizeof(init_payload);

    pthread_t open_th1;
    pthread_create(&open_th1, NULL, async_fp_open_thread, &arg1);

    /* Wait until Client 1 worker has connected to target and is inside write_cancellable */
    pthread_mutex_lock(&g_barrier_mu);
    while (!g_barrier_reached) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(acc_th1, NULL);
    check("Write cancel: Target 1 listener accepted TCP connection", acc1.cfd >= 0);

    /* Verify table mutex is NOT held during write_cancellable */
    check("Write cancel: g_fp_table_mu is free while Client 1 is writing",
          fp_test_trylock_table());

    /* 2. Perform independent OPEN for Client 2 while Client 1 is blocked in write_cancellable */
    uint8_t sid2_ok[16];
    int pfd2_ok = -1;
    uint8_t osid2_ok[16] = { 0xB2, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    check("Write cancel: parallel independent OPEN for Client 2 succeeds with 200 OK",
          open_plain_fp_session(&cfg, fp_id_2, osid2_ok, 777, sid2_ok, &pfd2_ok) == 0);
    check("Write cancel: Client 2 session is active in session table",
          fp_test_find_session_slot(sid2_ok) >= 0);

    /* 3. Revoke Client 1 while Client 1 is still blocked in write_cancellable */
    xhttpd_config_t cfg_rev = cfg;
    cfg_rev.num_cfg_fp_creds = 0;
    cfg_rev.num_fp_creds = 0;
    add_explicit_fp_cred(&cfg_rev, fp_id_2, sec2); /* Only Client 2 retained */
    rebuild_fp_credentials(&cfg_rev);

    uint64_t t_rev0 = now_ms();
    fp_engine_revoke_unauthorised(&cfg_rev);
    uint64_t elapsed_rev = now_ms() - t_rev0;
    check("Write cancel: fp_engine_revoke_unauthorised completes immediately (< 50ms)",
          elapsed_rev < 50);

    /* 4. Unblock Client 1 write hook */
    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_allow = true;
    pthread_cond_broadcast(&g_barrier_cond);
    pthread_mutex_unlock(&g_barrier_mu);

    /* Verify Client 1 worker finishes within bounded deadline (< 500ms) before joining */
    uint64_t t_join0 = now_ms();
    bool finished1 = wait_async_fp_open_done(&arg1, 500);
    uint64_t elapsed_join = now_ms() - t_join0;
    check("Write cancel (subtest 1): worker finishes within bounded deadline (< 500ms)",
          finished1 && elapsed_join < 500);
    if (finished1) {
        pthread_join(open_th1, NULL);
    }
    fp_test_set_write_hook(NULL);

    check("Write cancel (subtest 1): worker gets 401 Unauthorized (FP_STATUS_UNAUTHORIZED) for revoked client",
          arg1.resp_code == 401 && arg1.fp_status == FP_STATUS_UNAUTHORIZED);
    check("Write cancel (subtest 1): no session slot allocated for Client 1",
          fp_test_find_session_slot(arg1.assigned_sid) < 0);

    /* Verify Target 1 socket received real EOF / reset (never poll timeout) */
    check("Write cancel (subtest 1): Target 1 socket was closed/shut down upon revocation",
          verify_socket_closed_or_reset(acc1.cfd, 500));

    /* 5. Verify Client 2 session was unaffected and remains active */
    check("Write cancel (subtest 1): Client 2 session remains active in session table",
          fp_test_find_session_slot(sid2_ok) >= 0);

    if (acc1.cfd >= 0) close(acc1.cfd);
    close(lfd1);
    if (pfd2_ok >= 0) close(pfd2_ok);
    fp_engine_close_all();
    usleep(20000);

    /*
     * Subtest 2: Real socket buffer saturation (NO write hook) + revocation during blocked send.
     * Proves non-blocking send + poll loop on an actually saturated socket buffer (WSAEWOULDBLOCK / EAGAIN)
     * and immediate abort upon credential revocation well before the 5000ms write timeout.
     */
    rebuild_fp_credentials(&cfg);
    fp_engine_revoke_unauthorised(&cfg);
    fp_test_set_initial_write_timeout_ms(5000);
    fp_test_set_target_sndbuf(1024);

    uint16_t lport2 = 0;
    int lfd2 = create_loopback_listener(&lport2);
    int rcvbuf_small = 1024;
    setsockopt(lfd2, SOL_SOCKET, SO_RCVBUF, (const char *)&rcvbuf_small, sizeof(rcvbuf_small));
    accept_arg_t acc2 = { .lfd = lfd2, .cfd = -1 };
    pthread_t acc_th2;
    pthread_create(&acc_th2, NULL, accept_one_thread, &acc2);

    size_t big_len = 240 * 1024; /* 240 KiB (< FP_MAX_REQ_PAYLOAD=256 KiB) to saturate loopback buffers */
    uint8_t *big_payload = (uint8_t *)malloc(big_len);
    memset(big_payload, 0x3C, big_len);

    int wb_before2 = fp_test_get_write_wouldblock_count();
    async_fp_open_t arg2;
    memset(&arg2, 0, sizeof(arg2));
    arg2.cfg = &cfg;
    arg2.client_id = fp_id_1;
    arg2.open_sid[0] = 0xAB;
    arg2.nonce_seq = 962;
    arg2.target_port = lport2;
    arg2.vless_initial_payload = big_payload;
    arg2.vless_initial_payload_len = big_len;

    pthread_t open_th2;
    pthread_create(&open_th2, NULL, async_fp_open_thread, &arg2);

    pthread_join(acc_th2, NULL);
    check("Write cancel (subtest 2): Target 2 accepted TCP connection", acc2.cfd >= 0);

    /* Wait until write_cancellable hits real EWOULDBLOCK / WSAEWOULDBLOCK on saturated socket */
    uint64_t wait_wb_deadline = now_ms() + 1000;
    while (fp_test_get_write_wouldblock_count() == wb_before2 && now_ms() < wait_wb_deadline) {
        usleep(5000);
    }
    check("Write cancel (subtest 2): real send() hit EWOULDBLOCK/WSAEWOULDBLOCK on saturated target buffer",
          fp_test_get_write_wouldblock_count() > wb_before2);
    check("Write cancel (subtest 2): g_fp_table_mu is free while stalled on saturated socket buffer",
          fp_test_trylock_table());

    /* Revoke Client 1 while stalled on real saturated socket buffer */
    uint64_t t_rev2 = now_ms();
    fp_engine_revoke_unauthorised(&cfg_rev);
    bool finished2 = wait_async_fp_open_done(&arg2, 500);
    uint64_t elapsed2 = now_ms() - t_rev2;
    check("Write cancel (subtest 2): stalled non-blocking write aborts quickly (< 500ms) upon revocation",
          finished2 && elapsed2 < 500);
    if (finished2) {
        pthread_join(open_th2, NULL);
    }
    check("Write cancel (subtest 2): revoked saturated write returns 401 (FP_STATUS_UNAUTHORIZED)",
          arg2.resp_code == 401 && arg2.fp_status == FP_STATUS_UNAUTHORIZED);
    check("Write cancel (subtest 2): no session slot allocated for Client 1",
          fp_test_find_session_slot(arg2.assigned_sid) < 0);
    check("Write cancel (subtest 2): Target 2 socket closed/reset upon revocation",
          verify_socket_closed_or_reset(acc2.cfd, 500));
    if (acc2.cfd >= 0) close(acc2.cfd);
    close(lfd2);

    /*
     * Subtest 3: Non-reading target timeout WITHOUT credential revocation -> 502 Bad Gateway.
     * Proves that when the target stops reading and key remains valid, write_cancellable
     * enforces its absolute deadline on its own and returns 502 (FP_STATUS_DIAL_ERROR), not 401.
     */
    rebuild_fp_credentials(&cfg);
    fp_engine_revoke_unauthorised(&cfg);
    fp_test_set_initial_write_timeout_ms(180);
    fp_test_set_target_sndbuf(1024);

    uint16_t lport3 = 0;
    int lfd3 = create_loopback_listener(&lport3);
    setsockopt(lfd3, SOL_SOCKET, SO_RCVBUF, (const char *)&rcvbuf_small, sizeof(rcvbuf_small));
    accept_arg_t acc3 = { .lfd = lfd3, .cfd = -1 };
    pthread_t acc_th3;
    pthread_create(&acc_th3, NULL, accept_one_thread, &acc3);

    int wb_before3 = fp_test_get_write_wouldblock_count();
    async_fp_open_t arg3;
    memset(&arg3, 0, sizeof(arg3));
    arg3.cfg = &cfg;
    arg3.client_id = fp_id_1;
    arg3.open_sid[0] = 0xAC;
    arg3.nonce_seq = 963;
    arg3.target_port = lport3;
    arg3.vless_initial_payload = big_payload;
    arg3.vless_initial_payload_len = big_len;

    uint64_t t_start3 = now_ms();
    pthread_t open_th3;
    pthread_create(&open_th3, NULL, async_fp_open_thread, &arg3);

    pthread_join(acc_th3, NULL);
    check("Write timeout (subtest 3): Target 3 accepted TCP connection", acc3.cfd >= 0);

    bool finished3 = wait_async_fp_open_done(&arg3, 1000);
    uint64_t elapsed3 = now_ms() - t_start3;
    check("Write timeout (subtest 3): non-reading target times out via absolute deadline (< 600ms)",
          finished3 && elapsed3 >= 150 && elapsed3 < 600);
    if (finished3) {
        pthread_join(open_th3, NULL);
    }
    check("Write timeout (subtest 3): real send() hit EWOULDBLOCK before timeout",
          fp_test_get_write_wouldblock_count() > wb_before3);
    check("Write timeout (subtest 3): valid client gets 502 Bad Gateway (FP_STATUS_DIAL_ERROR), not 401",
          arg3.resp_code == 502 && arg3.fp_status == FP_STATUS_DIAL_ERROR);
    check("Write timeout (subtest 3): no session published on initial write timeout",
          fp_test_find_session_slot(arg3.assigned_sid) < 0);
    check("Write timeout (subtest 3): Target 3 socket closed upon write timeout",
          verify_socket_closed_or_reset(acc3.cfd, 500));
    if (acc3.cfd >= 0) close(acc3.cfd);
    close(lfd3);
    free(big_payload);

    /*
     * Subtest 4: Continuous slow progress timeout WITHOUT credential revocation -> 502 Bad Gateway.
     * Each 4 KiB chunk succeeds without poll timing out (25ms per chunk < 50ms slice),
     * proving that the absolute deadline is enforced on every loop iteration even when poll never returns 0.
     */
    fp_test_set_target_sndbuf(0);
    fp_test_set_initial_write_timeout_ms(160);
    fp_test_set_write_hook(slow_write_progress_hook);

    uint16_t lport4 = 0;
    int lfd4 = create_loopback_listener(&lport4);
    accept_arg_t acc4 = { .lfd = lfd4, .cfd = -1 };
    pthread_t acc_th4;
    pthread_create(&acc_th4, NULL, accept_one_thread, &acc4);

    size_t slow_len = 64 * 1024; /* 16 chunks * 25ms = 400ms > 160ms deadline */
    uint8_t *slow_payload = (uint8_t *)malloc(slow_len);
    memset(slow_payload, 0x4D, slow_len);

    async_fp_open_t arg4;
    memset(&arg4, 0, sizeof(arg4));
    arg4.cfg = &cfg;
    arg4.client_id = fp_id_1;
    arg4.open_sid[0] = 0xAD;
    arg4.nonce_seq = 964;
    arg4.target_port = lport4;
    arg4.vless_initial_payload = slow_payload;
    arg4.vless_initial_payload_len = slow_len;

    uint64_t t_start4 = now_ms();
    pthread_t open_th4;
    pthread_create(&open_th4, NULL, async_fp_open_thread, &arg4);

    pthread_join(acc_th4, NULL);
    check("Write slow-progress (subtest 4): Target 4 accepted TCP connection", acc4.cfd >= 0);

    bool finished4 = wait_async_fp_open_done(&arg4, 800);
    uint64_t elapsed4 = now_ms() - t_start4;
    fp_test_set_write_hook(NULL);
    check("Write slow-progress (subtest 4): continuous slow write aborted by absolute deadline (< 350ms)",
          finished4 && elapsed4 >= 140 && elapsed4 < 350);
    if (finished4) {
        pthread_join(open_th4, NULL);
    }
    check("Write slow-progress (subtest 4): valid client gets 502 Bad Gateway (FP_STATUS_DIAL_ERROR)",
          arg4.resp_code == 502 && arg4.fp_status == FP_STATUS_DIAL_ERROR);
    check("Write slow-progress (subtest 4): no session published on slow-progress timeout",
          fp_test_find_session_slot(arg4.assigned_sid) < 0);
    check("Write slow-progress (subtest 4): Target 4 socket closed upon slow-progress timeout",
          verify_socket_closed_or_reset(acc4.cfd, 500));
    if (acc4.cfd >= 0) close(acc4.cfd);
    close(lfd4);
    free(slow_payload);

    /*
     * Subtest 5: Immediate target socket reset during initial write with valid key -> 502 Bad Gateway.
     */
    fp_test_set_initial_write_timeout_ms(5000);
    g_barrier_reached = false;
    g_barrier_allow = false;
    fp_test_set_write_hook(write_barrier_hook);

    uint16_t lport5 = 0;
    int lfd5 = create_loopback_listener(&lport5);
    accept_arg_t acc5 = { .lfd = lfd5, .cfd = -1 };
    pthread_t acc_th5;
    pthread_create(&acc_th5, NULL, accept_one_thread, &acc5);

    size_t rst_len = 16 * 1024;
    uint8_t *rst_payload = (uint8_t *)malloc(rst_len);
    memset(rst_payload, 0x5E, rst_len);

    async_fp_open_t arg5;
    memset(&arg5, 0, sizeof(arg5));
    arg5.cfg = &cfg;
    arg5.client_id = fp_id_1;
    arg5.open_sid[0] = 0xAE;
    arg5.nonce_seq = 965;
    arg5.target_port = lport5;
    arg5.vless_initial_payload = rst_payload;
    arg5.vless_initial_payload_len = rst_len;

    pthread_t open_th5;
    pthread_create(&open_th5, NULL, async_fp_open_thread, &arg5);

    pthread_mutex_lock(&g_barrier_mu);
    while (!g_barrier_reached) {
        pthread_cond_wait(&g_barrier_cond, &g_barrier_mu);
    }
    pthread_mutex_unlock(&g_barrier_mu);

    pthread_join(acc_th5, NULL);
    check("Write error (subtest 5): Target 5 accepted TCP connection", acc5.cfd >= 0);

    /* Force hard RST on Target 5 socket while Client 1 key remains valid */
    struct linger sl = { .l_onoff = 1, .l_linger = 0 };
    setsockopt(acc5.cfd, SOL_SOCKET, SO_LINGER, (const char *)&sl, sizeof(sl));
    close(acc5.cfd);
    acc5.cfd = -1;
    usleep(10000);

    pthread_mutex_lock(&g_barrier_mu);
    g_barrier_allow = true;
    pthread_cond_broadcast(&g_barrier_cond);
    pthread_mutex_unlock(&g_barrier_mu);

    bool finished5 = wait_async_fp_open_done(&arg5, 500);
    fp_test_set_write_hook(NULL);
    check("Write error (subtest 5): worker finishes within 500ms after peer reset", finished5);
    if (finished5) {
        pthread_join(open_th5, NULL);
    }
    free(rst_payload);
    check("Write error (subtest 5): valid client gets 502 Bad Gateway (FP_STATUS_DIAL_ERROR) on peer reset",
          arg5.resp_code == 502 && arg5.fp_status == FP_STATUS_DIAL_ERROR);
    check("Write error (subtest 5): no session published on peer reset",
          fp_test_find_session_slot(arg5.assigned_sid) < 0);
    close(lfd5);

    fp_test_set_initial_write_timeout_ms(5000);
    fp_test_set_target_sndbuf(0);
    fp_engine_close_all();
    usleep(20000);
}

static void test_xhttpd_reload_endpoint_security(void) {
    char tmp_cfg[256];
#ifdef _WIN32
    const char *tmp_dir = getenv("TEMP");
    if (!tmp_dir) tmp_dir = ".";
    snprintf(tmp_cfg, sizeof(tmp_cfg), "%s\\xhttpd_reload_sec_test.conf", tmp_dir);
#else
    snprintf(tmp_cfg, sizeof(tmp_cfg), "/tmp/xhttpd_reload_sec_test.conf");
#endif
    int cfd = open(tmp_cfg, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    const char *cfg_txt = "mode=direct\nallow_private=1\nclient=b831381d-6324-4d53-ad4f-8cda48b30811\n";
    ssize_t w = write(cfd, cfg_txt, strlen(cfg_txt));
    (void)w;
    close(cfd);

    #define SEND_RELOAD_REQ(raw_req, exp_status) do { \
        int sv[2]; \
        check("Reload: socketpair", socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0); \
        xhttpd_spawn_client_worker_for_test(sv[0]); \
        send(sv[1], (raw_req), strlen(raw_req), 0); \
        char resp_buf[512] = {0}; \
        ssize_t rn = recv(sv[1], resp_buf, sizeof(resp_buf) - 1, 0); \
        close(sv[1]); \
        check("Reload: worker responded", rn > 0); \
        char exp_str[32]; \
        snprintf(exp_str, sizeof(exp_str), "HTTP/1.1 %d ", (exp_status)); \
        check("Reload: expected status " #exp_status, strstr(resp_buf, exp_str) != NULL); \
    } while(0)

    /* 1. Disabled reload: reload token unset -> returns 404 */
    xhttpd_test_set_reload_token("");
    xhttpd_test_set_config_path(tmp_cfg);
    SEND_RELOAD_REQ("POST /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\n\r\n", 404);

    /* 2. Configure reload token */
    xhttpd_test_set_reload_token("secret_reload_token_xyz987");

    /* 3. Non-POST method (GET) -> returns 405 Method Not Allowed */
    SEND_RELOAD_REQ("GET /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n", 405);

    /* 4. POST without token header -> returns 401 Unauthorized */
    SEND_RELOAD_REQ("POST /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\n\r\n", 401);

    /* 5. POST with wrong token -> returns 401 Unauthorized */
    SEND_RELOAD_REQ("POST /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Awgm-Reload-Token: wrong_token\r\nContent-Length: 0\r\n\r\n", 401);

    /* 6. POST with valid token, but invalid/nonexistent config path -> returns 500 Internal Server Error */
    xhttpd_test_set_config_path("/tmp/definitely_nonexistent_config_file_98765.conf");
    SEND_RELOAD_REQ("POST /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Awgm-Reload-Token: secret_reload_token_xyz987\r\nContent-Length: 0\r\n\r\n", 500);

    /* 7. POST with valid token and valid config path -> returns 200 OK */
    xhttpd_test_set_config_path(tmp_cfg);
    SEND_RELOAD_REQ("POST /.xhttpd/reload HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Awgm-Reload-Token: secret_reload_token_xyz987\r\nContent-Length: 0\r\n\r\n", 200);

    /* Reset reload token and config path */
    xhttpd_test_set_reload_token("");
    xhttpd_test_set_config_path("");
    unlink(tmp_cfg);
    #undef SEND_RELOAD_REQ
}

static void build_raw_header_fp_http_req(char *out_buf, size_t out_cap, const char *http_method,
                                         const char *client_id, const uint8_t secret[32],
                                         uint8_t op, const uint8_t sid[16],
                                         uint64_t up_off, uint64_t ack_down_off,
                                         uint64_t want_down_off, uint32_t max_down_bytes,
                                         const uint8_t *payload, size_t plen, uint32_t nonce_seq) {
    size_t wire_len = FP_REQ_HEADER_SIZE + plen;
    uint8_t *wire = (uint8_t *)calloc(1, wire_len);
    memcpy(wire, FP_PROTO_MAGIC, 4);
    wire[4] = op;
    wire[5] = 0;
    memcpy(wire + 6, sid, 16);
    put_be64_t(wire + 22, up_off);
    put_be64_t(wire + 30, ack_down_off);
    put_be64_t(wire + 38, want_down_off);
    put_be32_t(wire + 46, max_down_bytes);
    put_be32_t(wire + 50, (uint32_t)plen);
    if (plen > 0 && payload) {
        memcpy(wire + FP_REQ_HEADER_SIZE, payload, plen);
    }

    char b64[3072];
    base64url_encode(wire, wire_len, b64, sizeof(b64));

    uint8_t body_sha[32];
    sha256_hash(wire, wire_len, body_sha);
    free(wire);

    char ts_str[32], nonce_hex[33], sig_hex[65];
    snprintf(ts_str, sizeof(ts_str), "%llu", (unsigned long long)epoch_ms());
    snprintf(nonce_hex, sizeof(nonce_hex), "ccddeeff0011223344556677%08x", nonce_seq);

    const char *path = "/api/v1/upload";
    uint8_t msg[1024];
    size_t off = 0;
    const char *parts[6] = { FP_PROTO_VERSION, http_method, path, client_id, ts_str, nonce_hex };
    for (int i = 0; i < 6; i++) {
        size_t l = strlen(parts[i]);
        memcpy(msg + off, parts[i], l);
        off += l;
        msg[off++] = 0;
    }
    memcpy(msg + off, body_sha, 32);
    off += 32;

    uint8_t sig[32];
    hmac_sha256(secret, 32, msg, off, sig);
    bytes_to_hex(sig, 32, sig_hex);

    snprintf(out_buf, out_cap,
        "%s %s?_=%s HTTP/1.1\r\n"
        "Host: localhost\r\n"
        "X-FP-Version: %s\r\n"
        "X-FP-Auth: %s:%s:%s:%s\r\n"
        "X-FP-Data: %s\r\n"
        "\r\n",
        http_method, path, nonce_hex, FP_PROTO_VERSION,
        client_id, ts_str, nonce_hex, sig_hex, b64);
}

static void test_universal_http_methods_finiteproto(void) {
    /* 1. Verify base64url_encode / base64url_decode round-trip */
    uint8_t orig[58];
    for (int i = 0; i < 58; i++) orig[i] = (uint8_t)(i * 7 + 3);
    char b64[128] = {0};
    uint8_t dec[64] = {0};
    int elen = base64url_encode(orig, sizeof(orig), b64, sizeof(b64));
    int dlen = base64url_decode(b64, (size_t)elen, dec, sizeof(dec));
    check("Universal FP: base64url round-trip for 58-byte FP response header",
          elen == 78 && dlen == 58 && memcmp(orig, dec, 58) == 0);

    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(&cfg, 0, uuid);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);
    const fp_cred_t *cred = find_cred(&cfg, uuid);
    assert(cred != NULL);

    /* 2. Test GET, HEAD, OPTIONS, DELETE through read_http_request + handle_fp_http_request */
    const char *methods[4] = { "GET", "HEAD", "OPTIONS", "DELETE" };
    for (int m = 0; m < 4; m++) {
        const char *meth = methods[m];
        uint16_t eport = 0;
        int elisten = create_loopback_listener(&eport);
        int *earg = (int *)malloc(sizeof(int));
        *earg = elisten;
        pthread_t eth;
        pthread_create(&eth, NULL, echo_server_thread, earg);
        pthread_detach(eth);

        char target[64];
        snprintf(target, sizeof(target), "127.0.0.1:%u", eport);
        uint8_t open_sid[16] = { (uint8_t)(0xD0 + m), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };

        /* OPEN via meth + X-FP-Data */
        char raw_req[4096];
        build_raw_header_fp_http_req(raw_req, sizeof(raw_req), meth, uuid, cred->secret,
                                     FP_OP_OPEN, open_sid, 0, 0, 0, 0,
                                     (const uint8_t *)target, strlen(target), 2000 + m * 10);
        http_req_t parsed_req;
        int prc = feed_http_req(raw_req, strlen(raw_req), &parsed_req, NULL);
        char msg[128];
        snprintf(msg, sizeof(msg), "Universal FP (%s): read_http_request decodes X-FP-Data & is_fp_http_request=true", meth);
        check(msg, prc == 0 && is_fp_http_request(&parsed_req) && parsed_req.body_len == FP_REQ_HEADER_SIZE + strlen(target));

        int sv[2];
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        int hrc = handle_fp_http_request(sv[0], &parsed_req, &cfg);
        shutdown(sv[0], SHUT_WR);
        free(parsed_req.body);

        char resp_raw[4096] = {0};
        ssize_t rlen = recv(sv[1], resp_raw, sizeof(resp_raw) - 1, 0);
        close(sv[0]);
        close(sv[1]);

        const char *hdr_end = strstr(resp_raw, "\r\n\r\n");
        const char *fp_resp_line = strstr(resp_raw, "X-FP-Resp: ");
        snprintf(msg, sizeof(msg), "Universal FP (%s): OPEN returns HTTP 200 and X-FP-Resp header", meth);
        check(msg, hrc == 0 && rlen > 0 && strstr(resp_raw, "HTTP/1.1 200 OK") != NULL && fp_resp_line != NULL && hdr_end != NULL);

        uint8_t rhdr[64] = {0};
        if (fp_resp_line) {
            const char *val = fp_resp_line + 11;
            const char *eol = strstr(val, "\r\n");
            if (eol) {
                base64url_decode(val, (size_t)(eol - val), rhdr, sizeof(rhdr));
            }
        }
        uint8_t assigned_sid[16];
        memcpy(assigned_sid, rhdr + 6, 16);

        if (strcmp(meth, "HEAD") == 0 && hdr_end) {
            size_t body_bytes_after_hdr = (size_t)rlen - (size_t)((hdr_end + 4) - resp_raw);
            check("Universal FP (HEAD): response has Content-Length: 0 and 0 body bytes",
                  strstr(resp_raw, "Content-Length: 0\r\n") != NULL && body_bytes_after_hdr == 0);
        }

        /* UP via meth + X-FP-Data */
        const char *echo_msg = "universal-method-payload";
        build_raw_header_fp_http_req(raw_req, sizeof(raw_req), meth, uuid, cred->secret,
                                     FP_OP_UP, assigned_sid, 0, 0, 0, 0,
                                     (const uint8_t *)echo_msg, strlen(echo_msg), 2001 + m * 10);
        feed_http_req(raw_req, strlen(raw_req), &parsed_req, NULL);
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        handle_fp_http_request(sv[0], &parsed_req, &cfg);
        shutdown(sv[0], SHUT_WR);
        free(parsed_req.body);
        memset(resp_raw, 0, sizeof(resp_raw));
        rlen = recv(sv[1], resp_raw, sizeof(resp_raw) - 1, 0);
        close(sv[0]);
        close(sv[1]);
        snprintf(msg, sizeof(msg), "Universal FP (%s): UP succeeds with HTTP 200", meth);
        check(msg, rlen > 0 && strstr(resp_raw, "HTTP/1.1 200 OK") != NULL);

        usleep(30000);

        /* DOWN via meth + X-FP-Data */
        build_raw_header_fp_http_req(raw_req, sizeof(raw_req), meth, uuid, cred->secret,
                                     FP_OP_DOWN, assigned_sid, 0, 0, 0, 4096,
                                     NULL, 0, 2002 + m * 10);
        feed_http_req(raw_req, strlen(raw_req), &parsed_req, NULL);
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        handle_fp_http_request(sv[0], &parsed_req, &cfg);
        shutdown(sv[0], SHUT_WR);
        free(parsed_req.body);
        memset(resp_raw, 0, sizeof(resp_raw));
        rlen = recv(sv[1], resp_raw, sizeof(resp_raw) - 1, 0);
        close(sv[0]);
        close(sv[1]);

        if (strcmp(meth, "HEAD") == 0) {
            const char *fp_data_line = strstr(resp_raw, "X-FP-Data: ");
            uint8_t dpay[64] = {0};
            int dpay_len = 0;
            if (fp_data_line) {
                const char *v = fp_data_line + 11;
                const char *eol = strstr(v, "\r\n");
                if (eol) dpay_len = base64url_decode(v, (size_t)(eol - v), dpay, sizeof(dpay));
            }
            check("Universal FP (HEAD): DOWN delivers echoed payload inside X-FP-Data header with 0 body",
                  dpay_len == (int)strlen(echo_msg) && memcmp(dpay, echo_msg, strlen(echo_msg)) == 0);
        } else {
            hdr_end = strstr(resp_raw, "\r\n\r\n");
            bool down_ok = false;
            if (hdr_end) {
                const uint8_t *b = (const uint8_t *)(hdr_end + 4);
                size_t blen = (size_t)rlen - (size_t)((hdr_end + 4) - resp_raw);
                if (blen == FP_RESP_HEADER_SIZE + strlen(echo_msg) &&
                    memcmp(b + FP_RESP_HEADER_SIZE, echo_msg, strlen(echo_msg)) == 0) {
                    down_ok = true;
                }
            }
            snprintf(msg, sizeof(msg), "Universal FP (%s): DOWN delivers echoed payload", meth);
            check(msg, down_ok);
        }

        /* CLOSE via meth */
        build_raw_header_fp_http_req(raw_req, sizeof(raw_req), meth, uuid, cred->secret,
                                     FP_OP_CLOSE, assigned_sid, 0, 0, 0, 0,
                                     NULL, 0, 2003 + m * 10);
        feed_http_req(raw_req, strlen(raw_req), &parsed_req, NULL);
        socketpair(AF_UNIX, SOCK_STREAM, 0, sv);
        handle_fp_http_request(sv[0], &parsed_req, &cfg);
        close(sv[0]);
        close(sv[1]);
        free(parsed_req.body);
    }
    fp_engine_close_all();
}

typedef struct {
    const char *path;
    int rc;
} small_stack_reload_arg_t;

static void *small_stack_reload_worker(void *arg) {
    small_stack_reload_arg_t *a = (small_stack_reload_arg_t *)arg;
    a->rc = load_config_file(a->path, &g_cfg);
    return NULL;
}

typedef struct {
    int lfd;
    volatile int stop;
} vless_echo_srv_t;

static void *vless_echo_server_thread(void *arg) {
    vless_echo_srv_t *s = (vless_echo_srv_t *)arg;
    struct pollfd lpfd = { .fd = s->lfd, .events = POLLIN };
    if (poll(&lpfd, 1, 5000) <= 0) return NULL;
    int cfd = accept(s->lfd, NULL, NULL);
    if (cfd < 0) return NULL;
    uint8_t buf[1024];
    while (!__atomic_load_n(&s->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd cpfd = { .fd = cfd, .events = POLLIN };
        int pr = poll(&cpfd, 1, 50);
        if (pr < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (pr == 0) continue;
        ssize_t n = recv(cfd, (char *)buf, sizeof(buf), 0);
        if (n <= 0) break;
        if (write_all(cfd, buf, (size_t)n) < 0) break;
    }
    close(cfd);
    return NULL;
}

typedef struct {
    int client_fd;
    const xhttpd_config_t *cfg;
    char sid[64];
    volatile int done;
} vless_down_worker_t;

static void *vless_down_worker_thread(void *arg) {
    vless_down_worker_t *w = (vless_down_worker_t *)arg;
    http_req_t req;
    memset(&req, 0, sizeof(req));
    snprintf(req.method, sizeof(req.method), "GET");
    snprintf(req.path, sizeof(req.path), "/cdn-bridge/%s", w->sid);
    snprintf(req.peer_ip, sizeof(req.peer_ip), "127.0.0.1");
    req.keep_alive = false;
    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = w->client_fd;
    handle_vless_http_request(w->client_fd, &cr, &req, "/cdn-bridge", w->cfg);
    close(w->client_fd);
    __atomic_store_n(&w->done, 1, __ATOMIC_RELEASE);
    return NULL;
}

typedef struct {
    const xhttpd_config_t *cfg;
    const char *sid;
    uint64_t seq;
    size_t body_len;
    int http_code;
    volatile int done;
} vless_post_worker_t;

static int invoke_vless_post_sync(const xhttpd_config_t *cfg, const char *path,
                                  const uint8_t *body, size_t body_len) {
    http_req_t req;
    memset(&req, 0, sizeof(req));
    snprintf(req.method, sizeof(req.method), "POST");
    snprintf(req.path, sizeof(req.path), "%s", path);
    snprintf(req.peer_ip, sizeof(req.peer_ip), "127.0.0.1");
    req.keep_alive = false;
    req.content_length = (int64_t)body_len;
    if (body_len > 0) {
        req.body = (uint8_t *)malloc(body_len);
        memcpy(req.body, body, body_len);
        req.body_len = body_len;
    }
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) {
        free(req.body);
        return -1;
    }
    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = sv[0];
    handle_vless_http_request(sv[0], &cr, &req, "/cdn-bridge", cfg);
    close(sv[0]);
    free(req.body);

    char resp[512] = {0};
    ssize_t n = recv(sv[1], resp, sizeof(resp) - 1, 0);
    close(sv[1]);
    if (n <= 0) return -1;
    int code = -1;
    (void)sscanf(resp, "HTTP/1.1 %d", &code);
    return code;
}

static void *vless_post_worker_thread(void *arg) {
    vless_post_worker_t *pw = (vless_post_worker_t *)arg;
    uint8_t *buf = (uint8_t *)calloc(1, pw->body_len);
    char path[128];
    snprintf(path, sizeof(path), "/cdn-bridge/%s/%llu", pw->sid, (unsigned long long)pw->seq);
    pw->http_code = invoke_vless_post_sync(pw->cfg, path, buf, pw->body_len);
    free(buf);
    __atomic_store_n(&pw->done, 1, __ATOMIC_RELEASE);
    return NULL;
}

static bool recv_until_substr_ex(int fd, const char *needle, char *hdr_out, size_t hdr_out_sz, int timeout_ms) {
    size_t nlen = strlen(needle);
    char acc[4096];
    size_t used = 0;
    uint64_t deadline = now_ms() + (uint64_t)timeout_ms;
    if (hdr_out && hdr_out_sz > 0) hdr_out[0] = '\0';
    while (now_ms() < deadline && used + 1 < sizeof(acc)) {
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        int wait_ms = (int)(deadline - now_ms());
        if (wait_ms > 50) wait_ms = 50;
        if (wait_ms <= 0) break;
        int pr = poll(&pfd, 1, wait_ms);
        if (pr <= 0) continue;
        ssize_t r = recv(fd, acc + used, sizeof(acc) - 1 - used, 0);
        if (r <= 0) break;
        used += (size_t)r;
        acc[used] = '\0';
        if (hdr_out && hdr_out_sz > 0) {
            size_t cp = used < (hdr_out_sz - 1) ? used : (hdr_out_sz - 1);
            memcpy(hdr_out, acc, cp);
            hdr_out[cp] = '\0';
        }
        if (memmem(acc, used, needle, nlen) != NULL) return true;
    }
    return false;
}

static bool recv_until_substr(int fd, const char *needle, int timeout_ms) {
    return recv_until_substr_ex(fd, needle, NULL, 0, timeout_ms);
}

static void test_vless_session_races_and_small_stack_reload(void) {
    /* 1. Small-stack (128 KiB) SIGHUP / load_config_file test */
#ifdef _WIN32
    const char *conf_path = "xhttpd_small_stack_test.conf";
#else
    char conf_path[128];
    snprintf(conf_path, sizeof(conf_path), "/tmp/xhttpd_small_stack_%d.conf", (int)getpid());
#endif
    FILE *cf = fopen(conf_path, "w");
    check("VLESS/Stack: create temporary xhttpd config file", cf != NULL);
    if (cf) {
        fprintf(cf, "listen_host=127.0.0.1\nlisten_port=19008\nprefix=/cdn-bridge\nmode=direct\nallow_private=1\n");
        fprintf(cf, "client=b831381d-6324-4d53-ad4f-8cda48b30811\n");
        fclose(cf);
    }
    fp_engine_init();
    vless_engine_init();

    small_stack_reload_arg_t sarg = { .path = conf_path, .rc = -99 };
    pthread_t sth;
    pthread_attr_t sattr;
    pthread_attr_init(&sattr);
    pthread_attr_setstacksize(&sattr, 131072); /* 128 KiB: previously overflowed 116 KiB stack frame */
    check("VLESS/Stack: spawn 128 KiB stack thread for load_config_file",
          pthread_create(&sth, &sattr, small_stack_reload_worker, &sarg) == 0);
    pthread_attr_destroy(&sattr);
    pthread_join(sth, NULL);
    remove(conf_path);
    check("VLESS/Stack: load_config_file succeeds cleanly inside 128 KiB stack thread", sarg.rc == 0);

    /* 2. Authenticated VLESS packet-up + GET session survives spoofed-sid unauthenticated POSTs */
    xhttpd_config_t *cfg = (xhttpd_config_t *)calloc(1, sizeof(*cfg));
    cfg->egress_mode = EGRESS_MODE_DIRECT;
    cfg->allow_private = true;
    cfg->num_prefixes = 1;
    snprintf(cfg->prefixes[0], sizeof(cfg->prefixes[0]), "/cdn-bridge");
    cfg->num_clients = 1;
    cfg->max_sessions_per_ip = 64;
    cfg->max_pre_auth_sessions = 64;
    cfg->max_pre_auth_per_ip = 32;
    parse_uuid_str("b831381d-6324-4d53-ad4f-8cda48b30811", cfg->clients[0].uuid);

    uint16_t echo_port = 0;
    int lfd = create_loopback_listener(&echo_port);
    vless_echo_srv_t esrv = { .lfd = lfd, .stop = 0 };
    pthread_t eth;
    pthread_create(&eth, NULL, vless_echo_server_thread, &esrv);

    uint8_t seq0_pkt[26 + 5];
    memset(seq0_pkt, 0, sizeof(seq0_pkt));
    seq0_pkt[0] = 0x00;
    memcpy(seq0_pkt + 1, cfg->clients[0].uuid, 16);
    seq0_pkt[17] = 0x00;
    seq0_pkt[18] = VLESS_CMD_TCP;
    seq0_pkt[19] = (uint8_t)((echo_port >> 8) & 0xFF);
    seq0_pkt[20] = (uint8_t)(echo_port & 0xFF);
    seq0_pkt[21] = VLESS_ADDR_IPV4;
    seq0_pkt[22] = 127; seq0_pkt[23] = 0; seq0_pkt[24] = 0; seq0_pkt[25] = 1;
    memcpy(seq0_pkt + 26, "PING1", 5);

    check("VLESS/Spoof: legitimate packet-up seq=0 returns 200",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_live_1/0", seq0_pkt, sizeof(seq0_pkt)) == 200);

    int down_sv[2];
    socketpair(AF_UNIX, SOCK_STREAM, 0, down_sv);
    vless_down_worker_t dw;
    memset(&dw, 0, sizeof(dw));
    dw.client_fd = down_sv[0];
    dw.cfg = cfg;
    snprintf(dw.sid, sizeof(dw.sid), "sid_live_1");
    pthread_t dth;
    pthread_create(&dth, NULL, vless_down_worker_thread, &dw);

    char down_hdr[1024] = {0};
    check("VLESS/Spoof: GET downlink relays initial PING1 echo",
          recv_until_substr_ex(down_sv[1], "PING1", down_hdr, sizeof(down_hdr), 3000));
    check("VLESS/HTTP: streaming response emits Connection: close so HTTP/1.1 pools do not reuse closing stream",
          strstr(down_hdr, "Connection: close\r\n") != NULL && strstr(down_hdr, "Connection: keep-alive") == NULL);

    /* Spoofed unauthenticated seq=0 POST and stream-up POST with victim's sid */
    uint8_t spoof_pkt[26];
    memcpy(spoof_pkt, seq0_pkt, 26);
    memset(spoof_pkt + 1, 0xEE, 16); /* unauthorized UUID */
    check("VLESS/Spoof: unauthenticated spoofed seq=0 POST rejected with 403",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_live_1/0", spoof_pkt, sizeof(spoof_pkt)) == 403);
    check("VLESS/Spoof: unauthenticated spoofed stream-up POST rejected with 403",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_live_1", spoof_pkt, sizeof(spoof_pkt)) == 403);

    /* Victim session MUST still be alive and relay seq=1 */
    check("VLESS/Spoof: victim packet-up seq=1 after spoof attempts still returns 200",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_live_1/1", (const uint8_t *)"PING2", 5) == 200);
    check("VLESS/Window: out-of-window packet-up seq=100 rejected with 400",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_live_1/100", (const uint8_t *)"BADSEQ", 6) == 400);
    check("VLESS/Spoof: victim GET downlink still relays PING2 after spoof attempts",
          recv_until_substr(down_sv[1], "PING2", 3000));

    /* 3. Concurrent revoke_unauthorised + rapid new session allocation (slot/fd reuse race test) */
    xhttpd_config_t *empty_cfg = (xhttpd_config_t *)calloc(1, sizeof(*empty_cfg));
    vless_engine_revoke_unauthorised(empty_cfg);
    free(empty_cfg);

    for (int i = 0; i < 12; i++) {
        char ppath[96];
        snprintf(ppath, sizeof(ppath), "/cdn-bridge/sid_ churn_%d/0", i);
        /* Note: space makes invalid sid -> use valid sid_churn_%d */
        snprintf(ppath, sizeof(ppath), "/cdn-bridge/sid_churn_%d/0", i);
        (void)invoke_vless_post_sync(cfg, ppath, seq0_pkt, sizeof(seq0_pkt));
    }

    uint64_t rev_deadline = now_ms() + 1500ULL;
    while (!__atomic_load_n(&dw.done, __ATOMIC_ACQUIRE) && now_ms() < rev_deadline) {
        usleep(5000);
    }
    check("VLESS/Revoke: active GET downlink exits promptly (<1.5s) upon credential revocation",
          __atomic_load_n(&dw.done, __ATOMIC_ACQUIRE) == 1);
    pthread_join(dth, NULL);
    close(down_sv[1]);

    __atomic_store_n(&esrv.stop, 1, __ATOMIC_RELEASE);
    pthread_join(eth, NULL);
    close(lfd);
    vless_engine_close_all();

    /* 4. Full up_pipe writer does NOT block vless_engine_get_stats / vless_engine_close_all */
    check("VLESS/Pipe: open sid_fullpipe seq=0 succeeds",
          invoke_vless_post_sync(cfg, "/cdn-bridge/sid_fullpipe/0", seq0_pkt, sizeof(seq0_pkt)) == 200);

    vless_post_worker_t pw1 = { .cfg = cfg, .sid = "sid_fullpipe", .seq = 1, .body_len = 262144, .http_code = 0, .done = 0 };
    pthread_t pth;
    pthread_create(&pth, NULL, vless_post_worker_thread, &pw1);
    usleep(60000); /* Allow writer to saturate kernel pipe buffer and enter non-blocking poll */

    uint64_t t_stat = now_ms();
    int act_s = 0;
    vless_engine_get_stats(&act_s, NULL, NULL, NULL);
    uint64_t stat_dur = now_ms() - t_stat;
    check("VLESS/Pipe: vless_engine_get_stats does not block while pipe writer is stalled (<100ms)",
          stat_dur < 100 && act_s == 1);

    uint64_t t_close = now_ms();
    vless_engine_close_all();
    while (!__atomic_load_n(&pw1.done, __ATOMIC_ACQUIRE) && (now_ms() - t_close) < 1000ULL) {
        usleep(5000);
    }
    uint64_t close_dur = now_ms() - t_close;
    check("VLESS/Pipe: stalled pipe writer cancels promptly (<500ms) when session is closed",
          __atomic_load_n(&pw1.done, __ATOMIC_ACQUIRE) == 1 && close_dur < 500);
    pthread_join(pth, NULL);

    free(cfg);
}

static void test_fp_up_stalled_target_write_cancels_bounded(void) {
    xhttpd_config_t *cfg = (xhttpd_config_t *)calloc(1, sizeof(*cfg));
    assert(cfg != NULL);
    cfg->egress_mode = EGRESS_MODE_DIRECT;
    cfg->allow_private = true;
    cfg->num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(cfg, 0, uuid);

    rebuild_fp_credentials(cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(cfg);

    uint8_t open_sid[16] = { 0xC1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t assigned_sid[16] = {0};
    int peer_fd = -1;
    check("FP/UpWrite: open plain FP session succeeds",
          open_plain_fp_session(cfg, uuid, open_sid, 3100, assigned_sid, &peer_fd) == 0 && peer_fd >= 0);

    fp_test_set_target_sndbuf(1024);
    fp_test_set_initial_write_timeout_ms(220);

    size_t plen = 65536;
    uint8_t *big_payload = (uint8_t *)malloc(plen);
    assert(big_payload != NULL);
    memset(big_payload, 0x4B, plen);

    http_req_t up_req;
    build_signed_fp_http_req(&up_req, cfg, uuid, FP_OP_UP, assigned_sid, 0, big_payload, plen, 3101);
    free(big_payload);

    int sv[2];
    assert(socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0);
    uint64_t t0 = now_ms();
    (void)handle_fp_http_request(sv[0], &up_req, cfg);
    uint64_t elapsed = now_ms() - t0;
    close(sv[0]);
    free(up_req.body);

    char rbuf[1024];
    memset(rbuf, 0, sizeof(rbuf));
    ssize_t rn = recv(sv[1], rbuf, sizeof(rbuf) - 1, 0);
    close(sv[1]);

    fp_test_set_target_sndbuf(0);
    fp_test_set_initial_write_timeout_ms(5000);
    close(peer_fd);
    fp_engine_close_all();
    free(cfg);

    const char *hend = strstr(rbuf, "\r\n\r\n");
    int has_target_reset = (hend != NULL &&
                            rn >= (ssize_t)((hend - rbuf) + 4 + FP_RESP_HEADER_SIZE) &&
                            (((const uint8_t *)(hend + 4))[5] & FP_RESP_FLAG_TARGET_RESET) != 0);

    check("FP/UpWrite: stalled target write cancels within bounded timeout (<1500ms)",
          rn > (ssize_t)FP_RESP_HEADER_SIZE &&
          strstr(rbuf, "HTTP/1.1 200") != NULL &&
          elapsed >= 150 && elapsed < 1500 &&
          has_target_reset);
}

static void test_http_413_oversized_post_no_tcp_rst(void) {
    pthread_rwlock_wrlock(&g_cfg_lock);
    memset(&g_cfg, 0, sizeof(g_cfg));
    g_cfg.egress_mode = EGRESS_MODE_DIRECT;
    g_cfg.allow_private = true;
    g_cfg.num_prefixes = 1;
    snprintf(g_cfg.prefixes[0], sizeof(g_cfg.prefixes[0]), "/cdn-bridge");
    g_cfg.num_clients = 1;
    g_cfg.max_sessions_per_ip = 64;
    g_cfg.max_pre_auth_sessions = 64;
    g_cfg.max_pre_auth_per_ip = 32;
    parse_uuid_str("b831381d-6324-4d53-ad4f-8cda48b30811", g_cfg.clients[0].uuid);
    format_uuid_canonical(g_cfg.clients[0].uuid, g_cfg.clients[0].uuid_str);
    bytes_to_hex(g_cfg.clients[0].uuid, 16, g_cfg.clients[0].hex_no_dash);
    pthread_rwlock_unlock(&g_cfg_lock);

    uint16_t port = 0;
    int lfd = create_loopback_listener(&port);
    if (lfd >= 0) {
        (void)listen(lfd, 32);
    }

    const size_t body_len_1 = 2U * 1024U * 1024U + 1U;
    const size_t body_len_2 = 2U * 1024U * 1024U + 65536U;
    uint8_t *payload = (uint8_t *)malloc(body_len_2);
    if (payload) {
        memset(payload, 0x42, body_len_2);
    }

    struct sockaddr_in saddr;
    memset(&saddr, 0, sizeof(saddr));
    saddr.sin_family = AF_INET;
    saddr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    saddr.sin_port = htons(port);

    int conc_ok = 0;
    for (int iter = 0; iter < 20 && lfd >= 0 && payload != NULL; iter++) {
        int cli_fd = socket(AF_INET, SOCK_STREAM, 0);
        if (cli_fd < 0) break;
        if (connect(cli_fd, (struct sockaddr *)&saddr, sizeof(saddr)) != 0) {
            close(cli_fd);
            break;
        }
        int srv_fd = accept(lfd, NULL, NULL);
        if (srv_fd < 0 || xhttpd_spawn_client_worker_for_test(srv_fd) != 0) {
            close(cli_fd);
            break;
        }

#ifdef _WIN32
        u_long nb = 1;
        ioctlsocket((SOCKET)cli_fd, FIONBIO, &nb);
#else
        fcntl(cli_fd, F_SETFL, fcntl(cli_fd, F_GETFL, 0) | O_NONBLOCK);
#endif

        char hdr[256];
        int hlen = snprintf(hdr, sizeof(hdr),
            "POST /cdn-bridge/?sid=rst413c_%d&page=0 HTTP/1.1\r\n"
            "Host: 127.0.0.1:%u\r\n"
            "Content-Type: application/octet-stream\r\n"
            "Content-Length: %zu\r\n"
            "\r\n",
            iter, (unsigned)port, body_len_1);
        if (hlen <= 0 || (size_t)hlen >= sizeof(hdr)) {
            close(cli_fd);
            break;
        }
        size_t hdr_off = 0;
        size_t body_off = 0;
        bool stop_send = false;
        bool half_closed = false;
        bool clean_eof = false;
        bool io_err = false;
        char rbuf[4096];
        size_t rlen = 0;
        memset(rbuf, 0, sizeof(rbuf));

        uint64_t dl = now_ms() + 5000ULL;
        while (now_ms() < dl) {
            struct pollfd pfd;
            pfd.fd = cli_fd;
            pfd.events = (short)(POLLIN | (stop_send ? 0 : POLLOUT));
            pfd.revents = 0;
            int pr = poll(&pfd, 1, 50);
            if (pr < 0) {
                if (errno == EINTR) continue;
                io_err = true;
                break;
            }
            if (!stop_send && (pfd.revents & POLLOUT)) {
                if (hdr_off < (size_t)hlen) {
                    ssize_t w = send(cli_fd, hdr + hdr_off, (size_t)hlen - hdr_off, MSG_NOSIGNAL);
                    if (w > 0) hdr_off += (size_t)w;
                    else if (w < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR) io_err = true;
                } else if (body_off < body_len_1) {
                    size_t chunk = body_len_1 - body_off;
                    if (chunk > 65536U) chunk = 65536U;
                    ssize_t w = send(cli_fd, (const char *)(payload + body_off), chunk, MSG_NOSIGNAL);
                    if (w > 0) {
                        body_off += (size_t)w;
                        if (body_off >= body_len_1) stop_send = true;
                    } else if (w < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR) {
                        io_err = true;
                    }
                }
            }
            if (pfd.revents & (POLLIN | POLLHUP | POLLERR)) {
                while (1) {
                    size_t rem = sizeof(rbuf) - 1 - rlen;
                    uint8_t sink[1024];
                    ssize_t r = recv(cli_fd, rem > 0 ? (rbuf + rlen) : (char *)sink,
                                     rem > 0 ? rem : sizeof(sink), MSG_DONTWAIT);
                    if (r == 0) {
                        clean_eof = true;
                        break;
                    }
                    if (r < 0) {
                        if (errno == EAGAIN || errno == EWOULDBLOCK) break;
                        if (errno == EINTR) continue;
                        io_err = true;
                        break;
                    }
                    if (rem > 0) {
                        rlen += (size_t)r;
                        rbuf[rlen] = '\0';
                    }
                }
                if (strstr(rbuf, "HTTP/1.1 413") != NULL &&
                    strstr(rbuf, "request body too large") != NULL) {
                    stop_send = true;
                }
                if (clean_eof || io_err) break;
            }
            if (stop_send && !half_closed && (iter % 2) == 0) {
                (void)shutdown(cli_fd, SHUT_WR);
                half_closed = true;
            }
        }
        close(cli_fd);
        if (!io_err && clean_eof &&
            strstr(rbuf, "HTTP/1.1 413") != NULL &&
            strstr(rbuf, "request body too large") != NULL) {
            conc_ok++;
        }
    }

    int sync_ok = 0;
    for (int iter = 0; iter < 5 && lfd >= 0 && payload != NULL; iter++) {
        int cli_fd = socket(AF_INET, SOCK_STREAM, 0);
        if (cli_fd < 0) break;
        if (connect(cli_fd, (struct sockaddr *)&saddr, sizeof(saddr)) != 0) {
            close(cli_fd);
            break;
        }
        int srv_fd = accept(lfd, NULL, NULL);
        if (srv_fd < 0 || xhttpd_spawn_client_worker_for_test(srv_fd) != 0) {
            close(cli_fd);
            break;
        }

        char hdr[256];
        int hlen = snprintf(hdr, sizeof(hdr),
            "POST /cdn-bridge/?sid=rst413s_%d&page=0 HTTP/1.1\r\n"
            "Host: 127.0.0.1:%u\r\n"
            "Content-Type: application/octet-stream\r\n"
            "Content-Length: %zu\r\n"
            "\r\n",
            iter, (unsigned)port, body_len_2);
        bool write_ok = (hlen > 0 && (size_t)hlen < sizeof(hdr) &&
                         write_all(cli_fd, hdr, (size_t)hlen) == 0 &&
                         write_all(cli_fd, payload, body_len_2) == 0);

        char rbuf[4096];
        size_t rlen = 0;
        memset(rbuf, 0, sizeof(rbuf));
        bool clean_eof = false;
        bool recv_err = false;
        while (1) {
            size_t rem = sizeof(rbuf) - 1 - rlen;
            uint8_t sink[1024];
            ssize_t r = recv(cli_fd, rem > 0 ? (rbuf + rlen) : (char *)sink,
                             rem > 0 ? rem : sizeof(sink), 0);
            if (r == 0) {
                clean_eof = true;
                break;
            }
            if (r < 0) {
                if (errno == EINTR) continue;
                recv_err = true;
                break;
            }
            if (rem > 0) {
                rlen += (size_t)r;
                rbuf[rlen] = '\0';
            }
        }
        close(cli_fd);
        if (write_ok && !recv_err && clean_eof &&
            strstr(rbuf, "HTTP/1.1 413") != NULL &&
            strstr(rbuf, "request body too large") != NULL) {
            sync_ok++;
        }
    }

    uint64_t wait_dl = now_ms() + 2000ULL;
    while (xhttpd_get_active_http_workers() > 0 && now_ms() < wait_dl) {
        usleep(10000);
    }

    free(payload);
    if (lfd >= 0) close(lfd);

    check("HTTP/413: interleaved TCP POST (2MiB+1B x20) receives 413 + clean FIN without RST",
          conc_ok == 20);
    check("HTTP/413: full-body TCP POST (2MiB+64KiB x5) drains and returns 413 + clean FIN without RST",
          sync_ok == 5);
    check("HTTP/413: all HTTP worker threads exited cleanly (0 active)",
          xhttpd_get_active_http_workers() == 0);
}

static void *vless_oneshot_reply_close_thread(void *arg) {
    int lfd = (int)(intptr_t)arg;
    int cfd = accept(lfd, NULL, NULL);
    if (cfd >= 0) {
        char buf[64];
        (void)recv(cfd, buf, sizeof(buf), 0);
        (void)write_all(cfd, "REPLY", 5);
        close(cfd);
    }
    return NULL;
}

static void test_stream_up_post_returns_200_when_get_closes_first(void) {
    pthread_rwlock_wrlock(&g_cfg_lock);
    memset(&g_cfg, 0, sizeof(g_cfg));
    g_cfg.egress_mode = EGRESS_MODE_DIRECT;
    g_cfg.allow_private = true;
    g_cfg.num_prefixes = 1;
    snprintf(g_cfg.prefixes[0], sizeof(g_cfg.prefixes[0]), "/cdn-bridge");
    g_cfg.num_clients = 1;
    g_cfg.max_sessions_per_ip = 64;
    g_cfg.max_pre_auth_sessions = 64;
    g_cfg.max_pre_auth_per_ip = 32;
    parse_uuid_str("b831381d-6324-4d53-ad4f-8cda48b30811", g_cfg.clients[0].uuid);
    format_uuid_canonical(g_cfg.clients[0].uuid, g_cfg.clients[0].uuid_str);
    bytes_to_hex(g_cfg.clients[0].uuid, 16, g_cfg.clients[0].hex_no_dash);
    pthread_rwlock_unlock(&g_cfg_lock);

    fp_engine_init();
    vless_engine_init();

    uint16_t target_port = 0;
    int target_lfd = create_loopback_listener(&target_port);
    pthread_t target_th;
    bool target_started = (target_lfd >= 0 && pthread_create(&target_th, NULL,
                               vless_oneshot_reply_close_thread, (void *)(intptr_t)target_lfd) == 0);

    uint16_t port = 0;
    int lfd = create_loopback_listener(&port);
    if (lfd >= 0) (void)listen(lfd, 16);

    struct sockaddr_in saddr;
    memset(&saddr, 0, sizeof(saddr));
    saddr.sin_family = AF_INET;
    saddr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    saddr.sin_port = htons(port);

    int get_fd = (lfd >= 0) ? socket(AF_INET, SOCK_STREAM, 0) : -1;
    bool get_connected = (get_fd >= 0 && connect(get_fd, (struct sockaddr *)&saddr, sizeof(saddr)) == 0);
    int srv_get = get_connected ? accept(lfd, NULL, NULL) : -1;
    if (srv_get >= 0 && xhttpd_spawn_client_worker_for_test(srv_get) != 0) {
        srv_get = -1;
    }

    int post_fd = (lfd >= 0) ? socket(AF_INET, SOCK_STREAM, 0) : -1;
    bool post_connected = (post_fd >= 0 && connect(post_fd, (struct sockaddr *)&saddr, sizeof(saddr)) == 0);
    int srv_post = post_connected ? accept(lfd, NULL, NULL) : -1;
    if (srv_post >= 0 && xhttpd_spawn_client_worker_for_test(srv_post) != 0) {
        srv_post = -1;
    }

    char get_req[256];
    int glen = snprintf(get_req, sizeof(get_req),
        "GET /cdn-bridge/sup_close_first HTTP/1.1\r\nHost: 127.0.0.1:%u\r\n\r\n", (unsigned)port);
    bool get_sent = (get_connected && glen > 0 && write_all(get_fd, get_req, (size_t)glen) == 0);

    /* Wait briefly so GET worker registers as session downlink */
    usleep(50000);

    /* Send chunked stream-up POST WITH NO terminating 0\r\n\r\n chunk (simulates live H2 upload stream) */
    uint8_t vless_init[26 + 5];
    memset(vless_init, 0, sizeof(vless_init));
    vless_init[0] = 0x00;
    memcpy(vless_init + 1, g_cfg.clients[0].uuid, 16);
    vless_init[17] = 0x00;
    vless_init[18] = VLESS_CMD_TCP;
    vless_init[19] = (uint8_t)((target_port >> 8) & 0xFF);
    vless_init[20] = (uint8_t)(target_port & 0xFF);
    vless_init[21] = VLESS_ADDR_IPV4;
    vless_init[22] = 127; vless_init[23] = 0; vless_init[24] = 0; vless_init[25] = 1;
    memcpy(vless_init + 26, "HELLO", 5);

    char post_hdr[256];
    int phlen = snprintf(post_hdr, sizeof(post_hdr),
        "POST /cdn-bridge/sup_close_first HTTP/1.1\r\n"
        "Host: 127.0.0.1:%u\r\n"
        "Content-Type: application/octet-stream\r\n"
        "Transfer-Encoding: chunked\r\n"
        "\r\n%zx\r\n",
        (unsigned)port, sizeof(vless_init));
    bool post_sent = (post_connected && phlen > 0 &&
                      write_all(post_fd, post_hdr, (size_t)phlen) == 0 &&
                      write_all(post_fd, vless_init, sizeof(vless_init)) == 0 &&
                      write_all(post_fd, "\r\n", 2) == 0);

    /* Downlink GET receives REPLY and closes when oneshot target closes */
    bool got_reply = (get_sent && recv_until_substr(get_fd, "REPLY", 3000));
    char post_resp[1024] = {0};
    size_t post_rlen = 0;
    bool post_clean_eof = false;
    if (post_sent) {
        struct pollfd pfd = { .fd = post_fd, .events = POLLIN };
        uint64_t dl = now_ms() + 3000ULL;
        while (now_ms() < dl) {
            int rem = (int)(dl - now_ms());
            if (rem <= 0) break;
            if (poll(&pfd, 1, rem) <= 0) break;
            ssize_t r = recv(post_fd, post_resp + post_rlen,
                             sizeof(post_resp) - 1 - post_rlen, 0);
            if (r == 0) {
                post_clean_eof = true;
                break;
            }
            if (r < 0) {
                if (errno == EINTR) continue;
                break;
            }
            post_rlen += (size_t)r;
            post_resp[post_rlen] = '\0';
        }
    }

    if (get_fd >= 0) close(get_fd);
    if (post_fd >= 0) close(post_fd);
    if (target_started) pthread_join(target_th, NULL);
    if (target_lfd >= 0) close(target_lfd);
    if (lfd >= 0) close(lfd);

    uint64_t wait_dl = now_ms() + 2000ULL;
    while (xhttpd_get_active_http_workers() > 0 && now_ms() < wait_dl) {
        usleep(10000);
    }

    check("VLESS/StreamUp: GET receives REPLY when target closes first", got_reply);
    check("VLESS/StreamUp: open chunked POST receives HTTP 200 OK instead of empty EOF/503",
          post_clean_eof && strstr(post_resp, "HTTP/1.1 200 OK") != NULL);
    check("VLESS/StreamUp: all HTTP worker threads exited cleanly", xhttpd_get_active_http_workers() == 0);
}

#ifndef _WIN32
typedef struct {
    int fd;
    volatile int stop;
} udp_echo_ctx_t;

static void *udp_echo_server_thread(void *arg) {
    udp_echo_ctx_t *ctx = (udp_echo_ctx_t *)arg;
    uint8_t buf[2048];
    while (!__atomic_load_n(&ctx->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd pfd = { .fd = ctx->fd, .events = POLLIN };
        int pr = poll(&pfd, 1, 25);
        if (pr <= 0) continue;
        struct sockaddr_storage cli;
        socklen_t clen = sizeof(cli);
        ssize_t n = recvfrom(ctx->fd, buf, sizeof(buf), 0, (struct sockaddr *)&cli, &clen);
        if (n > 0) {
            sendto(ctx->fd, buf, (size_t)n, 0, (const struct sockaddr *)&cli, clen);
        }
    }
    return NULL;
}

static ssize_t run_fp_rpc_local(const xhttpd_config_t *cfg, const char *client_id, uint8_t op,
                                const uint8_t sid[16], uint64_t up_off, uint64_t ack_down,
                                uint64_t want_down, const uint8_t *payload, size_t plen,
                                uint32_t nonce_seq, uint8_t *out_raw, size_t out_cap) {
    http_req_t req;
    build_signed_fp_http_req_ex(&req, cfg, client_id, op, sid, up_off, ack_down, want_down, 65536,
                                payload, plen, nonce_seq);
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) {
        free(req.body);
        return -1;
    }
    handle_fp_http_request(sv[0], &req, cfg);
    close(sv[0]);
    free(req.body);

    size_t got = 0;
    while (got + 1 < out_cap) {
        ssize_t r = recv(sv[1], out_raw + got, out_cap - 1 - got, 0);
        if (r <= 0) break;
        got += (size_t)r;
    }
    out_raw[got] = '\0';
    close(sv[1]);
    return (ssize_t)got;
}

static size_t extract_fp_body(const uint8_t *raw, ssize_t raw_len, uint8_t *out_status,
                              uint8_t out_sid[16], uint64_t *out_down_end, const uint8_t **out_payload) {
    if (!raw || raw_len <= 0) return 0;
    const uint8_t *sep = NULL;
    for (ssize_t i = 0; i + 4 <= raw_len; i++) {
        if (raw[i] == '\r' && raw[i + 1] == '\n' && raw[i + 2] == '\r' && raw[i + 3] == '\n') {
            sep = raw + i + 4;
            break;
        }
    }
    if (!sep) return 0;
    size_t body_len = (size_t)((raw + raw_len) - sep);
    if (body_len < FP_RESP_HEADER_SIZE || memcmp(sep, FP_PROTO_MAGIC, 4) != 0) return 0;
    if (out_status) *out_status = sep[4];
    if (out_sid) memcpy(out_sid, sep + 6, 16);
    if (out_down_end) *out_down_end = get_be64_t(sep + 38);
    uint32_t plen = get_be32_t(sep + 54);
    if (FP_RESP_HEADER_SIZE + (size_t)plen > body_len) return 0;
    if (out_payload) *out_payload = sep + FP_RESP_HEADER_SIZE;
    return plen;
}

static void test_finiteproto_udp_over_tcp(void) {
    int ufd = socket(AF_INET, SOCK_DGRAM, 0);
    check("FP-UDP: create loopback UDP echo socket", ufd >= 0);
    struct sockaddr_in sin;
    memset(&sin, 0, sizeof(sin));
    sin.sin_family = AF_INET;
    sin.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    sin.sin_port = 0;
    check("FP-UDP: bind loopback UDP echo socket", bind(ufd, (struct sockaddr *)&sin, sizeof(sin)) == 0);
    socklen_t slen = sizeof(sin);
    getsockname(ufd, (struct sockaddr *)&sin, &slen);
    uint16_t udp_port = ntohs(sin.sin_port);

    udp_echo_ctx_t ectx = { .fd = ufd, .stop = 0 };
    pthread_t eth;
    check("FP-UDP: start UDP echo server thread", pthread_create(&eth, NULL, udp_echo_server_thread, &ectx) == 0);

    xhttpd_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.egress_mode = EGRESS_MODE_DIRECT;
    cfg.allow_private = true;
    cfg.num_clients = 1;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(&cfg, 0, uuid);
    rebuild_fp_credentials(&cfg);
    fp_engine_init();
    fp_engine_revoke_unauthorised(&cfg);

    char target_str[64];
    snprintf(target_str, sizeof(target_str), "udp://127.0.0.1:%u", udp_port);
    uint32_t nseq = 1;

    /* Test 1: Open udp:// and send 2 coalesced datagrams in one FP_OP_UP */
    uint8_t open_sid1[16] = { 0xA1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint8_t raw[4096];
    ssize_t rn = run_fp_rpc_local(&cfg, uuid, FP_OP_OPEN, open_sid1, 0, 0, 0,
                                  (const uint8_t *)target_str, strlen(target_str), nseq++, raw, sizeof(raw));
    uint8_t st = 255, sid1[16] = {0};
    uint64_t down_end = 0;
    const uint8_t *pl = NULL;
    (void)extract_fp_body(raw, rn, &st, sid1, &down_end, &pl);
    check("FP-UDP: open udp:// target succeeds with 200 OK",
          strstr((char *)raw, "HTTP/1.1 200 ") != NULL && st == FP_STATUS_OK);

    const uint8_t coalesced[] = {
        0x00, 0x04, 'P', 'I', 'N', 'G',
        0x00, 0x05, 'W', 'O', 'R', 'L', 'D'
    };
    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_UP, sid1, 0, 0, 0, coalesced, sizeof(coalesced), nseq++, raw, sizeof(raw));
    (void)extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
    check("FP-UDP: coalesced UP succeeds", strstr((char *)raw, "HTTP/1.1 200 ") != NULL && st == FP_STATUS_OK);

    uint8_t rx_buf[512];
    size_t rx_len = 0;
    uint64_t down_cur = 0;
    for (int tries = 0; tries < 20 && rx_len < sizeof(coalesced); tries++) {
        rn = run_fp_rpc_local(&cfg, uuid, FP_OP_DOWN, sid1, 0, down_cur, down_cur, NULL, 0, nseq++, raw, sizeof(raw));
        size_t pgot = extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
        if (pgot > 0 && down_end > down_cur && rx_len + pgot <= sizeof(rx_buf)) {
            memcpy(rx_buf + rx_len, pl, pgot);
            rx_len += pgot;
            down_cur = down_end;
        }
    }
    check("FP-UDP: coalesced datagrams echoed intact via DOWN",
          rx_len == sizeof(coalesced) && memcmp(rx_buf, coalesced, sizeof(coalesced)) == 0);

    /* Test 2: Single datagram split across two FP_OP_UP calls (partial length/payload buffering) */
    const uint8_t split_full[] = { 0x00, 0x09, 'S', 'P', 'L', 'I', 'T', '_', 'P', 'K', 'T' };
    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_UP, sid1, sizeof(coalesced), down_cur, down_cur,
                          split_full, 5, nseq++, raw, sizeof(raw));
    (void)extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
    check("FP-UDP: split part 1 buffered cleanly", st == FP_STATUS_OK);

    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_UP, sid1, sizeof(coalesced) + 5, down_cur, down_cur,
                          split_full + 5, sizeof(split_full) - 5, nseq++, raw, sizeof(raw));
    (void)extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
    check("FP-UDP: split part 2 reassembled cleanly", st == FP_STATUS_OK);

    size_t split_rx_len = 0;
    for (int tries = 0; tries < 20 && split_rx_len < sizeof(split_full); tries++) {
        rn = run_fp_rpc_local(&cfg, uuid, FP_OP_DOWN, sid1, 0, down_cur, down_cur, NULL, 0, nseq++, raw, sizeof(raw));
        size_t pgot = extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
        if (pgot > 0 && down_end > down_cur && split_rx_len + pgot <= sizeof(rx_buf)) {
            memcpy(rx_buf + split_rx_len, pl, pgot);
            split_rx_len += pgot;
            down_cur = down_end;
        }
    }
    check("FP-UDP: split datagram echoed intact",
          split_rx_len == sizeof(split_full) && memcmp(rx_buf, split_full, sizeof(split_full)) == 0);

    run_fp_rpc_local(&cfg, uuid, FP_OP_CLOSE, sid1, 0, 0, 0, NULL, 0, nseq++, raw, sizeof(raw));

    /* Test 3: VLESS-framed VLESS_CMD_UDP open with initial_up datagram */
    uint8_t vopen[32];
    vopen[0] = 0x00;
    memcpy(vopen + 1, cfg.clients[0].uuid, 16);
    vopen[17] = 0x00;
    vopen[18] = VLESS_CMD_UDP;
    vopen[19] = (uint8_t)(udp_port >> 8);
    vopen[20] = (uint8_t)(udp_port & 0xFF);
    vopen[21] = VLESS_ADDR_IPV4;
    vopen[22] = 127; vopen[23] = 0; vopen[24] = 0; vopen[25] = 1;
    const uint8_t vframe[] = { 0x00, 0x04, 'V', 'L', 'U', 'D' };
    memcpy(vopen + 26, vframe, sizeof(vframe));

    uint8_t open_sid2[16] = { 0xA2, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_OPEN, open_sid2, 0, 0, 0, vopen, sizeof(vopen), nseq++, raw, sizeof(raw));
    uint8_t sid2[16] = {0};
    (void)extract_fp_body(raw, rn, &st, sid2, &down_end, &pl);
    check("FP-UDP: VLESS_CMD_UDP open with initial_up succeeds",
          strstr((char *)raw, "HTTP/1.1 200 ") != NULL && st == FP_STATUS_OK);

    down_cur = 0;
    rx_len = 0;
    for (int tries = 0; tries < 20 && rx_len < 2 + sizeof(vframe); tries++) {
        rn = run_fp_rpc_local(&cfg, uuid, FP_OP_DOWN, sid2, 0, down_cur, down_cur, NULL, 0, nseq++, raw, sizeof(raw));
        size_t pgot = extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
        if (pgot > 0 && down_end > down_cur && rx_len + pgot <= sizeof(rx_buf)) {
            memcpy(rx_buf + rx_len, pl, pgot);
            rx_len += pgot;
            down_cur = down_end;
        }
    }
    check("FP-UDP: VLESS_CMD_UDP response has 0x00 0x00 prefix + length-prefixed reply",
          rx_len == 2 + sizeof(vframe) && rx_buf[0] == 0x00 && rx_buf[1] == 0x00 &&
          memcmp(rx_buf + 2, vframe, sizeof(vframe)) == 0);

    run_fp_rpc_local(&cfg, uuid, FP_OP_CLOSE, sid2, 0, 0, 0, NULL, 0, nseq++, raw, sizeof(raw));

    /* Test 4: ACL & QUIC 443 guards */
    const char *quic_str = "udp://8.8.8.8:443";
    uint8_t open_sid3[16] = { 0xA3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_OPEN, open_sid3, 0, 0, 0,
                          (const uint8_t *)quic_str, strlen(quic_str), nseq++, raw, sizeof(raw));
    check("FP-UDP: udp://8.8.8.8:443 QUIC blocked with 403 Forbidden",
          strstr((char *)raw, "HTTP/1.1 403 ") != NULL);

    cfg.allow_private = false;
    uint8_t open_sid4[16] = { 0xA4, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    rn = run_fp_rpc_local(&cfg, uuid, FP_OP_OPEN, open_sid4, 0, 0, 0,
                          (const uint8_t *)target_str, strlen(target_str), nseq++, raw, sizeof(raw));
    check("FP-UDP: private loopback blocked when allow_private=false",
          strstr((char *)raw, "HTTP/1.1 403 ") != NULL);

    /* Test 5: Global buffer accounting returns to 0 */
    usleep(50000);
    check("FP-UDP: global buffer bytes returns to 0 after close",
          fp_engine_get_global_buf_bytes() == 0);

    __atomic_store_n(&ectx.stop, 1, __ATOMIC_RELEASE);
    pthread_join(eth, NULL);
    close(ufd);
}

/* ---- F869: IPv6 egress ---- */
typedef struct {
    int fd;
    volatile int stop;
} fake_dns_ctx_t;

/* Answers: v6only.test -> A NODATA, AAAA 2606:4700:4700::1111; loop6.test -> AAAA ::1 (A NODATA);
 * dual.test -> A 1.1.1.1, AAAA 2606:4700:4700::1001; anything else NXDOMAIN. */
static void *fake_dns_thread(void *arg) {
    fake_dns_ctx_t *c = (fake_dns_ctx_t *)arg;
    while (!__atomic_load_n(&c->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd pfd = { .fd = c->fd, .events = POLLIN };
        if (poll(&pfd, 1, 25) <= 0) continue;
        uint8_t q[512];
        struct sockaddr_storage cli;
        socklen_t clen = sizeof(cli);
        ssize_t n = recvfrom(c->fd, q, sizeof(q), 0, (struct sockaddr *)&cli, &clen);
        if (n < 17) continue;
        char name[256] = {0};
        size_t off = 12, nl = 0;
        while (off < (size_t)n && q[off] != 0 && nl + q[off] + 2 < sizeof(name)) {
            if (nl) name[nl++] = '.';
            memcpy(name + nl, q + off + 1, q[off]);
            nl += q[off];
            off += 1 + q[off];
        }
        off++;
        if (off + 4 > (size_t)n) continue;
        uint16_t qtype = (uint16_t)((q[off] << 8) | q[off + 1]);
        size_t qend = off + 4;
        uint8_t rdata[16];
        size_t rdlen = 0;
        int rcode = 0;
        if (strcmp(name, "v6only.test") == 0) {
            if (qtype == 28) { inet_pton(AF_INET6, "2606:4700:4700::1111", rdata); rdlen = 16; }
        } else if (strcmp(name, "loop6.test") == 0) {
            if (qtype == 28) { inet_pton(AF_INET6, "::1", rdata); rdlen = 16; }
        } else if (strcmp(name, "dual.test") == 0) {
            if (qtype == 1) { inet_pton(AF_INET, "1.1.1.1", rdata); rdlen = 4; }
            if (qtype == 28) { inet_pton(AF_INET6, "2606:4700:4700::1001", rdata); rdlen = 16; }
        } else {
            rcode = 3;
        }
        uint8_t r[600];
        memcpy(r, q, qend);
        r[2] = 0x81; r[3] = (uint8_t)(0x80 | rcode);
        r[6] = 0; r[7] = rdlen ? 1 : 0;
        r[8] = r[9] = r[10] = r[11] = 0;
        size_t ro = qend;
        if (rdlen) {
            uint8_t ans[12] = { 0xc0, 0x0c, (uint8_t)(qtype >> 8), (uint8_t)qtype, 0, 1, 0, 0, 0, 60, 0, (uint8_t)rdlen };
            memcpy(r + ro, ans, 12); ro += 12;
            memcpy(r + ro, rdata, rdlen); ro += rdlen;
        }
        sendto(c->fd, r, ro, 0, (struct sockaddr *)&cli, clen);
    }
    return NULL;
}

static int bind_loopback(int family, int type, uint16_t *out_port) {
    int fd = socket(family, type, 0);
    if (fd < 0) return -1;
    struct sockaddr_storage ss;
    memset(&ss, 0, sizeof(ss));
    socklen_t sl;
    if (family == AF_INET6) {
        struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)&ss;
        s6->sin6_family = AF_INET6;
        s6->sin6_addr = in6addr_loopback;
        sl = sizeof(*s6);
    } else {
        struct sockaddr_in *s4 = (struct sockaddr_in *)&ss;
        s4->sin_family = AF_INET;
        s4->sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        sl = sizeof(*s4);
    }
    if (bind(fd, (struct sockaddr *)&ss, sl) != 0 || (type == SOCK_STREAM && listen(fd, 8) != 0)) {
        close(fd);
        return -1;
    }
    getsockname(fd, (struct sockaddr *)&ss, &sl);
    *out_port = ntohs(family == AF_INET6 ? ((struct sockaddr_in6 *)&ss)->sin6_port
                                         : ((struct sockaddr_in *)&ss)->sin_port);
    return fd;
}

/* Runs one stream-one POST synchronously and returns everything the handler wrote. */
static size_t invoke_vless_post_capture(const xhttpd_config_t *cfg, const uint8_t *body, size_t body_len,
                                        uint8_t *out, size_t out_cap) {
    http_req_t req;
    memset(&req, 0, sizeof(req));
    snprintf(req.method, sizeof(req.method), "POST");
    snprintf(req.path, sizeof(req.path), "/cdn-bridge");
    snprintf(req.peer_ip, sizeof(req.peer_ip), "127.0.0.1");
    req.content_length = (int64_t)body_len;
    req.body = (uint8_t *)malloc(body_len);
    memcpy(req.body, body, body_len);
    req.body_len = body_len;
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) { free(req.body); return 0; }
    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = sv[0];
    handle_vless_http_request(sv[0], &cr, &req, "/cdn-bridge", cfg);
    close(sv[0]);
    free(req.body);
    size_t got = 0;
    while (got < out_cap) {
        ssize_t r = recv(sv[1], out + got, out_cap - got, 0);
        if (r <= 0) break;
        got += (size_t)r;
    }
    close(sv[1]);
    return got;
}

typedef struct {
    int lfd;
    uint8_t atyp;
    uint8_t addr[16];
} socks_atyp_probe_t;

/* Minimal SOCKS5 CONNECT endpoint: records ATYP/address and answers success. */
static void *socks_atyp_probe_thread(void *arg) {
    socks_atyp_probe_t *p = (socks_atyp_probe_t *)arg;
    struct pollfd lp = { .fd = p->lfd, .events = POLLIN };
    if (poll(&lp, 1, 3000) <= 0) return NULL;
    int c = accept(p->lfd, NULL, NULL);
    if (c < 0) return NULL;
    uint8_t b[64];
    if (recv(c, b, 3, MSG_WAITALL) == 3 && write_all(c, "\x05\x00", 2) == 0 &&
        recv(c, b, 4, MSG_WAITALL) == 4) {
        p->atyp = b[3];
        size_t alen = b[3] == 0x04 ? 16 : (b[3] == 0x01 ? 4 : 0);
        if (alen && recv(c, p->addr, alen, MSG_WAITALL) == (ssize_t)alen && recv(c, b, 2, MSG_WAITALL) == 2) {
            write_all(c, "\x05\x00\x00\x01\x7f\x00\x00\x01\x00\x00", 10);
        }
    }
    usleep(100000);
    close(c);
    return NULL;
}

static void test_f869_ipv6_egress(void) {
    /* ACL: Teredo and NAT64 (Go internal/ipinspect parity) */
    check("F869: blocks Teredo 2001::/32", !check_destination_acl_ex("2001:0:4136:e378:8000:63bf:3fff:fdd2", false, false));
    check("F869: blocks NAT64 64:ff9b::7f00:1 (embedded loopback)", !check_destination_acl_ex("64:ff9b::7f00:1", false, false));
    check("F869: blocks NAT64 64:ff9b::c0a8:101 (embedded 192.168.1.1)", !check_destination_acl_ex("64:ff9b::c0a8:101", false, false));
    check("F869: allows NAT64 64:ff9b::808:808 (embedded 8.8.8.8)", check_destination_acl_ex("64:ff9b::808:808", false, false));
    check("F869: blocks local-use NAT64 64:ff9b:1::/48", !check_destination_acl_ex("64:ff9b:1::808:808", false, false));
    check("F869: still allows 2001:4860::8888 (not Teredo)", check_destination_acl_ex("2001:4860::8888", false, false));

    /* Config: domain_strategy=ipv4_only */
    char conf_path[] = "/tmp/xhttpd-f869-XXXXXX";
    int cfd = mkstemp(conf_path);
    const char *conf = "client=b831381d-6324-4d53-ad4f-8cda48b30811\ndomain_strategy=ipv4_only\n";
    check("F869: write config", cfd >= 0 && write(cfd, conf, strlen(conf)) == (ssize_t)strlen(conf));
    close(cfd);
    xhttpd_config_t *lc = (xhttpd_config_t *)calloc(1, sizeof(*lc));
    check("F869: domain_strategy=ipv4_only parsed", load_config_file(conf_path, lc) == 0 && lc->ipv4_only);
    unlink(conf_path);
    free(lc);

    /* AAAA fallback through the FIRC DNS proxy */
    uint16_t dns_port = 0;
    fake_dns_ctx_t dctx = { .fd = bind_loopback(AF_INET, SOCK_DGRAM, &dns_port), .stop = 0 };
    pthread_t dth;
    check("F869: fake DNS started", dctx.fd >= 0 && pthread_create(&dth, NULL, fake_dns_thread, &dctx) == 0);
    char ip[64] = {0};
    int rc = resolve_host_ip("v6only.test", EGRESS_MODE_ROUTER, "127.0.0.1", dns_port, false, false, ip, sizeof(ip));
    check("F869: AAAA-only name resolves to IPv6", rc == 0 && strcmp(ip, "2606:4700:4700::1111") == 0);
    memset(ip, 0, sizeof(ip));
    rc = resolve_host_ip("dual.test", EGRESS_MODE_ROUTER, "127.0.0.1", dns_port, false, false, ip, sizeof(ip));
    check("F869: dual-stack name keeps A first", rc == 0 && strcmp(ip, "1.1.1.1") == 0);
    rc = resolve_host_ip("loop6.test", EGRESS_MODE_ROUTER, "127.0.0.1", dns_port, false, false, ip, sizeof(ip));
    check("F869: AAAA answer ::1 still goes through ACL", rc != 0);
    rc = resolve_host_ip("v6only.test", EGRESS_MODE_ROUTER, "127.0.0.1", dns_port, false, true, ip, sizeof(ip));
    check("F869: ipv4_only skips AAAA", rc != 0);

    /* SOCKS egress: IPv6 target goes out as ATYP 4 */
    {
        socks_atyp_probe_t sp;
        memset(&sp, 0, sizeof(sp));
        uint16_t sport = 0;
        sp.lfd = bind_loopback(AF_INET, SOCK_STREAM, &sport);
        pthread_t sth;
        pthread_create(&sth, NULL, socks_atyp_probe_thread, &sp);
        xhttpd_config_t scfg;
        memset(&scfg, 0, sizeof(scfg));
        scfg.egress_mode = EGRESS_MODE_SOCKS;
        snprintf(scfg.socks_host, sizeof(scfg.socks_host), "127.0.0.1");
        scfg.socks_port = sport;
        int sfd = dial_target_tcp("2606:4700:4700::1111", 443, NULL, 0, &scfg, 2000);
        pthread_join(sth, NULL);
        uint8_t want[16];
        inet_pton(AF_INET6, "2606:4700:4700::1111", want);
        check("F869: SOCKS CONNECT to IPv6 uses ATYP 4", sfd >= 0 && sp.atyp == 0x04 && memcmp(sp.addr, want, 16) == 0);
        if (sfd >= 0) close(sfd);
        close(sp.lfd);
    }

    /* Dial over IPv6: literal (VLESS atyp 3) and AAAA-resolved name, TCP and UDP */
    uint16_t tport = 0, uport = 0;
    int tl = bind_loopback(AF_INET6, SOCK_STREAM, &tport);
    int ul = bind_loopback(AF_INET6, SOCK_DGRAM, &uport);
    if (tl < 0 || ul < 0) {
        printf("SKIP: F869 IPv6 dial checks: no ::1 on this host (run under `unshare -rn` with lo up)\n");
    } else {
        xhttpd_config_t cfg;
        memset(&cfg, 0, sizeof(cfg));
        cfg.egress_mode = EGRESS_MODE_ROUTER;
        cfg.allow_private = true;
        snprintf(cfg.dns_proxy_host, sizeof(cfg.dns_proxy_host), "127.0.0.1");
        cfg.dns_proxy_port = dns_port;

        int fd = dial_target_tcp("::1", tport, NULL, 0, &cfg, 2000);
        int afd = (fd >= 0) ? accept(tl, NULL, NULL) : -1;
        check("F869: TCP dial to IPv6 literal ::1", fd >= 0 && afd >= 0);
        if (fd >= 0) close(fd);
        if (afd >= 0) close(afd);

        fd = dial_target_tcp("loop6.test", tport, NULL, 0, &cfg, 2000);
        afd = (fd >= 0) ? accept(tl, NULL, NULL) : -1;
        check("F869: TCP dial to AAAA-only name", fd >= 0 && afd >= 0);
        if (fd >= 0) close(fd);
        if (afd >= 0) close(afd);

        udp_echo_ctx_t ectx = { .fd = ul, .stop = 0 };
        pthread_t eth;
        pthread_create(&eth, NULL, udp_echo_server_thread, &ectx);
        udp_egress_t ue;
        int ufd = dial_target_udp("::1", uport, &cfg, &ue);
        uint8_t ubuf[64];
        ssize_t un = -1;
        if (ufd >= 0 && udp_egress_send(ufd, &ue, (const uint8_t *)"V6UDP", 5) == 5) {
            struct pollfd upfd = { .fd = ufd, .events = POLLIN };
            if (poll(&upfd, 1, 2000) > 0) un = udp_egress_recv(ufd, &ue, ubuf, sizeof(ubuf), 0);
        }
        check("F869: UDP echo over IPv6", un == 5 && memcmp(ubuf, "V6UDP", 5) == 0);
        if (ufd >= 0) close(ufd);

        /* Mux (XUDP) New frame with mux atyp 3 (IPv6) */
        add_cfg_client(&cfg, 0, "b831381d-6324-4d53-ad4f-8cda48b30811");
        cfg.num_clients = 1;
        uint8_t body[128];
        size_t bl = 0;
        body[bl++] = 0x00;
        memcpy(body + bl, cfg.clients[0].uuid, 16); bl += 16;
        body[bl++] = 0x00;
        body[bl++] = VLESS_CMD_MUX;
        /* meta: sub_id, New, OptionData, net UDP, port, atyp 3, 16 bytes */
        size_t mlen = 2 + 1 + 1 + 1 + 2 + 1 + 16;
        body[bl++] = 0; body[bl++] = (uint8_t)mlen;
        body[bl++] = 0; body[bl++] = 7;
        body[bl++] = 0x01; body[bl++] = 0x01; body[bl++] = 0x02;
        body[bl++] = (uint8_t)(uport >> 8); body[bl++] = (uint8_t)uport;
        body[bl++] = 0x03;
        inet_pton(AF_INET6, "::1", body + bl); bl += 16;
        body[bl++] = 0; body[bl++] = 6;
        memcpy(body + bl, "MUXV6!", 6); bl += 6;
        uint8_t out[2048];
        size_t got = invoke_vless_post_capture(&cfg, body, bl, out, sizeof(out));
        check("F869: mux UDP New frame to IPv6 (atyp 3) echoed", memmem(out, got, "MUXV6!", 6) != NULL);

        __atomic_store_n(&ectx.stop, 1, __ATOMIC_RELEASE);
        pthread_join(eth, NULL);
    }
    if (tl >= 0) close(tl);
    if (ul >= 0) close(ul);
    __atomic_store_n(&dctx.stop, 1, __ATOMIC_RELEASE);
    pthread_join(dth, NULL);
    close(dctx.fd);
}

/* ---- F870/F863: stream-one slots, per-IP cap, mux revocation ---- */
typedef struct {
    int lfd;
    int fds[256];
    int n;
    volatile int stop;
} tcp_holder_t;

/* Accepts and holds target connections open, never answers. */
static void *tcp_holder_thread(void *arg) {
    tcp_holder_t *h = (tcp_holder_t *)arg;
    while (!__atomic_load_n(&h->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd p = { .fd = h->lfd, .events = POLLIN };
        if (poll(&p, 1, 25) <= 0) continue;
        int c = accept(h->lfd, NULL, NULL);
        if (c >= 0 && h->n < 256) h->fds[h->n++] = c;
        else if (c >= 0) close(c);
    }
    return NULL;
}

typedef struct {
    const xhttpd_config_t *cfg;
    uint8_t body[128];
    size_t body_len;
    char xff[64];
    int sv[2];
    volatile int done;
} so_worker_t;

static void *so_worker_thread(void *arg) {
    so_worker_t *w = (so_worker_t *)arg;
    http_req_t req;
    memset(&req, 0, sizeof(req));
    snprintf(req.method, sizeof(req.method), "POST");
    snprintf(req.path, sizeof(req.path), "/cdn-bridge");
    snprintf(req.peer_ip, sizeof(req.peer_ip), "127.0.0.1");
    snprintf(req.xff, sizeof(req.xff), "%s", w->xff);
    req.content_length = (int64_t)w->body_len;
    req.body = w->body;
    req.body_len = w->body_len;
    conn_reader_t cr;
    memset(&cr, 0, sizeof(cr));
    cr.fd = w->sv[0];
    handle_vless_http_request(w->sv[0], &cr, &req, "/cdn-bridge", w->cfg);
    shutdown(w->sv[0], SHUT_RDWR);
    __atomic_store_n(&w->done, 1, __ATOMIC_RELEASE);
    return NULL;
}

static size_t build_vless_body(uint8_t *b, const uint8_t uuid[16], uint8_t cmd, uint16_t port) {
    size_t n = 0;
    b[n++] = 0x00;
    memcpy(b + n, uuid, 16); n += 16;
    b[n++] = 0x00;
    b[n++] = cmd;
    if (cmd == VLESS_CMD_MUX) return n;
    b[n++] = (uint8_t)(port >> 8); b[n++] = (uint8_t)port;
    b[n++] = VLESS_ADDR_IPV4;
    b[n++] = 127; b[n++] = 0; b[n++] = 0; b[n++] = 1;
    b[n++] = 'x';
    return n;
}

/* Starts a stream-one in its own thread (512 KiB stack, as a real worker) and
 * returns the HTTP status line code seen on the client side (200 = stream started). */
static int start_so_worker(so_worker_t *w, pthread_t *th) {
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, w->sv) != 0) return -1;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setstacksize(&attr, 524288);
    int prc = pthread_create(th, &attr, so_worker_thread, w);
    pthread_attr_destroy(&attr);
    if (prc != 0) return -1;
    char hdr[64] = {0};
    if (!recv_until_substr_ex(w->sv[1], "\r\n", hdr, sizeof(hdr), 3000)) return -1;
    int code = -1;
    (void)sscanf(hdr, "HTTP/1.1 %d", &code);
    return code;
}

static bool wait_all_done(so_worker_t *ws, int n, int timeout_ms) {
    uint64_t deadline = now_ms() + (uint64_t)timeout_ms;
    for (;;) {
        bool all = true;
        for (int i = 0; i < n; i++) {
            if (!__atomic_load_n(&ws[i].done, __ATOMIC_ACQUIRE)) { all = false; break; }
        }
        if (all) return true;
        if (now_ms() >= deadline) return false;
        usleep(10000);
    }
}

static void test_f870_stream_one_limits_and_revocation(void) {
    vless_engine_init();
    uint16_t hport = 0;
    tcp_holder_t holder;
    memset(&holder, 0, sizeof(holder));
    holder.lfd = bind_loopback(AF_INET, SOCK_STREAM, &hport);
    listen(holder.lfd, 256);
    pthread_t hth;
    pthread_create(&hth, NULL, tcp_holder_thread, &holder);

    xhttpd_config_t *cfg = (xhttpd_config_t *)calloc(1, sizeof(*cfg));
    cfg->egress_mode = EGRESS_MODE_DIRECT;
    cfg->allow_private = true;
    cfg->idle_timeout_sec = 120;
    cfg->max_sessions_per_ip = 512; /* what vlessserver writes */
    snprintf(cfg->trusted_proxies[0], sizeof(cfg->trusted_proxies[0]), "127.0.0.1/32");
    cfg->num_trusted_proxies = 1;
    add_cfg_client(cfg, 0, "b831381d-6324-4d53-ad4f-8cda48b30811");
    cfg->num_clients = 1;
    xhttpd_config_t *empty = (xhttpd_config_t *)calloc(1, sizeof(*empty));

    /* 1. 64 streams from one REALITY client (loopback peer + XFF) and 8 from another:
     *    more than the old 64-slot table; the 65th from the first IP is refused. */
    enum { NA = 64, NB = 8, NT = NA + NB };
    so_worker_t *ws = (so_worker_t *)calloc(NT + 1, sizeof(*ws));
    pthread_t *ths = (pthread_t *)calloc(NT + 1, sizeof(*ths));
    int ok200 = 0;
    for (int i = 0; i < NT; i++) {
        ws[i].cfg = cfg;
        ws[i].body_len = build_vless_body(ws[i].body, cfg->clients[0].uuid, VLESS_CMD_TCP, hport);
        snprintf(ws[i].xff, sizeof(ws[i].xff), "%s", i < NA ? "198.51.100.10" : "198.51.100.20");
        if (start_so_worker(&ws[i], &ths[i]) == 200) ok200++;
    }
    check("F870: 72 concurrent stream-one (64 + 8 from two IPs) all start", ok200 == NT);
    ws[NT].cfg = cfg;
    ws[NT].body_len = build_vless_body(ws[NT].body, cfg->clients[0].uuid, VLESS_CMD_TCP, hport);
    snprintf(ws[NT].xff, sizeof(ws[NT].xff), "198.51.100.10");
    int code = start_so_worker(&ws[NT], &ths[NT]);
    check("F870: 65th stream-one from one client IP gets 429 (cap = workers/2)", code == 429);
    pthread_join(ths[NT], NULL);
    close(ws[NT].sv[1]);
    close(ws[NT].sv[0]);

    /* 2. Removing the client revokes every stream, including those past slot 64 */
    uint64_t t0 = now_ms();
    vless_engine_revoke_unauthorised(empty);
    bool all_done = wait_all_done(ws, NT, 3000);
    check("F870: client removal ends all 72 stream-one streams", all_done);
    printf("INFO: F870 revocation of %d streams took %llu ms\n", NT, (unsigned long long)(now_ms() - t0));
    if (all_done) {
        for (int i = 0; i < NT; i++) {
            pthread_join(ths[i], NULL);
            close(ws[i].sv[0]);
            close(ws[i].sv[1]);
        }
    }

    /* 3. A lower configured max_sessions_per_ip applies to stream-one too */
    cfg->max_sessions_per_ip = 2;
    memset(ws, 0, sizeof(*ws) * 4);
    int codes[4];
    const char *ips[4] = { "203.0.113.5", "203.0.113.5", "203.0.113.5", "203.0.113.6" };
    for (int i = 0; i < 4; i++) {
        ws[i].cfg = cfg;
        ws[i].body_len = build_vless_body(ws[i].body, cfg->clients[0].uuid, VLESS_CMD_TCP, hport);
        snprintf(ws[i].xff, sizeof(ws[i].xff), "%s", ips[i]);
        codes[i] = start_so_worker(&ws[i], &ths[i]);
    }
    check("F870: max_sessions_per_ip=2 admits two stream-one per IP, refuses the third",
          codes[0] == 200 && codes[1] == 200 && codes[2] == 429 && codes[3] == 200);
    vless_engine_revoke_unauthorised(empty);
    check("F870: capped-IP streams revoked", wait_all_done(ws, 4, 3000));
    for (int i = 0; i < 4; i++) {
        pthread_join(ths[i], NULL);
        close(ws[i].sv[0]);
        close(ws[i].sv[1]);
    }

    /* 4. Mux (XUDP) stream-one: frames already queued must not keep running after
     *    revocation. Five UDP frames to a sink that never answers cost 2 s each. */
    uint16_t sink_port = 0;
    int sink = bind_loopback(AF_INET, SOCK_DGRAM, &sink_port);
    so_worker_t mw;
    memset(&mw, 0, sizeof(mw));
    mw.cfg = cfg;
    mw.body_len = build_vless_body(mw.body, cfg->clients[0].uuid, VLESS_CMD_MUX, 0);
    for (int f = 0; f < 5; f++) {
        uint8_t *b = mw.body + mw.body_len;
        b[0] = 0; b[1] = 12;                         /* meta len */
        b[2] = 0; b[3] = (uint8_t)(f + 1);           /* sub id */
        b[4] = 0x01; b[5] = 0x01; b[6] = 0x02;       /* New, OptionData, UDP */
        b[7] = (uint8_t)(sink_port >> 8); b[8] = (uint8_t)sink_port;
        b[9] = 0x01; b[10] = 127; b[11] = 0; b[12] = 0; b[13] = 1;
        b[14] = 0; b[15] = 1; b[16] = 'q';
        mw.body_len += 17;
    }
    snprintf(mw.xff, sizeof(mw.xff), "203.0.113.9");
    pthread_t mth;
    code = start_so_worker(&mw, &mth);
    usleep(300000);
    t0 = now_ms();
    vless_engine_revoke_unauthorised(empty);
    bool mux_done = wait_all_done(&mw, 1, 4000);
    check("F870: mux stream-one stops within 1 s of client removal", code == 200 && mux_done && now_ms() - t0 < 1000);
    pthread_join(mth, NULL);
    close(mw.sv[0]);
    close(mw.sv[1]);
    close(sink);

    __atomic_store_n(&holder.stop, 1, __ATOMIC_RELEASE);
    pthread_join(hth, NULL);
    for (int i = 0; i < holder.n; i++) close(holder.fds[i]);
    close(holder.lfd);
    free(ws);
    free(ths);
    free(cfg);
    free(empty);
}

/* ---- F864: UDP in socks egress mode goes through SOCKS5 UDP ASSOCIATE ---- */
typedef struct {
    int lfd;
    volatile int stop;
    int assoc;           /* UDP ASSOCIATE requests served */
    int ctrl_closed;     /* control connections the client closed */
    int dgrams_ok;       /* client datagrams with RSV=0 FRAG=0 ATYP=1 */
    uint16_t out_ports[16];
    pthread_mutex_t mu;
} socks_udp_relay_t;

typedef struct {
    socks_udp_relay_t *r;
    int ctrl;
} socks_udp_conn_t;

static void *socks_udp_conn_thread(void *arg) {
    socks_udp_conn_t *c = (socks_udp_conn_t *)arg;
    socks_udp_relay_t *r = c->r;
    uint8_t b[2048];
    int ctrl = c->ctrl;
    free(c);
    if (recv(ctrl, b, 3, MSG_WAITALL) != 3 || write_all(ctrl, "\x05\x00", 2) < 0 ||
        recv(ctrl, b, 10, MSG_WAITALL) != 10 || b[1] != 0x03) {
        close(ctrl);
        return NULL;
    }
    uint16_t uport = 0, oport = 0;
    int ufd = bind_loopback(AF_INET, SOCK_DGRAM, &uport);
    int ofd = bind_loopback(AF_INET, SOCK_DGRAM, &oport);
    /* BND.ADDR 0.0.0.0: the client must fall back to the SOCKS server address */
    uint8_t rep[10] = { 0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, (uint8_t)(uport >> 8), (uint8_t)uport };
    write_all(ctrl, rep, sizeof(rep));
    pthread_mutex_lock(&r->mu);
    if (r->assoc < 16) r->out_ports[r->assoc] = oport;
    r->assoc++;
    pthread_mutex_unlock(&r->mu);
    struct sockaddr_in cli;
    socklen_t clen = 0;
    while (!__atomic_load_n(&r->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd p[3] = { { .fd = ctrl, .events = POLLIN }, { .fd = ufd, .events = POLLIN }, { .fd = ofd, .events = POLLIN } };
        if (poll(p, 3, 25) <= 0) continue;
        if (p[0].revents) {
            if (recv(ctrl, b, sizeof(b), 0) <= 0) {
                pthread_mutex_lock(&r->mu);
                r->ctrl_closed++;
                pthread_mutex_unlock(&r->mu);
                break;
            }
        }
        if (p[1].revents & POLLIN) {
            clen = sizeof(cli);
            ssize_t n = recvfrom(ufd, b, sizeof(b), 0, (struct sockaddr *)&cli, &clen);
            if (n > 10 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 0x01) {
                pthread_mutex_lock(&r->mu);
                r->dgrams_ok++;
                pthread_mutex_unlock(&r->mu);
                struct sockaddr_in dst;
                memset(&dst, 0, sizeof(dst));
                dst.sin_family = AF_INET;
                memcpy(&dst.sin_addr, b + 4, 4);
                memcpy(&dst.sin_port, b + 8, 2);
                sendto(ofd, b + 10, (size_t)n - 10, 0, (struct sockaddr *)&dst, sizeof(dst));
            }
        }
        if ((p[2].revents & POLLIN) && clen > 0) {
            struct sockaddr_in src;
            socklen_t slen = sizeof(src);
            uint8_t o[2048];
            ssize_t n = recvfrom(ofd, o + 10, sizeof(o) - 10, 0, (struct sockaddr *)&src, &slen);
            if (n > 0) {
                o[0] = 0; o[1] = 0; o[3] = 0x01;
                memcpy(o + 4, &src.sin_addr, 4);
                memcpy(o + 8, &src.sin_port, 2);
                uint8_t junk[14];
                memcpy(junk, o, 10);
                junk[2] = 0x01; /* FRAG=1: must be dropped by the client */
                memcpy(junk + 10, "JUNK", 4);
                sendto(ufd, junk, sizeof(junk), 0, (struct sockaddr *)&cli, clen);
                o[2] = 0;
                sendto(ufd, o, (size_t)n + 10, 0, (struct sockaddr *)&cli, clen);
            }
        }
    }
    close(ufd);
    close(ofd);
    close(ctrl);
    return NULL;
}

static void *socks_udp_relay_thread(void *arg) {
    socks_udp_relay_t *r = (socks_udp_relay_t *)arg;
    while (!__atomic_load_n(&r->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd p = { .fd = r->lfd, .events = POLLIN };
        if (poll(&p, 1, 25) <= 0) continue;
        int c = accept(r->lfd, NULL, NULL);
        if (c < 0) continue;
        socks_udp_conn_t *ca = (socks_udp_conn_t *)malloc(sizeof(*ca));
        ca->r = r;
        ca->ctrl = c;
        pthread_t th;
        if (pthread_create(&th, NULL, socks_udp_conn_thread, ca) == 0) pthread_detach(th);
        else { close(c); free(ca); }
    }
    return NULL;
}

typedef struct {
    int fd;
    volatile int stop;
    uint16_t last_src_port;
    int hits;
} udp_prefix_echo_t;

static void *udp_prefix_echo_thread(void *arg) {
    udp_prefix_echo_t *e = (udp_prefix_echo_t *)arg;
    uint8_t b[2048];
    while (!__atomic_load_n(&e->stop, __ATOMIC_ACQUIRE)) {
        struct pollfd p = { .fd = e->fd, .events = POLLIN };
        if (poll(&p, 1, 25) <= 0) continue;
        struct sockaddr_in src;
        socklen_t sl = sizeof(src);
        ssize_t n = recvfrom(e->fd, b + 2, sizeof(b) - 2, 0, (struct sockaddr *)&src, &sl);
        if (n <= 0) continue;
        __atomic_store_n(&e->last_src_port, ntohs(src.sin_port), __ATOMIC_RELEASE);
        __atomic_add_fetch(&e->hits, 1, __ATOMIC_ACQ_REL);
        b[0] = 'E'; b[1] = ':';
        sendto(e->fd, b, (size_t)n + 2, 0, (struct sockaddr *)&src, sl);
    }
    return NULL;
}

static bool relay_owns_port(socks_udp_relay_t *r, uint16_t port) {
    bool found = false;
    pthread_mutex_lock(&r->mu);
    for (int i = 0; i < r->assoc && i < 16; i++) {
        if (r->out_ports[i] == port) found = true;
    }
    pthread_mutex_unlock(&r->mu);
    return found;
}

static bool wait_ctrl_closed(socks_udp_relay_t *r, int want, int timeout_ms) {
    uint64_t deadline = now_ms() + (uint64_t)timeout_ms;
    while (now_ms() < deadline) {
        pthread_mutex_lock(&r->mu);
        int got = r->ctrl_closed;
        pthread_mutex_unlock(&r->mu);
        if (got >= want) return true;
        usleep(10000);
    }
    return false;
}

static void test_f864_socks_udp_associate(void) {
    socks_udp_relay_t relay;
    memset(&relay, 0, sizeof(relay));
    pthread_mutex_init(&relay.mu, NULL);
    uint16_t sport = 0;
    relay.lfd = bind_loopback(AF_INET, SOCK_STREAM, &sport);
    pthread_t rth;
    pthread_create(&rth, NULL, socks_udp_relay_thread, &relay);

    uint16_t eport = 0;
    udp_prefix_echo_t echo = { .fd = bind_loopback(AF_INET, SOCK_DGRAM, &eport), .stop = 0 };
    pthread_t eth;
    pthread_create(&eth, NULL, udp_prefix_echo_thread, &echo);

    xhttpd_config_t *cfg = (xhttpd_config_t *)calloc(1, sizeof(*cfg));
    cfg->egress_mode = EGRESS_MODE_SOCKS;
    snprintf(cfg->socks_host, sizeof(cfg->socks_host), "127.0.0.1");
    cfg->socks_port = sport;
    cfg->allow_private = true; /* target is loopback; 127/8 is not "LAN", so it still goes via SOCKS */
    cfg->max_sessions_per_ip = 16;
    const char *uuid = "b831381d-6324-4d53-ad4f-8cda48b30811";
    add_cfg_client(cfg, 0, uuid);
    cfg->num_clients = 1;
    rebuild_fp_credentials(cfg);
    vless_engine_init();

    /* 1. VLESS command UDP (stream-one) */
    uint8_t body[128];
    size_t bl = 0;
    body[bl++] = 0x00;
    memcpy(body + bl, cfg->clients[0].uuid, 16); bl += 16;
    body[bl++] = 0x00;
    body[bl++] = VLESS_CMD_UDP;
    body[bl++] = (uint8_t)(eport >> 8); body[bl++] = (uint8_t)eport;
    body[bl++] = VLESS_ADDR_IPV4;
    body[bl++] = 127; body[bl++] = 0; body[bl++] = 0; body[bl++] = 1;
    body[bl++] = 0; body[bl++] = 4;
    memcpy(body + bl, "ping", 4); bl += 4;
    uint8_t out[4096];
    size_t got = invoke_vless_post_capture(cfg, body, bl, out, sizeof(out));
    check("F864: VLESS UDP reply comes back through SOCKS relay",
          memmem(out, got, "E:ping", 6) != NULL && relay_owns_port(&relay, __atomic_load_n(&echo.last_src_port, __ATOMIC_ACQUIRE)));
    check("F864: fragmented SOCKS UDP datagram is dropped", memmem(out, got, "JUNK", 4) == NULL);
    pthread_mutex_lock(&relay.mu);
    int dgrams_ok = relay.dgrams_ok;
    pthread_mutex_unlock(&relay.mu);
    check("F864: datagram to relay carries RSV/FRAG=0/ATYP header", dgrams_ok >= 1);
    check("F864: control connection closed when the UDP stream ends", wait_ctrl_closed(&relay, 1, 3000));

    /* 2. Mux (XUDP) UDP sub-session */
    bl = 0;
    body[bl++] = 0x00;
    memcpy(body + bl, cfg->clients[0].uuid, 16); bl += 16;
    body[bl++] = 0x00;
    body[bl++] = VLESS_CMD_MUX;
    body[bl++] = 0; body[bl++] = 12;
    body[bl++] = 0; body[bl++] = 3;
    body[bl++] = 0x01; body[bl++] = 0x01; body[bl++] = 0x02;
    body[bl++] = (uint8_t)(eport >> 8); body[bl++] = (uint8_t)eport;
    body[bl++] = 0x01; body[bl++] = 127; body[bl++] = 0; body[bl++] = 0; body[bl++] = 1;
    body[bl++] = 0; body[bl++] = 3;
    memcpy(body + bl, "mux", 3); bl += 3;
    int hits_before = __atomic_load_n(&echo.hits, __ATOMIC_ACQUIRE);
    got = invoke_vless_post_capture(cfg, body, bl, out, sizeof(out));
    check("F864: mux UDP reply comes back through SOCKS relay",
          memmem(out, got, "E:mux", 5) != NULL && __atomic_load_n(&echo.hits, __ATOMIC_ACQUIRE) > hits_before &&
          relay_owns_port(&relay, __atomic_load_n(&echo.last_src_port, __ATOMIC_ACQUIRE)));
    check("F864: mux control connection closed", wait_ctrl_closed(&relay, 2, 3000));

    /* 3. FiniteProto udp:// session */
    fp_engine_init();
    fp_engine_revoke_unauthorised(cfg);
    char target[64];
    snprintf(target, sizeof(target), "udp://127.0.0.1:%u", eport);
    uint8_t raw[4096];
    uint8_t osid[16] = { 0xB1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15 };
    uint32_t nseq = 1;
    ssize_t rn = run_fp_rpc_local(cfg, uuid, FP_OP_OPEN, osid, 0, 0, 0, (const uint8_t *)target, strlen(target), nseq++, raw, sizeof(raw));
    uint8_t st = 255, sid[16] = {0};
    uint64_t down_end = 0;
    const uint8_t *pl = NULL;
    (void)extract_fp_body(raw, rn, &st, sid, &down_end, &pl);
    const uint8_t frame[] = { 0x00, 0x02, 'f', 'p' };
    rn = run_fp_rpc_local(cfg, uuid, FP_OP_UP, sid, 0, 0, 0, frame, sizeof(frame), nseq++, raw, sizeof(raw));
    uint8_t rx[256];
    size_t rx_len = 0;
    uint64_t down_cur = 0;
    for (int tries = 0; tries < 40 && rx_len < 6; tries++) {
        rn = run_fp_rpc_local(cfg, uuid, FP_OP_DOWN, sid, 0, down_cur, down_cur, NULL, 0, nseq++, raw, sizeof(raw));
        size_t pgot = extract_fp_body(raw, rn, &st, NULL, &down_end, &pl);
        if (pgot > 0 && down_end > down_cur && rx_len + pgot <= sizeof(rx)) {
            memcpy(rx + rx_len, pl, pgot);
            rx_len += pgot;
            down_cur = down_end;
        } else {
            usleep(20000);
        }
    }
    check("F864: FiniteProto UDP reply comes back through SOCKS relay (fragment dropped)",
          rx_len == 6 && memcmp(rx, "\x00\x04" "E:fp", 6) == 0 &&
          relay_owns_port(&relay, __atomic_load_n(&echo.last_src_port, __ATOMIC_ACQUIRE)));
    run_fp_rpc_local(cfg, uuid, FP_OP_CLOSE, sid, 0, 0, 0, NULL, 0, nseq++, raw, sizeof(raw));
    check("F864: FiniteProto close releases the control connection", wait_ctrl_closed(&relay, 3, 3000));

    __atomic_store_n(&echo.stop, 1, __ATOMIC_RELEASE);
    __atomic_store_n(&relay.stop, 1, __ATOMIC_RELEASE);
    pthread_join(eth, NULL);
    pthread_join(rth, NULL);
    usleep(50000);
    close(echo.fd);
    close(relay.lfd);
    free(cfg);
}
#endif

static void *suite_watchdog_thread(void *arg) {
    (void)arg;
    for (int i = 0; i < 450; i++) { /* 45 seconds total watchdog */
        usleep(100000);
    }
    fprintf(stderr, "\n[FATAL] xhttpd_audit_test watchdog timeout (45s exceeded)\n");
    _exit(2);
    return NULL;
}

int main(void) {
    setvbuf(stdout, NULL, _IONBF, 0);
    setvbuf(stderr, NULL, _IONBF, 0);
#ifdef _WIN32
    init_winsock();
#else
    signal(SIGPIPE, SIG_IGN);
#endif
    pthread_t wd_th;
    if (pthread_create(&wd_th, NULL, suite_watchdog_thread, NULL) == 0) {
        pthread_detach(wd_th);
    }
    printf(">>> Starting test_a1_a9_credentials...\n");
    test_a1_a9_credentials();
    printf(">>> Starting test_a2_http_parser...\n");
    test_a2_http_parser();
    printf(">>> Starting test_a3_a4_a5_finiteproto...\n");
    test_a3_a4_a5_finiteproto();
    printf(">>> Starting test_a6_acl_and_defaults...\n");
    test_a6_acl_and_defaults();
#ifndef _WIN32
    printf(">>> Starting test_a7_fakeip_dnat...\n");
    test_a7_fakeip_dnat();
#endif
    printf(">>> Starting test_r1_global_budget_ack_and_concurrent_growth...\n");
    test_r1_global_budget_ack_and_concurrent_growth();
    printf(">>> Starting test_r2_http_absolute_deadline_drip_feed...\n");
    test_r2_http_absolute_deadline_drip_feed();
    printf(">>> Starting test_r3_reader_running_barrier_and_slot_reuse...\n");
    test_r3_reader_running_barrier_and_slot_reuse();
    printf(">>> Starting test_fp_v2_auth_and_revocation...\n");
    test_fp_v2_auth_and_revocation();
    printf(">>> Starting test_fp_v2_barrier_races...\n");
    test_fp_v2_barrier_races();
    printf(">>> Starting test_fp_v2_pre_auth_revocation_barrier...\n");
    test_fp_v2_pre_auth_revocation_barrier();
    printf(">>> Starting test_fp_v2_open_initial_write_cancellation_and_table_availability...\n");
    test_fp_v2_open_initial_write_cancellation_and_table_availability();
    printf(">>> Starting test_xhttpd_reload_endpoint_security...\n");
    test_xhttpd_reload_endpoint_security();
    printf(">>> Starting test_universal_http_methods_finiteproto...\n");
    test_universal_http_methods_finiteproto();
    printf(">>> Starting test_vless_session_races_and_small_stack_reload...\n");
    test_vless_session_races_and_small_stack_reload();
    printf(">>> Starting test_fp_up_stalled_target_write_cancels_bounded...\n");
    test_fp_up_stalled_target_write_cancels_bounded();
    printf(">>> Starting test_http_413_oversized_post_no_tcp_rst...\n");
    test_http_413_oversized_post_no_tcp_rst();
    printf(">>> Starting test_stream_up_post_returns_200_when_get_closes_first...\n");
    test_stream_up_post_returns_200_when_get_closes_first();
#ifndef _WIN32
    printf(">>> Starting test_finiteproto_udp_over_tcp...\n");
    test_finiteproto_udp_over_tcp();
    printf(">>> Starting test_f869_ipv6_egress...\n");
    test_f869_ipv6_egress();
    printf(">>> Starting test_f870_stream_one_limits_and_revocation...\n");
    test_f870_stream_one_limits_and_revocation();
    printf(">>> Starting test_f864_socks_udp_associate...\n");
    test_f864_socks_udp_associate();
#endif

    printf("\nSummary: %d passed, %d failed\n", g_pass, g_fail);
    return g_fail ? 1 : 0;
}
