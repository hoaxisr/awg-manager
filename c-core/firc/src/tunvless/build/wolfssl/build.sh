#!/bin/sh
# Build wolfSSL from source into a static archive — one recipe for every target.
#
#     sh build/wolfssl/build.sh <wolfSSL sources> <output .a> [asm|noasm]
#
# Compiler and flags come from the environment: CC (default cc), CFLAGS (target, optimization),
# AR (default ar). STEER_WOLFSSL_DEFS adds library options that are not in user_settings.h: only
# the crypto tests use it (certificate issuing and a TLS server for the peer side, see the
# Makefile); tunvless builds never do.
#
# Next to the archive goes <output>.cflags — exactly the flags src/lib/scrypto.c must be compiled
# with against this archive (defines and -I). Not "roughly the same": STEER_WOLFSSL_ASM changes the
# layout of Aes (the AES-NI fields), and scrypto.c compiled without it next to a library built with
# it would write past the fields. So callers read the flags from the file instead of deriving them.
#
# WHY A RECIPE AND NOT configure. Cross builds (the Entware SDK, any CC=...) get one list of .c
# files, -DWOLFSSL_USER_SETTINGS and one options header (build/wolfssl/user_settings.h — the
# reasons for each option are there) — wolfSSL's own way for embedded builds — instead of
# autotools probing a foreign compiler.
#
# THE FILE LIST IS EXPLICIT, not "everything in wolfcrypt/src": more than a hundred files of which a
# third are needed. A forgotten one shows at once as an undefined reference at link time.
#
# A FILE THAT FAILS TO COMPILE IS NOT HIDDEN: its errors are printed and the build stops.
set -eu

SRC="${1:?need the wolfSSL source directory}"
OUT="${2:-}"
[ "$SRC" = list ] || [ -n "$OUT" ] || { echo "wolfssl: need the output archive path" >&2; exit 2; }
ASM="${3:-asm}"
CC="${CC:-cc}"
AR="${AR:-ar}"
CFLAGS="${CFLAGS:--O2}"
HERE=$(cd "$(dirname "$0")" && pwd)

# The list is a function, so that `build.sh list` can print it.
wolfssl_c_files() {
    for f in internal keys quic ssl tls tls13 wolfio; do echo "src/$f.c"; done
    # ge_operations.c is Ed25519 group math: X25519 uses it for the public key where the curve has
    # its own assembly (aarch64, CURVED25519_ASM_64BIT); on other targets the file is empty.
    for f in aes asn blake2b chacha chacha20_poly1305 coding cpuid curve25519 ecc error fe_operations \
             ge_operations hash hmac kdf logging md5 memory poly1305 random rsa sha sha256 sha3 sha512 \
             sp_c32 sp_int wc_encrypt wc_mldsa wc_mlkem wc_mlkem_poly wc_port wolfmath; do
        echo "wolfcrypt/src/$f.c"
    done
    # aarch64 inline assembly (WOLFSSL_ARMASM_INLINE in user_settings.h). Listed for every target:
    # elsewhere they compile to nothing (#ifdef __aarch64__ inside), so one list serves all.
    for f in aes-asm_c chacha-asm_c poly1305-asm_c sha256-asm_c sha512-asm_c curve25519_c sha3-asm_c; do
        echo "wolfcrypt/src/port/arm/armv8-$f.c"
    done
}
[ "$SRC" = list ] && { wolfssl_c_files; exit 0; }
[ -f "$SRC/wolfssl/wolfcrypt/settings.h" ] || {
    echo "wolfssl: no wolfSSL sources in $SRC (wolfssl/wolfcrypt/settings.h)" >&2; exit 2; }
# Absolute paths: they go into <output>.cflags, which is read from another working directory.
SRC=$(cd "$SRC" && pwd)
mkdir -p "$(dirname "$OUT")"

TRIPLE=$($CC -dumpmachine 2>/dev/null || echo unknown)
ASMFILES=
ASMDEF=
case "$ASM:$TRIPLE" in
    asm:x86_64-*)
        ASMDEF=-DSTEER_WOLFSSL_ASM
        for f in aes_x86_64_asm aes_gcm_asm; do
            ASMFILES="$ASMFILES wolfcrypt/src/$f.S"
        done ;;
esac

DEFS="-DWOLFSSL_USER_SETTINGS $ASMDEF ${STEER_WOLFSSL_DEFS:-}"
OBJ="$OUT.obj"
# Build stamp: options, this script, flags, compiler. An archive with the same stamp is not
# rebuilt; any edit of user_settings.h or the flags rebuilds it.
STAMP="$(cat "$HERE/user_settings.h" "$0"; echo "$CC|$CFLAGS|$DEFS|$TRIPLE")"
if [ -f "$OUT" ] && [ -f "$OUT.stamp" ] && [ "$(cat "$OUT.stamp")" = "$STAMP" ]; then
    exit 0
fi
rm -rf "$OBJ"
mkdir -p "$OBJ"

failed=
for f in $(wolfssl_c_files) $ASMFILES; do
    o="$OBJ/$(echo "$f" | tr '/' '_').o"
    extra=
    # The AES-NI intrinsics in aes.c (AES_set_decrypt_key_AESNI) need -maes; only that file gets it:
    # the compiler never emits AES instructions on its own, and the whole AES-NI path in aes.c is
    # behind a CPUID check (Check_CPU_support_AES), so the binary still runs without AES-NI.
    # SSE4.1 is withheld on purpose: the compiler DOES emit its instructions when vectorizing
    # ordinary code, and the file would then crash on a CPU without it.
    [ -n "$ASMDEF" ] && [ "$f" = wolfcrypt/src/aes.c ] && extra=-maes
    # shellcheck disable=SC2086
    if ! $CC $CFLAGS $extra $DEFS -I"$HERE" -I"$SRC" \
            -c "$SRC/$f" -o "$o" 2>"$o.err"; then
        failed="$failed $f"
        echo "wolfssl: $f failed to compile ($TRIPLE):" >&2
        sed 's/^/    /' "$o.err" >&2
    elif [ -s "$o.err" ]; then
        # Warnings are not hidden: tunvless itself builds without any, and the library should
        # too, so a new warning after a wolfSSL update shows here and not in a release.
        echo "wolfssl: warnings in $f ($TRIPLE):" >&2
        sed 's/^/    /' "$o.err" >&2
    fi
    rm -f "$o.err"
done
[ -z "$failed" ] || { echo "wolfssl: files failed to compile:$failed" >&2; exit 1; }

rm -f "$OUT"
# shellcheck disable=SC2086
$AR rcs "$OUT" "$OBJ"/*.o
printf '%s\n' "$DEFS -I$HERE -I$SRC" > "$OUT.cflags"
printf '%s' "$STAMP" > "$OUT.stamp"
rm -rf "$OBJ"
