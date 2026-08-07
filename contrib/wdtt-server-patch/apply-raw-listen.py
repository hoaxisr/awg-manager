#!/usr/bin/env python3
"""Добавляет -listen-raw в ildarmaga/wdtt server (без git apply)."""
from __future__ import annotations

import pathlib
import sys

ROOT = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".")


def patch_config(text: str) -> str:
    if "RawListen" in text:
        return text
    text = text.replace(
        "\tNatIface         string\n}",
        "\tNatIface         string\n\tRawListen        string\n}",
    )
    text = text.replace(
        "\tflagNatIface    *string\n)",
        "\tflagNatIface    *string\n\tflagListenRaw   *string\n)",
    )
    text = text.replace(
        '\t\tflagNatIface = flag.String("nat-if", "", "egress interface for MASQUERADE (empty = auto-detect WAN)")\n\t\tflag.Parse()',
        '\t\tflagNatIface = flag.String("nat-if", "", "egress interface for MASQUERADE (empty = auto-detect WAN)")\n'
        '\t\tflagListenRaw = flag.String("listen-raw", "", "Raw UDP (без DTLS/WG), например 0.0.0.0:56003")\n\t\tflag.Parse()',
    )
    text = text.replace(
        "\t\tNatIface:         strings.TrimSpace(*flagNatIface),\n\t}",
        "\t\tNatIface:         strings.TrimSpace(*flagNatIface),\n"
        "\t\tRawListen:        strings.TrimSpace(*flagListenRaw),\n\t}",
    )
    return text


def patch_server(text: str) -> str:
    needle = "\tif serverWrapKeys.Count() == 0 {\n\t\tlog.Fatalf(\"[WRAP] нет активных паролей для WRAP\")\n\t}\n"
    insert = needle + (
        "\tif rawListen := strings.TrimSpace(cfg.RawListen); rawListen != \"\" {\n"
        "\t\tgo func() {\n"
        "\t\t\tif err := runRawServer(ctx, cfg, rawListen); err != nil && ctx.Err() == nil {\n"
        "\t\t\t\tlog.Fatalf(\"[RAW] %v\", err)\n"
        "\t\t\t}\n"
        "\t\t}()\n"
        "\t}\n\n"
    )
    if "runRawServer(ctx, cfg, rawListen)" in text:
        return text
    if needle not in text:
        raise SystemExit("runServerOnce anchor not found in server.go")
    return text.replace(needle, insert, 1)


def main() -> int:
    config = ROOT / "server/config.go"
    server = ROOT / "server/server.go"
    config.write_text(patch_config(config.read_text(encoding="utf-8")), encoding="utf-8")
    server.write_text(patch_server(server.read_text(encoding="utf-8")), encoding="utf-8")
    print("patched server/config.go and server/server.go for -listen-raw")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
