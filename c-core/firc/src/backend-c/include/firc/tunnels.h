#ifndef FIRC_TUNNELS_H
#define FIRC_TUNNELS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/ktcp.h"
#include "firc/subhdr.h"

#define FIRC_TUN_MAX 16
#define FIRC_TUN_MAX_ACTIVE 8
#define FIRC_TUN_MAX_SOURCES 256
#define FIRC_TUN_MAX_KEYS 1024
#define FIRC_TUN_KEY_MAX 191
#define FIRC_TUN_DESCRIPTION_MAX 63
#define FIRC_TUN_MAX_INBOUNDS 16
#define FIRC_TUNNEL_TCP_PORT_BASE 3600

typedef enum { FIRC_UPLINK_AUTO, FIRC_UPLINK_IFACE, FIRC_UPLINK_TUNNEL } firc_uplink_kind_t;

typedef struct {
    char name[64];
    char *url;
    int interval_s;
    firc_subhdr_pair_t *headers;
    size_t n_headers;
} firc_tun_sub_t;

typedef enum { FIRC_TUN_SRC_LINK, FIRC_TUN_SRC_SUB } firc_tun_src_kind_t;

typedef struct {
    char id[9];
    firc_tun_src_kind_t kind;
    char *link;
    firc_tun_sub_t sub;
} firc_tun_src_t;

#define FIRC_TUN_NODE_POOL "*"

typedef struct {
    uint16_t port;
    char listen[16];
    char node[FIRC_TUN_KEY_MAX + 1];
    char user[256];
    char password[256];
} firc_tun_inbound_t;

typedef struct {
    char id[16];
    char device[16];
    char description[FIRC_TUN_DESCRIPTION_MAX + 1];
    bool enable;
    bool router_policies;
    firc_uplink_kind_t uplink;
    char uplink_ref[16];
    firc_tun_src_t *src;
    size_t n_src;
    char *filter;
    char **order;
    size_t n_order;
    char **exclude;
    size_t n_exclude;
    int active;
    char by[16];
    int interval_s, silence_s, timeout_s;
    bool insecure;
    bool kernel_tcp;
    char ca[256];
    char fingerprint[8];
    firc_tun_inbound_t inb[FIRC_TUN_MAX_INBOUNDS];
    size_t n_inb;
} firc_tunnel_t;

typedef struct {
    firc_tunnel_t *t;
    size_t n;
    bool file_known;
    uint8_t file_sha[32];
} firc_tunnels_t;

typedef struct {
    char where[96];
    char why[160];
} firc_tun_err_t;

/* Parses and validates a tunnels.yaml document; on failure err names the place and the reason. */
firc_err_t firc_tunnels_load_buffer(firc_tunnels_t *out, const char *buf, size_t len,
                                    firc_tun_err_t *err);

/* Like load_buffer from a file; a missing file is FIRC_OK with no tunnels. Records the sha256 of what it read. */
firc_err_t firc_tunnels_load_file(firc_tunnels_t *out, const char *path, firc_tun_err_t *err);

/* Writes the file atomically with mode 0600 (links are secrets); records the sha256 of what it wrote. */
firc_err_t firc_tunnels_save_file(firc_tunnels_t *in, const char *path);

/* True when path holds exactly what t was last loaded from or saved as; false when that is not known. */
bool firc_tunnels_file_unchanged(const firc_tunnels_t *t, const char *path);

void firc_tunnels_free(firc_tunnels_t *t);

/* True when tunvless for a would be started with the same options as for b; sources not compared. */
bool firc_tunnel_same_argv(const firc_tunnel_t *a, const firc_tunnel_t *b);

/* N of t's device, as firc_tundev_name_index reads it; -1 for a device that is not one. */
int firc_tunnel_index(const firc_tunnel_t *t);

/* Fills names[N] with the device of ts's tunnel N and "" for every N no tunnel has; NULL ts names none. */
void firc_tunnels_names(const firc_tunnels_t *ts, char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP]);

/* The tunnel of ts whose device is exactly dev; NULL when none is, or ts or dev is NULL. */
const firc_tunnel_t *firc_tunnels_by_device(const firc_tunnels_t *ts, const char *dev);

/* FIRC_TUNNEL_TCP_PORT_BASE plus the device number of t when t is enabled with kernelTcp, else 0. */
uint16_t firc_tunnel_tcp_port(const firc_tunnel_t *t);

/* True when a and b give tunvless the same nodes: source kinds, links and URLs in order. */
bool firc_tunnel_same_nodes(const firc_tunnel_t *a, const firc_tunnel_t *b);

/* 0 for a subscription without headers of its own, else a non-zero hash of its name/value pairs in order. */
uint64_t firc_tun_sub_headers_sig(const firc_tun_sub_t *sub);

#endif
