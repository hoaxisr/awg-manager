#ifndef TUNVLESS_FMASK_H
#define TUNVLESS_FMASK_H
#include <stddef.h>
#include <stdint.h>

#define FM_JSON_MAX 2048

enum fm_kind { FM_FILTER = 1, FM_FRAGMENT, FM_HEADER, FM_SUDOKU, FM_XMC };

struct fm_rng { int64_t from, to; };
struct fm_frag { int64_t pk_from, pk_to; size_t n_len, n_delay; struct fm_rng *len, *delay; struct fm_rng split; };
struct fm_hc_expr;
struct fm_hc_item { struct fm_rng delay; int32_t rand; uint8_t rmin, rmax; unsigned char *packet; size_t packet_n;
                    char *capture, *reuse; struct fm_hc_expr *expr; };
struct fm_hc_seq { size_t n; struct fm_hc_item *it; };
struct fm_hc { size_t n_cl, n_sv, n_er; struct fm_hc_seq *cl, *sv, *er; };
struct fm_sd { unsigned char *pw; size_t pw_n; uint8_t ascii; size_t n_pat; char (*pat)[9]; uint32_t pmin, pmax; };
struct fm_mask { enum fm_kind kind; union { struct fm_frag frag; struct fm_hc hc; struct fm_sd sd; } u; };
struct fm_cfg { size_t n; struct fm_mask *m; };

size_t fm_compact(const char *json, size_t n, char *out, size_t cap);
int fm_parse(const char *json, size_t n, struct fm_cfg *out, char *why, size_t why_n);
void fm_cfg_free(struct fm_cfg *c);

#endif
