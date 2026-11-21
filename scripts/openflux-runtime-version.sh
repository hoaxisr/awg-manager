#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BASE_VERSION="$(tr -d '[:space:]' < "$REPO_ROOT/openflux-core/RUNTIME_VERSION")"
if [[ ! "$BASE_VERSION" =~ ^[0-9][A-Za-z0-9.-]{0,60}$ ]] || [[ "$BASE_VERSION" == *"+"* ]]; then
  echo "Invalid openflux-core/RUNTIME_VERSION: '$BASE_VERSION'" >&2
  exit 1
fi

if [[ "${1:-}" == "--base" ]] || [[ "${OPENFLUX_EXACT_VERSION:-}" == "1" ]]; then
  echo "$BASE_VERSION"
  exit 0
fi

SHORT_HASH="$(REPO_ROOT="$REPO_ROOT" python3 - <<'PY'
import hashlib
import os
import pathlib

root = pathlib.Path(os.environ["REPO_ROOT"])
targets = [
    root / "openflux-core",
    root / "scripts" / "build-openflux.sh",
    root / "scripts" / "openflux-runtime-version.sh",
]

ignored_exts = {".o", ".a", ".so", ".exe", ".test", ".pyc"}
files = []
for target in targets:
    if not target.exists():
        raise SystemExit(f"Missing runtime input path: {target}")
    if target.is_file():
        files.append(target)
        continue
    for p in target.rglob("*"):
        if not p.is_file():
            continue
        parts = p.relative_to(root).parts
        if ".git" in parts or "__pycache__" in parts:
            continue
        if p.suffix in ignored_exts:
            continue
        files.append(p)

files = sorted(set(files), key=lambda x: x.relative_to(root).as_posix())
h = hashlib.sha256()
for p in files:
    rel = p.relative_to(root).as_posix().encode("utf-8")
    raw = p.read_bytes()
    if b"\x00" not in raw:
        raw = raw.replace(b"\r\n", b"\n")
    h.update(rel)
    h.update(b"\x00")
    h.update(str(len(raw)).encode("ascii"))
    h.update(b"\x00")
    h.update(raw)
    h.update(b"\x00")

print(h.hexdigest()[:8])
PY
)"

echo "${BASE_VERSION}.r${SHORT_HASH}"
