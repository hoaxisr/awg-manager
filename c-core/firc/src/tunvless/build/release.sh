#!/bin/sh
set -eu

NAME="${1:?need a library name}"
SHA256="${2:?need a checksum}"
URL="${3:?need a URL}"
PROBE="${4:?need a file the sources must hold}"
DEST="${5:?need a directory to unpack the sources into}"
TARBALL="${6:-}"

if [ -f "$DEST/$PROBE" ] && [ "$(cat "$DEST/.steer-$NAME" 2>/dev/null)" = "$SHA256" ]; then
    touch "$DEST/.steer-$NAME"
    exit 0
fi

mkdir -p "$DEST"
TGZ="$DEST.tar.gz"
if [ -n "$TARBALL" ]; then
    TGZ="$TARBALL"
elif ! curl -fsSL -o "$TGZ" "$URL"; then
    echo "$NAME: cannot download $URL" >&2
    rm -f "$TGZ"
    exit 1
fi
if ! echo "$SHA256  $TGZ" | sha256sum -c - >/dev/null 2>&1; then
    echo "$NAME: checksum of $TGZ does not match (expected $SHA256) — not unpacking" >&2
    [ -n "$TARBALL" ] || rm -f "$TGZ"
    exit 1
fi
NEW="$DEST.new"
rm -rf "$NEW"
mkdir -p "$NEW"
tar xzf "$TGZ" -C "$NEW" --strip-components=1
[ -n "$TARBALL" ] || rm -f "$TGZ"
[ -f "$NEW/$PROBE" ] || { echo "$NAME: no sources in the tarball" >&2; exit 1; }
echo "$SHA256" > "$NEW/.steer-$NAME"
rm -rf "$DEST"
mv "$NEW" "$DEST"
echo "$NAME: sources in $DEST (sha256 verified)"
