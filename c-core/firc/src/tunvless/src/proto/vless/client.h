/* Connection to a VLESS node and the check that the node accepted us (details in client.c).
 *
 * The connection is a struct transport (proto/transport/transport.h): the node's socket,
 * security and transport. VLESS on top of it is the request header (vless_proto.h) and Vision
 * frames (vision.h), written by the tunnel's dialer (vldial.c) and by the probe below. */
#ifndef STEER_CLIENT_H
#define STEER_CLIENT_H
#include "vless.h"
#include "transport.h"

/* VLESS's own codes. They share one number space with the transport's TR_* codes
 * (transport.h), so the values must not collide. */
#define VLESS_CONN_EBADUUID  (-35)
/* Reality did not accept the key: TLS is up, but the cover site answers. A separate code,
 * since this is the one failure that otherwise looks like a working node. */
#define VLESS_CONN_EREJECTED (-36)

/* Connects to the node: TCP, security and transport from the node's link. 0 on success. */
int vless_connect(const struct vless_node *node, struct transport *conn, int timeout_s);

/* Checks the node with a VLESS request (see client.c). 0 if the node answers as VLESS; why
 * gets the reason, or a short note on success. */
int vless_probe(const struct vless_node *node, int timeout_s, char *why, size_t why_n);

/* The same, with timings in milliseconds, -1 if that step was not reached:
 *
 *   handshake_ms  from the start of TCP to a ready transport (TCP + TLS + HTTP/2 if any):
 *                 the cost of connecting to the node, paid once;
 *   ttfb_ms       from sending the request to the first byte of the target's answer through
 *                 the tunnel: the latency a user feels, what curl reports as time to first byte.
 *
 * ttfb rather than ICMP: the tunnel does not forward ICMP, and this measures exactly the path
 * traffic takes. */
int vless_probe_timed(const struct vless_node *node, int timeout_s, char *why, size_t why_n,
                      int *handshake_ms, int *ttfb_ms);

int vless_probe6(const struct vless_node *node, int timeout_s, char *why, size_t why_n);

struct probe_target { unsigned char ip[16]; size_t ip_len; const char *host; };
extern const struct probe_target PROBE_TARGETS[2], PROBE6_TARGETS[2];
size_t probe_get(const struct probe_target *tg, char *out, size_t cap);

/* The reason text for a VLESS or transport code. */
const char *vless_strerror(int rc);

/* The smallest read buffer a caller may pass: transports over HTTP/2 return up to a whole TLS
 * record in one read. */
#define VLESS_MIN_RECV_CAP TRANSPORT_MIN_READ_CAP

#endif
