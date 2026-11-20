#ifndef FIRC_KMOD_H
#define FIRC_KMOD_H

#include <stddef.h>

/* Loads each mods[i].ko from dir (NULL: /lib/modules/<release>) in order; a missing file is skipped, a refusal
 * is warned naming `why`. Returns how many loaded. */
size_t firc_kmod_load(const char *dir, const char *const *mods, size_t n, const char *why);

/* The modular matches a netfilter pass may write; call before firc_xt_probe. */
size_t firc_kmod_load_netfilter(const char *dir);

#endif
