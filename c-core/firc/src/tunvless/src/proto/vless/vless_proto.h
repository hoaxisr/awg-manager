/* VLESS: the request header and the response. The format is described in vless_proto.c. */
#ifndef STEER_VLESS_PROTO_H
#define STEER_VLESS_PROTO_H
#include <stdint.h>
#include <stddef.h>

#define VLESS_EAGAIN (-1)   /* not enough data yet, read more */
#define VLESS_EPROTO (-2)   /* not a VLESS response: most likely Reality did not accept us */

enum vless_cmd { VLESS_CMD_TCP = 1, VLESS_CMD_UDP = 2, VLESS_CMD_MUX = 3 };
enum { VLESS_ADDR_IPV4 = 1, VLESS_ADDR_DOMAIN = 2, VLESS_ADDR_IPV6 = 3 };

/* Forms of the user id. The rule is Xray's (common/uuid/uuid.go, ParseString) and depends on
 * the string's length, not on whether it looks like hex: Xray derives a UUID from a string of
 * 15 hex digits rather than parsing it. A client that decides otherwise sends the wrong 16
 * bytes: the server finds no such user and just closes the connection. */
enum {
    VLESS_UUID_HEX     =  1,   /* 32..36 characters: a hex UUID, hyphens optional */
    VLESS_UUID_DERIVED =  2,   /* 1..30 characters: the UUID is derived (sha1, version 5) */
    VLESS_UUID_EMPTY   = -1,
    VLESS_UUID_GAP     = -2,   /* exactly 31: too long to derive, too short for a UUID */
    VLESS_UUID_TOOLONG = -3,   /* longer than 36 */
    VLESS_UUID_NOTHEX  = -4    /* UUID length, but not hex */
};

/* Classification only, no derivation: the subscription parser uses it to reject an unusable
 * node with a readable reason. The reason texts live in sub.c. */
int vless_uuid_form(const char *s);

/* The 16 bytes of the id from its text form. 0 on success, -1 if the form is unusable. */
int vless_uuid_parse(const char *s, unsigned char out[16]);

/* ip: 4 or 16 bytes (ip_len), used when host is empty.
 * flow: the flow name (xtls-rprx-vision), or NULL or "" for plain VLESS. */
size_t vless_build_request(const unsigned char uuid[16], enum vless_cmd cmd,
                           const char *host, const unsigned char *ip, int ip_len,
                           uint16_t port, const char *flow,
                           unsigned char *out, size_t cap);

int vless_parse_response(const unsigned char *buf, size_t n, size_t *skip);

/* TrustTunnel (HTTP/2 CONNECT) and SOCKS5 framing helpers. */
size_t tt_basic_auth(const char *userpass, char *out, size_t cap);
size_t tt_build_connect(const char *userpass, const char *authority,
                        const char *user_agent, int end_stream,
                        unsigned char *out, size_t cap);

size_t socks5_build_connect(const char *host, const unsigned char ip4[4],
                            uint16_t port, unsigned char *out, size_t cap);
int socks5_parse_reply(const unsigned char *buf, size_t n, size_t *skip);

/* Trojan (SHA-224 hex password + SOCKS5 address + CRLF) and Shadowsocks address helpers. */
void trojan_sha224_hex(const char *password, char out[57]);
size_t trojan_build_request(const char *password, uint8_t cmd,
                            const char *host, const unsigned char ip4[4],
                            uint16_t port, unsigned char *out, size_t cap);
size_t ss_build_addr(const char *host, const unsigned char ip4[4],
                     uint16_t port, unsigned char *out, size_t cap);

#endif
