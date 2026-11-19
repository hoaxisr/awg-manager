/* Transport: how a protocol stream (VLESS) reaches its node.
 *
 * Three layers, bottom up, each in its own files:
 *
 *   socket      TCP to the node over every address of its name, with SO_MARK and SO_BINDTODEVICE
 *               when set (trdial.c);
 *   security    the link's security= field: none, tls, reality (trsec.c, struct security_ops);
 *   transport   the link's type= field: tcp, grpc, xhttp, ws, httpupgrade (transport.c,
 *               trgrpc.c, trxhttp.c, trws.c, trupgrade.c; struct transport_ops).
 *
 * Security and transport are two tables, not one chain of layers ("tcp -> tls -> grpc"): in the
 * node link they are two independent fields and any pair is valid (grpc over reality, xhttp over
 * tls, tcp with no security). One chain would need rules on which layer may sit on which, a
 * second way of saying what the two fields already say. Security kinds differ only in the
 * handshake: after it tls and reality carry the same TLS 1.3 records, and none is the bare
 * socket. So security_ops has only the handshake, in two halves, and after it the stream goes
 * through the shared link code (tr_link_* in transport.c).
 *
 * Users: the VLESS client (proto/vless/client.c: vless_connect and the node probe) and, through
 * it, the tunnel dialer (proto/vless/vldial.c). The transport knows nothing of the protocol: it
 * gets the node as struct tr_node, the link fields that concern it, pointing into the parsed node.
 */
#ifndef STEER_TRANSPORT_H
#define STEER_TRANSPORT_H
#include <stdint.h>
#include <stddef.h>
#include <time.h>
#include <sys/types.h>
#include "tls13.h"
#include "h2.h"
#include "xhopts.h"
#include "sockmark.h"

/* Transport error codes. They share one number space with the VLESS client's codes: -35 and -36
 * are VLESS_CONN_* (client.h), so a new code must not take them. */
#define TR_EDNS      (-30)
#define TR_ESOCK     (-31)
#define TR_ECONNECT  (-32)
#define TR_EIO       (-33)
#define TR_ECLOSED   (-34)
#define TR_EPINFULL  (-49)
#define TR_ECONNTIMEOUT (-56)
#define TR_ECONNREFUSED (-57)
#define TR_EUNREACH     (-58)
#define TR_ENONODE      (-59)
#define TR_EMASK        (-65)
/* The transport needs HTTP/2 and the server chose something else. A code of its own because all
 * else works and only the data does not flow: without it the node looks like it connects and
 * stays silent. */
#define TR_ENOH2     (-37)
#define TR_EGRPC     (-38)   /* the gRPC stream has a shape we cannot read */
/* ws and httpupgrade (trupgrade.c, trws.c). Separate codes because each needs a different fix. A
 * status other than 101 is almost always a wrong path or host (Xray answers 404 to an unknown
 * path); the text names the status. 101 without Upgrade or without a valid Accept means a proxy or
 * cache answered, not a WebSocket server. An ALPN other than http/1.1 means the server behind TLS
 * chose another protocol, and the upgrade works only over HTTP/1.1. */
#define TR_ENOH1      (-39)  /* the server negotiated an ALPN other than http/1.1 */
#define TR_EUPSTATUS  (-40)  /* the Upgrade request got a status other than 101 (named in text) */
#define TR_ENOUPGRADE (-41)  /* 101 without Upgrade: websocket or Connection: upgrade */
#define TR_EWSACCEPT  (-42)  /* 101 without a valid Sec-WebSocket-Accept */
#define TR_EUPTIMEOUT (-43)  /* no answer to the Upgrade request within the connect timeout */
#define TR_EUPTOOBIG  (-44)  /* the Upgrade response is over the limit or not HTTP */
#define TR_EWSFRAME   (-45)  /* a WebSocket frame breaks RFC 6455 */
#define TR_EVENC      (-46)  /* VLESS encryption: handshake or format (reason: tr_venc_reason) */
#define TR_EVENCAUTH  (-47)  /* VLESS encryption: AEAD failed, the keys differ from the server's */
#define TR_EVENC0RTT  (-48)  /* VLESS encryption: 0-RTT ticket rejected; the next connection
                              * does a full handshake */

