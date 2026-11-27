/* Transport to the node: assembling the layers, link I/O and the tcp transport. The layers are
 * described in transport.h.
 *
 * Who closes what on each failure path is checked by tests/vlessmatch.c (descriptors and heap
 * after every failure, under LeakSanitizer); the xhttp requests and answers by tests/xhupmatch.c.
 */
#define _GNU_SOURCE
#include <stddef.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <poll.h>
#include <sys/socket.h>

#include "transport.h"
#include "xconn.h"
#include "reality.h"
#include "ech.h"
#include "fmlink.h"

/* ---- link I/O ------------------------------------------------------------------------ */

int tr_link_write(void *ctx, const unsigned char *d, size_t n) {
    struct tr_link *l = ctx;
    if (l->plain || l->tx_direct) {
        size_t sent = 0;
        while (sent < n) {
            ssize_t w = tr_lk_send(l, d + sent, n - sent);
            if (w <= 0) {
                if (w == -1 && errno == EINTR) continue;
                return w < -1 ? (int)w : TR_EIO;
            }
            sent += (size_t)w;
        }
        return 0;
    }
    return tls13_write(&l->tls, d, n);
}

int tr_link_read(void *ctx, unsigned char *d, size_t cap, size_t *got) {
    struct tr_link *l = ctx;
    /* Direct copy: the server stopped encrypting towards us, and the socket carries the target
     * connection's stream. Read it as is.
     *
     * But FIRST return what the record buffer has already read from the socket: the switch
     * happens mid-stream, and the start of the raw data is usually here by then. Reading the
     * socket without returning it drops a piece and desyncs from the server (Vision nodes then
     * deliver zero bytes). */
    if (l->rx_direct && !l->plain) {
        size_t pending = tls13_take_pending(&l->tls, d, cap);
        if (pending) { *got = pending; return 0; }
    }
    if (l->plain || l->rx_direct) {
        if (!l->tls.fm) {
            struct pollfd p = { .fd = l->fd, .events = POLLIN };
            int pr = poll(&p, 1, 0);
            if (pr <= 0 || !(p.revents & (POLLIN | POLLHUP | POLLERR))) {
                *got = 0;
                return 0;
            }
        }
        ssize_t r = tr_lk_recv(l, d, cap, 0);
        if (r < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK)) {
            *got = 0;
            return 0;
        }
        if (r <= 0) return tr_lk_err(l, r);
        *got = (size_t)r;
        return 0;
    }
    return tls13_read(&l->tls, d, cap, got);
}

ssize_t tr_lk_send(struct tr_link *l, const void *d, size_t n) {
    if (l->tls.fm) return fm_send(l->tls.fm, l->fd, d, n, l->tx_direct);
    /* send with MSG_NOSIGNAL, not write: a socket closed by the node fails the write (EPIPE)
     * instead of raising SIGPIPE, even in a process that did not ignore it (the tests). Over
     * TLS, tls13_write relies on main ignoring SIGPIPE (src/main.c). */
    return send(l->fd, d, n, MSG_NOSIGNAL);
}

ssize_t tr_lk_recv(struct tr_link *l, void *d, size_t cap, int flags) {
    if (l->tls.fm) return fm_recv(l->tls.fm, l->fd, d, cap, flags, l->rx_direct);
    return recv(l->fd, d, cap, flags);
}

int tr_lk_err(const struct tr_link *l, ssize_t r) {
    if (!r) return TR_ECLOSED;
    return r == -1 && l->tls.fm && errno == EPROTO ? TR_EMASK : TR_EIO;
}

void tr_link_close(struct tr_link *l) {
    if (l->fd >= 0) close(l->fd);
    l->fd = -1;
    fm_link_free(l->tls.fm);
    l->tls.fm = NULL;
    /* The cipher contexts live on the heap and leak over thousands of connections if not freed.
     * Also called for a link that never reached TLS: freeing an empty state is harmless, and
     * this place need not know how far the handshake got. */
    if (!l->plain) tls13_free(&l->tls);
    l->tls.ready = 0;
}

/* ---- the tcp transport: the protocol stream straight in the link --------------------- */

static int tcp_write(struct transport *t, const unsigned char *d, size_t n) {
    return tr_link_write(&t->link, d, n);
}

static int tcp_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    return tr_link_read(&t->link, d, cap, got);
}

const struct transport_ops tr_tcp = {
    .name = "tcp", .alpn = NULL, .zc = 1,
    .open = NULL, .write = tcp_write, .read = tcp_read, .moved = NULL, .close = NULL,
};

/* The transport by the link field. The node parser drops unsupported types (sl_link_usable_post
 * in sublink.c), so only these five arrive here; anything else is tcp. */
