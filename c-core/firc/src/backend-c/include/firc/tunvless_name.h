#ifndef FIRC_TUNVLESS_NAME_H
#define FIRC_TUNVLESS_NAME_H

#include <stdbool.h>

#define FIRC_OPKGTUN_MAX 16

/* True when name starts with opkgtun; NULL is false. */
bool firc_tundev_is_opkg(const char *name);

/* N of a canonical tunvless<N> (N 0..99) or opkgtun<N> (N 0..FIRC_OPKGTUN_MAX), no leading zero, else -1. */
int firc_tundev_name_index(const char *name);

#endif