/* The node as the transport sees it: only what concerns the connection. The pointers point into
 * the parsed node (struct vless_node), no copies: the node outlives every connection to it. */
struct tr_node {
    const char *proto;         /* vless | tt | socks5 | trojan | ss | naive */
    const char *uuid;          /* secret / user:pass / method:password */
    const char *host;
    uint16_t port;
    const char *type;          /* tcp | grpc | xhttp | ws | httpupgrade */
    const char *security;      /* none | tls | reality */
    const char *sni;           /* SNI in the ClientHello; for reality, the camouflage domain */
    /* Browser fingerprint of the ClientHello: firefox, anything else Chrome (reality.h). */
    const char *fp;
    const char *pbk;           /* Reality public key, base64url */
    const char *sid;           /* Reality short id, hex */
    const char *path;          /* xhttp, ws, httpupgrade; ws/httpupgrade keep `?ed=` (trpath.h) */
    /* ws and httpupgrade: the Host header (empty: sni, then the node address, as in Xray) and
     * extra request headers as "Name: value\n" lines (from an Xray config, see vless.h). NULL is
     * the same as empty. */
    const char *http_host;
    const char *headers;
    const char *service;       /* grpc serviceName */
    const char *mode;          /* grpc: multi/gun; xhttp: auto/packet-up... */
    /* xhttp padding length announced by the node (see vless_node.pad_from in vless.h). pad_to 0:
     * not announced, the Xray default applies. */
    uint16_t pad_from, pad_to;
    /* xhttp packet-up POST body limit (see vless_node.post_from). post_to 0: not announced. */
    uint32_t post_from, post_to;
    /* Reality: ML-DSA-65 public key that verifies the certificate signature, base64url (1952
     * bytes decoded), or NULL. Xray's mldsa65Verify, `pqv` in the link. */
    const char *pqv;
    /* security=tls: certificate checks set by the node (Xray: pinnedPeerCertSha256,
     * verifyPeerCertByName; sing-box: certificate_public_key_sha256), strings as in vless_node,
     * NULL when absent. insecure comes from --insecure, not from the subscription. */
    const char *pcs, *pks, *vcn;
    int insecure;
    int record_fragment;
    /* security=tls: ECHConfigList in base64 (Xray echConfigList, `ech=` in the link), or NULL. */
    const char *ech;
    /* VLESS encryption: the node's `encryption` string as is (mlkem768x25519plus...), or NULL for
     * none. trvenc.c parses and runs it over the open transport. */
    const char *encryption;
    const char *xhx;
    const char *fm;
    const char *fp_id;
    const char *fp_secret;
    const char *fp_method;
    uint32_t fp_chunk;
    uint8_t fp_conc;
    const struct tr_node *dl;
};

/* A link: the socket and the security over it, one protected byte stream. A struct of its own
 * because xhttp keeps its links in HTTP/2 connections of their own (xconn.h), with the same
 * handshake. */
struct tr_link {
    int fd;
    int plain;                 /* security=none: no TLS at all */
    /* The server switched to direct copy (the Vision direct command): the socket no longer
     * carries our TLS records but the target connection's stream as is. Set from outside by the
     * Vision frame parser (transport_direct), because the command lives in those frames. */
    int rx_direct;
    int tx_direct;
    int nopq;
    struct tls13 tls;
};

/* gRPC message stream parser.
 *
 * State is needed because the boundaries of a gRPC message, an HTTP/2 frame and a TLS record
 * never line up: one message can arrive in three records, one record can bring a message and a
 * half. Counters, not a buffer: a buffer per connection would cost megabytes in total, the
 * counters cost twenty bytes. */
