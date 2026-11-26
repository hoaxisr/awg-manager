#include "../fpc.c"
#include <dirent.h>
#include <assert.h>

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

static int count_open_fds(void) {
    DIR *d = opendir("/proc/self/fd");
    if (!d) return -1;
    int cnt = 0;
    struct dirent *de;
    while ((de = readdir(d)) != NULL) {
        if (de->d_name[0] == '.') continue;
        cnt++;
    }
    closedir(d);
    return cnt - 1; /* exclude opendir fd */
}

typedef struct {
    int         fd;
    const char *raw_resp;
    size_t      raw_len;
    bool        drip_slow;
} mock_http_peer_t;

static void *mock_http_peer_thread(void *arg) {
    mock_http_peer_t *m = (mock_http_peer_t *)arg;
    char req_buf[4096];
    size_t rtot = 0;
    while (rtot + 1 < sizeof(req_buf)) {
        ssize_t n = recv(m->fd, req_buf + rtot, sizeof(req_buf) - 1 - rtot, 0);
        if (n <= 0) break;
        rtot += (size_t)n;
        req_buf[rtot] = '\0';
        if (strstr(req_buf, "\r\n\r\n") != NULL) break;
    }
    /* Тело запроса клиент пишет отдельно от заголовков: закрыть сокет, не дочитав
     * его, — ECONNRESET/EPIPE у клиента вместо ответа (флак CI под TSan). */
    const char *hend = strstr(req_buf, "\r\n\r\n");
    const char *cl = strcasestr(req_buf, "\r\nContent-Length:");
    if (hend != NULL && cl != NULL && cl < hend) {
        size_t want = (size_t)strtoul(cl + 17, NULL, 10);
        size_t have = rtot - (size_t)(hend + 4 - req_buf);
        char sink[1024];
        while (have < want) {
            size_t chunk = want - have < sizeof(sink) ? want - have : sizeof(sink);
            ssize_t n = recv(m->fd, sink, chunk, 0);
            if (n <= 0) break;
            have += (size_t)n;
        }
    }
    if (m->drip_slow) {
        for (size_t i = 0; i < m->raw_len; i++) {
            if (send(m->fd, m->raw_resp + i, 1, MSG_NOSIGNAL) <= 0) break;
            usleep(40000);
        }
    } else if (m->raw_resp && m->raw_len > 0) {
        (void)send(m->fd, m->raw_resp, m->raw_len, MSG_NOSIGNAL);
    }
    shutdown(m->fd, SHUT_WR);
    close(m->fd);
    return NULL;
}

static int run_fp_rpc_once_against_raw(const char *method, bool in_hdr, const char *raw_resp, size_t raw_len, fp_parsed_resp_t *out_resp, uint8_t *down_buf, size_t down_cap) {
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) return -99;
    mock_http_peer_t peer = {
        .fd = sv[1],
        .raw_resp = raw_resp,
        .raw_len = raw_len,
        .drip_slow = false,
    };
    pthread_t th;
    pthread_create(&th, NULL, mock_http_peer_thread, &peer);

    uint8_t wire[FP_REQ_HEADER_SIZE];
    memset(wire, 0, sizeof(wire));
    memcpy(wire, FP_PROTO_MAGIC, 4);
    wire[4] = FP_OP_DOWN;

    int rc = fp_rpc_once(sv[0], method, in_hdr, "00112233445566778899aabb",
                         wire, sizeof(wire), "cid:1700000000000:00112233445566778899aabb:deadbeef",
                         out_resp, down_buf, down_cap);
    close(sv[0]);
    pthread_join(th, NULL);
    return rc;
}

