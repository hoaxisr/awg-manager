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
3.0 device params require the tool from `amnezia-vpn/amneziawg-tools` tag
**v3.0.20260730**, which parses `HeaderProtectionKey`, `ContentPaddingAddition`,
`RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout` and
`MaxHandshakeAttempts` (config.c) and serializes them to netlink. The stock tool
rejects those keys outright.

Build it with the Keenetic SDK: `keenetic-sdk/package/net/amneziawg-tools`, one
build per arch (`./configure.sh <MODEL>` then
`make package/net/amneziawg-tools/compile`). The binary links against the
firmware's musl loader (`/lib/ld-musl-*.so.1`), which is present on any Keenetic
running Entware — no static build needed.

Replacing the tool ahead of the modules is safe: it negotiates on the netlink
family version the kernel reports (2 for the AWG 2.0 modules, 3 for awg3) and
falls back to the old on-the-wire form. It does send the awg3 attributes
unconditionally, but an older module ignores them — which is why the UI gates
the awg3 editor on the loaded module version. See `../kmod/README.md` for the
combined cut-over (including the `ExpectedKmodVersion` bump).
