#ifndef FIRC_TUNPROBE_H
#define FIRC_TUNPROBE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/loop.h"

typedef struct firc_tunprobe firc_tunprobe_t;

typedef struct {
    bool have, ok;
    int handshake_ms, first_byte_ms;
    char why[160];
} firc_tunprobe_row_t;

typedef struct {
    const char *binary;
    uint32_t mark;
    bool insecure;
    const char *ca;
    const char *fingerprint;
    int timeout_s;
    int64_t kill_after_ms;
} firc_tunprobe_opts_t;

/* rows[i] answers links[i]; have is false for a link the child never reported. Runs on the loop thread. */
typedef void (*firc_tunprobe_done_fn)(const firc_tunprobe_row_t *rows, size_t n, void *ud);

/* Loop thread only. Runs `binary --probe -t T [-m M] [--insecure] [--ca F] [--fp P] -` with the links on its stdin and
   only fds 0-2; kills it after kill_after_ms. done runs once, then the probe frees itself. */
firc_err_t firc_tunprobe_start(firc_loop_t *loop, const firc_tunprobe_opts_t *o, char *const *links, size_t n,
                               firc_tunprobe_done_fn done, void *ud, firc_tunprobe_t **out);

/* Kills and reaps the child; done never runs. NULL-safe, not from inside done. */
void firc_tunprobe_cancel(firc_tunprobe_t *p);

/* How long a probe of n links may run: 6 s per link (a long and a short ClientHello) plus 5 s. */
int64_t firc_tunprobe_deadline_ms(size_t n);

#endif
