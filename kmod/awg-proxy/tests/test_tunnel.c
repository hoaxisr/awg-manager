// SPDX-License-Identifier: GPL-2.0
/*
 * Userspace unit tests for kmod/awg-proxy/src/tunnel.c.
 */

#include "shim.h"
#include "../src/tunnel.h"

#include <string.h>
#include <stdio.h>
#include <stdarg.h>
#include <arpa/inet.h>

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

/*
 * Endpoint parsing — IPv4 legacy form must stay byte-identical (same
 * in_aton result, same htons'd port), bracketed IPv6 must parse via
 * in6_pton, and the ambiguous bare-IPv6 form must be rejected.
 */
static void test_parses_legacy_ipv4_endpoint(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("203.0.113.1:51820 S1=16", &cfg);
	ASSERT_TRUE("parses_legacy_ipv4_endpoint", ret == 0,
		    "plain IPv4 endpoint must parse");
	if (!ret) {
		ASSERT_TRUE("parses_legacy_ipv4_endpoint",
			    cfg.remote_addr.family == AF_INET,
			    "family must be AF_INET");
		ASSERT_TRUE("parses_legacy_ipv4_endpoint",
			    cfg.remote_addr.ip4 == (__be32)inet_addr("203.0.113.1"),
			    "ip4 must equal in_aton of the address (byte-identical)");
		ASSERT_TRUE("parses_legacy_ipv4_endpoint",
			    cfg.remote_port == htons(51820),
			    "port must be htons(51820)");
		ASSERT_TRUE("parses_legacy_ipv4_endpoint", cfg.s1 == 16,
			    "params after the endpoint must still parse");
		awg_config_free(&cfg);
	}
}

static void test_parses_bracketed_ipv6_endpoint(void)
{
	awg_config_t cfg;
	struct in6_addr want;
	int ret;

	tests_run++;
	ret = awg_config_parse("[2001:db8::1]:51820 S1=16", &cfg);
	ASSERT_TRUE("parses_bracketed_ipv6_endpoint", ret == 0,
		    "[v6]:port endpoint must parse");
	if (!ret) {
		inet_pton(AF_INET6, "2001:db8::1", &want);
		ASSERT_TRUE("parses_bracketed_ipv6_endpoint",
			    cfg.remote_addr.family == AF_INET6,
			    "family must be AF_INET6");
		ASSERT_TRUE("parses_bracketed_ipv6_endpoint",
			    memcmp(&cfg.remote_addr.ip6, &want, 16) == 0,
			    "ip6 must equal inet_pton of the address");
		ASSERT_TRUE("parses_bracketed_ipv6_endpoint",
			    cfg.remote_port == htons(51820),
			    "port must be htons(51820)");
		ASSERT_TRUE("parses_bracketed_ipv6_endpoint", cfg.s1 == 16,
			    "params after the endpoint must still parse");
		awg_config_free(&cfg);
	}
}

static void test_rejects_bare_ipv6_endpoint(void)
{
	awg_config_t cfg;
	int ret;

	tests_run++;
	ret = awg_config_parse("2001:db8::1:51820", &cfg);
	ASSERT_TRUE("rejects_bare_ipv6_endpoint", ret != 0,
		    "bare IPv6 without brackets must be rejected");
}

static void test_rejects_malformed_bracket_endpoints(void)
{
	static const char *bad[] = {
		"[2001:db8::1:51820",	/* missing ']' */
		"[]:51820",		/* empty address */
		"[2001:db8::1]51820",	/* missing ':' after ']' */
		"[2001:db8::1]",	/* missing port */
		"[2001:db8::1]:",	/* empty port */
		"[not-an-address]:51820",
		"[2001:db8::1]:0",	/* port out of range */
		"[2001:db8::1]:99999",	/* port out of range */
	};
	size_t k;

	for (k = 0; k < sizeof(bad) / sizeof(bad[0]); k++) {
		awg_config_t cfg;
		int ret;

		tests_run++;
		ret = awg_config_parse(bad[k], &cfg);
		if (ret == 0) {
			test_fail("rejects_malformed_bracket_endpoints",
				  "%s must be rejected", bad[k]);
			awg_config_free(&cfg);
		}
	}
}

