# AmneziaWG Binaries

Place the following binaries here before building IPK packages:

## For mipsel-3.4 (Keenetic with MIPS)
- `amneziawg-go-mipsle` - userspace WireGuard daemon
- `awg-mipsle` - CLI tool (awg setconf, awg show, etc.)

## For aarch64-3.10 (Keenetic with ARM64)
- `amneziawg-go-arm64` - userspace WireGuard daemon
- `awg-arm64` - CLI tool

## Download
Get pre-built binaries from:
https://github.com/amnezia-vpn/amneziawg-go/releases

Or build from source:
```bash
# For mipsle
GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -o amneziawg-go-mipsle ./main.go

# For arm64
GOOS=linux GOARCH=arm64 go build -o amneziawg-go-arm64 ./main.go
```

## AWG 3.0 (awg3) `awg` tool

The kernel-mode backend runs `/opt/sbin/awg setconf` (this `awg-*` CLI). The AWG
3.0 device params require the tool built from
`amnezia-vpn/amneziawg-tools` branch `feat/awg3` (pin the commit — verified at
`c8aaf3d`, 2026-07-30). That build parses `HeaderProtectionKey`,
`ContentPaddingAddition`, `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`,
`KeepaliveTimeout`, `MaxHandshakeAttempts` (config.c) and serializes them to
netlink (ipc-linux.h `kernel_set_device`). The stock 1.5 tool rejects these keys,
so `awg setconf` on an awg3 config would fail. Replace `awg-mips`, `awg-mipsle`,
`awg-arm64` together with the awg3 kernel modules — see `../kmod/README.md` for
the combined cut-over (including the `ExpectedKmodVersion` bump).
