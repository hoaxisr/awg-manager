#ifndef TUNVLESS_CONTROL_H
#define TUNVLESS_CONTROL_H

#include <stddef.h>
#include <stdint.h>

void ctl_init(int enabled);
void ctl_close_inherited_fds(void);
void ctl_emit_ready(const char *dev, int tcp_port);
void ctl_emit_map(int count);
void ctl_emit_close(int count);
void ctl_emit_miss(const char *fake);
void ctl_emit_bytes(uint32_t rx, uint32_t tx);
void ctl_emit_active(const char *const *names, const int *index, const int *v6, size_t n);
void ctl_emit_node_down(const char *node, int index, const char *why);
void ctl_emit_node_up(const char *node, int index);
void ctl_emit_nodes(int count);
void ctl_emit_inbound(unsigned port, const char *state, const char *why);
void ctl_emit_ibytes(unsigned port, uint32_t rx, uint32_t tx);
void ctl_emit_inbounds(long count);
void ctl_emit_pins_full(void);
void ctl_emit_no_node(int retry_s);

size_t ctl_json_str(char *out, size_t cap, const char *s);

#endif