/* The del path (main.c) uses awg_endpoint_parse directly — exercise both
 * accepted forms through the shared parser. */
static void test_endpoint_parse_del_forms(void)
{
	struct awg_endpoint_addr addr;
	struct in6_addr want;
	__be16 port;

	tests_run++;
	ASSERT_TRUE("endpoint_parse_del_forms",
		    awg_endpoint_parse("203.0.113.1:51820", &addr, &port) == 0,
		    "v4 del line must parse");
	ASSERT_TRUE("endpoint_parse_del_forms",
		    addr.family == AF_INET &&
		    addr.ip4 == (__be32)inet_addr("203.0.113.1") &&
		    port == htons(51820),
		    "v4 del parse must match add parse");

	ASSERT_TRUE("endpoint_parse_del_forms",
		    awg_endpoint_parse("[2001:db8::1]:443", &addr, &port) == 0,
		    "v6 del line must parse");
	inet_pton(AF_INET6, "2001:db8::1", &want);
	ASSERT_TRUE("endpoint_parse_del_forms",
		    addr.family == AF_INET6 &&
		    memcmp(&addr.ip6, &want, 16) == 0 &&
		    port == htons(443),
		    "v6 del parse must match add parse");

	ASSERT_TRUE("endpoint_parse_del_forms",
		    awg_endpoint_parse("2001:db8::1:443", &addr, &port) != 0,
		    "bare v6 del line must be rejected");
	ASSERT_TRUE("endpoint_parse_del_forms",
		    awg_endpoint_parse("no-port-here", &addr, &port) != 0,
		    "token without ':' must be rejected");
}

/* AWG 3.1: header-protection key (64-hex → 32 bytes) enables header protection;
 * S1-S4 must be >= 12 (the first 12 padding bytes are the ChaCha20 nonce). */
static void test_accepts_header_protection_key(void)
{
	awg_config_t cfg;
	int ret;

	memset(&cfg, 0, sizeof(cfg));
	ret = awg_config_parse("203.0.113.1:51820 "
		"HP_KEY=0102030405060708090a0b0c0d0e0f10"
		"1112131415161718191a1b1c1d1e1f20 "
		"S1=12 S2=12 S3=12 S4=12", &cfg);
	ASSERT_TRUE("accepts_header_protection_key", ret == 0, "valid HP_KEY must parse");
	ASSERT_TRUE("accepts_header_protection_key", cfg.hp_key_set, "hp_key_set must be true");
	ASSERT_TRUE("accepts_header_protection_key", cfg.hp_key[0] == 0x01 && cfg.hp_key[31] == 0x20,
		    "hp_key bytes must decode from hex");
}

static void test_accepts_random_trailers_flag(void)
{
	awg_config_t cfg;
	int ret;

	memset(&cfg, 0, sizeof(cfg));
	ret = awg_config_parse("203.0.113.1:51820 RT=1", &cfg);
	ASSERT_TRUE("accepts_random_trailers_flag", ret == 0, "RT=1 must parse");
	ASSERT_TRUE("accepts_random_trailers_flag", cfg.random_trailers, "random_trailers must be set");
}

static void test_rejects_hp_with_short_padding(void)
{
	awg_config_t cfg;
	int ret;

	memset(&cfg, 0, sizeof(cfg));
	ret = awg_config_parse("203.0.113.1:51820 "
		"HP_KEY=0102030405060708090a0b0c0d0e0f10"
		"1112131415161718191a1b1c1d1e1f20 "
		"S1=8 S2=12 S3=12 S4=12", &cfg);
	ASSERT_TRUE("rejects_hp_with_short_padding", ret != 0,
		    "S1<12 with header protection must be rejected (nonce needs 12 bytes)");
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
	test_parses_legacy_ipv4_endpoint();
	test_parses_bracketed_ipv6_endpoint();
	test_rejects_bare_ipv6_endpoint();
	test_rejects_malformed_bracket_endpoints();
	test_endpoint_parse_del_forms();
	test_accepts_header_protection_key();
	test_accepts_random_trailers_flag();
	test_rejects_hp_with_short_padding();

	printf("\n=== %d run, %d failed ===\n", tests_run, tests_failed);
	return tests_failed == 0 ? 0 : 1;
}
