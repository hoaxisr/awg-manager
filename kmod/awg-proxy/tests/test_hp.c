// SPDX-License-Identifier: GPL-2.0
/*
 * Userspace unit tests for the AmneziaWG 3.0+ header-protection ChaCha20
 * primitive (awg_chacha20_xor in src/cookie.c).
 *
 * Header protection XORs the WireGuard header with a raw ChaCha20 keystream:
 * IETF/RFC-8439 (32-byte key, 12-byte nonce, 32-bit block counter), counter
 * starting at 0, keying = header_protection_key used directly, nonce = the
 * first 12 on-wire junk-padding bytes. This must be byte-identical to
 * amneziawg-go's chacha20.NewUnauthenticatedCipher(key, nonce).XORKeyStream.
 *
 * The 148-byte known-answer vector (KS148) was generated independently from
 * the code under test via Python cryptography (OpenSSL ChaCha20), counter=0,
 * key=00..1f, nonce=00 00 00 09 00 00 00 4a 00 00 00 00 — spanning 3 blocks so
 * the counter-increment path is covered.
 *
 * Run via `make test`.
 */

#include "shim.h"
#include "../src/cookie.h"

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

static void hexdec(const char *hex, u8 *out, size_t n)
{
	for (size_t i = 0; i < n; i++) {
		unsigned int b;
		sscanf(hex + 2 * i, "%2x", &b);
		out[i] = (u8)b;
	}
}

/* Independent golden vector (Python cryptography, OpenSSL ChaCha20, ctr=0). */
static const char KEY_HEX[] =
	"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
static const char NONCE_HEX[] = "000000090000004a00000000";
static const char KS148_HEX[] =
	"8adc91fd9ff4f0f51b0fad50ff15d637e40efda206cc52c783a74200503c1582"
	"cd9833367d0a54d57d3c9e998f490ee69ca34c1ff9e939a75584c52d690a35d4"
	"10f1e7e4d13b5915500fdd1fa32071c4c7d1f4c733c068030422aa9ac3d46c4e"
	"d2826446079faa0914c2d705d98b02a2b5129cd1de164eb9cbd083e8a2503c4e"
	"0a88837739d7bf4ef8ccacb0ea2bb9d69d56c394";

/* XOR against an all-zero buffer yields the raw keystream — compare to the
 * independent 148-byte reference across 3 ChaCha20 blocks (counter 0,1,2). */
static void test_keystream_kat(void)
{
	u8 key[32], nonce[12], want[148], buf[148] = {0};

	tests_run++;
	hexdec(KEY_HEX, key, 32);
	hexdec(NONCE_HEX, nonce, 12);
	hexdec(KS148_HEX, want, 148);

	awg_chacha20_xor(key, nonce, 0, buf, sizeof(buf));

	if (memcmp(buf, want, sizeof(buf)) != 0)
		test_fail("keystream_kat", "multi-block keystream mismatch vs OpenSSL reference");
}

/* XOR is its own inverse: encrypt then decrypt returns the plaintext. */
static void test_roundtrip_identity(void)
{
	u8 key[32], nonce[12], msg[148], orig[148];

	tests_run++;
	hexdec(KEY_HEX, key, 32);
	hexdec(NONCE_HEX, nonce, 12);
	for (int i = 0; i < 148; i++)
		msg[i] = (u8)(0x40 + (i * 7));
	memcpy(orig, msg, sizeof(msg));

	awg_chacha20_xor(key, nonce, 0, msg, sizeof(msg));
	if (memcmp(msg, orig, sizeof(msg)) == 0)
		test_fail("roundtrip_identity", "ciphertext equals plaintext (no encryption happened)");
	awg_chacha20_xor(key, nonce, 0, msg, sizeof(msg));
	if (memcmp(msg, orig, sizeof(msg)) != 0)
		test_fail("roundtrip_identity", "decrypt did not restore plaintext");
}

/* Keystream depends on the nonce (the first 12 padding bytes on the wire). */
static void test_nonce_dependence(void)
{
	u8 key[32], n1[12], n2[12], b1[16] = {0}, b2[16] = {0};

	tests_run++;
	hexdec(KEY_HEX, key, 32);
	hexdec(NONCE_HEX, n1, 12);
	memcpy(n2, n1, 12);
	n2[0] ^= 0x01;

	awg_chacha20_xor(key, n1, 0, b1, sizeof(b1));
	awg_chacha20_xor(key, n2, 0, b2, sizeof(b2));
	if (memcmp(b1, b2, sizeof(b1)) == 0)
		test_fail("nonce_dependence", "different nonces produced identical keystream");
}

int main(void)
{
	test_keystream_kat();
	test_roundtrip_identity();
	test_nonce_dependence();

	if (tests_failed) {
		fprintf(stderr, "test_hp: %d/%d FAILED\n", tests_failed, tests_run);
		return 1;
	}
	printf("test_hp: %d tests passed\n", tests_run);
	return 0;
}
