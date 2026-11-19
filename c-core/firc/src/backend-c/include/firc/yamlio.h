#ifndef FIRC_YAMLIO_H
#define FIRC_YAMLIO_H

#include <stddef.h>

#include "firc/err.h"
#include "firc/models.h"

/* Parses YAML into cfg, which must already hold defaults; on error cfg may be partially modified. */
firc_err_t firc_config_load_buffer(firc_config_t *cfg, const char *buf, size_t len);

/* Reads and parses a whole file; a missing file is FIRC_ERR_NOENT (the caller decides what that means). */
firc_err_t firc_config_load_file(firc_config_t *cfg, const char *path);

/* Serializes the full tree with version; returns a malloc'd buffer. */
firc_err_t firc_config_save_buffer(const firc_config_t *cfg, const char *version,
                               char **out, size_t *out_len);

/* Atomic write to path (0600). */
firc_err_t firc_config_save_file(const firc_config_t *cfg, const char *version,
                             const char *path);

/* Which half of the tree to write: settings (app) or groups; every key belongs to exactly one half. */
typedef enum {
    FIRC_CFG_SETTINGS = 1, /* app */
    FIRC_CFG_GROUPS = 2,   /* groups, each with its list */
} firc_cfg_part_t;

firc_err_t firc_config_save_buffer_part(const firc_config_t *cfg, const char *version,
                                        unsigned parts, char **out, size_t *out_len);
firc_err_t firc_config_save_part_file(const firc_config_t *cfg, const char *version,
                                      unsigned parts, const char *path);

/* The sibling groups.yaml for a settings file (same directory); FIRC_ERR_INVAL if it doesn't fit cap. */
firc_err_t firc_config_groups_path(const char *conf_path, char *out, size_t cap);

#endif /* FIRC_YAMLIO_H */
