#ifndef TUNVLESS_CTLNODES_H
#define TUNVLESS_CTLNODES_H
#include <stddef.h>
#include <stdio.h>

struct ctln_hooks {
    int (*select)(const int *sel, size_t n);
    void (*ack)(int count);
    void (*pins_full)(void);
    int resolve_ms;
    void (*map_ack)(int count);
    int (*wake)(const int *idx, size_t n);
};

int ctln_start(const struct ctln_hooks *h);
void ctln_read(FILE *in);
void ctln_pin_extra(char **hosts, size_t n);

#endif