struct grpc_de {
    unsigned char hdr[5];        /* compressed flag (1) + length (4) */
    unsigned char hdr_n;
    uint32_t msg_left;           /* bytes left of the message body */
    unsigned char pb[8];         /* protobuf field tag and length */
    unsigned char pb_n;
    uint32_t field_left;         /* bytes left of the bytes field inside the message */
    uint8_t naive;               /* 1: NaiveProxy 8-frame padding active on stream */
    uint8_t naive_tx;            /* NaiveProxy padded frames written so far (0..8) */
    uint8_t naive_rx;            /* NaiveProxy padded frames read so far (0..8) */
    uint8_t naive_pad_left;      /* unread padding bytes trailing current Naive frame */
};

/* xhttp mode. It decides how many HTTP requests one connection carries and how they split the
 * directions, not the framing: the body bytes are the same in all three.
 *
 *   stream-one  one POST: the request body goes up, the response body comes down. One
 *               connection, one stream, the lowest latency. Xray picks it with reality;
 *   stream-up   two requests: a GET for the download and a long POST for the upload. For a
 *               proxy that does not pass a bidirectional body but tolerates a streamed upload;
 *   packet-up   a GET for the download and a series of short POSTs, one per chunk, numbered in
 *               the path. The only mode that passes a proxy which does not stream uploads at
 *               all, such as a CDN that buffers requests.
 */
enum xhttp_mode { XH_STREAM_ONE = 0, XH_STREAM_UP, XH_PACKET_UP };

struct xh_side {
    char authority[128];       /* repeated in every request of the series */
    struct xh_opts opts;
    char base_path[256];
    char query[256];
    const char *headers;
    /* Padding length announced by the node. Kept here because the packet-up upload requests are
     * built without the node at hand, and each needs padding: the server checks it on EVERY
     * request, not only the first. */
    uint16_t pad_from, pad_to;
};

struct xconn;
struct xm_client;

struct xs { struct xconn *x; uint16_t slot; uint32_t sid; };

#define XH_POST_MAX 64

#define XH_POST_RETRY 2

struct xc_chunk;
struct xc_q { struct xc_chunk *head, *tail; size_t n; };

struct xh_post { struct xs s; uint64_t seq; struct xc_q keep; uint8_t tries, redo; };

struct xm_mgr;
struct transport;

struct xh_flow {
    struct xs one, down, up;
    struct xh_post post[XH_POST_MAX];
    uint8_t post_n;
    struct xm_client *cl, *cl_dl, *cl_post;
    struct xm_mgr *mgr, *mgr_dl;
    struct xconn *wait, *post_x, *hold[2];
    struct transport *wnext, *bnext, *hnext[2];
    struct xc_q bat;
    size_t bres, blim;
    uint64_t post_at, bw_at, wait_until, feed_seq;
    uint32_t feed;
    int end;
    uint8_t wait_n, shared, post_dec, post_wait, blisted, bstarved, wrote, again, get_wait, redo_n;
    uint16_t owner;
};

/* xhttp state. */
struct xh_state {
    enum xhttp_mode mode;
    struct xh_flow f;
    struct xconn *priv[2];
    int err;
    int wait_ms;
    uint64_t seq;              /* packet-up chunk number, from 0 */
    struct xh_side main_side, down_side;
    char sid[136];
    size_t worst_base;
    /* packet-up: the most one POST carries on this connection, picked from the node's range at
     * open (as Xray's client does); a write larger than that goes as several chunks. 0: no
     * limit. */
    uint32_t post_max;
};

/* Parser of WebSocket frames from the server (RFC 6455, section 5): streaming, chunks of any size.
 *
 * State, not a buffer, for the same reason as grpc_de: frame boundaries do not match TLS record
 * boundaries, a 64 KB frame arrives in several records and a record carries several frames. Data
 * frame payload is passed on as it arrives; only the frame header (up to 14 bytes) and a control
 * frame body (up to 125 bytes, the RFC 6455 5.5 limit) are buffered. */
