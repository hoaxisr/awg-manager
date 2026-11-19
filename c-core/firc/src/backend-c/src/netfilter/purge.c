#include "firc/purge.h"

#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include "firc/log.h"
#include "firc/netfilter_cleaner.h"

static bool unlink_quiet(const char *path, firc_err_t *first) {
    if (path == NULL || unlink(path) == 0 || errno == ENOENT) { return true; }
    FIRC_WARN("purge: %s not removed: %s", path, strerror(errno));
    if (*first == FIRC_OK) { *first = firc_err_from_errno(errno); }
    return false;
}

firc_err_t firc_purge_router(const firc_opkgtun_io_t *io, firc_err_t (*revoke)(void *), void *ud) {
    firc_err_t err = FIRC_OK;
    if (io != NULL) {
        size_t removed = 0;
        bool unsaved = false;
        err = firc_opkgtun_sweep(io, NULL, 0, &removed, &unsaved);
        if (unsaved) {
            FIRC_WARN("purge: firc's OpkgTun records were removed but not saved; they come back after a reboot with "
                      "no firc to own them; save the router's configuration, or delete them again after the reboot");
        }
        if (err != FIRC_OK && err != FIRC_ERR_STATE) {
            FIRC_WARN("purge: firc's OpkgTun records may remain; delete OpkgTun<N> in the router (%s)",
                      firc_err_str(err));
        }
    }
    if (revoke != NULL) { (void)revoke(ud); }
    return err;
}

int firc_purge_begin(const firc_purge_lock_t *lock, const firc_opkgtun_io_t *io, firc_err_t (*revoke)(void *),
                     void *ud, int *lock_fd) {
    *lock_fd = -1;
    firc_err_t err = lock->lock(lock->path, lock_fd);
    if (err == FIRC_ERR_EXIST) {
        *lock_fd = -1;
        FIRC_ERROR("another fircd holds %s: is the service already running?", lock->path);
        return 1;
    }
    if (err != FIRC_OK) {
        *lock_fd = -1;
        if (io != NULL) {
            FIRC_WARN("purge could not take the instance lock (%s); firc's OpkgTun records are left; delete "
                      "OpkgTun<N> in the router", firc_err_str(err));
        } else {
            FIRC_WARN("purge could not take the instance lock (%s)", firc_err_str(err));
        }
        (void)firc_purge_router(NULL, revoke, ud);
        return 0;
    }
    (void)firc_purge_router(io, revoke, ud);
    return 0;
}

firc_err_t firc_purge_sets(firc_setnl_t *sets, const char *chain_prefix, size_t *destroyed) {
    size_t n = 0;
    if (destroyed != NULL) { *destroyed = 0; }
    if (sets == NULL || firc_setnl_probe(sets, NULL) == FIRC_ERR_NOSYS) { return FIRC_OK; }
    firc_err_t err = firc_setnl_sweep(sets, chain_prefix, NULL, 0, &n);
    if (destroyed != NULL) { *destroyed = n; }
    if (err == FIRC_ERR_AGAIN) {
        FIRC_WARN("%zu set(s) of ours removed; a %s* set in use by a rule was left in place", n, chain_prefix);
    } else if (err != FIRC_OK) {
        FIRC_WARN("the %s* sets were not removed: %s", chain_prefix, firc_err_str(err));
    } else if (n > 0) {
        FIRC_INFO("%zu set(s) of ours removed", n);
    }
    return err;
}

firc_err_t firc_purge(firc_ipt_t *ipt4, firc_ipt_t *ipt6, firc_rtnl_t *rtnl, firc_setnl_t *sets,
                      const char *chain_prefix, const firc_purge_paths_t *paths, firc_purge_report_t *report) {
    firc_purge_report_t rep;
    memset(&rep, 0, sizeof(rep));
    firc_err_t first = FIRC_OK;

    firc_err_t err = firc_netfilter_clean_retrying(ipt4, ipt6, NULL);
    rep.iptables_ok = err == FIRC_OK;
    if (err != FIRC_OK) {
        FIRC_WARN("purge: the netfilter chains (the FORWARD barrier among them) were not removed: %s",
                  firc_err_str(err));
        first = err;
    } else {
        FIRC_INFO("purge: every %s* chain removed, the FORWARD barrier included", chain_prefix);
    }

    err = firc_purge_sets(sets, chain_prefix, &rep.sets);
    if (err != FIRC_OK && first == FIRC_OK) { first = err; }

    if (rtnl != NULL) {
        err = firc_rtnl_clean_stale_rules(rtnl, &rep.rules_removed);
        if (err != FIRC_OK) {
            FIRC_WARN("purge: ip rules not swept: %s", firc_err_str(err));
            if (first == FIRC_OK) { first = err; }
        }
        size_t left = 0;
        err = firc_rtnl_purge_tagged_routes(rtnl, &rep.routes_removed, &left);
        rep.routes_ok = err == FIRC_OK && left == 0;
        if (err != FIRC_OK) {
            FIRC_WARN("purge: our routes were not all removed: %s", firc_err_str(err));
            if (first == FIRC_OK) { first = err; }
        } else if (left != 0) {
            FIRC_WARN("purge: %zu route(s) of ours still in the kernel after the purge", left);
            if (first == FIRC_OK) { first = FIRC_ERR_STATE; }
        }
        FIRC_INFO("purge: %zu ip rule(s) and %zu route(s) of ours removed", rep.rules_removed, rep.routes_removed);
    } else {
        FIRC_WARN("purge: no netlink: ip rules and the pool's routes were left in place");
        if (first == FIRC_OK) { first = FIRC_ERR_STATE; }
    }

    if (paths != NULL) {
        char tmp[512];
        unlink_quiet(paths->pool_file, &first);
        if (paths->pool_file != NULL && snprintf(tmp, sizeof(tmp), "%s.tmp", paths->pool_file) < (int)sizeof(tmp)) {
            unlink_quiet(tmp, &first);
        }
        unlink_quiet(paths->fields_file, &first);
        if (paths->fields_file != NULL &&
            snprintf(tmp, sizeof(tmp), "%s.tmp", paths->fields_file) < (int)sizeof(tmp)) {
            unlink_quiet(tmp, &first);
        }
        unlink_quiet(paths->sock, &first);
        unlink_quiet(paths->lock, &first);
        if (paths->run_dir != NULL) {
            static const char *const k_dumps[] = {
                "refused-ipv4-filter.bin", "refused-ipv4-mangle.bin", "refused-ipv4-nat.bin",
                "refused-ipv6-filter.bin", "refused-ipv6-mangle.bin", "refused-ipv6-nat.bin"};
            for (size_t i = 0; i < sizeof(k_dumps) / sizeof(k_dumps[0]); i++) {
                char dump[512];
                if (snprintf(dump, sizeof(dump), "%s/%s", paths->run_dir, k_dumps[i]) < (int)sizeof(dump)) {
                    unlink_quiet(dump, &first);
                }
            }
            rep.run_dir_removed = rmdir(paths->run_dir) == 0;
            if (!rep.run_dir_removed) {
                if (errno == ENOENT) {
                    rep.run_dir_removed = true;
                } else {
                    FIRC_WARN("purge: %s not removed: %s", paths->run_dir, strerror(errno));
                    if (first == FIRC_OK) { first = firc_err_from_errno(errno); }
                }
            }
        }
    }
    if (report != NULL) { *report = rep; }
    return first;
}
