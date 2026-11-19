/* scalar resolution, shared by the loader (typing) and the saver (quoting decisions) */
#ifndef FIRC_YAML_SCALAR_H
#define FIRC_YAML_SCALAR_H

#include <stdbool.h>
#include <stdint.h>

typedef enum firc_scalar_kind {
    FIRC_SCALAR_STR = 0,
    FIRC_SCALAR_NULL,
    FIRC_SCALAR_BOOL,
    FIRC_SCALAR_INT,   /* fits int64 or uint64 */
    FIRC_SCALAR_FLOAT,
    FIRC_SCALAR_TIMESTAMP,
    FIRC_SCALAR_MERGE, /* "<<" */
} firc_scalar_kind_t;

typedef struct firc_scalar_value {
    firc_scalar_kind_t kind;
    bool b;          /* BOOL */
    bool is_uint;    /* INT: value only fits uint64 */
    int64_t i;       /* INT (signed) */
    uint64_t u;      /* INT (unsigned, when is_uint) */
    double f;        /* FLOAT */
} firc_scalar_value_t;

/* Resolves a plain (unquoted) scalar's type. */
firc_scalar_value_t firc_yaml_resolve_plain(const char *s);

/* True for a base-60 (sexagesimal) numeric string, e.g. "1:30". */
bool firc_yaml_is_base60_float(const char *s);

/* True when a string must be double-quoted on emit to stay a string on reload. */
bool firc_yaml_string_needs_quote(const char *s);

#endif /* FIRC_YAML_SCALAR_H */