struct ws_rx {
    unsigned char hdr[14];
    uint8_t hdr_n;
    uint8_t in_payload;        /* header parsed, reading the frame body */
    uint8_t op;                /* opcode of the current frame */
    uint8_t in_msg;            /* inside a fragmented message (waiting for a continuation) */
    uint64_t left;             /* body bytes of the current frame not yet received */
    unsigned char ctl[125];    /* control frame body */
    uint8_t ctl_n;
    /* A ping arrived: answer a pong with the same body. The reader (trws.c) sends it after
     * parsing the chunk: the parser is pure and does not write to the network, so it can be
     * tested in memory. Two pings in one chunk get one answer, to the last, as RFC 6455 5.5.3
     * allows. */
    uint8_t pong_due;
    uint8_t pong_n;
    unsigned char pong[125];
    uint8_t closed;            /* close received: no more data */
    uint8_t close_sent;        /* our close reply is sent */
    uint16_t close_code;       /* code from close; 1005: no code */
};

/* ws and httpupgrade: what remains after the 101 response.
 *
 * stash holds bytes read together with the 101 response but lying past its headers: the server
 * may send data right after the response, and one TLS record (or one socket read) brings both.
 * Dropping them would desync the stream from its first bytes (for httpupgrade the start of the
 * VLESS response, for ws the first frame). On the heap and only when present: this is rare, and
 * a 16 KB buffer in each of the hundreds of connections in the stack's table would add up. The
 * reader frees it (tr_h1_free); transport_close frees what was not read.
 *
 * EARLY DATA (`?ed=N` in the path, Xray's Ed) changes when the request goes out and when the
 * response is parsed, exactly as in Xray (details in trws.c and trupgrade.c):
 *   ws           the request waits for the first write (H1_DEFER); a write no longer than Ed goes
 *                in Sec-WebSocket-Protocol, a longer one separately, as frames after the 101;
 *   httpupgrade  the request goes out at once but the response is not awaited: data follows, and
 *                the first read parses the response (H1_WAIT). Over TLS or Reality only: with
 *                security=none the 101 is awaited as without Ed (tr_h1_upgrade says why).
 * Until the 101 arrives (H1_WAIT), ws queues its writes in q. In Xray such a write just waits for
 * the response, but the tunnel loop must not wait; the queue goes out as frames right after the
 * 101. */
enum { H1_OPEN = 0, H1_DEFER, H1_WAIT };
struct h1_resp;
struct h1_state {
    unsigned char *stash;
    uint32_t stash_n, stash_off;
    struct ws_rx rx;           /* ws only */
    uint8_t phase;             /* H1_OPEN, H1_DEFER, H1_WAIT */
    uint8_t upgraded;          /* 101 accepted: ws sends close 1000 when closing */
    uint32_t ed;               /* Ed from the path (trpath.h) */
    /* The node for tr_h1_send, which the deferred ws request calls after open: a copy, because
     * struct tr_node itself lives on the opener's stack (its pointers point into the parsed
     * node, which outlives the connection). */
    struct tr_node node;
    struct h1_resp *resp;      /* response parser in H1_WAIT, on the heap while waiting */
    char accept[29];           /* expected Sec-WebSocket-Accept */
    /* ws in H1_WAIT: writes made before the 101, each [4-byte length][data], on the heap. */
    unsigned char *q;
    uint32_t q_n, q_cap;
};

struct transport;

/* A transport, the link's type= field: how the protocol stream is laid inside the protected link.
 * ws and httpupgrade send an HTTP/1.1 Upgrade request in open (trupgrade.c) with ALPN
 * "http/1.1"; ws frames its writes and reads (trws.c), httpupgrade has no frames and passes the
 * stream as is after the 101. The tunnel stack and the dialer see a transport only through
 * transport_write and transport_read. */
