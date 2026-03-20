/* SPDX-License-Identifier: GPL-2.0 */
/*
 * AWG transform types and functions.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 */
#ifndef _AWG_TRANSFORM_H
#define _AWG_TRANSFORM_H

#include <linux/types.h>
#include <linux/if.h>
#include <asm/div64.h>

/* WireGuard message types (LE uint32 in first 4 bytes) */
#define WG_HANDSHAKE_INIT      1
#define WG_HANDSHAKE_RESPONSE  2
#define WG_COOKIE_REPLY        3
#define WG_TRANSPORT_DATA      4

/* WireGuard packet sizes */
#define WG_INIT_SIZE     148
#define WG_RESP_SIZE      92
#define WG_COOKIE_SIZE    64
#define WG_TRANSPORT_MIN  32

/* Max junk packet count (bounds sizes[] stack array) */
#define AWG_MAX_JC       128

/* H range */
typedef struct {
	u32 min, max;
} hrange_t;

static inline u32 hrange_pick(const hrange_t *r, u64 rand_val)
{
	u32 range, rem;
	u64 tmp;

	if (r->min == r->max)
		return r->min;
	range = r->max - r->min + 1;
	tmp = rand_val;
	rem = do_div(tmp, range);
	return r->min + rem;
}

static inline int hrange_contains(const hrange_t *r, u32 v)
{
	return v >= r->min && v <= r->max;
}

/* CPS segment kinds */
#define CPS_STATIC        'b'
#define CPS_RANDOM        'r'
#define CPS_TIMESTAMP     't'
#define CPS_COUNTER       'c'
#define CPS_RANDOM_CHARS  'C'
#define CPS_RANDOM_DIGITS 'D'

#define CPS_MAX_SEGMENTS 32
#define CPS_MAX_STATIC   1500

typedef struct {
	u8  kind;
	u16 size;       /* for r/rc/rd */
	u16 data_off;   /* offset into static_data for 'b' */
	u16 data_len;   /* length in static_data for 'b' */
} cps_segment_t;

typedef struct {
	cps_segment_t segs[CPS_MAX_SEGMENTS];
	u8  static_data[CPS_MAX_STATIC];
	int nseg;
	int static_used;
} cps_template_t;

/* AWG config struct */
typedef struct {
	int jc, jmin, jmax;
	int s1, s2, s3, s4;
	hrange_t h1, h2, h3, h4;

	cps_template_t *cps[5]; /* I1-I5, NULL if not configured */

	u8 server_pub[32];
	u8 client_pub[32];
	u8 mac1key_server[32];
	u8 mac1key_client[32];

	u32 h4_fixed;
	int h4_noop;        /* H4={4,4} && S4==0 */
	int init_total;     /* S1 + 148 */
	int resp_total;     /* S2 + 92 */
	int cookie_total;   /* S3 + 64 */
	int has_server_pub;
	int has_client_pub;
	int transport_size_ambiguous;

	/* Proxy-specific (not in reference) */
	__be32 remote_ip;
	__be16 remote_port;
	char bind_iface[IFNAMSIZ]; /* SO_BINDTODEVICE interface, empty = no binding */
} awg_config_t;

/* Compute MAC1 keys and fast-path flags. Call after setting all fields. */
void config_compute(awg_config_t *cfg);

/*
 * Transform outbound WG->AWG.
 * buf has dataoff bytes of headroom before the packet data.
 * sendJunk is set to 1 if junk+CPS should be sent before this packet.
 * Returns pointer to output data.
 */
u8 *transform_outbound(u8 *buf, int dataoff, int n,
		       const awg_config_t *cfg, u64 rand_val,
		       int *out_len, int *sendJunk);

/*
 * Transform inbound AWG->WG.
 * Returns pointer to output data, or NULL if invalid/junk.
 */
u8 *transform_inbound(u8 *buf, int n, const awg_config_t *cfg, int *out_len);

/*
 * Generate junk packet sizes.
 * junk_buf should be pre-filled with random data.
 * sizes[] receives the packet sizes.
 * Returns number of junk packets.
 */
int generate_junk(const awg_config_t *cfg, u8 *junk_buf,
		  int *sizes, int max_sizes);

#endif /* _AWG_TRANSFORM_H */
