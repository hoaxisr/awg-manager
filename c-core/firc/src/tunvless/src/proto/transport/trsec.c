/* Link security: security=none, tls and reality — the handshake over the socket to the node (the
 * middle layer in transport.h). Every link, the main one and xhttp's second one, uses this one
 * handshake; the only difference is the alpn the transport passes in.
 *
 * Reality has no negative answer. A server that does not recognise the client does not refuse:
 * it proxies the connection to the real site it hides behind. The handshake can complete and
 * still lead to someone else's site. Only the protocol above the transport can tell (vless_probe
 * in proto/vless/client.c), so here "the handshake passed" means exactly that and no more.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <time.h>
#include <pthread.h>
#include <poll.h>

#include "transport.h"
#include "reality.h"
#include "scrypto.h"
#include "roots.h"
#include "certverify.h"
#include "ech.h"
#include "fmlink.h"

/* security=none — a bare stream, no TLS at all (for a trusted network). Without TLS there is no
 * ALPN negotiation, so an HTTP/2 transport starts HTTP/2 at once: the server either accepts bare
 * h2 over TCP (h2c) or answers with garbage, and the probe sees it. */
static int sec_none(struct tr_link *l, const struct tr_node *n, const char *alpn,
                    struct sec_pend *p) {
    (void)n; (void)alpn; (void)p;
    l->plain = 1;
    return 0;
}

struct ech_heap {
    struct ech_cfg cfg;
    struct ech_state st;
    unsigned char list[1100];
    unsigned char outer[6144];
    size_t outer_n;
};

static void ech_heap_free(struct ech_heap *eh) {
    if (!eh) return;
    sc_wipe(eh, sizeof *eh);
    free(eh);
}

struct sec_pend {
    struct reality_state rst;
    struct ech_heap *eh;
    struct tls13_ech te;
    const unsigned char *sent;
    size_t sent_n;
    int is_tls, have_pqv;
    unsigned char pqv[SC_MLDSA65_PK];
    unsigned char hello[2560];
};

static void pend_drop(struct sec_pend *p) {
    ech_heap_free(p->eh);
    p->eh = NULL;
    sc_wipe(&p->rst, sizeof p->rst);
}

/* security=tls (plain TLS) and security=reality.
 *
 * They differ in exactly three things, all required. Plain TLS sends a ClientHello without the
 * Reality authenticator (a TLS server would only see meaningless bytes in session_id). It needs a
 * NAME: Reality allows an empty SNI (the server expects a Hello without the extension), but plain
 * TLS without a name has nothing to send and nothing to check the certificate against. And it
 * verifies the certificate, the only proof of the server's identity here.
 *
 * The name is sni, with host only as a fallback: sni is what the server is asked for, so the
 * certificate is checked against it. With an IP address and no sni the check would be against the
 * address, as in Xray; certificates are almost never issued to an IP, so the subscription parser
 * (sublink.c) skips such a node before it gets here.
 *
 * The Reality state (our ephemeral key and the authenticator key) lives in struct sec_pend, not
 * in the connection, and is wiped by pend_drop: nothing needs it after the handshake. */
