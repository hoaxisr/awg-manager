#ifndef TUNVLESS_HY2WIRE_H
#define TUNVLESS_HY2WIRE_H
#include <stddef.h>
#include <stdint.h>

#define HY2_FRAME_TCP 0x401
#define HY2_STATUS_OK 233
#define HY2_UDP_FRAG_MAX 32
#define HY2_AUTH_RESP_MAX 16384

size_t hy2_varint_put(uint8_t *out, size_t cap, uint64_t v);
size_t hy2_varint_get(const uint8_t *d, size_t n, uint64_t *v);

size_t hy2_h3_control(uint8_t *out, size_t cap);

struct hy2_field { char name[64]; char value[2304]; };

int hy2_qpack_start(const uint8_t **p, const uint8_t *end);
int hy2_qpack_next(const uint8_t **p, const uint8_t *end, struct hy2_field *f);

size_t hy2_auth_request(uint8_t *out, size_t cap, const char *auth, uint64_t rx_bps, const char *pad, size_t pad_n);

struct hy2_auth_resp { int status; uint64_t rx_bps; int udp; };
int hy2_auth_response(const uint8_t *d, size_t n, struct hy2_auth_resp *r);

size_t hy2_tcp_request(uint8_t *out, size_t cap, const char *host, uint16_t port, const char *pad, size_t pad_n);
int hy2_tcp_request_parse(const uint8_t *d, size_t n, char *addr, size_t addr_cap, size_t *used);

struct hy2_tcp_resp { int ok; char msg[128]; size_t used; };
int hy2_tcp_response(const uint8_t *d, size_t n, struct hy2_tcp_resp *r);
size_t hy2_tcp_response_put(uint8_t *out, size_t cap, int ok, const char *msg, size_t pad_n);

struct hy2_udp_msg { uint32_t sid; uint16_t pkt; uint8_t frag, count; const char *addr; size_t addr_n; const uint8_t *data; size_t data_n; };
size_t hy2_udp_header(uint8_t *out, size_t cap, uint32_t sid, uint16_t pkt, uint8_t frag, uint8_t count, const char *host, uint16_t port);
int hy2_udp_parse(const uint8_t *d, size_t n, struct hy2_udp_msg *m);
unsigned hy2_udp_frags(size_t data_n, size_t header_n, size_t dgram_max);

struct hy2_rasm { uint16_t pkt; uint8_t count; uint32_t have; uint16_t len[32]; uint8_t *buf; size_t piece; };
const uint8_t *hy2_rasm_feed(struct hy2_rasm *r, const struct hy2_udp_msg *m, size_t *out_n);
void hy2_rasm_free(struct hy2_rasm *r);

void hy2_pad_fill(char *out, size_t n, const uint8_t *rnd);
size_t hy2_pad_len(uint16_t rnd, unsigned lo, unsigned hi);

struct hy2_salamander { uint8_t psk[256]; size_t psk_n; };
int hy2_salamander_init(struct hy2_salamander *s, const char *password);
size_t hy2_salamander_wrap(const struct hy2_salamander *s, const uint8_t salt[8], uint8_t *dst, const uint8_t *src, size_t n);
size_t hy2_salamander_unwrap(const struct hy2_salamander *s, uint8_t *dst, const uint8_t *src, size_t n);

#endif