struct transport_ops {
    const char *name;          /* as in the node link: tcp, grpc, xhttp, ws, httpupgrade */
    /* The ALPN protocol the transport needs ("h2", "http/1.1"), or NULL: any will do (tcp).
     * The ClientHello offers "h2, http/1.1" as Chrome does whatever this is, or "http/1.1"
     * alone when that is what is needed (ws, httpupgrade; trsec.c, reality.c). If the server
     * negotiates SOMETHING ELSE, transport_open fails with TR_ENOH2 (TR_ENOH1 when http/1.1 was
     * asked); a server that negotiates nothing is accepted. */
    const char *alpn;
    /* The protocol data sits in the TLS records as is: a read may return a pointer into the
     * decrypted record instead of a copy (transport_read_zc). True for tcp and httpupgrade; grpc
     * and xhttp have HTTP/2 between TLS and the data, ws has frames, and the body is copied. */
    int zc;
    /* Open the transport over the already protected link: HTTP/2, requests.
     * NULL: nothing to open (tcp). On failure nothing needs closing: transport_open closes. */
    int  (*open)(struct transport *t, const struct tr_node *n, int timeout_s);
    int  (*write)(struct transport *t, const unsigned char *d, size_t n);
    /* 0 bytes with code 0 is valid: a record or frame without data arrived (an HTTP/2 control
     * frame, for one). End of stream is a code. */
    int  (*read)(struct transport *t, unsigned char *d, size_t cap, size_t *got);
    int  (*dial)(struct transport *t, const struct tr_node *n, int timeout_s);
    int  (*fd)(const struct transport *t);
    /* The struct moved in memory (a connection takes the link of a spare session, vl_take in
     * vldial.c): fix the pointers to itself. NULL: the transport has none. */
    void (*moved)(struct transport *t);
    /* Free what the transport holds beyond the main link (xhttp's connections, the rest after
     * the 101). NULL: nothing. */
    void (*close)(struct transport *t);
    /* Whether the transport holds unread data that neither the kernel nor the link knows of: the
     * rest after the 101, a deferred ws end of stream. Without it the tunnel loop would wait for
     * the socket while the data sits until the next packet from the server, which may never come
     * if the server has said everything (see transport_has_data). NULL: never. */
    int  (*pending)(const struct transport *t);
    /* The transport still parses its own data over the TLS records (the 101 response of
     * httpupgrade with early data): no zero-copy read yet, even with nothing pending. NULL:
     * never. */
    int  (*busy)(const struct transport *t);
    /* How many bytes a write can take right now: the HTTP/2 send window (grpc, xhttp). A write
     * larger than that fails with H2_EWINDOW. NULL: no limit (tcp, ws, httpupgrade: the socket
     * buffer). */
    long (*room)(struct transport *t);
    int  (*due)(struct transport *t, uint64_t now_ms, int *next_ms);
};

/* Security, the link's security= field. The kinds differ only in the handshake (see the head). */
struct sec_pend;
struct security_ops {
    const char *name;          /* none | tls | reality */
    /* Handshake over l->fd, sent by hello and completed by finish (NULL: nothing to wait for);
     * alpn comes from the transport. On failure neither closes the socket: the caller does, the
     * same way for the main link and the second one. */
    int (*hello)(struct tr_link *l, const struct tr_node *n, const char *alpn, struct sec_pend *p);
    int (*finish)(struct tr_link *l, const struct tr_node *n, struct sec_pend *p);
};

/* A connection to a node: the main link, the transport over it and its state. xhttp has no main
 * link: it keeps its own connections in xh, which shares the memory of the others. */