static int sec_tls_hello(struct tr_link *l, const struct tr_node *n, const char *alpn, int is_tls,
                         struct sec_pend *p) {
    struct reality_state *rst = &p->rst;
    p->is_tls = is_tls;
    p->eh = NULL;
    const char *verify_host = n->sni[0] ? n->sni : n->host;
    struct reality_cfg cfg = {
        .sni = is_tls ? verify_host : n->sni,
        .pbk = n->pbk, .sid = n->sid, .fp = n->fp,
        /* ALPN only when the transport needs it. tcp has none, and its Hello stays the one
         * verified against live nodes. */
        .alpn = alpn,
        .plain = is_tls,
        /* The X25519MLKEM768 hybrid in the ClientHello, as in Chrome 131+, uTLS
         * HelloChrome_Auto and Go 1.24+, so in any Xray client with fp=chrome or no fp. Without
         * it the Hello looks like an old Chrome, both by size (about 540 bytes instead of about
         * 1760) and by supported_groups. Setting STEER_NOPQ (to any value) turns it off, for
         * debugging a middlebox that drops a Hello longer than one segment. */
        .pq = !getenv("STEER_NOPQ") && !l->nopq,
    };
    /* pqv: an ML-DSA-65 key, base64url. Decoded BEFORE the Hello is sent: a broken key fails the
     * node rather than letting the handshake go on without checking the signature. The
     * subscription parser (sublink.c) already rejects such keys; this is the second line. */
    p->have_pqv = !is_tls && n->pqv && n->pqv[0];
    if (p->have_pqv && xc_b64url_decode(n->pqv, p->pqv, sizeof p->pqv) != SC_MLDSA65_PK)
        return REALITY_EBADKEY;
    unsigned char *hello = p->hello;
    size_t hello_n = 0;
    /* ws and httpupgrade offer ONLY http/1.1 in ALPN, as Xray does (uTLS
     * WebsocketHandshakeContext rewrites the fingerprint's ALPN to http/1.1 alone) and as Chrome
     * does on a WebSocket connection. With the usual "h2, http/1.1" the server behind TLS may pick
     * h2, and the HTTP/1.1 Upgrade request becomes garbage on an HTTP/2 connection. For the other
     * transports the Hello does not change by a bit (tests/hellofreeze.c): the carrier is used
     * only for these two. */
    struct reality_carrier car = { .alpn_http11 = 1 };
    const int h11 = alpn && !strcmp(alpn, "http/1.1");
    int rc = h11 ? reality_build_hello_carry(&cfg, rst, &car, hello, sizeof(p->hello), &hello_n)
                 : reality_build_hello(&cfg, rst, hello, sizeof(p->hello), &hello_n);
    if (rc) return rc;

    /* ECH (security=tls, the node's `ech=`): the Hello built above, with the real SNI, becomes the
     * inner one; the outer one with public_name from the ECHConfig goes on the wire (ech.h). The
     * buffers are on the heap: the outer Hello holds an encrypted copy of the inner one, twice the
     * usual size, and the threads that run handshakes have small stacks (connectors: 128 KB). */
    struct ech_heap *eh = NULL;
    struct tls13_ech *te = &p->te;
    *te = (struct tls13_ech){ 0 };
    const unsigned char *send_hello = hello;
    size_t send_n = hello_n;
    if (is_tls && n->ech && n->ech[0]) {
        eh = calloc(1, sizeof *eh);
        if (!eh) return TR_EIO;
        int ln = ech_b64_decode(n->ech, eh->list, sizeof eh->list);
        int er = ln <= 0 ? ECH_EPARSE : ech_pick(eh->list, (size_t)ln, &eh->cfg);
        if (er == 0) er = ech_wrap(&eh->cfg, hello, hello_n, eh->outer, sizeof eh->outer, &eh->outer_n, &eh->st);
        if (er != 0) {
            ech_heap_free(eh);
            return er;                              /* an ECH_E* code (transport.c, tr_strerror) */
        }
        send_hello = eh->outer;
        send_n = eh->outer_n;
        te->inner = eh->st.inner;
        te->inner_n = eh->st.inner_n;
        te->random = eh->st.random;
        p->eh = eh;
    }

    if (n->record_fragment && send_n > 5 + 16) {
        /* Split the ClientHello TLS record on the wire into two TLS records (for DPI bypass /
         * TrustTunnel anti_dpi). Prefer splitting inside the SNI hostname if present; the TLS 1.3
         * handshake transcript still hashes the unsplit Handshake message in send_hello. */
        size_t payload_n = send_n - 5;
        size_t split = payload_n / 2;
        size_t vlen = verify_host ? strlen(verify_host) : 0;
        if (vlen >= 4) {
            const unsigned char *pos = memmem(send_hello + 5, payload_n, verify_host, vlen);
            if (pos) split = (size_t)(pos - (send_hello + 5)) + vlen / 2;
        }
        if (split < 1) split = 1;
        if (split >= payload_n) split = payload_n - 1;
        size_t part2 = payload_n - split;
        unsigned char *frag = malloc(send_n + 5);
        if (!frag) return TR_EIO;
        memcpy(frag, send_hello, 3);
        frag[3] = (unsigned char)(split >> 8);
        frag[4] = (unsigned char)(split & 0xFF);
        memcpy(frag + 5, send_hello + 5, split);
        memcpy(frag + 5 + split, send_hello, 3);
        frag[5 + split + 3] = (unsigned char)(part2 >> 8);
        frag[5 + split + 4] = (unsigned char)(part2 & 0xFF);
        memcpy(frag + 10 + split, send_hello + 5 + split, part2);
        size_t f1 = 5 + split, f_tot = send_n + 5;
        for (size_t s1 = 0; s1 < f1; ) {
            ssize_t w = tr_lk_send(l, frag + s1, f1 - s1);
            if (w <= 0) {
                if (w == -1 && errno == EINTR) continue;
                free(frag);
                return w < -1 ? (int)w : TR_EIO;
            }
            s1 += (size_t)w;
        }
        for (size_t s2 = f1; s2 < f_tot; ) {
            ssize_t w = tr_lk_send(l, frag + s2, f_tot - s2);
            if (w <= 0) {
                if (w == -1 && errno == EINTR) continue;
                free(frag);
                return w < -1 ? (int)w : TR_EIO;
            }
            s2 += (size_t)w;
        }
        free(frag);
    } else {
        size_t sent = 0;
        while (sent < send_n) {
            ssize_t w = tr_lk_send(l, send_hello + sent, send_n - sent);
            if (w <= 0) {
                if (w == -1 && errno == EINTR) continue;
                return w < -1 ? (int)w : TR_EIO;
            }
            sent += (size_t)w;
        }
    }
    p->sent = send_hello;
    p->sent_n = send_n;
    return 0;
}

