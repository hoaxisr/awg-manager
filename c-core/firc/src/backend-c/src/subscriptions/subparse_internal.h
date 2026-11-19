#ifndef FIRC_SUBPARSE_INTERNAL_H
#define FIRC_SUBPARSE_INTERNAL_H
/* What the list parsers share; not a public API. */
#include "firc/subnet.h"
#include "firc/subparse.h"

typedef struct firc_sub_sink firc_sub_sink_t;

/* Stores one rule; piece is trimmed/folded in place; type NULL means guess it; proto/ports NULL means none. */
firc_err_t firc_sub_sink_take(firc_sub_sink_t *s, char *piece, const char *type, const char *proto,
                              const char *ports, bool *took);

char *firc_sub_trim(char *s);

bool firc_sub_keyword_wildcard(const char *kw, char *buf, size_t cap);

firc_err_t firc_sub_typed_line(firc_sub_sink_t *s, char *raw, bool *typed, bool *took);

/* The sing-box walker: body starts with '{'; emits into sink, counts into st. */
firc_err_t firc_sub_parse_singbox(const char *body, firc_sub_sink_t *sink, firc_sub_parse_stats_t *st,
                                  firc_sub_parse_progress_fn progress, void *ud);

firc_err_t firc_sub_parse_srs(const char *body, size_t len, firc_sub_sink_t *sink, firc_sub_parse_stats_t *st,
                              firc_sub_parse_progress_fn progress, void *ud);

typedef struct firc_sub_walk {
    firc_sub_sink_t *sink;
    firc_sub_parse_stats_t *st;
    firc_sub_parse_progress_fn progress;
    void *ud;
    size_t values;
    size_t text;
    size_t text_cap;
} firc_sub_walk_t;

enum { FIRC_SUB_NET_ANY = 0, FIRC_SUB_NET_TCP = 1, FIRC_SUB_NET_UDP = 2 };

#define FIRC_SUB_PORT_ITEMS 64

typedef struct firc_sub_ports {
    char item[FIRC_SUB_PORT_ITEMS][12];
    int cost[FIRC_SUB_PORT_ITEMS];
    size_t n;
} firc_sub_ports_t;

bool firc_sub_ports_add(firc_sub_ports_t *p, long a, long b);
bool firc_sub_ports_add_range(firc_sub_ports_t *p, const char *s);
bool firc_sub_net_add(int *net, const char *s);

typedef struct firc_sub_cidr_plan {
    const char *protos[2];
    size_t n_protos;
    const char *chunks[FIRC_SUB_PORT_ITEMS];
    size_t n_chunks;
    char bufs[FIRC_SUB_PORT_ITEMS][FIRC_PORTS_STR_MAX];
} firc_sub_cidr_plan_t;

void firc_sub_cidr_plan(firc_sub_cidr_plan_t *c, int net, const firc_sub_ports_t *ports);
firc_err_t firc_sub_walk_cidr(firc_sub_walk_t *w, const firc_sub_cidr_plan_t *c, const char *text);
firc_err_t firc_sub_walk_name(firc_sub_walk_t *w, const char *s, const char *type, bool constrained);
firc_err_t firc_sub_walk_drop(firc_sub_walk_t *w);
#endif
