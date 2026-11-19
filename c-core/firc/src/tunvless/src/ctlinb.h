#ifndef TUNVLESS_CTLINB_H
#define TUNVLESS_CTLINB_H
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

#define IB_MAX 16
#define IB_NODE_POOL (-2)

struct ib_cfg {
    uint32_t addr;
    uint16_t port;
    int node;
    char user[256];
    char pass[256];
};

int ctlinb_init(void);
int ctlinb_read_cmd(FILE *in, char **line, size_t *cap);
size_t ctlinb_take(struct ib_cfg *out, size_t cap, unsigned *gen);
int ctlinb_wake_fd(void);

#endif
