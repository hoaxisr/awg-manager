#!/usr/bin/env python3
"""Обновляет SHA256/Size в internal/wdtt/install.go из build/wdtt/."""
from __future__ import annotations

import hashlib
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
INSTALL = ROOT / "internal/wdtt/install.go"
OUT = ROOT / "build/wdtt"

# URL suffix in install.go -> built artifact name
BINARIES = {
    "wt-client-linux-arm64": "wt-client-linux-arm64",
    "wt-client-linux-mipsle-softfloat": "wt-client-linux-mipsle-softfloat",
    "wt-client-linux-mips-softfloat": "wt-client-linux-mips-softfloat",
    "wdtt-server-linux-arm64": "wdtt-server-linux-arm64",
}


def sha256_size(path: pathlib.Path) -> tuple[str, int]:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest(), path.stat().st_size


def patch_by_url_suffix(text: str, url_suffix: str, sha: str, size: int) -> str:
    esc = re.escape(url_suffix)
    pattern = rf'(\+ "{esc}",\s*SHA256: ")[a-f0-9]+(".*?Size: )\d+'

    def repl(m: re.Match[str]) -> str:
        return f"{m.group(1)}{sha}{m.group(2)}{size}"

    new, n = re.subn(pattern, repl, text, count=1, flags=re.DOTALL)
    if n != 1:
        raise SystemExit(f"patch failed for URL suffix {url_suffix}")
    return new


def main() -> int:
    if not INSTALL.is_file():
        print(f"missing {INSTALL}", file=sys.stderr)
        return 1
    text = INSTALL.read_text(encoding="utf-8")
    updated = 0
    for url_suffix, fname in BINARIES.items():
        path = OUT / fname
        if not path.is_file():
            print(f"skip missing {path}", file=sys.stderr)
            continue
        sha, size = sha256_size(path)
        text = patch_by_url_suffix(text, url_suffix, sha, size)
        print(f"{url_suffix}: sha256={sha} size={size}")
        updated += 1
    if updated == 0:
        print("nothing updated — run build-wdtt-*.sh first", file=sys.stderr)
        return 1
    INSTALL.write_text(text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