static int sec_tls_finish(struct tr_link *l, const struct tr_node *n, struct sec_pend *p) {
    struct reality_state *rst = &p->rst;
    const int is_tls = p->is_tls;
    const char *verify_host = n->sni[0] ? n->sni : n->host;

    /* Our PRIVATE key is passed, not a ready secret: the TLS key schedule needs the server's
     * ephemeral key, which only arrives in the ServerHello. */
    /* The node kind decides how the server proves itself, always exactly one way: plain TLS by
     * the chain and the name, Reality by an HMAC in the signature field of a temporary
     * certificate, keyed with what only the owner of the long-term key pair has. */
    struct tls13_auth auth = { 0 };
    /* Pins, verify names and an explicit opt-out of verification come from the node
     * (certverify.h). Without them the pointer stays NULL and verification is the default:
     * the chain up to the roots, and SNI. */
    struct cert_policy pol = { .pcs = n->pcs, .pks = n->pks, .vcn = n->vcn, .insecure = n->insecure };
    if (is_tls) {
        auth.host = verify_host;
        auth.roots = tls_cert_roots();
        auth.cert_algs = reality_cert_algs(reality_fp_of(n->fp));
        if (pol.pcs || pol.pks || pol.vcn || pol.insecure) auth.policy = &pol;
    } else auth.reality_key = rst->authkey;
    if (rst->pq) auth.mlkem_dk = rst->mlkem_dk;
    auth.mldsa_pk = p->have_pqv ? p->pqv : NULL;

    if (p->eh) auth.ech = &p->te;
    return tls13_handshake_auth(&l->tls, l->fd, p->sent, p->sent_n, rst->priv, &auth);
}

static int sec_tls(struct tr_link *l, const struct tr_node *n, const char *alpn, struct sec_pend *p) {
    return sec_tls_hello(l, n, alpn, 1, p);
}

static int sec_reality(struct tr_link *l, const struct tr_node *n, const char *alpn,
                       struct sec_pend *p) {
    return sec_tls_hello(l, n, alpn, 0, p);
}

const struct security_ops tr_sec_none    = { .name = "none",    .hello = sec_none };
const struct security_ops tr_sec_tls     = { .name = "tls",     .hello = sec_tls, .finish = sec_tls_finish };
const struct security_ops tr_sec_reality = { .name = "reality", .hello = sec_reality, .finish = sec_tls_finish };

/* Anything that is not none or tls is reality: the subscription parser (sublink.c) rejects an
 * unsupported value before the node gets here. */
const struct security_ops *tr_security(const char *name) {
    if (!strcmp(name, "none")) return &tr_sec_none;
    if (!strcmp(name, "tls")) return &tr_sec_tls;
    return &tr_sec_reality;
}

long tr_nopq_ttl_s = 3600;
long tr_nopq_skip_s = 300;

struct nopq_ent {
    uint64_t host;
    uint16_t port;
    uint8_t used, logged;
    long until, skip_until;
    uint64_t seq;
};
static struct nopq_ent g_nopq[TR_NOPQ_MAX];
static uint64_t g_nopq_seq;
static pthread_mutex_t g_nopq_mu = PTHREAD_MUTEX_INITIALIZER;

static long nopq_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long)ts.tv_sec;
}

