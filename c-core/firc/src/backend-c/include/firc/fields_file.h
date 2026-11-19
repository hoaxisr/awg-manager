#ifndef FIRC_FIELDS_FILE_H
#define FIRC_FIELDS_FILE_H

#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/id.h"
#include "firc/rtnl.h"

#define FIRC_FIELDS_OWNER_LEN 20

typedef struct {
    char owner[FIRC_FIELDS_OWNER_LEN];
    uint32_t field;
} firc_fields_entry_t;

/* Writes the whole map through <path>.tmp, fsync and rename; a failed save unlinks <path> and returns why. */
firc_err_t firc_fields_file_save(const char *path, const firc_rtnl_field_t *v, size_t n);

/* Reads the map; NOENT when absent, INVAL for anything the writer could not have produced
 * (then *out is NULL and *n 0). Caller frees *out. */
firc_err_t firc_fields_file_load(const char *path, firc_fields_entry_t **out, size_t *n);

#endif /* FIRC_FIELDS_FILE_H */
