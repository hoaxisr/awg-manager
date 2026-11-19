#ifndef FIRC_LIST_CATALOG_H
#define FIRC_LIST_CATALOG_H

#include <stddef.h>

#include <cjson/cJSON.h>

#include "firc/err.h"

#define FIRC_LIST_CATALOG_URL "https://raw.githubusercontent.com/hoaxisr/rulesets/master/catalog.json"
#define FIRC_LIST_CATALOG_MAX_BYTES ((size_t)1024 * 1024)

/* Checks a rule-set index and copies {"generated","sources"} into *out (caller deletes). INVAL: why names the first offence. */
firc_err_t firc_list_catalog_parse(const char *body, size_t len, cJSON **out, char *why, size_t why_cap);

#endif
