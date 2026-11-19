#ifndef FIRC_TUNNODES_H
#define FIRC_TUNNODES_H

#include <pthread.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/tunnels.h"

#define FIRC_TUN_MAX_NODES 256
#define FIRC_TUN_SAID_MAX 120

typedef struct {
    const char *url;
    uint32_t mark;
    const char *body;
    size_t len;
    uint64_t hsig;
} firc_tun_body_t;

typedef struct {
    char key[192];
    char name[101];
    char source[64];
    bool is_new, excluded, over_cap;
    char skip_reason[96];
    size_t link_len;
    char *link;
} firc_tun_node_t;

typedef struct {
    firc_tun_node_t *v;
    size_t n;
    size_t matched, total;
    size_t seen, sinks;
    char sink_name[FIRC_TUN_SAID_MAX + 1];
} firc_tun_nodes_t;

/* Serialises every use of the tunvless parser, whose intern table and insecure flag are global. */
extern pthread_mutex_t firc_tun_parse_mutex;

/* The effective node table of t: ordered rows first, then the rest in source order; a row with a
   link is sent to tunvless. Subscription sources take the body matching (url, mark, headers signature), if any. */
firc_err_t firc_tun_nodes_build(const firc_tunnel_t *t, uint32_t mark, const firc_tun_body_t *bodies,
                                size_t n_bodies, firc_tun_nodes_t *out);

void firc_tun_nodes_free(firc_tun_nodes_t *n);

/* Drops from t's order and exclude every key of a source whose nodes are known (a link, a subscription with its body
   among bodies) and that gives no node of that key, filtered out or not; *dropped is how many keys went. */
firc_err_t firc_tun_nodes_prune(firc_tunnel_t *t, uint32_t mark, const firc_tun_body_t *bodies, size_t n_bodies,
                                size_t *dropped);

/* links[i] is the malloc'd link of the node keyed keys[i] among every node t's sources and bodies give, before
   filter, exclude and the node cap, NULL when no such node can be sent; one parse for all keys. On an error every
   links[i] is NULL. */
firc_err_t firc_tun_nodes_links_of(const firc_tunnel_t *t, uint32_t mark, const firc_tun_body_t *bodies,
                                   size_t n_bodies, const char *const *keys, size_t n_keys, char **links);

size_t firc_tun_nodes_parses_for_test(void);        /* test seam: how many times sources were parsed */
void firc_tun_nodes_set_fail_for_test(bool fail); /* test seam: links_of fails as out of memory */

#endif
