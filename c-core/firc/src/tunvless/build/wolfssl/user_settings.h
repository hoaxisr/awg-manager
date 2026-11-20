/* wolfSSL build options for tunvless — the ONLY place they are written.
 *
 * Every wolfSSL .c sees this file (-DWOLFSSL_USER_SETTINGS), and so does the one tunvless file
 * that includes wolfSSL headers, src/lib/scrypto.c. That is a correctness condition, not taste:
 * the layout of wolfSSL structures (Aes, wc_Sha256, Hmac...) depends on these macros, and a
 * library built with one set next to a caller built with another corrupts memory silently. So
 * the options live nowhere else — not on command lines, not in configure.
 *
 * WHAT IS NEEDED. The TLS and REALITY client is our own (src/proto/tls); wolfSSL provides
 * primitives only: SHA-256/384/512, HMAC, HKDF, AES-GCM, ChaCha20-Poly1305, AES-CTR (VLESS
 * encryption), X25519, ML-KEM-768 and ML-DSA-65 verification, and X.509 chain verification with
 * name and validity (RSA PKCS#1 v1.5 and PSS, ECDSA P-256/P-384/P-521 signatures). The chain is
 * checked by wolfSSL's X509 store, which is why part of its TLS layer (OPENSSL_EXTRA) is built.
 *
 * WHAT IS LEFT OUT ON PURPOSE: wolfSSL's TLS 1.2 and older, a TLS server, DH, DSA, DES, RC4, MD4, PSK, PBKDF, Ed25519 (certverify.c
 * reads the Ed25519 Reality certificate itself), the filesystem (certverify.c reads the roots
 * itself, into a buffer) and socket I/O.
 *
 * SIZE. tunvless links wolfSSL statically with -ffunction-sections and --gc-sections, so what is
 * built but never called does not reach the binary.
 */
#ifndef STEER_WOLFSSL_USER_SETTINGS_H
#define STEER_WOLFSSL_USER_SETTINGS_H

/* ---- platform -------------------------------------------------------------------------- */
/* Seed for the DRBG: os_rand_seed (src/lib/osrand.c) — getrandom(2) where the kernel has it,
 * /dev/urandom where it does not. WOLFSSL_GETRANDOM alone fails on the 3.4 kernels Entware's mips
 * and mipsel targets support (the call appeared in 3.17), and NO_FILESYSTEM below removes
 * wolfSSL's own fallback to /dev/urandom. */
#if !defined(__ASSEMBLER__)
extern int os_rand_seed(unsigned char *out, unsigned int n);
#endif
#define CUSTOM_RAND_GENERATE_SEED os_rand_seed
/* Byte order. configure sets WORDS_BIGENDIAN; with user settings nothing does, wolfSSL then assumes
 * little endian, and on big-endian MIPS (Entware mips-3.4) every hash, cipher and curve comes out
 * wrong — SHA-256("abc") included. Found by running the crypto tests under qemu-mips. */
#if defined(__BYTE_ORDER__) && __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
#define BIG_ENDIAN_ORDER
#endif
#define NO_FILESYSTEM
#define WOLFSSL_USER_IO
/* Without socket I/O wolfio.h does not include <sys/time.h>, yet wolfSSL's tls13.c on Linux
 * expects gettimeofday from it. An implicit declaration is an error with new compilers
 * (-Werror=implicit-function-declaration), so the header is included here. Not for assembler:
 * .S files see this file too (through settings.h), and a C header does not parse there. */
#if !defined(__ASSEMBLER__) && defined(__linux__)
#include <sys/time.h>
#endif
/* The connector threads run with a modest stack (src/tunnel/stack.c), while certificate parsing
 * and RSA math keep kilobytes on the stack. SMALL_STACK moves large temporary buffers to the
 * heap: a few percent slower where speed does not matter (the handshake), and safe where a stack
 * overflow would silently corrupt a neighbouring thread. */
#define WOLFSSL_SMALL_STACK
/* Files that wolfSSL includes into other .c files (ssl_*.c into ssl.c, misc.c as inline) are also
 * compiled on their own, as empty units; without this each of them warns. */
#define WOLFSSL_IGNORE_FILE_WARN
#define NO_ERROR_STRINGS
/* Reproducible build: no build date and time in the binary (OpenSSL_version string of the
 * compatibility layer). The OpenWrt and Entware wolfssl package does the same. */
#define HAVE_REPRODUCIBLE_BUILD

