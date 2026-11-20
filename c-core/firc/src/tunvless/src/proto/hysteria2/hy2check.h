#ifndef TUNVLESS_HY2CHECK_H
#define TUNVLESS_HY2CHECK_H
#include <stddef.h>

#include "vless.h"

#define HY2_ANSWER_MS 3000

int hy2_probe_timed(const struct vless_node *n, int timeout_s, char *why, size_t why_n, int *handshake_ms, int *ttfb_ms);
int hy2_probe6(const struct vless_node *n, int timeout_s, char *why, size_t why_n);

#endif
