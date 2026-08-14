#!/usr/bin/env python3
"""Обновляет SHA256/Size в internal/wdtt/install.go из релизов форка WDTT.

Источник истины — релиз GitHub: `checksums.txt` даёт SHA256, GitHub API — размер
ассета. Локально ничего не собирается и не считается: локальный тулчейн даёт
другие байты, чем CI, и пины разошлись бы с тем, что лежит на зеркале.

    scripts/update-wdtt-pins.py --client-tag awgm-client-v1.4.0-1 \
                                --server-tag awgm-server-v1.4.0-1 [--check]

--check — ничего не писать, только показать, что получилось бы.

Падает, если артефакт из списка пинов отсутствует в релизе или в checksums.txt.
Если в install.go ещё нет секции под артефакт (серверные mips появятся позже),
скрипт об этом громко сообщает и завершается с ненулевым кодом.
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
INSTALL = ROOT / "internal/wdtt/install.go"
REPO = "hoaxisr/proxy-turn-vk-android"

# Артефакты релиза, для которых в install.go ожидаются пины.
CLIENT_ARTIFACTS = [
    "wt-client-linux-arm64",
    "wt-client-linux-mipsle-softfloat",
    "wt-client-linux-mips-softfloat",
]
SERVER_ARTIFACTS = [
    "wdtt-server-linux-arm64",
    "wdtt-server-linux-mipsle-softfloat",
    "wdtt-server-linux-mips-softfloat",
]


def fetch(url: str, accept: str) -> bytes:
    req = urllib.request.Request(url, headers={"Accept": accept})
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.read()
    except urllib.error.HTTPError as e:
        raise SystemExit(f"{url}: HTTP {e.code} {e.reason}")
    except urllib.error.URLError as e:
        raise SystemExit(f"{url}: {e.reason}")


def release_assets(repo: str, tag: str) -> dict[str, dict]:
    """Ассеты релиза: имя -> {size, url}."""
    data = json.loads(
        fetch(
            f"https://api.github.com/repos/{repo}/releases/tags/{tag}",
            "application/vnd.github+json",
        )
    )
    return {
        a["name"]: {"size": int(a["size"]), "url": a["browser_download_url"]}
        for a in data.get("assets", [])
    }


def parse_checksums(raw: bytes) -> dict[str, str]:
    sums: dict[str, str] = {}
    for line in raw.decode("utf-8", "replace").splitlines():
        parts = line.split()
        if len(parts) == 2 and re.fullmatch(r"[a-f0-9]{64}", parts[0]):
            sums[parts[1].lstrip("*")] = parts[0]
    return sums


def release_pins(repo: str, tag: str, artifacts: list[str]) -> dict[str, tuple[str, int]]:
    """SHA256+размер по каждому артефакту; падает на любом недостающем."""
    assets = release_assets(repo, tag)
    if "checksums.txt" not in assets:
        raise SystemExit(f"{tag}: в релизе нет checksums.txt")
    sums = parse_checksums(fetch(assets["checksums.txt"]["url"], "application/octet-stream"))

    pins: dict[str, tuple[str, int]] = {}
    missing: list[str] = []
    for name in artifacts:
        if name not in assets:
            missing.append(f"{name}: нет ассета в релизе")
            continue
        if name not in sums:
            missing.append(f"{name}: нет строки в checksums.txt")
            continue
        pins[name] = (sums[name], assets[name]["size"])
    if missing:
        raise SystemExit(f"{tag}: неполный релиз:\n  " + "\n  ".join(missing))
    return pins


def patch_by_url_suffix(text: str, url_suffix: str, sha: str, size: int) -> tuple[str, bool]:
    esc = re.escape(url_suffix)
    pattern = rf'(\+ "{esc}",\s*SHA256: ")[a-f0-9]+(".*?Size: )\d+'

    def repl(m: re.Match[str]) -> str:
        return f"{m.group(1)}{sha}{m.group(2)}{size}"

    new, n = re.subn(pattern, repl, text, count=1, flags=re.DOTALL)
    return (new, True) if n == 1 else (text, False)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--client-tag", required=True, help="тег релиза клиента, напр. awgm-client-v1.4.0-1")
    ap.add_argument("--server-tag", required=True, help="тег релиза сервера, напр. awgm-server-v1.4.0-1")
    ap.add_argument("--repo", default=REPO, help=f"репозиторий форка (по умолчанию {REPO})")
    ap.add_argument("--check", action="store_true", help="не писать install.go, только показать")
    args = ap.parse_args()

    if not INSTALL.is_file():
        print(f"missing {INSTALL}", file=sys.stderr)
        return 1

    pins = release_pins(args.repo, args.client_tag, CLIENT_ARTIFACTS)
    pins.update(release_pins(args.repo, args.server_tag, SERVER_ARTIFACTS))

    text = INSTALL.read_text(encoding="utf-8")
    patched: list[str] = []
    no_section: list[str] = []
    for name, (sha, size) in pins.items():
        text, ok = patch_by_url_suffix(text, name, sha, size)
        if ok:
            patched.append(name)
            print(f"{name}: sha256={sha} size={size}")
        else:
            no_section.append(name)

    for name in no_section:
        sha, size = pins[name]
        print(
            f"НЕТ СЕКЦИИ в install.go для {name} "
            f"(релиз даёт sha256={sha} size={size}) — пин не обновлён",
            file=sys.stderr,
        )

    if args.check:
        print(f"--check: обновилось бы {len(patched)}, без секции {len(no_section)}; install.go не тронут")
    elif patched:
        INSTALL.write_text(text, encoding="utf-8")
        print(f"install.go обновлён: {len(patched)} пинов")

    if no_section:
        return 2
    if not patched:
        print("ни одного пина не обновлено", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