struct fp_ctx;
struct transport {
    const struct transport_ops *fr;
    struct venc *enc;          /* VLESS encryption over the transport (trvenc.c), or NULL */
    struct fp_ctx *fp;         /* FiniteProto v2 context, or NULL */
    const char *why;
    union {
        struct {
            struct tr_link link;
            struct h2 h2;      /* grpc only */
            struct grpc_de de; /* grpc only */
            struct h1_state h1; /* ws and httpupgrade only */
        };
        struct xh_state xh;
    };
};

/* VLESS encryption (trvenc.c): a handshake over the open transport, then AEAD records in place of
 * its transport_write/read. transport_open calls it when the node has encryption set. */
struct venc;
int tr_venc_open(struct transport *t, const struct tr_node *n, int timeout_s);
int tr_venc_write(struct transport *t, const unsigned char *d, size_t n);
int tr_venc_read(struct transport *t, unsigned char *d, size_t cap, size_t *got);
int tr_venc_pending(const struct transport *t);
void tr_venc_close(struct transport *t);
const char *tr_venc_reason(void);

/* Full setup: TCP, the security handshake, the transport open. 0: ready; otherwise an error code,
 * and no descriptor or heap keys are left behind. */
int transport_open(struct transport *t, const struct tr_node *n, int timeout_s);

/* Data exchange in the form the node's transport needs. Callers do not know the transport, so
 * no caller can forget it and send the stream in the wrong wrapping. */
int transport_write(struct transport *t, const unsigned char *d, size_t n);
int transport_read(struct transport *t, unsigned char *d, size_t cap, size_t *got);

/* Bytes of the caller's data a transport_write can take now, after the transport's own framing
 * (gRPC message header, VLESS encryption records); -1: no limit. The tunnel sizes the window it
 * gives the client by it, so the client sends what the node can take instead of finding out by
 * retransmission timeouts (see dialer_ops.room). */
long transport_room(struct transport *t);

int transport_whole_writes(const struct transport *t);

/* Read WITHOUT AN EXTRA COPY where the transport allows it (transport_ops.zc).
 *
 * *data points either into the connection (at a record decrypted in place) or into buf; the
 * caller reads through the pointer either way. All downloaded traffic goes through here, so a
 * copy would cost a pass over memory per downloaded byte.
 *
 * One rule: use the data BEFORE the next call on this connection. More in tls13.h at
 * tls13_read_ref. */
int transport_read_zc(struct transport *t, unsigned char *buf, size_t cap,
                      const unsigned char **data, size_t *got);

/* Whether we hold already read data that the kernel will not report as socket readiness. Details
 * in transport.c. */
int transport_has_data(const struct transport *t);

/* The server announced direct copy (Vision): from now on the socket carries the stream as is, not
 * TLS records. Called by the protocol frame parser. */
void transport_direct(struct transport *t);

void transport_tx_direct(struct transport *t);
int  transport_can_tx_direct(const struct transport *t);

/* The struct moved in memory: fix the pointers to itself (transport_ops.moved). */
void transport_moved(struct transport *t);

static inline int transport_fd(const struct transport *t) {
    return t->fr && t->fr->fd ? t->fr->fd(t) : t->link.fd;
}

static inline int transport_rx_direct(const struct transport *t) {
    return !(t->fr && t->fr->dial) && t->link.rx_direct;
}

static inline size_t transport_used(const struct transport *t) {
    return t->fr && t->fr->dial ? offsetof(struct transport, xh) + sizeof t->xh : sizeof *t;
}

/* Close everything: the descriptors of every link and the cipher contexts on the heap. Also fine
 * for a struct that transport_open did not finish or never opened (fd -1). */
void transport_close(struct transport *t);

const char *transport_strerror(int rc);

/* Resolve host once and use these addresses for every later connection to it, instead of a DNS
 * query per connection (trdial.c says why). Number of addresses of both families, or TR_EDNS. */
int transport_pin_host(const char *host);

/* Address i % count of the node's addresses (the pinned ones, else one resolution) as an IP
 * literal in lit. The count, or -1 when none resolves or lit is too small. */