static void test_chunked_overflow_and_malformed_responses(void) {
    snprintf(g_http_host, sizeof(g_http_host), "cdn.example.com");
    snprintf(g_http_path, sizeof(g_http_path), "/cdn-bridge");

    fp_parsed_resp_t resp;
    uint8_t down[256];

    /* 1. Chunked 64-bit wraparound ffffffffffffffff MUST fail closed immediately without UB or OOB write */
    const char *overflow_chunk =
        "HTTP/1.1 200 OK\r\n"
        "X-FP-Version: fp/1\r\n"
        "Transfer-Encoding: chunked\r\n"
        "\r\n"
        "ffffffffffffffff\r\n"
        "FP01";
    int rc = run_fp_rpc_once_against_raw("POST", false, overflow_chunk, strlen(overflow_chunk), &resp, down, sizeof(down));
    check("FPC chunked: rejects ffffffffffffffff chunk size without overflow", rc == -1);

    /* 2. Chunked 32-bit boundary 100000000 (4 GiB) MUST fail closed */
    const char *overflow_chunk32 =
        "HTTP/1.1 200 OK\r\n"
        "X-FP-Version: fp/1\r\n"
        "Transfer-Encoding: chunked\r\n"
        "\r\n"
        "100000000\r\n"
        "FP01";
    rc = run_fp_rpc_once_against_raw("POST", false, overflow_chunk32, strlen(overflow_chunk32), &resp, down, sizeof(down));
    check("FPC chunked: rejects 4 GiB chunk size on 32/64-bit", rc == -1);

    /* 3. Non-hex chunk size MUST fail closed (never treated as 0 EOF) */
    const char *garbage_chunk =
        "HTTP/1.1 200 OK\r\n"
        "X-FP-Version: fp/1\r\n"
        "Transfer-Encoding: chunked\r\n"
        "\r\n"
        "not_hex\r\n\r\n";
    rc = run_fp_rpc_once_against_raw("GET", true, garbage_chunk, strlen(garbage_chunk), &resp, down, sizeof(down));
    check("FPC chunked: rejects non-hex chunk size line", rc == -1);

    /* 4. Negative / garbage Content-Length MUST fail closed */
    const char *bad_cl =
        "HTTP/1.1 200 OK\r\n"
        "X-FP-Version: fp/1\r\n"
        "Content-Length: 58junk\r\n"
        "\r\n";
    rc = run_fp_rpc_once_against_raw("POST", false, bad_cl, strlen(bad_cl), &resp, down, sizeof(down));
    check("FPC Content-Length: rejects trailing junk in Content-Length", rc == -1);

    /* 5. Valid multi-chunk response with multi-line trailers succeeds cleanly */
    uint8_t body[FP_RESP_HEADER_SIZE + 5];
    memset(body, 0, sizeof(body));
    memcpy(body, FP_PROTO_MAGIC, 4);
    body[4] = FP_STATUS_OK;
    body[5] = FP_RESP_FLAG_TARGET_EOF;
    put_be64(body + 30, 0);
    put_be64(body + 38, 5);
    put_be64(body + 46, 5);
    put_be32(body + 54, 5);
    memcpy(body + FP_RESP_HEADER_SIZE, "hello", 5);

    char valid_chunked[512];
    const char *hdr =
        "HTTP/1.1 200 OK\r\n"
        "X-FP-Version: fp/1\r\n"
        "Transfer-Encoding: chunked\r\n"
        "Connection: close\r\n"
        "\r\n"
        "1e\r\n"; /* 30 bytes */
    size_t off = strlen(hdr);
    memcpy(valid_chunked, hdr, off);
    memcpy(valid_chunked + off, body, 30);
    off += 30;
    const char *mid = "\r\n21\r\n"; /* 33 bytes (30 + 33 = 63 = 58 + 5) */
    memcpy(valid_chunked + off, mid, strlen(mid));
    off += strlen(mid);
    memcpy(valid_chunked + off, body + 30, 33);
    off += 33;
    const char *trl = "\r\n0\r\nX-Trailer-1: a\r\nX-Trailer-2: b\r\n\r\n";
    memcpy(valid_chunked + off, trl, strlen(trl));
    off += strlen(trl);

    memset(&resp, 0, sizeof(resp));
    memset(down, 0, sizeof(down));
    rc = run_fp_rpc_once_against_raw("POST", false, valid_chunked, off, &resp, down, sizeof(down));
    check("FPC chunked: parses valid multi-chunk FP01 response with multi-line trailers",
          rc == 1 && resp.status == FP_STATUS_OK && resp.payload_len == 5 && memcmp(down, "hello", 5) == 0);
}

static void test_monotonic_drip_feed_deadline_and_pool_fd_hygiene(void) {
    int fds_before = count_open_fds();

    /* 1. Drip-feeding 1 byte every 40ms with a 120ms deadline MUST abort around ~120ms, not 40ms * 64 */
    int sv[2];
    assert(socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0);
    mock_http_peer_t peer = {
        .fd = sv[1],
        .raw_resp = "HTTP/1.1 200 OK (no newline ever arrives...)",
        .raw_len = 20,
        .drip_slow = true,
    };
    pthread_t th;
    pthread_create(&th, NULL, mock_http_peer_thread, &peer);

    char line[128];
    uint64_t t0 = now_ms();
    int lrc = read_http_line(sv[0], line, sizeof(line), 120);
    uint64_t elapsed = now_ms() - t0;
    close(sv[0]);
    pthread_join(th, NULL);
    check("FPC deadline: read_http_line enforces monotonic total deadline against slow drip-feed",
          lrc == -1 && elapsed >= 90 && elapsed < 450);

    /* 2. Pool acquire/release/close_all_pool_conns leaves zero leaked file descriptors */
    for (int i = 0; i < MAX_POOL_CONNS + 6; i++) {
        int psv[2];
        assert(socketpair(AF_UNIX, SOCK_STREAM, 0, psv) == 0);
        close(psv[1]); /* peer closed */
        release_pool_conn(psv[0]);
    }
    /* All pooled fds have closed peers; acquire_pool_conn should evict them cleanly and fail dial */
    g_connect_port = 1;
    snprintf(g_connect_host, sizeof(g_connect_host), "256.256.256.256"); /* invalid IP -> dial fails fast */
    bool reused = true;
    int afd = acquire_pool_conn(&reused);
    check("FPC pool: evicts dead keep-alive sockets on acquire", afd == -1 && !reused && g_pool_count == 0);

    int live_sv[2];
    assert(socketpair(AF_UNIX, SOCK_STREAM, 0, live_sv) == 0);
    release_pool_conn(live_sv[0]);
    close_all_pool_conns();
    close(live_sv[1]);

    int fds_after = count_open_fds();
    check("FPC fd hygiene: zero leaked fds across RPC, drip-feed, and pool eviction tests",
          fds_before < 0 || fds_before == fds_after);
}