static const struct transport_ops *transport_of(const char *type) {
    if (!strcmp(type, "grpc")) return &tr_grpc;
    if (!strcmp(type, "xhttp")) return &tr_xhttp;
    if (!strcmp(type, "ws")) return &tr_ws;
    if (!strcmp(type, "httpupgrade")) return &tr_httpupgrade;
    return &tr_tcp;
}

static int fr_pending(const struct transport *t) {
    return t->fr && t->fr->pending && t->fr->pending(t);
}

static int fr_busy(const struct transport *t) {
    return t->fr && t->fr->busy && t->fr->busy(t);
}

/* VLESS encryption (the node's encryption) or Shadowsocks/VMess/Mieru AEAD is the last layer:
 * over the ready transport, before the protocol request. */
static int tr_venc_after(struct transport *t, const struct tr_node *n, int timeout_s) {
    int is_proto_enc = n->proto && (!strcmp(n->proto, "ss") ||
                                    !strcmp(n->proto, "vmess") ||
                                    !strcmp(n->proto, "mieru") ||
                                    !strcmp(n->proto, "anytls"));
    if ((!n->encryption || !n->encryption[0]) && !is_proto_enc) return 0;
    int rc = tr_venc_open(t, n, timeout_s);
    if (rc) transport_close(t);
    return rc;
}

/* ---- assembling the layers ----------------------------------------------------------- */

static int own_links(const struct transport *t) { return t->fr && t->fr->dial; }

int transport_open(struct transport *t, const struct tr_node *n, int timeout_s) {
    if (n->proto && !strcmp(n->proto, "hy2")) {
        memset((char *)t + offsetof(struct transport, enc), 0, transport_used(t) - offsetof(struct transport, enc));
        t->link.fd = -1;
        return TR_ENONODE;
    }
    if (n->mode && !strcmp(n->mode, "finiteproto")) {
        t->fr = &tr_fp;
    } else {
        int is_h2_connect = n->proto && (!strcmp(n->proto, "tt") || !strcmp(n->proto, "naive"));
        t->fr = is_h2_connect ? &tr_tt : transport_of(n->type);
    }
    memset((char *)t + offsetof(struct transport, enc), 0, transport_used(t) - offsetof(struct transport, enc));
    if (!own_links(t)) t->link.fd = -1;
    int rc = t->fr->dial ? t->fr->dial(t, n, timeout_s) : tr_link_open(&t->link, n, t->fr->alpn, timeout_s);
    if (rc) return rc;
    if (!t->fr->open) return tr_venc_after(t, n, timeout_s);

    /* ALPN is NOT required here.
     *
     * A Reality server that accepted the client serves the connection itself with
     * `NextProtos: nil` (Xray config.go): it selects no ALPN and sends empty
     * EncryptedExtensions. So "h2 negotiated" follows from the node config, not from the answer;
     * Xray decides the HTTP version for reality the same way (decideHTTPVersion returns "2" for
     * reality without looking at anything). On a live node openssl with -alpn h2 gets "h2"
     * because it was NOT accepted and was proxied to the real site. Requiring ALPN would reject
     * exactly the nodes that work. So only a contradiction is an error: the server named a
     * protocol, and not the one asked for.
     *
     * Failures AFTER a successful handshake close through transport_close, not close(fd): the
     * keys are expanded by now and the cipher contexts live on the HEAP. Callers do not clean up
     * (the node probe returns at once, the spare pool just marks the slot empty). A grpc
     * node with security=reality that Reality did not accept fails here on EVERY attempt (a
     * cover site without h2 picks http/1.1), and the spare pool refills on every SYN: a leak
     * here grows RSS until the OOM killer. */
    if (!own_links(t) && !t->link.plain && t->fr->alpn && t->link.tls.alpn[0] &&
        strcmp(t->link.tls.alpn, t->fr->alpn) != 0) {
        transport_close(t);
        /* The code follows WHAT was asked: ws and httpupgrade ask for http/1.1 only, and "did not
         * agree to HTTP/2" would send the user the wrong way. */
        return strcmp(t->fr->alpn, "h2") ? TR_ENOH1 : TR_ENOH2;
    }
    /* An open failure closes the same way for every security, security=none included: a
     * descriptor leaked per probe of a node that does not speak h2 hits RLIMIT_NOFILE within a
     * day. */
    rc = t->fr->open(t, n, timeout_s);
    if (rc) { transport_close(t); return rc; }
    return tr_venc_after(t, n, timeout_s);
}

void transport_set_loop(struct transport *t) {
    if (!own_links(t) && t->link.tls.fm) fm_set_loop(t->link.tls.fm, 1);
}

int transport_idle(const struct transport *t) {
    return !t->fr || (!t->fr->due && (own_links(t) || !t->link.tls.fm));
}

