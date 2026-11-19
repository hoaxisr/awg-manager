#ifndef TUNVLESS_CTLMAP_H
#define TUNVLESS_CTLMAP_H
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

enum { CM_NONE, CM_REAL, CM_DENIED, CM_PENDING };

struct cm_real { uint8_t b[16]; uint8_t v6; };

int ctlmap_init(uint32_t pool_be, int pool_len);
int ctlmap_in_pool(uint32_t a_be);
int ctlmap_lookup(uint32_t fake_be, struct cm_real *real);
int ctlmap_ask(uint32_t fake_be);
int ctlmap_answer(uint32_t fake_be, struct cm_real *real);
void ctlmap_unask(uint32_t fake_be);
int ctlmap_read_cmd(FILE *in, char **line, size_t *cap, int all);
int ctlmap_read_close(FILE *in, char **line, size_t *cap);
size_t ctlmap_take_closes(uint32_t *base_be, uint8_t *len, size_t cap, int *ends);
int ctlmap_wake_fd(void);

#endif