typedef struct {
    int fd;
    size_t want;
    size_t got;
} fpc_drain_arg_t;

static void *fpc_drain_thread(void *arg) {
    fpc_drain_arg_t *d = (fpc_drain_arg_t *)arg;
    uint8_t buf[1024];
    usleep(20000);
    while (d->got < d->want) {
        ssize_t n = recv(d->fd, buf, sizeof(buf), 0);
        if (n <= 0) break;
        d->got += (size_t)n;
    }
    return NULL;
}

static void test_dial_bridge_timeouts_and_nonblock_write_all(void) {
    int fds_before = count_open_fds();

    int lfd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    assert(lfd >= 0);
    int one = 1;
    setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    assert(bind(lfd, (struct sockaddr *)&sa, sizeof(sa)) == 0);
    assert(listen(lfd, 8) == 0);
    socklen_t salen = sizeof(sa);
    assert(getsockname(lfd, (struct sockaddr *)&sa, &salen) == 0);
    uint16_t port = ntohs(sa.sin_port);

    snprintf(g_connect_host, sizeof(g_connect_host), "127.0.0.1");
    g_connect_port = port;
    int cfd = dial_bridge_conn();
    int cflags = (cfd >= 0) ? fcntl(cfd, F_GETFL, 0) : -1;
    struct timeval rtv = {0}, stv = {0};
    socklen_t tvlen = sizeof(rtv);
    int rrc = (cfd >= 0) ? getsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &rtv, &tvlen) : -1;
    tvlen = sizeof(stv);
    int src = (cfd >= 0) ? getsockopt(cfd, SOL_SOCKET, SO_SNDTIMEO, &stv, &tvlen) : -1;
    check("FPC dial_bridge_conn: sets blocking mode and 15s SO_RCVTIMEO/SO_SNDTIMEO",
          cfd >= 0 && cflags >= 0 && !(cflags & O_NONBLOCK) &&
          rrc == 0 && rtv.tv_sec == 15 && src == 0 && stv.tv_sec == 15);
    int afd = accept(lfd, NULL, NULL);
    if (afd >= 0) close(afd);
    if (cfd >= 0) close(cfd);
    close(lfd);

    int cfd_refused = dial_bridge_conn();
    check("FPC dial_bridge_conn: closed port fails closed without leak", cfd_refused == -1);

    int sv[2];
    assert(socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0);
    int wf = fcntl(sv[0], F_GETFL, 0);
    assert(wf >= 0 && fcntl(sv[0], F_SETFL, wf | O_NONBLOCK) == 0);
    int small_snd = 4096;
    setsockopt(sv[0], SOL_SOCKET, SO_SNDBUF, &small_snd, sizeof(small_snd));

    size_t pay_len = 65536;
    uint8_t *pay = (uint8_t *)malloc(pay_len);
    assert(pay != NULL);
    memset(pay, 0x3C, pay_len);
    fpc_drain_arg_t darg = { .fd = sv[1], .want = pay_len, .got = 0 };
    pthread_t dth;
    assert(pthread_create(&dth, NULL, fpc_drain_thread, &darg) == 0);
    int wrc = write_all(sv[0], pay, pay_len);
    close(sv[0]);
    pthread_join(dth, NULL);
    close(sv[1]);
    free(pay);
    check("FPC write_all: completes non-blocking writes via poll(POLLOUT)",
          wrc == 0 && darg.got == pay_len);

    int fds_after = count_open_fds();
    check("FPC dial/write fd hygiene: zero leaked fds",
          fds_before < 0 || fds_before == fds_after);
}

int main(void) {
    signal(SIGPIPE, SIG_IGN);
    test_chunked_overflow_and_malformed_responses();
    test_monotonic_drip_feed_deadline_and_pool_fd_hygiene();
    test_dial_bridge_timeouts_and_nonblock_write_all();
    printf("\nFPC Audit Summary: %d passed, %d failed\n", g_pass, g_fail);
    return g_fail ? 1 : 0;
}