int transport_due(struct transport *t, uint64_t now_ms, int *next_ms) {
    *next_ms = -1;
    if (!t->fr) return 0;
    if (t->fr->due) return t->fr->due(t, now_ms, next_ms);
    if (own_links(t) || !t->link.tls.fm) return 0;
    return fm_kick(t->link.tls.fm, t->link.fd, now_ms, next_ms);
}

long transport_mask_room(struct transport *t) {
    if (!t->fr || own_links(t) || !t->link.tls.fm) return -1;
    return (long)fm_room(t->link.tls.fm);
}

int transport_write(struct transport *t, const unsigned char *d, size_t n) {
    if (t->enc) return tr_venc_write(t, d, n);
    return t->fr->write(t, d, n);
}

long transport_room(struct transport *t) {
    if (!t->fr || !t->fr->room) return -1;
    long r = t->fr->room(t);
    /* VLESS encryption wraps every write in an AEAD record: length and tag. A margin rather than
     * the exact overhead, which depends on how the data is split into writes. */
    if (t->enc) r -= r / 32 + 64;
    return r > 0 ? r : 0;
}

int transport_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    *got = 0;
    if (t->enc) return tr_venc_read(t, d, cap, got);
    return t->fr->read(t, d, cap, got);
}

int transport_read_zc(struct transport *t, unsigned char *buf, size_t cap,
                      const unsigned char **data, size_t *got) {
    *got = 0;
    *data = buf;
    /* Zero copy only where the data sits in the TLS records as is (transport_ops.zc). Direct
     * copy (rx_direct) and security=none read the socket themselves. The transport's own unread
     * data (the rest after the 101 of httpupgrade) goes before the TLS records: it arrived
     * before them. */
    if (!t->enc && t->fr->zc && !t->link.plain && !t->link.rx_direct && !fr_pending(t) && !fr_busy(t))
        return tls13_read_ref(&t->link.tls, data, got);
    return transport_read(t, buf, cap, got);
}

/* Whether we hold unread data the kernel will not report.
 *
 * The tunnel asks before it waits for events: data already taken from the socket into the
 * record buffer does not exist for epoll. Why it matters: tls13.h at tls13_has_record.
 *
 * Three modes. Without TLS there is no buffer. In direct copy there are no record boundaries,
 * and any byte counts. In normal mode only a WHOLE record counts: part of a record yields
 * nothing, and counting it as ready would spin the loop until the rest arrives.
 *
 * Before all of them comes the transport's own unread data (transport_ops.pending): the rest
 * after the 101 and the ws end of stream, which the socket will not report any more. */
int transport_has_data(const struct transport *t) {
    /* Encrypted stream: decrypted data or a whole record in the layer's input (trvenc.c). */
    if (t->enc && tr_venc_pending(t)) return 1;
    if (fr_pending(t)) return 1;
    if (own_links(t)) return 0;
    if (fm_pending(t->link.tls.fm)) return 1;
    if (t->link.plain) return 0;
    if (t->link.rx_direct) return tls13_buffered(&t->link.tls) > 0;
    return tls13_has_record(&t->link.tls);
}

void transport_direct(struct transport *t) {
    if (!own_links(t)) t->link.rx_direct = 1;
}

void transport_tx_direct(struct transport *t) {
    if (!own_links(t)) t->link.tx_direct = 1;
}

int transport_can_tx_direct(const struct transport *t) {
    return !t->enc && t->fr == &tr_tcp && !t->link.plain && !t->link.tls.t12;
}

void transport_moved(struct transport *t) {
    if (t->fr && t->fr->moved) t->fr->moved(t);
}

void transport_close(struct transport *t) {
    /* The transport closes its part FIRST, while the link is alive: ws sends a close frame on
     * closing, as Xray does, and after tr_link_close there is nothing to encrypt it with. xhttp's
     * connections are closed here too (transport_ops.close); forgetting them leaks both a
     * descriptor and a set of cipher contexts per connection. fr is NULL if opening did not get
     * to choosing the transport; then there were none either. */
    if (t->enc) tr_venc_close(t);
    if (t->fr && t->fr->close) t->fr->close(t);
    if (!own_links(t)) tr_link_close(&t->link);
}

const char *tr_host_literal(const char *host, char *buf, size_t cap) {
    if (!strchr(host, ':')) return host;
    snprintf(buf, cap, "[%s]", host);
    return buf;
}

