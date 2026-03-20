// SPDX-License-Identifier: GPL-2.0
/*
 * CPS template parsing and packet generation.
 * Ported from timbrs/amneziawg-mikrotik-c reference implementation.
 * Adapted: fastrand → prandom_bytes, time() → ktime_get_real_seconds().
 */

#include <linux/kernel.h>
#include <linux/string.h>
#include <linux/random.h>
#include <linux/ktime.h>
#include <linux/timekeeping.h>
#include <asm/byteorder.h>

#include "cps.h"

static const char alphanum[] =
	"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
#define ALPHANUM_LEN 62

static int hex_val(char c)
{
	if (c >= '0' && c <= '9')
		return c - '0';
	if (c >= 'a' && c <= 'f')
		return c - 'a' + 10;
	if (c >= 'A' && c <= 'F')
		return c - 'A' + 10;
	return -1;
}

static int parse_int(const char *s, int len)
{
	int v = 0, i;

	if (len <= 0)
		return -1;
	for (i = 0; i < len; i++) {
		if (s[i] < '0' || s[i] > '9')
			return -1;
		if (v > 100000)
			return -1;
		v = v * 10 + (s[i] - '0');
	}
	return v;
}

static int cps_skip_spaces(const char *s, int i, int len)
{
	while (i < len && (s[i] == ' ' || s[i] == '\t'))
		i++;
	return i;
}

int cps_parse(const char *s, cps_template_t *tmpl)
{
	int slen = 0;
	int i = 0;

	memset(tmpl, 0, sizeof(*tmpl));
	while (s[slen])
		slen++;

	while (i < slen) {
		int end, inner_start, inner_len;
		cps_segment_t *seg;
		char kind;
		int j;

		/* Skip whitespace */
		if (s[i] == ' ' || s[i] == '\t' ||
		    s[i] == '\n' || s[i] == '\r') {
			i++;
			continue;
		}
		if (s[i] != '<')
			return -1;

		/* Find closing '>' */
		end = -1;
		for (j = i + 1; j < slen; j++) {
			if (s[j] == '>') {
				end = j;
				break;
			}
		}
		if (end < 0)
			return -1;

		if (tmpl->nseg >= CPS_MAX_SEGMENTS)
			return -1;

		inner_start = i + 1;
		inner_len = end - inner_start;
		if (inner_len <= 0)
			return -1;

		seg = &tmpl->segs[tmpl->nseg];
		kind = s[inner_start];

		switch (kind) {
		case 'b': {
			/* <b 0xHEXDATA> */
			int p = cps_skip_spaces(s, inner_start + 1, end);
			int hexlen, nbytes, k;

			if (p + 2 >= end || s[p] != '0' ||
			    (s[p + 1] != 'x' && s[p + 1] != 'X'))
				return -1;
			p += 2;
			hexlen = end - p;
			if (hexlen % 2 != 0)
				return -1;
			nbytes = hexlen / 2;
			if (tmpl->static_used + nbytes > CPS_MAX_STATIC)
				return -1;
			seg->kind = CPS_STATIC;
			seg->data_off = tmpl->static_used;
			seg->data_len = nbytes;
			for (k = 0; k < hexlen; k += 2) {
				int hi = hex_val(s[p + k]);
				int lo = hex_val(s[p + k + 1]);

				if (hi < 0 || lo < 0)
					return -1;
				tmpl->static_data[tmpl->static_used++] =
					(u8)((hi << 4) | lo);
			}
			break;
		}
		case 'r': {
			/* <r SIZE>, <rc SIZE>, <rd SIZE> */
			if (inner_len > 1 && s[inner_start + 1] == 'c') {
				int p = cps_skip_spaces(s, inner_start + 2, end);
				int size = parse_int(s + p, end - p);

				if (size <= 0)
					return -1;
				seg->kind = CPS_RANDOM_CHARS;
				seg->size = size;
			} else if (inner_len > 1 &&
				   s[inner_start + 1] == 'd') {
				int p = cps_skip_spaces(s, inner_start + 2, end);
				int size = parse_int(s + p, end - p);

				if (size <= 0)
					return -1;
				seg->kind = CPS_RANDOM_DIGITS;
				seg->size = size;
			} else {
				int p = cps_skip_spaces(s, inner_start + 1, end);
				int size = parse_int(s + p, end - p);

				if (size <= 0)
					return -1;
				seg->kind = CPS_RANDOM;
				seg->size = size;
			}
			break;
		}
		case 't':
			seg->kind = CPS_TIMESTAMP;
			break;
		case 'c':
			seg->kind = CPS_COUNTER;
			break;
		default:
			return -1;
		}

		tmpl->nseg++;
		i = end + 1;
	}

	return (tmpl->nseg > 0) ? 0 : -1;
}

int cps_max_size(const cps_template_t *tmpl)
{
	int total = 0;
	int i;

	for (i = 0; i < tmpl->nseg; i++) {
		const cps_segment_t *seg = &tmpl->segs[i];

		switch (seg->kind) {
		case CPS_STATIC:
			total += seg->data_len;
			break;
		case CPS_RANDOM:
		case CPS_RANDOM_CHARS:
		case CPS_RANDOM_DIGITS:
			total += seg->size;
			break;
		case CPS_TIMESTAMP:
		case CPS_COUNTER:
			total += 4;
			break;
		}
	}
	return total;
}

int cps_generate(const cps_template_t *tmpl, u32 counter,
		 u8 *buf, int bufsize)
{
	int off = 0;
	int i;

	for (i = 0; i < tmpl->nseg; i++) {
		const cps_segment_t *seg = &tmpl->segs[i];

		switch (seg->kind) {
		case CPS_STATIC:
			if (off + seg->data_len > bufsize)
				return off;
			memcpy(buf + off,
			       tmpl->static_data + seg->data_off,
			       seg->data_len);
			off += seg->data_len;
			break;
		case CPS_RANDOM:
			if (off + seg->size > bufsize)
				return off;
			prandom_bytes(buf + off, seg->size);
			off += seg->size;
			break;
		case CPS_RANDOM_CHARS: {
			int j;
			u32 r;

			if (off + seg->size > bufsize)
				return off;
			for (j = 0; j < seg->size; j++) {
				prandom_bytes(&r, sizeof(r));
				buf[off + j] = alphanum[r % ALPHANUM_LEN];
			}
			off += seg->size;
			break;
		}
		case CPS_RANDOM_DIGITS: {
			int j;
			u32 r;

			if (off + seg->size > bufsize)
				return off;
			for (j = 0; j < seg->size; j++) {
				prandom_bytes(&r, sizeof(r));
				buf[off + j] = '0' + (r % 10);
			}
			off += seg->size;
			break;
		}
		case CPS_TIMESTAMP: {
			__le32 ts_le;

			if (off + 4 > bufsize)
				return off;
			ts_le = cpu_to_le32((u32)ktime_get_real_seconds());
			memcpy(buf + off, &ts_le, 4);
			off += 4;
			break;
		}
		case CPS_COUNTER: {
			__le32 ctr_le;

			if (off + 4 > bufsize)
				return off;
			ctr_le = cpu_to_le32(counter);
			memcpy(buf + off, &ctr_le, 4);
			off += 4;
			break;
		}
		}
	}
	return off;
}

int cps_generate_all(cps_template_t *templates[5], u32 *counter,
		     u8 bufs[][1500], int lens[])
{
	int count = 0;
	int i;

	for (i = 0; i < 5; i++) {
		if (!templates[i])
			continue;
		lens[count] = cps_generate(templates[i], *counter,
					   bufs[count], 1500);
		(*counter)++;
		count++;
	}
	return count;
}
