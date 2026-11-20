#ifndef FIRC_FAKEIP_H
#define FIRC_FAKEIP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

#include "firc/logrep.h"
#include "firc/err.h"
#include "firc/fakeip_addr.h"

#define FIRC_FAKEIP_DEFAULT_MAX_NAMES 65536

#define FIRC_FAM_V4 0u
#define FIRC_FAM_V6 1u

typedef struct firc_fakeip firc_fakeip_t;

typedef struct firc_fakeip_family_cfg {
    firc_ip_t base;
    uint8_t pool_cidr;
    uint8_t chunk_cidr;
} firc_fakeip_family_cfg_t;

/* Validates a family's pool geometry (alignment, chunk_cidr > pool_cidr, chunks < 2^32); FIRC_ERR_INVAL if bad. */
firc_err_t firc_fakeip_check_geometry(const firc_ip_t *base, uint8_t pool_cidr,
                                      uint8_t chunk_cidr, uint8_t addr_len);

#define FIRC_FAKEIP_MAX_WINDOW_SECS (INT64_C(365) * 24 * 3600)

/* Validates idle_secs/clamp_secs are both in (0, FIRC_FAKEIP_MAX_WINDOW_SECS]. */
firc_err_t firc_fakeip_check_windows(int64_t idle_secs, int64_t clamp_secs);


#define FIRC_FAKEIP_V6_GEN_CIDR 48

typedef struct firc_fakeip_cfg {
    firc_fakeip_family_cfg_t v4; /* base.len must be 4 */
    firc_fakeip_family_cfg_t v6; /* base.len must be 16 */

    size_t max_names; /* 0 selects FIRC_FAKEIP_DEFAULT_MAX_NAMES */

    int64_t idle_secs; /* seconds unresolved before a name's addresses are released */

    int64_t clamp_secs; /* TTL clamp seconds; must be > 0 */
} firc_fakeip_cfg_t;

/* On refusal *out is set to NULL. */
firc_err_t firc_fakeip_new(const firc_fakeip_cfg_t *cfg, firc_fakeip_t **out);
void firc_fakeip_free(firc_fakeip_t *f);

/* Allocates/returns qname's address pair in group_id (one mapping per name and group); LIMIT/NOMEM still write the blackhole address, INVAL none. */
firc_err_t firc_fakeip_get(firc_fakeip_t *f, const char *qname, const char *group_id, int64_t now,
                           firc_ip_t *v4_out, firc_ip_t *v6_out);

/* True when prefix_len bits of addr overlap that family's pool prefix. */
bool firc_fakeip_overlaps(const firc_fakeip_t *f, const firc_ip_t *addr, uint8_t prefix_len);

/* The prefix that family's addresses are issued from; false for an unknown family or a NULL argument. */
bool firc_fakeip_pool_prefix(const firc_fakeip_t *f, unsigned family, firc_ip_t *base_out,
                             uint8_t *prefix_out);

/* Records the real address behind qname's mapping in group_id (DNAT side); FIRC_ERR_NOENT if there is none. */
firc_err_t firc_fakeip_set_real(firc_fakeip_t *f, const char *qname, const char *group_id, const firc_ip_t *real);

/* Same, from a whole answer; per family keeps the stored address while still offered, else the first offered. */
firc_err_t firc_fakeip_set_reals(firc_fakeip_t *f, const char *qname, const char *group_id,
                                 const firc_ip_t *reals, size_t n);

/* True when the mapping holding fake has an uncommitted real address in fake's family. */
bool firc_fakeip_needs_commit(const firc_fakeip_t *f, const firc_ip_t *fake);

/* Drops the real of the family len (4 or 16) of qname's mapping in group_id and advances that pair's generation; unknown mapping: no-op. */
void firc_fakeip_clear_real(firc_fakeip_t *f, const char *qname, const char *group_id, int len);

/* Pool generation, advanced on every kernel-relevant change; compare before/after a call. */
uint64_t firc_fakeip_gen(const firc_fakeip_t *f);

/* Marks generation gen committed; never moves backwards. Loop thread only (firc_loop_post). */
void firc_fakeip_mark_committed(firc_fakeip_t *f, uint64_t gen);

/* Marks group_id's resolved pairs as needing commit; returns the count marked. Loop thread. */
size_t firc_fakeip_uncommit_group(firc_fakeip_t *f, const char *group_id);

const char *firc_fakeip_name_of(const firc_fakeip_t *f, const firc_ip_t *addr);

/* The group and real (its v4, else its v6) of a live or parked mapping at fake; false when none or no real yet. */
bool firc_fakeip_pair_of(const firc_fakeip_t *f, const firc_ip_t *fake, const char **group_id, firc_ip_t *real);