/* ---- wolfSSL TLS stack: just what the X509 store needs --------------------------------- */
#define WOLFSSL_TLS13
#define WOLFSSL_NO_TLS12
#define NO_OLD_TLS
/* No TLS server in the shipped library. STEER_WOLFSSL_SERVER gives one to the test build of the
 * library (Makefile) for the tests that need a TLS peer, as WOLFSSL_CERT_GEN gives them
 * certificate issuing; tunvless builds never set it. */
#ifndef STEER_WOLFSSL_SERVER
#define NO_WOLFSSL_SERVER
#endif
#if defined(STEER_WOLFSSL_SERVER) || defined(__mips__)
#define WOLFSSL_USE_ALIGN
#endif
#define HAVE_TLS_EXTENSIONS
#define HAVE_SUPPORTED_CURVES
#define HAVE_SNI
#define HAVE_ALPN
#define HAVE_EX_DATA
#define WOLFSSL_QUIC
#define HAVE_SESSION_TICKET
#define OPENSSL_EXTRA

/* ---- primitives ------------------------------------------------------------------------ */
#define HAVE_HKDF
#define WOLFSSL_SHA384
#define WOLFSSL_SHA512
/* SHA-384/512 serve the handshake (TLS_AES_256_GCM_SHA384, REALITY's HMAC-SHA512, certificate
 * signatures): a dozen calls per connection, while the unrolled compression loop cost 12 KB of
 * flash. SHA-256 (which also hashes the transcript) stays fast. */
#define USE_SLOW_SHA512
#define HAVE_HASHDRBG
#define HAVE_BLAKE2
#define HAVE_BLAKE2B

#define HAVE_AESGCM
/* A 4-bit GHASH table (512 bytes per key), not 8-bit (4 KB): there is a key per direction of
 * every connection, and with hundreds of connections the 8-bit table would cost megabytes. */
#define GCM_TABLE_4BIT
/* AES-CTR — the masking stream of VLESS encryption (src/proto/transport/trvenc.c). DIRECT is needed
 * for setting a key without a mode (wc_AesSetKeyDirect) and for encrypting one block. */
#define WOLFSSL_AES_COUNTER
#define WOLFSSL_AES_DIRECT
#define HAVE_AES_ECB
#define NO_AES_192
#define NO_AES_CBC
/* Not NO_AES_DECRYPT: GCM and CTR do not use the Td table or the inverse block, but in wolfSSL
 * 5.9.4 that option also removes wc_AesGcmDecrypt (checked by building). */

#define HAVE_CHACHA
#define HAVE_POLY1305
#define HAVE_ONE_TIME_AUTH

#define HAVE_CURVE25519

#define HAVE_ECC
#define ECC_USER_CURVES
#undef  NO_ECC256
#define HAVE_ECC384
#define HAVE_ECC521
#define ECC_SHAMIR
#define ECC_TIMING_RESISTANT

/* ---- post-quantum (parity with Xray-core) ---------------------------------------------- */
/* ML-KEM-768 is half of the X25519MLKEM768 hybrid in TLS 1.3 (Chrome 131+ ClientHello, the
 * REALITY server's answer) and of the "mlkem768x25519plus" exchange of VLESS encryption.
 * ML-KEM-512 and -1024 are used by no peer the client meets (Xray and Go use only 768); leaving
 * them out drops a third of the tables and code.
 *
 * SHA-3 (SHAKE128/256, SHA3-256/512) is what ML-KEM is built on: the matrix A expands from the
 * seed with SHAKE128, the noise with SHAKE256, the key and ciphertext hashes are SHA3.
 *
 * ML-DSA-65 is needed FOR VERIFICATION ONLY: REALITY puts a signature into an extension of its
 * fake certificate, and a client whose node sets mldsa65Verify (`pqv` in the link) must check it.
 * We never sign or make keys, so VERIFY_ONLY drops signing, key generation and private key
 * parsing; no ASN.1 parsing is needed (the key comes raw, 1952 bytes). The memory-saving
 * variants (SMALL_MEM) are not used: verification is rare, but speed matters more than size. */
/* ML-KEM in portable C, without the aarch64 assembly: WOLFSSL_ARMASM turns on wolfSSL's
 * armv8-mlkem-asm, which needs SQRDMLAH from ARMv8.1 (`rdm`). The Cortex-A53 of routers lacks it,
 * and the assembler refuses to build. Speed: a handshake does one keygen and one decaps, a
 * fraction of a millisecond on x86 and a few on weak cores; it is never the bottleneck. */
