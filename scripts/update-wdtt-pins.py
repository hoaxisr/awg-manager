#!/usr/bin/env python3
"""Обновляет пины wdtt в internal/proxyapp/install/pins.go из релизов форка.

Источник истины — релиз GitHub: `checksums.txt` даёт SHA256, GitHub API — размер
ассета. Локально ничего не собирается и не считается: локальный тулчейн даёт
другие байты, чем CI, и пины разошлись бы с тем, что лежит на зеркале.

    scripts/update-wdtt-pins.py --client-tag awgm-client-v1.4.0-3 \\
                                --server-tag awgm-server-v1.4.0-3 [--check]

--check — ничего не писать, только показать, что получилось бы.

Скрипт правит и версии (`WdttPinnedClientVersion`, `WdttPinnedServerVersion`), и
суммы с размерами. Версии выводятся из тегов и уезжают в URL — обновлять их
руками нельзя: URL и суммы разъехались бы молча, и загрузка падала бы сверкой
суммы уже у пользователя.

После правки URL каждого пина проверяется на зеркале запросом HEAD: адрес обязан
существовать и отдавать Content-Length, равный размеру ассета. Проверка заведена
после 2026-08-28, когда пин указывал на СОСЕДНИЙ каталог зеркала со сборкой того
же имени, но другого происхождения — суммы в файле были верны для релиза, а
качался чужой бинарь без обвязки протокола. Отключается `--no-mirror-check`
(офлайн), но тогда адрес не проверен ничем.

Падает, если артефакт из списка пинов отсутствует в релизе или в checksums.txt.
Если в pins.go нет секции под артефакт, скрипт об этом громко сообщает и
завершается с ненулевым кодом.
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
PINS = ROOT / "internal/proxyapp/install/pins.go"
REPO = "hoaxisr/proxy-turn-vk-android"

# Артефакты релиза, для которых в pins.go ожидаются пины.
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

# Тег → константа версии в pins.go и база URL, из которой собирается адрес.
CLIENT_TAG_PREFIX = "awgm-client-v"
SERVER_TAG_PREFIX = "awgm-server-v"
CLIENT_VERSION_CONST = "WdttPinnedClientVersion"
SERVER_VERSION_CONST = "WdttPinnedServerVersion"
CLIENT_BASE_CONST = "wdttReleaseBase"
SERVER_BASE_CONST = "wdttServerReleaseBase"


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


def head_length(url: str) -> int | None:
    """Content-Length по HEAD; None, если адрес недоступен."""
    req = urllib.request.Request(url, method="HEAD")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.headers.get("Content-Length")
            return int(raw) if raw and raw.isdigit() else None
    except (urllib.error.HTTPError, urllib.error.URLError, OSError):
        return None


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


def version_from_tag(tag: str, prefix: str) -> str:
    """Версия пина из тега релиза. Чужой формат — отказ, а не догадка."""
    if not tag.startswith(prefix):
        raise SystemExit(f"тег {tag!r}: ожидался префикс {prefix!r}")
    version = tag[len(prefix):]
    if not version:
        raise SystemExit(f"тег {tag!r}: пустая версия после префикса")
    return version


def patch_version_const(text: str, const: str, version: str) -> tuple[str, bool]:
    pattern = rf'(const {re.escape(const)} = ")[^"]*(")'
    new, n = re.subn(pattern, rf"\g<1>{version}\g<2>", text, count=1)
    return (new, True) if n == 1 else (text, False)


def url_base(text: str, const: str, version: str) -> str | None:
    """Адрес каталога релиза: строковая часть базы + подставленная версия."""
    m = re.search(rf'const {re.escape(const)} = "([^"]*)" \+ \w+ \+ "([^"]*)"', text)
    if not m:
        return None
    return m.group(1) + version + m.group(2)


def patch_by_url_suffix(text: str, url_suffix: str, sha: str, size: int) -> tuple[str, bool]:
    esc = re.escape(url_suffix)
    pattern = rf'(\+ "{esc}",\s*SHA256: ")[a-f0-9]+(".*?Size: )\d+'

    def repl(m: re.Match[str]) -> str:
        return f"{m.group(1)}{sha}{m.group(2)}{size}"

    new, n = re.subn(pattern, repl, text, count=1, flags=re.DOTALL)
    return (new, True) if n == 1 else (text, False)


def check_mirror(text: str, artifacts: list[str], base_const: str, version: str,
                 pins: dict[str, tuple[str, int]]) -> list[str]:
    """Адреса пинов на зеркале: существуют и совпадают по размеру с релизом."""
    base = url_base(text, base_const, version)
    if base is None:
        return [f"{base_const}: не разобрана база URL — проверить зеркало нечем"]
    problems: list[str] = []
    for name in artifacts:
        url = base + name
        length = head_length(url)
        if length is None:
            problems.append(f"{url}: недоступен")
        elif length != pins[name][1]:
            problems.append(
                f"{url}: размер {length} ≠ {pins[name][1]} в релизе — на зеркале ЧУЖОЙ файл"
            )
    return problems


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--client-tag", required=True, help="тег релиза клиента, напр. awgm-client-v1.4.0-3")
    ap.add_argument("--server-tag", required=True, help="тег релиза сервера, напр. awgm-server-v1.4.0-3")
    ap.add_argument("--repo", default=REPO, help=f"репозиторий форка (по умолчанию {REPO})")
    ap.add_argument("--check", action="store_true", help="не писать pins.go, только показать")
    ap.add_argument("--no-mirror-check", action="store_true",
                    help="не проверять адреса на зеркале (офлайн)")
    args = ap.parse_args()

    if not PINS.is_file():
        print(f"missing {PINS}", file=sys.stderr)
        return 1

    client_version = version_from_tag(args.client_tag, CLIENT_TAG_PREFIX)
    server_version = version_from_tag(args.server_tag, SERVER_TAG_PREFIX)

    pins = release_pins(args.repo, args.client_tag, CLIENT_ARTIFACTS)
    pins.update(release_pins(args.repo, args.server_tag, SERVER_ARTIFACTS))

    text = PINS.read_text(encoding="utf-8")

    for const, version in ((CLIENT_VERSION_CONST, client_version),
                           (SERVER_VERSION_CONST, server_version)):
        text, ok = patch_version_const(text, const, version)
        if not ok:
            print(f"НЕТ КОНСТАНТЫ {const} в {PINS.name} — версия не обновлена", file=sys.stderr)
            return 2
        print(f"{const} = {version}")

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
            f"НЕТ СЕКЦИИ в {PINS.name} для {name} "
            f"(релиз даёт sha256={sha} size={size}) — пин не обновлён",
            file=sys.stderr,
        )

    if not args.no_mirror_check:
        problems = check_mirror(text, CLIENT_ARTIFACTS, CLIENT_BASE_CONST, client_version, pins)
        problems += check_mirror(text, SERVER_ARTIFACTS, SERVER_BASE_CONST, server_version, pins)
        if problems:
            print("зеркало не подтверждает пины:\n  " + "\n  ".join(problems), file=sys.stderr)
            print(f"{PINS.name} НЕ записан", file=sys.stderr)
            return 3
        print("зеркало: все адреса на месте, размеры совпали")

    if args.check:
        print(f"--check: обновилось бы {len(patched)}, без секции {len(no_section)}; {PINS.name} не тронут")
    elif patched:
        PINS.write_text(text, encoding="utf-8")
        print(f"{PINS.name} обновлён: {len(patched)} пинов")

    if no_section:
        return 2
    if not patched:
        print("ни одного пина не обновлено", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