const char *transport_strerror(int rc) {
    switch (rc) {
        case 0: return "ok";
        case TR_EDNS: return "name did not resolve";
        case TR_ESOCK: return "no socket";
        case TR_ECONNECT: case TR_ECONNTIMEOUT: case TR_ECONNREFUSED: case TR_EUNREACH:
        case TR_ENONODE: return tr_dial_strerror(rc);
        case TR_EIO: return "I/O error";
        case TR_EMASK: {
            static __thread char why[128];
            const char *d = fm_last_why();
            snprintf(why, sizeof why, "finalmask: %s", d[0] ? d : "the mask failed");
            return why;
        }
        case TR_ECLOSED: return "server closed the connection";
        case TR_ENOH2: return "server did not agree to HTTP/2 (needed for grpc)";
        case TR_EGRPC: return "unexpected gRPC stream format";
        case TR_ENOH1: return "server did not pick HTTP/1.1 (needed for ws and httpupgrade)";
        /* The status goes in the text: 404 and 400 almost always mean a wrong path or host (that
         * is Xray's answer to an unknown path), 403 and 5xx point at a proxy or CDN in front. */
        case TR_EUPSTATUS: {
            static __thread char why[96];
            snprintf(why, sizeof why, "server answered %d instead of 101 (check path and host)",
                     tr_h1_last_status());
            return why;
        }
        case TR_ENOUPGRADE: return "101 response without Upgrade: websocket or Connection: Upgrade";
        case TR_EWSACCEPT: return "101 response with a wrong Sec-WebSocket-Accept";
        case TR_EUPTIMEOUT: return "server did not answer the Upgrade request (timeout)";
        case TR_EUPTOOBIG: return "cannot parse the response to the Upgrade request";
        case TR_EWSFRAME: return "WebSocket frame violates RFC 6455";
        case TR_EVENC: {
            static __thread char why[128];
            snprintf(why, sizeof why, "VLESS encryption: %s", tr_venc_reason());
            return why;
        }
        case TR_EVENCAUTH: return "VLESS encryption: keys do not match the server's "
                                  "(check the encryption string)";
        case TR_EVENC0RTT: return "VLESS encryption: server rejected the 0-RTT ticket";
        case H2_EIO: case H2_EPROTO: case H2_ESTATUS:
        case H2_ERESET: case H2_ETOOBIG: case H2_EWINDOW:
        case H2_EFULL: case H2_EFLOW: return h2_strerror(rc);
        case XC_EPING: return "HTTP/2 PING got no answer within 15 s";
        case XC_EGOAWAY: return "server sent GOAWAY before this stream was served";
        case XC_EREFUSED: return "server refused the stream before serving it";
        case REALITY_EBADKEY: return "cannot parse pbk or sid";
        case REALITY_ECRYPTO: return "crypto failure";
        case REALITY_ETOOBIG: return "ClientHello does not fit";
        case TLS13_EAUTH: return "AEAD failed (keys out of sync)";
        case TLS13_EFINISHED: return "server Finished did not verify";
        case TLS13_ENOKEYSHARE: return "ServerHello without key_share";
        case TLS13_EBADSUITE: return "server chose an unsupported cipher suite";
        case TLS13_EBADREC: return "corrupt TLS record";
        case TLS13_EECH: return "server rejected ECH (stale or unknown ECH key in the link)";
        case TLS13_EGROUP: {
            static __thread char why[96];
            snprintf(why, sizeof why, "the server chose key share group 0x%04x, which this client "
                     "offers but cannot complete", tls13_server_group());
            return why;
        }
        case TLS13_EHRR: return "the server asked for a HelloRetryRequest, which is not supported";
        case ECH_EPARSE: return "ECH: cannot parse the ECHConfigList from the link";
        case ECH_ENOCONFIG: return "ECH: no usable config in the ECHConfigList (need X25519, "
                                   "HKDF-SHA256, AES-128-GCM or ChaCha20-Poly1305)";
        case ECH_ECRYPTO: return "ECH: crypto failure";
        case ECH_ETOOBIG: return "ECH: ClientHello does not fit";
        case TLS13_ECLOSED: return "TLS closed by the server";
        case TLS13_EIO: return "TLS read error";
        /* "Did not answer", not "error": TCP is up but ClientHello gets no answer. That is how
         * SNI blocking, a dead node or a lost packet look, so the cause is outside the client,
         * and the text must send the user to look there. */
        case TLS13_ETIMEOUT: return "node did not answer the ClientHello (timeout)";
        /* One code, different causes: "nothing to verify with" is not "verified and did not
         * match". tls13.c gives the exact text; the general phrase here says what it is about. */
        case TLS13_ECERT: {
            static __thread char why[128];
            const char *d = tls13_verify_reason();
            snprintf(why, sizeof why, "server did not prove its identity%s%s",
                     d && d[0] ? ": " : "", d && d[0] ? d : "");
            return why;
        }
        default: return "unknown error";
    }
}
