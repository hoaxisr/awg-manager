#ifndef FIRC_REGDOMAIN_H
#define FIRC_REGDOMAIN_H

#include <stdbool.h>
#include <stddef.h>

bool firc_regdomain(const char *host, char *out, size_t cap);

/* True when the registrable domain sits under a shared-hosting suffix. */
bool firc_regdomain_shared(const char *domain);

#define FIRC_AUTOLIST_EXCLUDE_MAX 1000

bool firc_autolist_exclude_ok(const char *name);

bool firc_autolist_excluded(const char *host, char *const *list, size_t n);

#endif
