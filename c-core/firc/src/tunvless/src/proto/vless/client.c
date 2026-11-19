/* Connection to a VLESS node and the check that the node accepted us.
 *
 * Reality has no negative answer. A server that does not recognise the client does not refuse:
 * it proxies the connection to the real site it hides behind. The handshake can complete, the
 * keys agree, TLS comes up, and it is still that site, not the tunnel.
 *
 * Only the first byte of the VLESS response tells them apart: the server answers with version 0,
 * the real site with anything else (HTTP, HTML, a redirect). So vless_probe() below is the only
 * honest check of a node; the node choice at startup and the pool's health check use it.
 *
 * Connection setup (TCP to every address, security, transports) lives in proto/transport. This
 * file holds what knows VLESS: the node as transport parameters and the probe with a VLESS request.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <poll.h>
#include <time.h>

#include "vless.h"
#include "vless_proto.h"
#include "vision.h"
#include "client.h"
#include "sublink.h"

static int64_t now_ms(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return (int64_t)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}

static int socks5_read_exact(struct transport *c, unsigned char *out, size_t need, int timeout_s) {
    size_t have = 0;
    int64_t deadline = now_ms() + (int64_t)(timeout_s > 0 ? timeout_s : 8) * 1000;
    while (have < need) {
        size_t got = 0;
        int rc = transport_read(c, out + have, need - have, &got);
        if (rc) return rc;
        have += got;
        if (have >= need) return 0;
        if (now_ms() >= deadline) return TR_EIO;
        struct pollfd pw = { .fd = transport_fd(c), .events = POLLIN, .revents = 0 };
        poll(&pw, 1, 200);
    }
    return 0;
}

static int socks5_handshake(const struct vless_node *node, struct transport *c, int timeout_s) {
    unsigned char greet[4] = { 0x05, node->uuid[0] ? 0x02 : 0x01, 0x00, 0x02 };
    int rc = transport_write(c, greet, node->uuid[0] ? 4 : 3);
    if (rc) return rc;
    unsigned char rep[2];
    rc = socks5_read_exact(c, rep, 2, timeout_s);
    if (rc) return rc;
    if (rep[0] != 0x05) return VLESS_CONN_EREJECTED;
    if (rep[1] == 0x02) {
        const char *colon = strchr(node->uuid, ':');
        size_t ulen = colon ? (size_t)(colon - node->uuid) : strlen(node->uuid);
        const char *pass = colon ? colon + 1 : "";
        size_t plen = strlen(pass);
        if (ulen > 255 || plen > 255) return VLESS_CONN_EREJECTED;
        unsigned char auth[3 + 255 + 255];
        auth[0] = 0x01;
        auth[1] = (unsigned char)ulen;
        if (ulen) memcpy(auth + 2, node->uuid, ulen);
        auth[2 + ulen] = (unsigned char)plen;
        if (plen) memcpy(auth + 3 + ulen, pass, plen);
        rc = transport_write(c, auth, 3 + ulen + plen);
        if (rc) return rc;
        rc = socks5_read_exact(c, rep, 2, timeout_s);
        if (rc) return rc;
        if (rep[0] != 0x01 || rep[1] != 0x00) return VLESS_CONN_EREJECTED;
        return 0;
    }
    if (rep[1] != 0x00) return VLESS_CONN_EREJECTED;
    return 0;
}

int vless_connect(const struct vless_node *node, struct transport *conn, int timeout_s) {
    struct tr_node tn, td;
    sl_tr_nodes(node, &tn, &td);
    int rc = transport_open(conn, &tn, timeout_s);
    if (rc) return rc;
    if (!strcmp(node->proto_str, "socks5")) {
        rc = socks5_handshake(node, conn, timeout_s);
        if (rc) {
            transport_close(conn);
            return rc;
        }
    }
    return 0;
}

/* The node check: the only way to learn whether Reality accepted us.
 *
 * Ask the server to connect to a known live address and look at the first byte of the answer.
 * Version 0 is a VLESS response: the server is ours. Anything else means we were not accepted
 * and talk to the real site; that connection works and a page loads, so without this check the
 * node would look healthy. The answer also measures latency over the path real traffic takes.
 *
 * Two targets, not one: a node may not pass a given address (the server's provider blocks it,
 * the hoster runs its own DNS on it, a rule on the server), and with a single target such a
 * node would be rejected as a whole. The second target is tried only when the target may be
 * to blame: the server connection came up but no data came back. A handshake failure or a
 * Reality rejection belongs to the node, and retrying it would double the probe time of every
 * dead node. */
