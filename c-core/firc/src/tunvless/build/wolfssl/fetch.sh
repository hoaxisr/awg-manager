#!/bin/sh
# wolfSSL sources for the build: download the release, VERIFY THE CHECKSUM, unpack.
#
#     sh build/wolfssl/fetch.sh <directory>
#     sh build/wolfssl/fetch.sh version      prints the version
#
# THE VERSION AND CHECKSUM ARE WRITTEN HERE ONLY. `make fetch`, the Entware package build
# (tools/ci/entware-package/Makefile) and the tests all get the sources through this script, so they
# share one version, and changing it is an edit of the two lines below in one commit.
#
# WHY A DOWNLOAD AND NOT SOURCES IN THE TREE. A wolfSSL release is 35 MB and over a thousand
# files, of which the build needs a little over thirty (build/wolfssl/build.sh). Keeping them in
# the tree would put foreign code into every clone and every history rewrite, for what one
# checksum verifies.
#
# WHY THIS CHECKSUM. The tarball is GitHub's archive of the release tag (the OpenWrt package,
# package/libs/wolfssl, takes the same one), and that is what wolfSSL signs:
# wolfssl-<version>.tar.gz.asc sits next to the release. The signature was checked with
# wolfSSL's key (A2A4 8E7B CB96 C5BE CB98 7314 EBC8 0E41 5CA2 9677, "wolfSSL
# <secure@wolfssl.com>") when the checksum was recorded here. The build checks the checksum, not
# the signature, so it needs neither gpg nor a key server.
#
# 5.9.4 (25 September 2026) fixes six CVEs found in 5.9.2 and older (ChangeLog.md: CVE-2026-93302,
# -89102, -89136, -93304, -89133, -89134). Not all of them touch our configuration, but there is
# no reason to build a release with known holes.
set -eu

WOLFSSL_VERSION=5.9.4
WOLFSSL_SHA256=7256bfc89b183a75183806c7debfa203443873b0b4a562e1b80d68e01b45ac57
URL="https://github.com/wolfSSL/wolfssl/archive/refs/tags/v${WOLFSSL_VERSION}-stable.tar.gz"

[ "${1:-}" = version ] && { echo "$WOLFSSL_VERSION"; exit 0; }
DEST="${1:?need a directory to unpack the sources into}"
if [ -f "$DEST/wolfssl/wolfcrypt/settings.h" ] && [ "$(cat "$DEST/.steer-wolfssl" 2>/dev/null)" = "$WOLFSSL_SHA256" ]; then
    exit 0
fi

mkdir -p "$DEST"
TGZ="$DEST.tar.gz"
# WOLFSSL_TARBALL supplies a tarball for a build without network; it is verified the same way.
if [ -n "${WOLFSSL_TARBALL:-}" ]; then
    TGZ="$WOLFSSL_TARBALL"
elif ! curl -fsSL -o "$TGZ" "$URL"; then
    echo "wolfssl: cannot download $URL" >&2
    rm -f "$TGZ"
    exit 1
fi
if ! echo "$WOLFSSL_SHA256  $TGZ" | sha256sum -c - >/dev/null 2>&1; then
    echo "wolfssl: checksum of $TGZ does not match (expected $WOLFSSL_SHA256) — not unpacking" >&2
    [ -n "${WOLFSSL_TARBALL:-}" ] || rm -f "$TGZ"
    exit 1
fi
# Unpack next to DEST and replace DEST as a whole: unpacked in place, an older version would mix
# with the new one (a file removed in the new release would stay and get built).
NEW="$DEST.new"
rm -rf "$NEW"
mkdir -p "$NEW"
tar xzf "$TGZ" -C "$NEW" --strip-components=1
[ -n "${WOLFSSL_TARBALL:-}" ] || rm -f "$TGZ"
[ -f "$NEW/wolfssl/wolfcrypt/settings.h" ] || { echo "wolfssl: no sources in the tarball" >&2; exit 1; }
# The marker is the tarball checksum: sources of another version in DEST are replaced, not used.
echo "$WOLFSSL_SHA256" > "$NEW/.steer-wolfssl"
rm -rf "$DEST"
mv "$NEW" "$DEST"
echo "wolfssl: sources $WOLFSSL_VERSION in $DEST (sha256 verified)"
