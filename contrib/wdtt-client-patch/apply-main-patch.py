#!/usr/bin/env python3
"""Правки main.go для -mode rawtun (без git apply — надёжно на Windows/Linux)."""
from __future__ import annotations

import pathlib
import sys

RAWTUN_BLOCK = """
	if activeConnMode == "rawtun" {
		wrapStatus := "OFF"
		if len(wrapKey) == wrapKeyLen {
			wrapStatus = "ON (password HKDF + RTP AEAD)"
		}
		numGroups := (*numW + workersPerGroup - 1) / workersPerGroup
		log.Println("[КЛИЕНТ] ═══════════════════════════════════════")
		log.Printf("[КЛИЕНТ] Воркеров: %d (групп: %d, по %d)", *numW, numGroups, workersPerGroup)
		log.Printf("[КЛИЕНТ] Пир (raw): %s", *peerAddr)
		log.Printf("[КЛИЕНТ] Режим: rawtun (TUN + WRAP, без DTLS/WG)")
		log.Printf("[КЛИЕНТ] WRAP: %s", wrapStatus)
		log.Printf("[КЛИЕНТ] Device ID: %s", *deviceID)
		log.Println("[КЛИЕНТ] ═══════════════════════════════════════")

		stats := NewStats()
		shutdownCh := make(chan struct{})
		go func() {
			<-ctx.Done()
			close(shutdownCh)
		}()
		go stats.RunLoop(shutdownCh)

		runRawTunClient(ctx, tp, peer, *numW, *deviceID, *connPassword, stats, &pauseFlag)
		return
	}

"""

MARKER = "\t// Слушаем локально (SO_REUSEADDR — быстрый перезапуск без «address already in use»)"


def main() -> int:
    path = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "main.go")
    text = path.read_text(encoding="utf-8")

    text = text.replace(
        'connMode := flag.String("mode", "vpn", "режим клиента (vpn|socks)")',
        'connMode := flag.String("mode", "vpn", "режим клиента (vpn|socks|rawtun)")',
    )

    if "applyCompatFlags(vkAuthMode, vkAnonPath)" not in text:
        text = text.replace(
            "\tflag.Parse()\n\tactiveConnMode",
            "\tflag.Parse()\n\tapplyCompatFlags(vkAuthMode, vkAnonPath)\n\tactiveConnMode",
        )

    old_mode = (
        "\tactiveConnMode := strings.ToLower(strings.TrimSpace(*connMode))\n"
        "\tif activeConnMode != \"socks\" {\n"
        "\t\tactiveConnMode = \"vpn\"\n"
        "\t}"
    )
    new_mode = (
        "\tactiveConnMode := strings.ToLower(strings.TrimSpace(*connMode))\n"
        "\tswitch activeConnMode {\n"
        "\tcase \"socks\":\n"
        "\tcase \"rawtun\":\n"
        "\tdefault:\n"
        "\t\tactiveConnMode = \"vpn\"\n"
        "\t}"
    )
    if old_mode in text:
        text = text.replace(old_mode, new_mode)

    if "runRawTunClient(ctx, tp, peer" not in text:
        if MARKER not in text:
            print("marker not found in main.go", file=sys.stderr)
            return 1
        text = text.replace(MARKER, RAWTUN_BLOCK + MARKER)

    path.write_text(text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
