#ifndef FIRC_KEENETIC_RCI_H
#define FIRC_KEENETIC_RCI_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"

#define FIRC_KN_RCI_BASE_URL "http://127.0.0.1:79"

#define FIRC_KN_RCI_TIMEOUT_SECONDS 2L

#define FIRC_KN_RCI_SERIAL_TIMEOUT_SECONDS 10L

#define FIRC_KN_RCI_MAX_BODY_BYTES (1024 * 1024)

typedef struct firc_kn_alias {
    char system_name[16]; /* IFNAMSIZ */
    char description[64];
    char ndms_name[64];
    bool inet;
} firc_kn_alias_t;

typedef struct firc_kn_aliases {
    firc_kn_alias_t *items;
    size_t n;
} firc_kn_aliases_t;

/* Fills *out (free with firc_kn_aliases_free); empty is success. FIRC_ERR_UPSTREAM/PROTO/NOMEM on failure. */
firc_err_t firc_kn_get_iface_aliases(firc_kn_aliases_t *out);

/* Same against base_url (no trailing slash needed); does RCI calls in every build, for stub servers in tests. */
firc_err_t firc_kn_get_iface_aliases_from(const char *base_url, firc_kn_aliases_t *out);

/* One RCI GET of path under base_url; *out_body is NUL-terminated, caller frees. */
firc_err_t firc_kn_rci_get(const char *base_url, const char *path, char **out_body);

/* The same with a timeout of its own; stop (NULL for none) ends it within about a second, FIRC_ERR_CANCELED. */
firc_err_t firc_kn_rci_get_timeout(const char *base_url, const char *path, long timeout_s, const atomic_bool *stop,
                                   char **out_body);

/* One RCI POST of body (JSON) to path under base_url; *out_body is NUL-terminated, caller frees. */
firc_err_t firc_kn_rci_post(const char *base_url, const char *path, const char *body, char **out_body);

/* The POST with a timeout of its own; stop (NULL for none) ends it within about a second, FIRC_ERR_CANCELED. */
firc_err_t firc_kn_rci_post_timeout(const char *base_url, const char *path, const char *body, long timeout_s,
                                    const atomic_bool *stop, char **out_body);

/* The same, also ended by cut (NULL for none) once the transfer runs; the token wait ends on stop only. */
firc_err_t firc_kn_rci_post_cut(const char *base_url, const char *path, const char *body, long timeout_s,
                                const atomic_bool *stop, const atomic_bool *cut, char **out_body);

/* NDMS description for system_name, or NULL; a NULL or empty set is fine. */
const char *firc_kn_aliases_lookup(const firc_kn_aliases_t *aliases, const char *system_name);

/* True when NDMS lists an interface with this kernel name. */
bool firc_kn_aliases_known(const firc_kn_aliases_t *aliases, const char *system_name);

/* NDMS interface-name for system_name, or NULL; a NULL or empty set is fine. */
const char *firc_kn_aliases_ndms_name(const firc_kn_aliases_t *aliases, const char *system_name);

/* True when an entry for system_name carries the router's inet role. */
bool firc_kn_aliases_inet(const firc_kn_aliases_t *aliases, const char *system_name);

/* Frees *aliases and zeroes it; safe on a zeroed struct. */
void firc_kn_aliases_free(firc_kn_aliases_t *aliases);

typedef struct firc_kn_iface_meta {
    char id[64];
    char description[64];
    char interface_name[64];
    char system_name[16];
    bool inet;
} firc_kn_iface_meta_t;

/* Parses GET /rci/show/interface, skipping non-object members; inet from its own role or a "for" role. Caller frees *out. */
firc_err_t firc_kn_parse_interface_list(const char *json, firc_kn_iface_meta_t **out, size_t *out_n);

/* Builds the POST /rci/ batch body; caller frees; NULL on allocation failure. */
char *firc_kn_build_system_name_request(const firc_kn_iface_meta_t *metas, size_t n);

/* Fills metas[i].system_name by position (RCI echoes no id); extra or missing elements are tolerated. */
firc_err_t firc_kn_parse_system_names(const char *json, firc_kn_iface_meta_t *metas, size_t n);

/* One entry per interface with a kernel name; description and interface-name kept apart, each emptied when equal to the kernel name. Free *out with firc_kn_aliases_free(). */
firc_err_t firc_kn_build_aliases(const firc_kn_iface_meta_t *metas, size_t n, firc_kn_aliases_t *out);

/* RCI interface id to kernel name ("Wireguard0" to "nwg0"). */
typedef struct firc_kn_id_entry {
    char id[64];
    char system_name[16];
} firc_kn_id_entry_t;

typedef struct firc_kn_id_map {
    firc_kn_id_entry_t *items;
    size_t n;
} firc_kn_id_map_t;

/* Skips interfaces without a kernel name; free with firc_kn_id_map_free. */
firc_err_t firc_kn_build_id_map(const firc_kn_iface_meta_t *metas, size_t n, firc_kn_id_map_t *out);
firc_err_t firc_kn_get_id_map_from(const char *base_url, firc_kn_id_map_t *out);
/* The kernel name for rci_id, or NULL. */
const char *firc_kn_id_map_lookup(const firc_kn_id_map_t *m, const char *rci_id);
void firc_kn_id_map_free(firc_kn_id_map_t *m);

#endif
