/* The transport and security half of a node link in Xray form (sublink.c): link strings, the
 * parameters type, security, sni, fp, pbk, sid, path, host, serviceName, mode, extra, pcs, vcn,
 * ech, allowInsecure, pqv, headerType, fm, and whether the node is usable for the transport.
 *
 * The VLESS half (flow, encryption, the UUID) and whole subscriptions (link lists, Xray and
 * sing-box configs, Clash YAML) are in sub.c.
 *
 * No network and no cryptography: the subscription tests build this file without libraries. */
#ifndef STEER_SUBLINK_H
#define STEER_SUBLINK_H
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include "vless.h"
#include "transport.h"

/* ---- link strings ---------------------------------------------------------------------------- */

/* An address where no peer can ever be (0.0.0.0, ::, loopback, broadcast). */
int sl_host_leads_nowhere(const char *h);
/* Strip one enclosing [...] pair in place: the form a config may write an IPv6 address in. */
void sl_host_unbracket(char *h);
/* A name (1) or an IPv4/IPv6 address (0), judged by the string, without resolving. */
int sl_host_is_name(const char *h);
/* Percent-decoding in place; "%" not followed by two hex digits stays as it is. */
void sl_pct_decode(char *s);
void sl_set_field(char *dst, size_t n, const char *src, size_t len);
/* A percent-encoded field: decode first, then cut to the field size. */
void sl_set_pct(char *dst, size_t n, const char *src, size_t len);
/* Drop an incomplete UTF-8 sequence from the end of the string. */
void sl_utf8_trim_tail(char *s);
/* Node name: percent-decoded and cut to the buffer without broken characters. */
void sl_set_name(char *dst, size_t n, const char *src);
size_t sl_unescape(const char *p, int yaml, char out[4], size_t *olen);
/* Port from a string of digits: 1..65535, else 0. */
uint16_t sl_port_of(const char *s);
/* UUID (16 bytes) from 32 hex digits, hyphens optional. 0 — parsed, -1 — not a UUID. Unlike
 * vless_uuid_parse, a short string is not hashed into a UUID. */
int sl_uuid_parse(const char *s, unsigned char out[16]);
/* 1, true, yes — on. */
int sl_truthy(const char *v);
/* A parameter value on the heap, percent-decoded; NULL — out of memory. */
char *sl_param_dup(const char *v, size_t vlen);
int sl_hdr_valid(const char *key, const char *val);

/* ---- long values and certificate checks ------------------------------------------------------ */

/* Markers of bad values: compared by address, so there is one of each per process. */
extern const char SL_BAD_PQV[], SL_FULL[], SL_BAD_PIN[], SL_BAD_ECH[], SL_ECH_DNS[], SL_BAD_XHX[],
    SL_BAD_XHX_JSON[], SL_BAD_FM[];
/* Shared table of long strings (pqv, encryption, pins, vcn, ech, fm): equal values share one copy.
 * Bounded: when it is full (or memory runs out), SL_FULL is returned. */
const char *sl_intern(const char *v, size_t n);
/* A node blob in its own table of 512, compared whole; NULL when it is full or memory runs out. */
const struct vless_node *sl_intern_node(const struct vless_node *d);
void sl_set_pqv(struct vless_node *n, const char *v);
void sl_add_pins(struct vless_node *n, const char *v, int spki);
void sl_set_vcn(struct vless_node *n, const char *v);
void sl_set_ech(struct vless_node *n, const char *v);
void sl_set_fm(struct vless_node *n, const char *json, size_t len);
/* xhttp: padding length "512" or "50-150", and the link's extra field (JSON). */
void sl_pad_range(struct vless_node *n, const char *v);
void sl_post_range(struct vless_node *n, const char *v);
void sl_parse_extra(struct vless_node *n, const char *extra);
struct sl_xh { char buf[768]; size_t n; uint8_t bad; };
void sl_xh_put(struct sl_xh *a, const char *key, const char *val, size_t vlen);
void sl_xh_done(struct vless_node *n, const struct sl_xh *a);
struct sl_dl { struct vless_node n; uint8_t bad, quic, alpn, notls; };
void sl_dl_done(struct vless_node *n, struct sl_dl *d);

/* ---- the whole link -------------------------------------------------------------------------- */

/* A protocol's own parameter: 1 — parsed (look no further), 0 — not its own. */
typedef int (*sl_own_fn)(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen);