#define WC_MLKEM_NO_ASM
#define WOLFSSL_HAVE_MLKEM
#define WOLFSSL_WC_MLKEM
#define WOLFSSL_NO_ML_KEM_512
#define WOLFSSL_NO_ML_KEM_1024
#define WOLFSSL_SHA3
#define WOLFSSL_SHAKE128
#define WOLFSSL_SHAKE256
#define WOLFSSL_HAVE_MLDSA
#define WOLFSSL_WC_MLDSA
#define WOLFSSL_MLDSA_VERIFY_ONLY
#define WOLFSSL_MLDSA_NO_ASN1
#define WOLFSSL_NO_ML_DSA_44
#define WOLFSSL_NO_ML_DSA_87

#define WC_RSA_PSS
/* PSS salt of any length: RFC 8446 wants it as long as the hash, but some servers (and
 * re-signing middleboxes) use another length, and rejecting them would declare a node broken
 * where the signature is valid (certverify.c). */
#define WOLFSSL_PSS_SALT_LEN_DISCOVER
#define WOLFSSL_PSS_LONG_SALT
#define WC_RSA_BLINDING
#define TFM_TIMING_RESISTANT
#define WOLFSSL_SP_MATH_ALL
#define WOLFSSL_HAVE_SP_ECC
#define WOLFSSL_SP_384

#define WOLFSSL_ASN_TEMPLATE
/* A certificate name can be an IP address (DoH on 1.1.1.1): without these two an IP SAN is not
 * parsed and the name check would reject a valid certificate. */
#define WOLFSSL_ALT_NAMES
#define WOLFSSL_IP_ALT_NAME

/* SHA-224 stays because the hash interface of src/lib/scrypto.h names it (as it names MD5 and
 * SHA-1); the VLESS path itself does not use them, and the linker drops what is not called. */
#define WOLFSSL_SHA224

#define NO_DSA
#define NO_DH
#define NO_RC4
#define NO_MD4
#define NO_DES3
#define NO_DES3_TLS_SUITES
#define NO_PSK
#define NO_PWDBASED

/* ---- per-architecture acceleration ----------------------------------------------------- */
/* x86_64: AES-NI, PCLMUL and AVX/AVX2 for AES and AES-GCM only, in wolfSSL's assembly
 * (aes_x86_64_asm.S, aes_gcm_asm.S), chosen by CPUID at run time: on a CPU without AES-NI the
 * software path runs, so the binary runs wherever it did. build/wolfssl/build.sh sets the macro
 * together with the .S files; without them the build stays portable C.
 *
 * Not the whole USE_INTEL_SPEEDUP (assembly also for ChaCha20, Poly1305, SHA and X25519): it
 * added over 540 KB to the x86_64 binary, since the assembly is one section and the linker cannot
 * drop its unused AVX-512 and VAES branches. On x86_64 the tunnel cipher is AES-GCM (reality.c
 * picks it by AES-NI, as Chrome does), so only that is accelerated; VAES and AVX-512 (CPUs that
 * routers do not have) are off. */
#if defined(STEER_WOLFSSL_ASM) && defined(__x86_64__)
#define WOLFSSL_X86_64_BUILD
#define WOLFSSL_AESNI
#define USE_INTEL_SPEEDUP_FOR_AES
#define NO_VAES_SUPPORT
#define NO_AVX512_SUPPORT
#endif
/* aarch64: ARMv8 Crypto for AES, PMULL for GHASH, NEON for ChaCha20 and Poly1305, own X25519 and
 * SHA code, all as wolfSSL's inline assembly (port/arm/armv8-*_c.c: plain C that any compiler
 * builds). The crypto instructions are chosen by getauxval(AT_HWCAP) at run time
 * (wolfcrypt/src/cpuid.c, aes->use_aes_hw_crypto): on a Cortex-A53 without the extension
 * (Raspberry Pi 4 and the like) AES runs in software instead of dying with SIGILL. Without this,
 * AES-GCM on aarch64 routers would fall back to tables. Off aarch64 the files compile to nothing,
 * so build.sh has one file list for all targets. */
#if defined(__aarch64__) && !defined(STEER_WOLFSSL_NO_ARMASM)
#define WOLFSSL_ARMASM
#define WOLFSSL_ARMASM_INLINE
#endif

#endif