static uint64_t nopq_hash(const char *s) {
    uint64_t h = 0xcbf29ce484222325ull;
    for (; *s; s++) h = (h ^ (unsigned char)*s) * 0x100000001b3ull;
    return h;
}

static struct nopq_ent *nopq_find(uint64_t h, uint16_t port) {
    for (size_t i = 0; i < TR_NOPQ_MAX; i++)
        if (g_nopq[i].used && g_nopq[i].host == h && g_nopq[i].port == port) return &g_nopq[i];
    return NULL;
}

static struct nopq_ent *nopq_slot(const struct tr_node *n, long now) {
    uint64_t h = nopq_hash(n->host);
    struct nopq_ent *e = nopq_find(h, n->port);
    if (!e) {
        e = &g_nopq[0];
        for (size_t i = 0; i < TR_NOPQ_MAX; i++) {
            struct nopq_ent *c = &g_nopq[i];
            if (!c->used || (c->until <= now && c->skip_until <= now)) { e = c; break; }
            if (c->seq < e->seq) e = c;
        }
        memset(e, 0, sizeof *e);
        e->used = 1;
        e->host = h;
        e->port = n->port;
    }
    e->seq = ++g_nopq_seq;
    return e;
}

enum { NOPQ_NONE, NOPQ_SHORT, NOPQ_USELESS };

static int nopq_state(const struct tr_node *n) {
    long now = nopq_now();
    pthread_mutex_lock(&g_nopq_mu);
    struct nopq_ent *e = nopq_find(nopq_hash(n->host), n->port);
    int r = !e ? NOPQ_NONE : e->until > now ? NOPQ_SHORT : e->skip_until > now ? NOPQ_USELESS : NOPQ_NONE;
    pthread_mutex_unlock(&g_nopq_mu);
    return r;
}

static void nopq_useless(const struct tr_node *n) {
    long now = nopq_now();
    pthread_mutex_lock(&g_nopq_mu);
    nopq_slot(n, now)->skip_until = now + tr_nopq_skip_s;
    pthread_mutex_unlock(&g_nopq_mu);
}

static void nopq_remember(const struct tr_node *n) {
    long now = nopq_now();
    pthread_mutex_lock(&g_nopq_mu);
    struct nopq_ent *e = nopq_slot(n, now);
    e->until = now + tr_nopq_ttl_s;
    int say = !e->logged;
    e->logged = 1;
    pthread_mutex_unlock(&g_nopq_mu);
    if (say)
        fprintf(stderr, "tunvless[warn]: node %s:%u does not answer a long ClientHello, using a short one "
                        "without the post-quantum key share (the long one is tried again in %ld min)\n",
                n->host, (unsigned)n->port, tr_nopq_ttl_s / 60);
}

static struct fm_link *(*g_fm_new)(const struct tr_node *n);

static int link_chain(const struct tr_node *n, struct fm_link **out) {
    if (!g_fm_new) return fm_link_open(n->fm, out);
    *out = g_fm_new(n);
    return 0;
}

static void link_unchain(struct tr_link *l) {
    fm_link_free(l->tls.fm);
    l->tls.fm = NULL;
}

static int link_hello(struct tr_link *l, const struct tr_node *n, const char *alpn, int timeout_s,
                      int nopq, struct sec_pend *p) {
    l->fd = -1;
    l->nopq = nopq;
    l->tls.fm = NULL;
    p->eh = NULL;
    struct fm_link *fm;
    int rc = link_chain(n, &fm);
    if (rc) return rc;
    int fd = tr_dial(n->host, n->port, timeout_s);
    if (fd < 0) {
        fm_link_free(fm);
        return fd;
    }
    l->fd = fd;
    l->tls.fm = fm;
    rc = fm ? fm_link_hs(fm, fd, timeout_s * 1000) : 0;
    if (!rc) rc = tr_security(n->security)->hello(l, n, alpn, p);
    if (rc) {
        pend_drop(p);
        close(fd);
        l->fd = -1;
        link_unchain(l);
    }
    return rc;
}

static void link_drop(struct tr_link *l, struct sec_pend *p) {
    pend_drop(p);
    close(l->fd);
    l->fd = -1;
    link_unchain(l);
}

/* On a failed handshake close() and the chain, not tr_link_close: tls13_handshake_auth frees its
 * own heap state on failure, and tests/vlessmatch.c checks that under AddressSanitizer for every
 * failure branch. */