/* scheme://secret@host:port?params#name. The node is zeroed; the secret (up to '@', as is) goes
 * to n->uuid, and its start and length in the link to *secret and *secret_n (for a protocol that
 * needs it longer than the field, or decoded). Each parameter goes to own first (own may be NULL),
 * then to the transport and security fields (sl_link_param). type defaults to tcp. 0 — parsed
 * (usability not checked yet), -1 — another scheme or a link that does not parse.
 * flags 0: the secret and a single port are required, as in a vless:// link.
 * SL_LINK_ANON: the secret is optional and ends at the last '@' of the authority; without one it
 * is empty. SL_LINK_PORTS: the port is optional (443), may be a list (n->port is its first port,
 * n->ports the canonical list when it is more than one plain port), and '/' may stand before '?'.
 * A list that does not fit n->ports parses, with the reason in n->skip_reason. */
#define SL_LINK_ANON 1u
#define SL_LINK_PORTS 2u
int sl_link_parse(const char *url, const char *scheme, struct vless_node *n, sl_own_fn own,
                  const char **secret, size_t *secret_n, unsigned flags);
/* 1 and the reason when the node's address leads nowhere (the check of sl_link_usable_post). */
int sl_link_nowhere(struct vless_node *n);
/* A transport or security parameter of the link: 1 — parsed, 0 — not one of them. */
int sl_link_param(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen);

/* Whether the node's transport and security are usable, in two halves; between them the protocol
 * checks its own fields (VLESS: the id), which sets the order in which reasons are reported.
 * 0 — usable, 1 — not (reason in skip_reason). pre: values that did not parse (pqv, pins, ech,
 * fm) or did not fit the table, tcp headerType=http, xhttp settings, certificate checks and
 * allowInsecure. post: security, tls without sni, reality without pbk, the transport and its path,
 * an address with nobody to answer, the xhttp mode. */
int sl_link_usable_pre(struct vless_node *n);
int sl_link_usable_post(struct vless_node *n);

/* ---- skip reasons ---------------------------------------------------------------------------- */

/* Count an unusable node under its reason (grouped by text; example: the name or host:port). */
void sl_skip_note(struct vless_sub_stats *st, const struct vless_node *n, const char *reason);
void sl_node_ok(const struct vless_node *n);
void sl_skip_node(struct vless_sub_stats *st, const struct vless_node *n, const char *reason);

/* ---- the node as the transport sees it ------------------------------------------------------- */

/* Pointers into the node, no copies: the node must outlive t. Certificate checks apply only to
 * security=tls: for reality these fields mean nothing. */
static inline void sl_tr_node(const struct vless_node *n, struct tr_node *t) {
    t->proto = n->proto_str[0] ? n->proto_str : (n->proto == VLESS_PROTO_HY2 ? "hy2" : "vless");
    t->uuid = n->uuid;
    t->host = n->host;
    t->port = n->port;
    t->type = n->type;
    t->security = n->security;
    t->sni = n->sni;
    t->fp = vless_fp() ? vless_fp() : n->fp;
    t->pbk = n->pbk;
    t->sid = n->sid;
    t->path = n->path;
    t->service = n->service;
    t->mode = n->mode;
    t->pad_from = n->pad_from;
    t->pad_to = n->pad_to;
    t->post_from = n->post_from;
    t->post_to = n->post_to;
    t->http_host = n->http_host;
    t->headers = n->headers;
    t->pqv = n->pqv;
    int tls = !strcmp(n->security, "tls");
    t->pcs = tls ? n->pcs : NULL;
    t->pks = tls ? n->pks : NULL;
    t->vcn = tls ? n->vcn : NULL;
    t->ech = tls ? n->ech : NULL;
    t->insecure = tls && n->insecure;
    t->record_fragment = n->record_fragment;
    t->encryption = n->encryption;
    t->xhx = n->xhx;
    t->fm = n->fm;
    t->fp_id = n->fp_id;
    t->fp_secret = n->fp_secret;
    t->fp_method = n->fp_method;
    t->fp_chunk = n->fp_chunk;
    t->fp_conc = n->fp_conc;
    t->dl = NULL;
}

static inline void sl_tr_nodes(const struct vless_node *n, struct tr_node *t, struct tr_node *td) {
    sl_tr_node(n, t);
    if (!n->dl || n->dl == &SL_DL_FULL) return;
    sl_tr_node(n->dl, td);
    t->dl = td;
}

#endif
