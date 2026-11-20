/* The Upgrade request path of the ws and httpupgrade transports — exactly the one Xray sends.
 *
 * On the way from the node link to the request line Xray parses the path with Go's url package
 * three times, and each pass changes something. Building the config (infra/conf,
 * WebSocketConfig.Build and HttpUpgradeConfig.Build) cuts `ed=` (early data) out of the query and
 * re-encodes the rest with url.Values.Encode; for ws, gorilla/websocket then parses `ws://host` +
 * path again and prints RequestURI; httpupgrade puts the whole path, `?` included, into URL.Path,
 * so `?` goes out as `%3F` (captured from Xray 26.3.27: `GET /p/q%3Fx=1 HTTP/1.1`). The Xray
 * server compares against its own copy of the same path, so "roughly the same" means "wrong path":
 * a 404 and a node that looks dead.
 *
 * The functions are pure (strings only, no network, no libraries): both the transport and the
 * subscription parser (sublink.c) call them, so a path Xray would trip over is rejected up front,
 * with a stated reason, instead of failing every connection attempt later. One rule for both
 * places. */
#ifndef STEER_TRPATH_H
#define STEER_TRPATH_H
#include <stddef.h>
#include <stdint.h>

/* The request-target for a node's path.
 *
 * ws — 1 for WebSocket, 0 for HTTPUpgrade. Returns 0 with a path starting with a slash in out;
 * otherwise -1 and *why, a short human-readable reason (it ends up in the node's skip_reason,
 * 96 bytes). why may be NULL.
 *
 * Only what Xray parses unambiguously is accepted: no control characters, no `#` (url.Parse
 * would cut it off as a fragment), no leading `//` (read as a host name), and no `:` in the first
 * segment of a path without a leading slash (Go would take it for a scheme). For ws, every `%`
 * before the `?` must also start a valid %XX sequence, or url.Parse fails and Xray does not
 * connect at all. */
int tr_upgrade_target(const char *path, int ws, char *out, size_t cap, const char **why);

/* The same, plus Ed — the early data size as Xray's Build computes it
 * (`uint32(strconv.Atoi(ed))`): 0 if `ed=` was not cut out (absent, empty, or the path did not
 * parse) or is not a number. ed_out may be NULL. trws.c and trupgrade.c say what Ed changes on
 * the wire. */
int tr_upgrade_target_ed(const char *path, int ws, char *out, size_t cap, const char **why,
                         uint32_t *ed_out);

#endif