int transport_node_ip(const char *host, unsigned i, char *lit, size_t cap);
int transport_pin_count(const char *host);
/* host as a URL or Host value has it: an IPv6 address in brackets, else as it is. */
const char *tr_host_literal(const char *host, char *buf, size_t cap);
/* The IPv4 addresses pinned for host (network order), at most max: how many. */
int transport_pinned_addrs(const char *host, uint32_t *out, int max);
typedef void (*transport_pin_fn)(void *ud, const char *host, int ok);
void transport_pin_hosts(const char *const *hosts, size_t n, int timeout_ms, transport_pin_fn fn,
                         int (*stop)(void *ud), void *ud);

/* The room a transport_read caller must give: transports over HTTP/2 return up to a whole TLS
 * record at once. */
#define TRANSPORT_MIN_READ_CAP H2_MIN_READ_CAP

/* ---- for the files of this directory ------------------------------------------------------ */

extern const struct transport_ops tr_tcp, tr_grpc, tr_xhttp, tr_ws, tr_httpupgrade, tr_tt, tr_fp;
extern const struct security_ops tr_sec_none, tr_sec_tls, tr_sec_reality;
int xh_flow_start(struct transport *t, struct xconn *main, struct xconn *down, uint16_t owner);
int xh_attach(struct transport *t, const struct tr_node *n, uint16_t owner);
int xh_post_flush(struct transport *t);
int xh_post_feed(struct transport *t);
int xh_get_open(struct transport *t, struct xconn *x);
void xh_posts_drop(struct transport *t);

/* ---- ws and httpupgrade: the shared Upgrade request (trupgrade.c) and frames (trws.c) ------ */

/* Open ws or httpupgrade over the protected link. Without early data: the Upgrade request and the
 * 101 response synchronously, within timeout_s (opening runs in a connector thread, not in the
 * tunnel loop), and the bytes past the response go to t->h1.stash. With early data, as in Xray
 * (h1_state): ws defers the request to the first write, httpupgrade over TLS or Reality sends it
 * and does not wait for the response. ws: 1 for WebSocket, 0 for httpupgrade. */
int tr_h1_upgrade(struct transport *t, const struct tr_node *n, int ws, int timeout_s);

/* Send the Upgrade request for the node t->h1.node; for ws with a new key (the expected Accept
 * goes to t->h1.accept) and, if ed is not NULL, with early data in Sec-WebSocket-Protocol
 * (unpadded base64url, like Xray's RawURLEncoding). */
int tr_h1_send(struct transport *t, int ws, const unsigned char *ed, size_t ed_n);

/* Deferred response (H1_WAIT): feed a chunk of input. 0: the response is not over yet; 1:
 * accepted (phase H1_OPEN, *used is the bytes taken by the headers, the rest is already the
 * stream); otherwise a TR_* code. */
int tr_h1_lazy(struct transport *t, int ws, const unsigned char *in, size_t n, size_t *used);

/* Build the Upgrade request in out, without the network, for opening and for the test
 * (tests/wsmatch.c). key: Sec-WebSocket-Key for ws, NULL for httpupgrade; proto:
 * Sec-WebSocket-Protocol (early data) or NULL. The request length, or 0: it did not fit or the
 * path is invalid. */
size_t tr_h1_request(const struct tr_node *n, int ws, const char *key, const char *proto,
                     char *out, size_t cap);

/* The response to the Upgrade request, parsed as a stream from input cut anywhere. */
struct h1_resp {
    int done;                  /* the response headers are over */
    int status;                /* response status; 0: no status line yet */
    uint8_t up_ok, conn_ok, acc_ok;
    uint8_t seen;              /* which of the three headers have been seen */
    uint8_t bad;               /* not HTTP at all, or over the limit */
    uint32_t total;            /* header bytes, for the limit */
    uint16_t line_n;
    char line[256];            /* the current line; a longer one is cut (see trupgrade.c) */
};
/* Feed a chunk; *used is the bytes taken by the headers (the rest is already the stream after
 * them). accept: the expected Sec-WebSocket-Accept for ws, NULL for httpupgrade. */
