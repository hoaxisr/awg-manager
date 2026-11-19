#ifndef FIRC_PURGE_H
#define FIRC_PURGE_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"
#include "firc/iptables.h"
#include "firc/opkgtun.h"
#include "firc/rtnl.h"
#include "firc/setnl.h"

typedef struct {
    const char *pool_file;
    const char *fields_file;
    const char *sock;
    const char *lock;
    const char *run_dir;
} firc_purge_paths_t;

typedef struct {
    bool iptables_ok;
    bool routes_ok;
    size_t rules_removed;
    size_t routes_removed;
    size_t sets;
    bool run_dir_removed;
} firc_purge_report_t;

/* `fircd --purge`: runs every step even after a failure, returns the first error. Args nullable. */
firc_err_t firc_purge(firc_ipt_t *ipt4, firc_ipt_t *ipt6, firc_rtnl_t *rtnl, firc_setnl_t *sets,
                      const char *chain_prefix, const firc_purge_paths_t *paths, firc_purge_report_t *report);

typedef struct {
    firc_err_t (*lock)(const char *path, int *fd);
    const char *path;
} firc_purge_lock_t;

/* The router step of `fircd --purge`: one sweep that keeps no record, a save only when it removed one or the intent
   file was dirty, then revoke(ud). Failures are warned and returned; io and revoke nullable. */
firc_err_t firc_purge_router(const firc_opkgtun_io_t *io, firc_err_t (*revoke)(void *), void *ud);

/* Takes the instance lock, then runs the router step; 1 when another fircd holds the lock, which touches nothing.
   Another lock error skips the sweep and still revokes; *lock_fd is -1 unless the lock was taken. */
int firc_purge_begin(const firc_purge_lock_t *lock, const firc_opkgtun_io_t *io, firc_err_t (*revoke)(void *),
                     void *ud, int *lock_fd);

/* Destroys every set named chain_prefix*, saying what it did; a set in use is FIRC_ERR_AGAIN. NULL sets: FIRC_OK. */
firc_err_t firc_purge_sets(firc_setnl_t *sets, const char *chain_prefix, size_t *destroyed);

#endif
