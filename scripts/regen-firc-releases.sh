#!/usr/bin/env bash
# Regenerate internal/firc/firc_releases.json from a published awgm-route release.
# Usage:
#   ./scripts/regen-firc-releases.sh <release-tag>
# Env:
#   FIRC_RUNTIME_REPO      GitHub repo hosting the release (default: hoaxisr/awg-manager)
#   FIRC_RUNTIME_URL_BASE  Optional HTTPS mirror base URL for packageUrl (keeps GitHub URL as mirrorUrl)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TAG="${1:?usage: regen-firc-releases.sh <release-tag>}"
RUNTIME_REPO="${FIRC_RUNTIME_REPO:-hoaxisr/awg-manager}"
URL_BASE="${FIRC_RUNTIME_URL_BASE:-}"
OUT_JSON="$REPO_ROOT/internal/firc/firc_releases.json"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading firc_releases.json from $RUNTIME_REPO@$TAG..."
gh release download "$TAG" --repo "$RUNTIME_REPO" --pattern "firc_releases.json" --dir "$TMP"

python3 - "$TMP/firc_releases.json" "$URL_BASE" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
url_base = sys.argv[2].rstrip("/")
data = json.loads(path.read_text(encoding="utf-8"))
if url_base:
    for arch, entry in data.items():
        pkg_url = entry["packageUrl"]
        filename = pkg_url.rsplit("/", 1)[-1]
        entry["mirrorUrl"] = pkg_url
        entry["packageUrl"] = f"{url_base}/{filename}"
path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
PY

if [[ -n "$URL_BASE" ]]; then
  python3 "$SCRIPT_DIR/merge-firc-releases.py" --verify "$TMP/firc_releases.json"
else
  python3 "$SCRIPT_DIR/merge-firc-releases.py" --verify "$TMP/firc_releases.json" --expected-repo "$RUNTIME_REPO"
fi

cp "$TMP/firc_releases.json" "$OUT_JSON"
echo "Updated $OUT_JSON from $RUNTIME_REPO@$TAG"
