/* Совместимость с эталоном Phobos: только локально, эталон из $(PHOBOS_SRC),
 * в репозиторий не кладётся и бинарь не распространяется (спека §3.8). */
#include <assert.h>
#include <stdio.h>
#include "obfuscation.h"      /* из PHOBOS_SRC */
#include "../src/phobos.h"

static u32 xs = 0xABCDEF;
static u32 xs_next(void *ctx) { (void)ctx; u32 x = xs; x ^= x << 13; x ^= x >> 17; x ^= x << 5; return xs = x; }
static const struct awgmr_rng rng = { xs_next, NULL };

int main(void)
{
	static const char *keys[] = { "benchkey-0123456789", "k", "0123456789012345678901234567890123456789" };
	static const int lens[] = { 4, 32, 92, 148, 1000, 1023, 1024, 1420 };
	static const int mds[] = { 0, 4, 1024 };
	static const int nbs[] = { 0, 16, 2000 };
	u8 orig[2048], b[2048];
	int ki, li, mi, ni, t, k, total = 0;

	awgmr_crc8_init();
	init_crc8_table();
	for (ki = 0; ki < 3; ki++) for (li = 0; li < 8; li++) for (mi = 0; mi < 3; mi++) for (ni = 0; ni < 3; ni++) for (t = 1; t <= 4; t++) for (k = 0; k < 20; k++) {
		struct awgmr_phobos_cfg c = { .key_len = (int)strlen(keys[ki]), .max_dummy = mds[mi], .obf_bytes = nbs[ni] };
		int len = lens[li], x, n, m;
		uint8_t ver = 1;

		memcpy(c.key, keys[ki], c.key_len);
		memset(orig, 0, sizeof(orig));
		orig[0] = (u8)t;
		for (x = 4; x < len; x++)
			orig[x] = (u8)xs_next(NULL);

		/* наш encode -> decode эталона */
		memcpy(b, orig, len);
		n = awgmr_encode(&c, b, len, sizeof(b), &rng);
		m = decode(b, n, (char *)keys[ki], c.key_len, &ver, nbs[ni]);
		assert(m == len && !memcmp(b, orig, len));

		/* encode эталона -> наш decode */
		memcpy(b, orig, len);
		n = encode(b, len, (char *)keys[ki], c.key_len, 1, mds[mi], nbs[ni]);
		m = awgmr_decode(&c, b, n);
		assert(m == len && !memcmp(b, orig, len));
		total++;
	}
	printf("compat_phobos: OK (%d cases x 2 directions)\n", total);
	return 0;
}
