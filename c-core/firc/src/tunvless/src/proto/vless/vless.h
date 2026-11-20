/* VLESS nodes and subscriptions: the node as parsed from a link or a config, and the parsers.
 *
 * Why a client of its own rather than xray or sing-box: those binaries are a client and a server
 * for two dozen protocols, 27–38 MB. A router with a 6.9 MB overlay cannot hold them, and only one
 * client path is needed.
 */
#ifndef STEER_VLESS_H
#define STEER_VLESS_H
#include <stdint.h>
#include <stddef.h>

#define VLESS_PROTO_VLESS 0
#define VLESS_PROTO_HY2 1

#define HY2_RANGES_MAX 32
#define HY2_PORTS_CAP (HY2_RANGES_MAX * 12)

/* A node. Fields stay strings, as the link gives them, and the transport takes them as they are
 * (sl_tr_node): there is no second, converted form that could diverge from the first. */
struct vless_node {
    char name[128];        /* display name: the #fragment (decoded), remarks, tag or name */
    char host[128];
    uint16_t port;
    char proto_str[16];    /* empty or vless | tt | socks5 | trojan | ss | naive | vmess | mieru | anytls */
    char uuid[128];        /* VLESS UUID, or user:pass for tt / socks5 */
    char type[16];         /* tcp | grpc | xhttp | ws | httpupgrade | h2 */
    char security[16];     /* none | tls | reality */
    uint8_t record_fragment; /* TLS ClientHello record fragmentation (TrustTunnel anti_dpi) */
    char sni[128];         /* SNI of the ClientHello; for reality, the camouflage domain */
    char fp[16];           /* browser fingerprint: chrome, firefox, qq… */
    char pbk[64];          /* server public key, base64url */
    char sid[32];          /* short id, hex */
    char flow[32];         /* xtls-rprx-vision or empty */
    /* xhttp, ws, httpupgrade. For ws and httpupgrade it is kept as in the link, with `?ed=N`: the
     * early data parameter is cut from the path when the request is built, as Xray does
     * (src/proto/transport/trpath.h). */
    char path[128];
    char service[64];      /* grpc serviceName */
    char mode[16];         /* grpc: multi/gun; xhttp: auto/packet-up… */
    /* ws and httpupgrade: the Host header — the link's `host` parameter, or `host` in wsSettings or
     * httpupgradeSettings of an Xray config. Empty — sni, then the node address (Xray's rule). */
    char http_host[128];
    /* ws and httpupgrade: extra request headers, as "Name: value\n" lines. Xray's link format has no
     * such field; only config subscriptions and firc's private `headers=` link key carry them.
     * Xray config headers are checked when parsed: the name is HTTP token characters and the value
     * has no line break (otherwise one header would become two request lines). sing-box and Clash
     * headers are checked only for size. */
    char headers[192];
    /* A config header did not fit into headers or is invalid: the node is unusable (skip_reason)
     * rather than sent without that header. */
    uint8_t headers_bad;

    /* xhttp padding length, in characters: what the server accepts in x_padding. This is a
     * requirement: an xhttp server checks the length and answers 400 on a mismatch, so a node
     * with the wrong padding looks broken while everything else is right.
     *
     * pad_to == 0 — not announced: Xray's default 100..1000 applies
     * (GetNormalizedXPaddingBytes). */
    uint16_t pad_from, pad_to;
    /* xhttp packet-up: the most a POST body may carry (Xray scMaxEachPostBytes, a number or a
     * range; the client picks one value per connection). The server answers 413 to more.
     * post_to == 0 — not announced: Xray's default 1000000, above any chunk we send. */
    uint32_t post_from, post_to;
    /* Post-quantum fields of Xray-core, and fm, the finalmask JSON compacted (NULL also when it
     * has no tcp mask). They are long (an ML-DSA-65 key is 2603 base64url characters, a VLESS
     * encryption string with an ML-KEM-768 key about 1600, a finalmask up to FM_JSON_MAX), so the
     * strings live in a shared table (sl_intern, sublink.c), not in the node: equal values share
     * one copy, which is never freed and does not grow on repeated parsing. NULL — no such field.
     * The pointer outlives the node and its copies. */
    const char *pqv;         /* reality: mldsa65Verify / pqv */
    const char *encryption;  /* vless: encryption=mlkem768x25519plus.… (none is not stored) */
    const char *fm;
    /* Settings the client does not support and the server requires (the connection would not
     * open): the node is unusable at once, with a named reason, instead of failing every connect.
     * tcp_http — tcp HTTP camouflage header (headerType=http). */
    uint8_t tcp_http;
    const char *xhx;
    const struct vless_node *dl;
    /* FiniteProto v2 transport fields */
    char fp_id[64];
    char fp_secret[68];
    char fp_method[16];
    uint32_t fp_chunk;
    uint8_t fp_conc;
    /* Certificate check of a security=tls node (Xray-core: pinnedPeerCertSha256 / `pcs`,
     * verifyPeerCertByName / `vcn`; sing-box: certificate_public_key_sha256). Interned strings
     * (sl_intern), NULL — no such field. pcs and pks — lowercase hex SHA-256, comma separated:
     * pcs of the whole certificate (DER), pks of its SubjectPublicKeyInfo; vcn — comma separated
     * names. */
    const char *pcs, *pks, *vcn;
    /* security=tls: ECHConfigList in base64 (Encrypted Client Hello), interned; NULL — no ECH. */
    const char *ech;
    /* allowInsecure=1 (skip-cert-verify, insecure) in the subscription. The subscription alone
     * does not turn certificate checks off: the node is usable only with vless_set_insecure
     * (--insecure), see sl_link_usable_pre. */
    uint8_t allow_insecure;
    /* --insecure as it was at parse time (sl_link_usable_pre): this node's certificate is not
     * verified. Kept in the node rather than read from the global at connect time, so the
     * transport (trsec.c, via sl_tr_node) does not depend on the parser: the transport tests are
     * built without sublink.c. */
    uint8_t insecure;
    char skip_reason[96];  /* why the node is unusable, for display */
    uint8_t proto;
    const char *auth;
    char obfs[16];
    const char *obfs_pw;
    uint32_t up_mbps, down_mbps;
    char ports[HY2_PORTS_CAP];
    uint16_t hop_from, hop_to;
    uint8_t hop_mode;
    const char *qp;
};

