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
