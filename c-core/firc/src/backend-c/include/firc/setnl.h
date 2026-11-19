#ifndef FIRC_SETNL_H
#define FIRC_SETNL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/ipset_to_link.h"

typedef struct firc_setnl firc_setnl_t;

typedef struct {
    bool has_type;
    uint8_t rev4, rev6;
} firc_setnl_caps_t;

typedef void (*firc_setnl_name_fn)(const char *name, void *ud);

/* Opens an ip_set netlink socket. Not thread-safe: one caller at a time. */
firc_err_t firc_setnl_new(firc_setnl_t **out);
/* Test seam: takes ownership of an already-connected socket; NULL (fd closed) on no memory. */
firc_setnl_t *firc_setnl_new_fd(int fd);
void firc_setnl_free(firc_setnl_t *s);

/* Asks for the protocol and the hash:net revisions; FIRC_ERR_NOSYS when the kernel has no ip_set. */
firc_err_t firc_setnl_probe(firc_setnl_t *s, firc_setnl_caps_t *out);
/* Fills scratch with v (family 4|6) and swaps it in as live, or renames it when live is absent.
   On failure scratch is destroyed and live is untouched. */
firc_err_t firc_setnl_replace(firc_setnl_t *s, const char *live, const char *scratch, uint8_t family,
                              const firc_nf_prefix_t *v, size_t n);
/* An absent set is FIRC_OK; a set in use is FIRC_ERR_AGAIN; a NULL or empty name sends nothing. */
firc_err_t firc_setnl_destroy(firc_setnl_t *s, const char *name);
/* Calls fn once per set the kernel holds. */
firc_err_t firc_setnl_names(firc_setnl_t *s, firc_setnl_name_fn fn, void *ud);
/* Destroys every set named prefix* not in keep; a set in use is skipped and the result is FIRC_ERR_AGAIN. */
firc_err_t firc_setnl_sweep(firc_setnl_t *s, const char *prefix, const char *const *keep, size_t n_keep,
                            size_t *destroyed);

#endif
