#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BASE_VERSION="$(tr -d '[:space:]' < "$REPO_ROOT/c-core/firc/RUNTIME_VERSION")"
if [[ ! "$BASE_VERSION" =~ ^[0-9][A-Za-z0-9.-]{0,60}$ ]] || [[ "$BASE_VERSION" == *"+"* ]]; then
  echo "Invalid c-core/firc/RUNTIME_VERSION: '$BASE_VERSION'" >&2
  exit 1
fi

SHORT_HASH="$(REPO_ROOT="$REPO_ROOT" python3 - <<'PY'
import hashlib
import os
import pathlib

root = pathlib.Path(os.environ["REPO_ROOT"])
targets = [
    root / "c-core" / "firc",
    root / "cmd" / "firc-runtime-package",
    root / "internal" / "firc" / "runtime_archive.go",
    root / "internal" / "firc" / "installer.go",
    root / "scripts" / "firc-loopback-inbound.py",
]

ignored_exts = {".o", ".a", ".so", ".pyc"}
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
        rel_firc = p.relative_to(root / "c-core" / "firc").as_posix() if (root / "c-core" / "firc") in p.parents else ""
        parts = p.relative_to(root).parts
        if ".git" in parts or "__pycache__" in parts:
            continue
        if rel_firc.startswith("src/tunvless/third_party/") or rel_firc.startswith("src/tunvless/out") or rel_firc.startswith("src/backend-c/build/"):
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
