#!/bin/sh
# Fetch a sing-box binary rule-set (.srs) and decompile it to JSON.
#
# Needs: sing-box with `rule-set decompile` (awg-manager bundle or recent sing-box).
# Fetch: wget or curl.
#
# Usage:
#   ./decompile-ruleset.sh <url-or-local.srs> [output.json]
#
# Examples:
#   ./decompile-ruleset.sh 'https://github.com/hoaxisr/rulesets/raw/master/srs/telegram.srs'
#   ./decompile-ruleset.sh 'https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-meta.srs' /tmp/meta.json
#   ./decompile-ruleset.sh ./geosite-youtube.srs

set -eu

usage() {
	cat <<'EOF' >&2
Usage: decompile-ruleset.sh <url-or-path.srs> [output.json]

Decompiles a sing-box .srs rule-set to JSON (sing-box rule-set decompile).
If output is omitted, writes next to the source basename: <name>.json
EOF
	exit 2
}

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

find_singbox() {
	if [ -n "${SINGBOX:-}" ] && [ -x "$SINGBOX" ]; then
		printf '%s' "$SINGBOX"
		return 0
	fi
	for candidate in \
		/opt/etc/awg-manager/singbox/awgm-box \
		/opt/etc/awg-manager/singbox/sing-box \
		/opt/bin/sing-box \
		/usr/bin/sing-box; do
		if [ -x "$candidate" ]; then
			printf '%s' "$candidate"
			return 0
		fi
	done
	if command -v sing-box >/dev/null 2>&1; then
		command -v sing-box
		return 0
	fi
	return 1
}

fetch_to() {
	url=$1
	dest=$2
	if [ -f "$url" ]; then
		cp "$url" "$dest"
		return 0
	fi
	case $url in
		http://* | https://*) ;;
		*) die "not a file and not http(s): $url" ;;
	esac
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 30 --max-time 600 -o "$dest" "$url" \
			|| die "curl failed: $url"
		return 0
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$url" || die "wget failed: $url"
		return 0
	fi
	die "need curl or wget to fetch $url"
}

[ $# -ge 1 ] && [ $# -le 2 ] || usage

SRC_ARG=$1
OUT_ARG=${2:-}

SB=$(find_singbox) || die "sing-box not found (set SINGBOX=/path/to/sing-box)"
$SB version >/dev/null 2>&1 || die "sing-box not runnable: $SB"
$SB help rule-set decompile >/dev/null 2>&1 \
	|| die "this sing-box has no 'rule-set decompile' (too old?)"

WORKDIR=$(mktemp -d /tmp/decompile-rs.XXXXXX) || die "mktemp failed"
trap 'rm -rf "$WORKDIR"' EXIT INT HUP TERM

SRS="$WORKDIR/input.srs"
fetch_to "$SRC_ARG" "$SRS"

[ -s "$SRS" ] || die "empty download: $SRC_ARG"

# SRS magic: 0x53 0x52 0x53 ("SRS")
MAGIC=$(dd if="$SRS" bs=1 count=3 2>/dev/null | od -An -tx1 | tr -d ' \n')
if [ "$MAGIC" != "535253" ]; then
	die "file is not a sing-box .srs (bad magic), got: $MAGIC"
fi

BASE=$(basename "$SRC_ARG")
BASE=${BASE%.srs}
[ -n "$BASE" ] || BASE=ruleset

if [ -n "$OUT_ARG" ]; then
	OUT=$OUT_ARG
else
	OUT="./${BASE}.json"
fi
case $OUT in
	/*) ;;
	*) OUT=$(cd "$(dirname "$OUT")" 2>/dev/null && pwd)/$(basename "$OUT") || OUT="./$(basename "$OUT")" ;;
esac
mkdir -p "$(dirname "$OUT")" 2>/dev/null || true

log "sing-box: $SB"
log "input:  $SRC_ARG"
log "output: $OUT"

$SB rule-set decompile "$SRS" -o "$OUT" \
	|| die "decompile failed (AdGuard-encoded rulesets cannot be decompiled)"

log "done: $OUT"
wc -c "$OUT" 2>/dev/null | awk '{print "size:", $1, "bytes"}' >&2 || true