const struct probe_target PROBE_TARGETS[2] = {
    { { 1, 1, 1, 1 }, 4, "1.1.1.1" },
    { { 8, 8, 8, 8 }, 4, "8.8.8.8" },
};
const struct probe_target PROBE6_TARGETS[2] = {
    { { 0x26, 0x06, 0x47, 0x00, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x11, 0x11 }, 16, "[2606:4700:4700::1111]" },
    { { 0x20, 0x01, 0x48, 0x60, 0x48, 0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0x88, 0x88 }, 16, "[2001:4860:4860::8888]" },
};

size_t probe_get(const struct probe_target *tg, char *out, size_t cap) {
    int n = snprintf(out, cap, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", tg->host);
    return n > 0 && (size_t)n < cap ? (size_t)n : 0;
}

static int probe_once(const struct vless_node *node, int timeout_s, char *why, size_t why_n,
                      int *handshake_ms, int *ttfb_ms,
                      const struct probe_target *tg, int need_data, int *connected);

int vless_probe(const struct vless_node *node, int timeout_s, char *why, size_t why_n) {
    return vless_probe_timed(node, timeout_s, why, why_n, NULL, NULL);
}

static int probe_targets(const struct vless_node *node, int timeout_s, char *why, size_t why_n,
                         int *handshake_ms, int *ttfb_ms, const struct probe_target *tg, size_t tg_n,
                         int need_data) {
    int rc = TR_EIO;
    for (size_t i = 0; i < tg_n; i++) {
        int connected = 0;
        rc = probe_once(node, timeout_s, why, why_n, handshake_ms, ttfb_ms, &tg[i], need_data,
                        &connected);
        if (rc == 0) return 0;
        /* The server was not reached or did not accept us: not the target's fault. */
        if (!connected || rc == VLESS_CONN_EREJECTED || rc == VLESS_CONN_EBADUUID)
            return rc;
    }
    return rc;
}

int vless_probe_timed(const struct vless_node *node, int timeout_s, char *why, size_t why_n,
                      int *handshake_ms, int *ttfb_ms) {
    return probe_targets(node, timeout_s, why, why_n, handshake_ms, ttfb_ms, PROBE_TARGETS,
                         sizeof(PROBE_TARGETS) / sizeof(PROBE_TARGETS[0]), 0);
}

int vless_probe6(const struct vless_node *node, int timeout_s, char *why, size_t why_n) {
    return probe_targets(node, timeout_s, why, why_n, NULL, NULL, PROBE6_TARGETS,
                         sizeof(PROBE6_TARGETS) / sizeof(PROBE6_TARGETS[0]), 1);
}

static int probe_once(const struct vless_node *node, int timeout_s, char *why, size_t why_n,
                      int *handshake_ms, int *ttfb_ms,
                      const struct probe_target *tg, int need_data, int *connected) {
    if (handshake_ms) *handshake_ms = -1;
    if (ttfb_ms) *ttfb_ms = -1;
    *connected = 0;

    struct transport c;
    int64_t t0 = now_ms();
    int rc = vless_connect(node, &c, timeout_s);
    if (rc) {
        snprintf(why, why_n, "%s", vless_strerror(rc));
        return rc;
    }
    if (handshake_ms) *handshake_ms = (int)(now_ms() - t0);
    *connected = 1;

    int is_tt = !strcmp(node->proto_str, "tt");
    int is_naive = !strcmp(node->proto_str, "naive");
    int is_socks = !strcmp(node->proto_str, "socks5");
    int is_trojan = !strcmp(node->proto_str, "trojan");
    int is_ss = !strcmp(node->proto_str, "ss");
    int is_vmess = !strcmp(node->proto_str, "vmess");
    int is_mieru = !strcmp(node->proto_str, "mieru");
    int is_anytls = !strcmp(node->proto_str, "anytls");
    unsigned char uuid[16];
    unsigned char req[512];
    size_t req_n = 0;
    size_t http_used = 0;

    if (is_tt) {
        req_n = tt_build_connect(node->uuid, "_check", "TrustTunnel-Client/1.0", 1,
                                 req, sizeof(req));
        if (!req_n) {
            transport_close(&c);
            snprintf(why, why_n, "cannot build the TrustTunnel CONNECT header");
            return TR_EIO;
        }
    } else if (is_naive) {
        char auth[64];
        snprintf(auth, sizeof(auth), "%s:80", tg->host);
        req_n = tt_build_connect(node->uuid, auth, "Chrome/128.0.0.0", 0, req, sizeof(req));
        if (!req_n) {
            transport_close(&c);
            snprintf(why, why_n, "cannot build the Naive CONNECT header");
            return TR_EIO;
        }
        char http[128];
        size_t http_n = probe_get(tg, http, sizeof(http));
        if (http_n > 0 && req_n + http_n <= sizeof(req)) {
            memcpy(req + req_n, http, http_n);
            req_n += http_n;
        }
    } else if (is_socks || is_mieru || is_trojan || is_ss || is_vmess || is_anytls) {
        if (is_socks || is_mieru)
            req_n = socks5_build_connect(tg->ip_len == 4 ? NULL : tg->host, tg->ip, 80, req, sizeof(req));
        else if (is_trojan)
            req_n = trojan_build_request(node->uuid, 0x01, tg->ip_len == 4 ? NULL : tg->host, tg->ip, 80, req, sizeof(req));
        else
            req_n = ss_build_addr(tg->ip_len == 4 ? NULL : tg->host, tg->ip, 80, req, sizeof(req));
        if (!req_n) {
            transport_close(&c);
            snprintf(why, why_n, "cannot build the request header");
            return TR_EIO;
        }
        char http[128];
        size_t http_n = probe_get(tg, http, sizeof(http));
        if (http_n > 0 && req_n + http_n <= sizeof(req)) {
            memcpy(req + req_n, http, http_n);
            req_n += http_n;
        }
    } else {
        if (vless_uuid_parse(node->uuid, uuid) != 0) {
            transport_close(&c);
            snprintf(why, why_n, "bad UUID");
            return VLESS_CONN_EBADUUID;
        }

        req_n = vless_build_request(uuid, VLESS_CMD_TCP, NULL, tg->ip, tg->ip_len, 80,
                                    node->flow, req, sizeof(req));
        if (!req_n) { transport_close(&c); snprintf(why, why_n, "cannot build the request header"); return TR_EIO; }

        /* A minimal HTTP request goes with the header: the server does not answer until it has
         * data to forward, and without it the probe would wait for the timeout. */
        char http[128];
        size_t http_n = probe_get(tg, http, sizeof(http));
        /* How much data actually follows the header. Vision below subtracts exactly this to split
         * the VLESS header from the data, and the HTTP request may not have fit. */
        if (http_n > 0 && req_n + http_n <= sizeof(req)) {
            memcpy(req + req_n, http, http_n);
            req_n += http_n;
            http_used = http_n;
        }
    }

    /* With Vision the VLESS header goes raw, immediately followed by the first Vision frame
     * with the data; the header is not inside the frame. In Xray's XtlsPadding the wrapping
     * applies to data buffers, and "we do a long padding to hide vless header" means the next
     * frame's padding hides the header by sharing its TLS record. With the header inside the
     * frame the server matches the frame's UUID, then reads the VLESS version and the start of
     * the header's UUID as the command and lengths, and closes the connection, which looks
     * like a key rejection. */
    if (node->flow[0]) {
        struct vision vis;
        vless_uuid_parse(node->uuid, uuid);
        vision_init(&vis, uuid);
        unsigned char framed[8192];
        size_t header_n = req_n - http_used;
        size_t fn = 0;
        if (vision_wrap(&vis, req + header_n, req_n - header_n, 0, framed, sizeof(framed), &fn)) { transport_close(&c); snprintf(why, why_n, "cannot build the Vision frame"); return TR_EIO; }
        /* One write: the header and the frame must leave together, or their split across
         * records becomes a recognisable feature itself. */
        unsigned char together[8704];
        if (header_n + fn > sizeof(together)) { transport_close(&c); snprintf(why, why_n, "request too large"); return TR_EIO; }
        memcpy(together, req, header_n);
        memcpy(together + header_n, framed, fn);
        rc = transport_write(&c, together, header_n + fn);
    } else {
        rc = transport_write(&c, req, req_n);
    }
    if (rc) { transport_close(&c); snprintf(why, why_n, "request not sent: %s", vless_strerror(rc)); return rc; }
    int64_t t_sent = now_ms();

    /* Sized by the transport's minimum: over HTTP/2 one read returns up to a whole TLS record,
     * and a smaller buffer would fail on a legal frame. */
    unsigned char buf[VLESS_MIN_RECV_CAP];
    unsigned char head[2 + 255];
    size_t head_n = 0, total = 0, skip = 0;
    int pr = VLESS_EAGAIN;
    /* Wait for data by the clock, not by a number of attempts. A read of zero bytes means
     * "nothing yet": an HTTP/2 control frame (SETTINGS, WINDOW_UPDATE) or a record not fully
     * arrived. Reads are non-blocking (the tunnel runs one loop for all connections), so a
     * fixed number of attempts passes in microseconds, before the answer can arrive, and fails
     * a working node. Wait on the socket, not in a busy loop. */
    int64_t rx_deadline = now_ms() + (int64_t)(timeout_s > 0 ? timeout_s : 8) * 1000;
    for (;;) {
        size_t got = 0;
        rc = transport_read(&c, buf, sizeof(buf), &got);
        if (rc) break;
        if (got) {
            /* Timed before parsing: parsing waits for nothing, and counting it would measure
             * our own work. */
            if (!total && ttfb_ms) *ttfb_ms = (int)(now_ms() - t_sent);
            total += got;
            size_t take = got < sizeof(head) - head_n ? got : sizeof(head) - head_n;
            memcpy(head + head_n, buf, take);
            head_n += take;
            if (pr == VLESS_EAGAIN) {
                if (is_socks || is_mieru)
                    pr = socks5_parse_reply(head, head_n, &skip);
                else if (is_trojan || is_ss || is_vmess || is_anytls)
                    pr = (head_n >= 5 && !memcmp(head, "HTTP/", 5)) ? 0 : (head_n < 5 ? VLESS_EAGAIN : VLESS_EPROTO);
                else
                    pr = vless_parse_response(head, head_n, &skip);
            }
            if (pr == VLESS_EPROTO || (pr == 0 && (!need_data || total > skip))) break;
            continue;
        }
        if (now_ms() >= rx_deadline) break;
        struct pollfd pw = { .fd = transport_fd(&c), .events = POLLIN, .revents = 0 };
        poll(&pw, 1, 200);
    }
    transport_close(&c);

    if (pr == VLESS_EPROTO) {
        /* The silent Reality rejection. Say it plainly: otherwise it looks like a working
         * node, since TLS is up and an answer came, only from the cover site. */
        snprintf(why, why_n,
                 "server did not accept the key — the cover site answers, not the tunnel "
                 "(check pbk, sid and sni)");
        return VLESS_CONN_EREJECTED;
    }
    if (pr == 0 && need_data && total <= skip) {
        snprintf(why, why_n, "the target sent no data after the VLESS response");
        return TR_EIO;
    }
    if (pr == 0) {
        snprintf(why, why_n, "ok, VLESS response (%zu bytes)", total);
        return 0;
    }
    if (rc) { snprintf(why, why_n, "no response: %s", vless_strerror(rc)); return rc; }
    if (!total) { snprintf(why, why_n, "server sent no data"); return TR_EIO; }
    snprintf(why, why_n, "response too short (%zu bytes)", total);
    return TR_EIO;
}

const char *vless_strerror(int rc) {
    switch (rc) {
        case VLESS_CONN_EBADUUID: return "bad UUID";
        case VLESS_CONN_EREJECTED: return "server did not accept the key";
        default: return transport_strerror(rc);
    }
}