size_t b64_decode(const char *in, size_t n, char *out, size_t out_n);

/* 0 — usable node, 1 — skipped (reason in skip_reason), -1 — not a vless link. */
int vless_parse_url(const char *url, struct vless_node *n);

size_t vless_link_scheme(const char *s);

/* Skip reasons, grouped by their text: a subscription of 26 nodes with the same unsupported
 * security gives one line, not 26. There is no reason code on purpose: the text already names the
 * class and the value ("transport X is not supported"), and a code would be a second way to say
 * the same thing that drifts away from the first. */
#define VLESS_SKIP_REASONS 8

#define VLESS_SUB_NODES_MAX 1024

struct vless_skip {
    /* The same string vless_node.skip_reason would hold, and the same size, so it is not cut. */
    char reason[96];
    char example[144];     /* name of the FIRST node with this reason, else host:port;
                            * for a glued or too long link, its length and first bytes.
                            * Longer than name[128] on purpose: host:port fits a whole
                            * host plus ":65535", and a cut host in an explanation is worse
                            * than none. */
    size_t count;
};

/* What parsing a subscription found besides the usable nodes (their count is the return value):
 * what was skipped and why. */
struct vless_sub_stats {
    size_t skipped;                              /* unusable VLESS nodes and links */
    size_t foreign;                              /* links and Clash proxies of other protocols */
    size_t reasons_n;                            /* distinct reasons collected */
    size_t reasons_dropped;                      /* nodes whose reason did not fit */
    struct vless_skip reasons[VLESS_SKIP_REASONS];
};

/* --insecure: nodes with allowInsecure are usable, and the client does not verify certificates of
 * security=tls nodes. Set BEFORE the nodes are parsed; off by default. A global, because the
 * parsers decide whether a node is usable and take no such argument. */
void vless_set_insecure(int on);
int vless_insecure(void);

/* --fp: chrome or firefox in place of every link's fp; NULL or "" keeps the link's. Call it
 * before worker threads start; the pointer is kept, so the string must live on (argv does). */
void vless_set_fp(const char *fp);
const char *vless_fp(void);

typedef void (*vless_node_fn)(void *ud, const struct vless_node *n, const char *skip_reason);
void vless_set_node_hook(vless_node_fn fn, void *ud);
void sl_intern_reset(void);
void sl_set_interner(const char *(*fn)(const char *v, size_t n));
extern const struct vless_node SL_DL_FULL;

static inline const struct vless_node *vless_dl(const struct vless_node *n) {
    return n->dl && n->dl != &SL_DL_FULL ? n->dl : NULL;
}

/* Turn a subscription file as read into text for vless_parse_sub: a JSON or YAML config and a
 * list of links are returned as is, base64 is decoded into dec. Details in sub.c. */
const char *vless_sub_text(const char *raw, size_t raw_n, char *dec, size_t dec_n);

/* st may be NULL. */
size_t vless_parse_sub(const char *text, struct vless_node *out, size_t max,
                       struct vless_sub_stats *st);

size_t vless_sub_count(const char *text);

/* Subscription file → array of nodes on the heap (the caller frees it), *cnt — usable nodes.
 * NULL — the file did not open, is over 64 MiB, or there is no memory. Below that cap, neither
 * the node count nor the subscription size is limited. */
struct vless_node *vless_load_sub(const char *path, size_t *cnt, struct vless_sub_stats *st);

#endif
