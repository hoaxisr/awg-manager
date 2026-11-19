#ifndef TUNVLESS_NODES_H
#define TUNVLESS_NODES_H
#include <stddef.h>

int nodes_init(size_t size);
int nodes_add(const void *node, const char *key);
int nodes_find(const char *key);
int nodes_count(void);
const void *node_at(int i);
const char *nodes_intern(const char *v, size_t n);

#endif
