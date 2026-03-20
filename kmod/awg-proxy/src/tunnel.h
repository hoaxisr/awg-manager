/* SPDX-License-Identifier: GPL-2.0 */
/*
 * AWG Proxy — tunnel configuration parsing.
 * Parses procfs config lines into awg_config_t (defined in transform.h).
 */
#ifndef _AWG_PROXY_TUNNEL_H
#define _AWG_PROXY_TUNNEL_H

#include "transform.h"

/*
 * Parse config line into an awg_config_t struct.
 * Format: "IP:PORT H1=min-max ... S1=N ... PUB_SERVER=hex PUB_CLIENT=hex I1=\"...\""
 * Calls config_compute() and cps_parse() internally.
 * Returns 0 on success, negative errno on error.
 */
int awg_config_parse(const char *config_line, awg_config_t *cfg);

/* Free CPS templates allocated by awg_config_parse */
void awg_config_free(awg_config_t *cfg);

#endif /* _AWG_PROXY_TUNNEL_H */