static int link_finish(struct tr_link *l, const struct tr_node *n, struct sec_pend *p) {
    const struct security_ops *s = tr_security(n->security);
    int rc = s->finish ? s->finish(l, n, p) : 0;
    if (rc) link_drop(l, p);
    else pend_drop(p);
    return rc;
}

static int link_try(struct tr_link *l, const struct tr_node *n, const char *alpn, int timeout_s, int nopq) {
    struct sec_pend p;
    int rc = link_hello(l, n, alpn, timeout_s, nopq, &p);
    return rc ? rc : link_finish(l, n, &p);
}

static int link_nopq(const struct tr_node *n, int *retry) {
    *retry = 0;
    if (reality_fp_of(n->fp) == REALITY_FP_FIREFOX) return 0;
    const int pq_able = !getenv("STEER_NOPQ");
    const int st = pq_able ? nopq_state(n) : NOPQ_NONE;
    *retry = pq_able && st == NOPQ_NONE;
    return st == NOPQ_SHORT || !pq_able;
}

static int link_retry(struct tr_link *l, const struct tr_node *n, const char *alpn, int timeout_s,
                      int rc, int retry) {
    if (rc != TLS13_ETIMEOUT || !retry) return rc;
    int rc2 = link_try(l, n, alpn, timeout_s, 1);
    if (rc2 == 0) {
        nopq_remember(n);
        return 0;
    }
    if (rc2 == TLS13_ETIMEOUT) nopq_useless(n);
    return rc;
}

int tr_link_open(struct tr_link *l, const struct tr_node *n, const char *alpn, int timeout_s) {
    int retry;
    const int nopq = link_nopq(n, &retry);
    return link_retry(l, n, alpn, timeout_s, link_try(l, n, alpn, timeout_s, nopq), retry);
}

static int link_first(const struct tr_link *a, const struct tr_node *na, const struct tr_link *b,
                      const struct tr_node *nb, int timeout_s) {
    if (!tr_security(na->security)->finish) return 0;
    if (!tr_security(nb->security)->finish) return 1;
    if (fm_pending(a->tls.fm)) return 0;
    if (fm_pending(b->tls.fm)) return 1;
    struct pollfd pv[2] = { { .fd = a->fd, .events = POLLIN }, { .fd = b->fd, .events = POLLIN } };
    int r;
    do r = poll(pv, 2, timeout_s * 1000); while (r < 0 && errno == EINTR);
    if (r <= 0) return -1;
    return pv[0].revents ? 0 : 1;
}

int tr_link_open2(struct tr_link *a, const struct tr_node *na, struct tr_link *b,
                  const struct tr_node *nb, const char *alpn, int timeout_s) {
    struct tr_link *l[2] = { a, b };
    const struct tr_node *n[2] = { na, nb };
    int rc[2] = { 0, 0 }, retry[2], q[2];
    a->fd = b->fd = -1;
    struct sec_pend *p = calloc(2, sizeof *p);
    if (!p) return TR_EIO;
    for (int i = 0; i < 2; i++) q[i] = link_nopq(n[i], &retry[i]);
    rc[0] = link_hello(a, na, alpn, timeout_s, q[0], &p[0]);
    if (!rc[0]) rc[1] = link_hello(b, nb, alpn, timeout_s, q[1], &p[1]);
    if (rc[1]) link_drop(a, &p[0]);
    if (!rc[0] && !rc[1]) {
        int f = link_first(a, na, b, nb, timeout_s), s = f == 1 ? 0 : 1;
        if (f < 0) {
            f = 0;
            rc[0] = rc[1] = TLS13_ETIMEOUT;
            link_drop(a, &p[0]);
            link_drop(b, &p[1]);
        } else {
            rc[f] = link_finish(l[f], n[f], &p[f]);
            if (rc[f] && !(rc[f] == TLS13_ETIMEOUT && retry[f])) link_drop(l[s], &p[s]);
            else rc[s] = link_finish(l[s], n[s], &p[s]);
        }
        if (!rc[s] || (rc[s] == TLS13_ETIMEOUT && retry[s]))
            rc[f] = link_retry(l[f], n[f], alpn, timeout_s, rc[f], retry[f]);
        if (!rc[f]) rc[s] = link_retry(l[s], n[s], alpn, timeout_s, rc[s], retry[s]);
        for (int i = 0; i < 2; i++)
            if ((rc[0] || rc[1]) && !rc[i] && l[i]->fd >= 0) tr_link_close(l[i]);
    }
    free(p);
    return rc[0] ? rc[0] : rc[1];
}
