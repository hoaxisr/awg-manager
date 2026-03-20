/* SPDX-License-Identifier: GPL-2.0 */
/*
 * CPS (Custom Packet Signatures) template parsing and generation.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 */
#ifndef _AWG_CPS_H
#define _AWG_CPS_H

#include "transform.h"

/* Parse CPS template string into structured segments.
 * tmpl must be pre-allocated by caller. Returns 0 on success. */
int cps_parse(const char *s, cps_template_t *tmpl);

/* Generate CPS packet from template. Returns packet length. */
int cps_generate(const cps_template_t *tmpl, u32 counter,
		 u8 *buf, int bufsize);

/* Max possible size of generated packet from template. */
int cps_max_size(const cps_template_t *tmpl);

/* Generate all configured CPS packets (I1-I5).
 * Returns number of packets generated. counter is incremented per packet. */
int cps_generate_all(cps_template_t *templates[5], u32 *counter,
		     u8 bufs[][1500], int lens[]);

#endif /* _AWG_CPS_H */
