// SPDX-License-Identifier: GPL-2.0
/*
 * Userspace unit tests for kmod/awg-proxy/src/tunnel.c.
 */

#include "shim.h"
#include "../src/tunnel.h"

#include <string.h>
#include <stdio.h>
#include <stdarg.h>

static int tests_run, tests_failed;

static void test_fail(const char *test, const char *fmt, ...)
{
	va_list ap;

	fprintf(stderr, "FAIL %s: ", test);
	va_start(ap, fmt);
	vfprintf(stderr, fmt, ap);
	va_end(ap);
	fputc('\n', stderr);
	tests_failed++;
}

#define ASSERT_TRUE(test, cond, msg) do { \
	if (!(cond)) test_fail((test), "%s", (msg)); \
} while (0)

static void test_accepts_non_overlapping_h_ranges(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 H1=100-199 H2=200-299 H3=300-399 H4=400-499",
			       &cfg);
	ASSERT_TRUE("accepts_non_overlapping_h_ranges", ret == 0,
		    "non-overlapping H ranges should parse");
	if (!ret)
		awg_config_free(&cfg);
}

static void test_rejects_overlapping_h_ranges(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 H1=100-200 H2=200-300 H3=400 H4=500",
			       &cfg);
	ASSERT_TRUE("rejects_overlapping_h_ranges", ret != 0,
		    "H ranges sharing a boundary must be rejected");
}

static void test_rejects_range_overlapping_default_header(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 H2=1-20",
			       &cfg);
	ASSERT_TRUE("rejects_range_overlapping_default_header", ret != 0,
		    "configured H2 range must not overlap default H1=1");
}

static void test_accepts_exact_public_keys(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 "
			       "PUB_SERVER=000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f "
			       "PUB_CLIENT=202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f",
			       &cfg);
	ASSERT_TRUE("accepts_exact_public_keys", ret == 0,
		    "64-char hex public keys should parse");
	if (!ret) {
		ASSERT_TRUE("accepts_exact_public_keys", cfg.has_server_pub,
			    "server public key should be marked present");
		ASSERT_TRUE("accepts_exact_public_keys", cfg.has_client_pub,
			    "client public key should be marked present");
		awg_config_free(&cfg);
	}
}

static void test_rejects_short_public_key(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 "
			       "PUB_SERVER=000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e",
			       &cfg);
	ASSERT_TRUE("rejects_short_public_key", ret != 0,
		    "short public key hex must be rejected");
}

static void test_rejects_long_public_key(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 "
			       "PUB_SERVER=000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f00",
			       &cfg);
	ASSERT_TRUE("rejects_long_public_key", ret != 0,
		    "long public key hex must be rejected");
}

static void test_rejects_invalid_public_key_hex(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 "
			       "PUB_CLIENT=zz0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			       &cfg);
	ASSERT_TRUE("rejects_invalid_public_key_hex", ret != 0,
		    "non-hex public key data must be rejected");
}

/*
 * Fail-closed parsing (M3): a non-numeric S/Jc value must reject the whole
 * config, not silently leave the field at 0 and disable obfuscation.
 */
static void test_rejects_non_numeric_s_value(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 S1=abc", &cfg);
	ASSERT_TRUE("rejects_non_numeric_s_value", ret != 0,
		    "non-numeric S1 must be rejected, not coerced to 0");
}

static void test_rejects_non_numeric_jc_value(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 Jc=xyz", &cfg);
	ASSERT_TRUE("rejects_non_numeric_jc_value", ret != 0,
		    "non-numeric Jc must be rejected, not coerced to 0");
}

/*
 * Fail-closed parsing (M4): a malformed H value (neither "N" nor "N-M") must
 * reject the config, not silently keep the default header range.
 */
static void test_rejects_malformed_h_value(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 H1=garbage", &cfg);
	ASSERT_TRUE("rejects_malformed_h_value", ret != 0,
		    "malformed H1 must be rejected, not left at default");
}

/*
 * Guard: valid S/Jc and a single-value H ("N", sscanf returns 1) still parse.
 */
static void test_accepts_valid_numeric_values(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 H1=100 S1=16 Jc=2 Jmin=40 Jmax=60",
			       &cfg);
	ASSERT_TRUE("accepts_valid_numeric_values", ret == 0,
		    "valid numeric S/Jc and single-value H must parse");
	if (!ret) {
		ASSERT_TRUE("accepts_valid_numeric_values",
			    cfg.s1 == 16 && cfg.jc == 2 &&
			    cfg.h1.min == 100 && cfg.h1.max == 100,
			    "parsed values must match input");
		awg_config_free(&cfg);
	}
}

int main(void)
{
	test_accepts_non_overlapping_h_ranges();
	test_rejects_overlapping_h_ranges();
	test_rejects_range_overlapping_default_header();
	test_accepts_exact_public_keys();
	test_rejects_short_public_key();
	test_rejects_long_public_key();
	test_rejects_invalid_public_key_hex();
	test_rejects_non_numeric_s_value();
	test_rejects_non_numeric_jc_value();
	test_rejects_malformed_h_value();
	test_accepts_valid_numeric_values();

	printf("\n=== %d run, %d failed ===\n", tests_run, tests_failed);
	return tests_failed == 0 ? 0 : 1;
}