void tr_h1_resp_feed(struct h1_resp *r, int ws, const char *accept,
                     const unsigned char *in, size_t n, size_t *used);
/* Verdict on the parsed response: 0 or a TR_* code (TR_EUPSTATUS, TR_ENOUPGRADE, TR_EWSACCEPT). */
int tr_h1_resp_verdict(const struct h1_resp *r, int ws);
/* The status of this thread's last TR_EUPSTATUS failure, for the reason text. */
int tr_h1_last_status(void);

/* The text of a dial failure code, or NULL if rc is not one. */
const char *tr_dial_strerror(int rc);
/* Free the bytes left after the 101 (t->h1.stash). */
void tr_h1_free(struct transport *t);
/* Random bytes from the kernel (ws request key, frame mask). 0 or -1. */
int tr_h1_random(unsigned char *out, size_t n);

/* Sec-WebSocket-Accept for a key (RFC 6455, 4.2.2): base64(SHA-1(key + GUID)), 28 characters. */
void tr_ws_accept(const char *key, char out[29]);

/* A client frame: header, mask key and masked body (RFC 6455, 5.2-5.3). A client MUST mask every
 * frame, and a server must drop the connection on an unmasked one. The frame length, or 0 if it
 * does not fit in cap. */
size_t tr_ws_frame(unsigned char *out, size_t cap, int opcode, int fin, const unsigned char key[4],
                   const unsigned char *d, size_t n);

/* Parse a chunk of server frames: data frame bodies go to out (out may equal in: parsing only
 * moves bytes left), control frames go to the state (ping, close). The input is consumed whole.
 * 0 or TR_EWSFRAME; H2_ETOOBIG: did not fit in out (the caller gave less room than input). */
int tr_ws_parse(struct ws_rx *r, const unsigned char *in, size_t n,
                unsigned char *out, size_t cap, size_t *out_n);

/* TCP to the node over every address of its name (trdial.c). A descriptor or a negative TR_*. */
int tr_dial(const char *host, uint16_t port, int timeout_s);
int tr_reach(const char *const *hosts, const uint16_t *ports, size_t n, int timeout_ms, char *ok, int *err);

/* Security by the link field: none, tls, otherwise reality (trsec.c). */
const struct security_ops *tr_security(const char *name);

/* A whole link: socket and security handshake. On failure the socket is closed and fd == -1.
 * Shared by the main link and xhttp's connections precisely so that they cannot diverge. */
int tr_link_open(struct tr_link *l, const struct tr_node *n, const char *alpn, int timeout_s);
/* Two links at once: both Hellos go out before either answer is read. On failure both are
 * closed. */
int tr_link_open2(struct tr_link *a, const struct tr_node *na, struct tr_link *b,
                  const struct tr_node *nb, const char *alpn, int timeout_s);
#define TR_NOPQ_MAX 256

/* I/O of a link in the struct h2_io form (ctx is a struct tr_link). */
int tr_link_write(void *ctx, const unsigned char *d, size_t n);
int tr_link_read(void *ctx, unsigned char *d, size_t cap, size_t *got);

/* Close a link: the socket and, for TLS, the keys on the heap. */
void tr_link_close(struct tr_link *l);
int tr_link_nonblock(struct tr_link *l);

ssize_t tr_lk_send(struct tr_link *l, const void *d, size_t n);
ssize_t tr_lk_recv(struct tr_link *l, void *d, size_t cap, int flags);
int tr_lk_err(const struct tr_link *l, ssize_t r);

static inline uint64_t tr_mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}
void transport_set_loop(struct transport *t);
int transport_idle(const struct transport *t);
int transport_due(struct transport *t, uint64_t now_ms, int *next_ms);
long transport_mask_room(struct transport *t);

#endif