/* Called once per (name, family) with a fake address; real is NULL if none recorded yet. */
typedef void (*firc_fakeip_walk_fn)(void *ud, const char *qname, const char *group_id,
                                    const firc_ip_t *fake, const firc_ip_t *real);
void firc_fakeip_walk(const firc_fakeip_t *f, firc_fakeip_walk_fn fn, void *ud);

/* Every chunk the group (every group if NULL) holds, minus the blackhole chunk; fn must not call the pool. */
typedef void (*firc_fakeip_chunk_fn)(void *ud, const char *group_id, unsigned family,
                                     const firc_ip_t *base, uint8_t prefix);
void firc_fakeip_walk_chunks(const firc_fakeip_t *f, const char *group_id,
                             firc_fakeip_chunk_fn fn, void *ud);

/* An immutable copy of what netfilter needs; the committer thread must never walk the live pool. */
typedef struct firc_fakeip_snapshot firc_fakeip_snapshot_t;

/* Like firc_fakeip_walk, but omits the queried name (netfilter keys only on addresses and groups). */
typedef void (*firc_fakeip_snap_map_fn)(void *ud, const char *group_id, unsigned family,
                                        const firc_ip_t *fake, const firc_ip_t *real);

/* NULL on OOM, or for a NULL pool. Call from the thread that owns the pool. */
firc_fakeip_snapshot_t *firc_fakeip_snapshot_take(const firc_fakeip_t *f);
void firc_fakeip_snapshot_free(firc_fakeip_snapshot_t *s);

/* Same queries as the live pool, against the copy; safe from any thread. */
void firc_fakeip_snapshot_walk(const firc_fakeip_snapshot_t *s, firc_fakeip_snap_map_fn fn,
                               void *ud);
/* One call per mapping with a v4 fake, no v4 real and a v6 real. */
typedef void (*firc_fakeip_cross_fn)(void *ud, const char *group_id, const firc_ip_t *fake4,
                                     const firc_ip_t *real6);
void firc_fakeip_snapshot_cross_walk(const firc_fakeip_snapshot_t *s, firc_fakeip_cross_fn fn, void *ud);
void firc_fakeip_snapshot_walk_chunks(const firc_fakeip_snapshot_t *s, const char *group_id,
                                      firc_fakeip_chunk_fn fn, void *ud);
bool firc_fakeip_snapshot_pool_prefix(const firc_fakeip_snapshot_t *s, unsigned family,
                                      firc_ip_t *base_out, uint8_t *prefix_out);

/* Generation this snapshot was taken at; hand to firc_fakeip_mark_committed once its pass completes. */
uint64_t firc_fakeip_snapshot_gen(const firc_fakeip_snapshot_t *s);

/* Releases idle names (>= idle_secs) and expired quarantine; generation advances on any change. */
void firc_fakeip_reclaim(firc_fakeip_t *f, int64_t now);

/* Drops group_id's mappings; its chunks return to the pool quarantined. No-op for an unknown group. */
void firc_fakeip_drop_group(firc_fakeip_t *f, const char *group_id, int64_t now);

/* Writes every mapping as text; `clean` marks the file exact (nothing more will be issued after it). */
firc_err_t firc_fakeip_save(const firc_fakeip_t *f, FILE *out, bool clean);
/* Reads into an EMPTY pool of matching geometry (refuses otherwise); *restored (nullable) counts mappings taken. */
firc_err_t firc_fakeip_load(firc_fakeip_t *f, FILE *in, int64_t now, size_t *restored);
/* File variants: atomic write; FIRC_ERR_NOENT if the file is missing on load. */
firc_err_t firc_fakeip_save_file(const firc_fakeip_t *f, const char *path, bool clean);
firc_err_t firc_fakeip_load_file(firc_fakeip_t *f, const char *path, int64_t now, size_t *restored);
/* firc_fakeip_save_file that says a failure at ERROR through said, its repeats per firc_logrep, and the next good save. */
firc_err_t firc_fakeip_save_file_said(const firc_fakeip_t *f, const char *path, bool clean, firc_logrep_t *said);
/* fn fires synchronously after the pool takes a new chunk, so a crash before the next save can't reissue it. */
typedef void (*firc_fakeip_chunk_hook)(void *ud, const firc_fakeip_t *f);
void firc_fakeip_set_on_chunk(firc_fakeip_t *f, firc_fakeip_chunk_hook fn, void *ud);
/* Treats the pool as loaded from a not-exact file: parks every held v4 chunk, advances v6 marks. */
void firc_fakeip_distrust(firc_fakeip_t *f, int64_t now);
/* Whether anything changed since the last save (for the periodic writer). */
bool firc_fakeip_dirty_since_save(const firc_fakeip_t *f);

#endif /* FIRC_FAKEIP_H */
