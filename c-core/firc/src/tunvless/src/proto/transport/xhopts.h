#ifndef TUNVLESS_XHOPTS_H
#define TUNVLESS_XHOPTS_H
#include <stddef.h>
#include <stdint.h>

#define XH_PATH_MAX 8192

enum xh_place { XHP_PATH, XHP_QUERY, XHP_HEADER, XHP_COOKIE, XHP_BODY, XHP_AUTO, XHP_QUERY_IN_HEADER };

struct xm_range { int32_t from, to; };
struct xm_cfg { struct xm_range max_conc, max_conns, c_reuse, h_reqs, h_secs; int64_t keepalive_s; uint8_t set; };

struct xh_opts {
    enum xh_place sess_place, seq_place, data_place, pad_place;
    char sess_key[64], seq_key[64], data_key[64], pad_key[64], pad_header[64];
    char table[128];
    uint16_t id_len_from, id_len_to;
    uint32_t chunk_from, chunk_to;
    char method[16];
    uint8_t obfs, tokenish, no_grpc;
    uint32_t posts, max_hdr;
    struct xm_cfg xmux;
    struct xm_range interval;
};

int xh_opts_parse(const char *xhx, const char *mode, uint16_t pad_to, struct xh_opts *o, char *why,
                  size_t why_n);

void xm_cfg_effective(const struct xm_cfg *in, struct xm_cfg *out);

#endif
