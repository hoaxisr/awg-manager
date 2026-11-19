#ifndef TUNVLESS_FMLINK_H
#define TUNVLESS_FMLINK_H
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>
#include "fmask.h"

#define FM_Q_MAX 65536
#define FM_Q_HARD (4 * FM_Q_MAX)
#define FM_HC_MAX 65536
#define FM_HC_VARS (4 * FM_HC_MAX)

enum fm_op { FMO_CONCAT = 1, FMO_SLICE, FMO_XOR16, FMO_XOR32, FMO_BE16, FMO_BE32, FMO_LE16, FMO_LE32, FMO_LE64,
             FMO_PAD, FMO_TRUNCATE, FMO_ADD, FMO_SUB, FMO_AND, FMO_OR, FMO_SHL, FMO_SHR };
enum fm_arg_kind { FMA_BYTES = 1, FMA_U64, FMA_VAR, FMA_META, FMA_EXPR };

struct fm_hc_arg { enum fm_arg_kind kind; uint64_t u64; unsigned char *bytes; size_t bytes_n; char *name;
                   struct fm_hc_expr *expr; };
struct fm_hc_expr { enum fm_op op; size_t n; struct fm_hc_arg *a; };

struct fm_hc_var { const char *name; unsigned char *b; size_t n; };
struct fm_hc_env { size_t n, cap; struct fm_hc_var *v; unsigned meta; uint64_t mv[4]; char why[128]; };

typedef int (*fm_emit_fn)(void *arg, const unsigned char *d, size_t n, int64_t gap_ms);

struct fm_link;
struct fm_layer;

struct fm_ops {
    int splice;
    size_t st_size;
    int  (*init)(void *st, const struct fm_mask *m);
    int  (*hs)(struct fm_layer *me, int fd);
    int  (*wr)(struct fm_layer *me, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg);
    long (*rd)(struct fm_layer *me, unsigned char *d, size_t n, size_t cap);
    int  (*pending)(const struct fm_layer *me);
    void (*free)(void *st);
    const char *(*why)(const struct fm_layer *me);
    unsigned (*grow)(const struct fm_mask *m);
};

struct fm_layer {
    const struct fm_ops *ops;
    const struct fm_mask *m;
    void *st;
    struct fm_link *l;
    size_t idx;
};

extern const struct fm_ops fm_frag_ops, fm_hc_ops, fm_sd_ops;

const struct fm_cfg *fm_cfg_get(const char *json);
int     fm_link_open(const char *fm, struct fm_link **out);
struct fm_link *fm_link_new_ops(const struct fm_ops *const *ops, const struct fm_mask *const *m, size_t n);
void    fm_link_free(struct fm_link *l);
int     fm_link_hs(struct fm_link *l, int fd, int timeout_ms);
int     fm_shape(struct fm_link *l, const unsigned char *d, size_t n, fm_emit_fn emit, void *arg);
ssize_t fm_send(struct fm_link *l, int fd, const void *d, size_t n, int direct);
ssize_t fm_recv(struct fm_link *l, int fd, void *d, size_t cap, int flags, int direct);
int     fm_pending_any(const struct fm_link *l);
static inline int fm_pending(const struct fm_link *l) { return l && fm_pending_any(l); }
void    fm_set_loop(struct fm_link *l, int on);
int     fm_kick(struct fm_link *l, int fd, uint64_t now_ms, int *next_ms);
size_t  fm_room(const struct fm_link *l);
unsigned fm_grow(const struct fm_link *l);
const char *fm_last_why(void);
int64_t fm_rand_between(int64_t from, int64_t to);
void    fm_rand_bytes(unsigned char *b, size_t n, uint8_t from, uint8_t to);
int     fm_lower_write(struct fm_layer *me, const unsigned char *d, size_t n, int64_t gap_ms);
int     fm_lower_read_full(struct fm_layer *me, int fd, unsigned char *d, size_t n);
int     fm_lower_sleep(struct fm_layer *me, int64_t ms);
void    fm_sleep_ms(int64_t ms);
const char *fm_hc_op_name(enum fm_op op);
typedef int (*fm_hc_size_fn)(const void *ctx, const char *name, uint64_t *sz);
int     fm_hc_measure_expr(const struct fm_hc_expr *e, fm_hc_size_fn size, const void *ctx, uint64_t *sz, char *why,
                           size_t why_n);
int     fm_hc_measure_item(const struct fm_hc_item *it, fm_hc_size_fn size, const void *ctx, uint64_t *sz, char *why,
                           size_t why_n);
void    fm_hc_env_init(struct fm_hc_env *e, int fd);
void    fm_hc_env_free(struct fm_hc_env *e);
int     fm_hc_eval_item(struct fm_hc_env *e, const struct fm_hc_item *it, unsigned char **out, size_t *n);

#endif
