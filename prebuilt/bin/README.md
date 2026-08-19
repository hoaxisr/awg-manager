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
3.x device params require the tool from `amnezia-vpn/amneziawg-tools` tag
**v3.1.20260812**, which parses `HeaderProtectionKey`, `ContentPaddingAddition`,
`RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`,
`MaxHandshakeAttempts` and — since 3.1 — the two boolean flags `RandomTrailers`
and `DisableCookies` (config.c), and serializes them to netlink. The stock tool
rejects those keys outright.

The two 3.1 flags go through `parse_bool` (`config.c`), which accepts `on` /
`off` case-insensitively or a number. It does **not** accept `true`: the first
character fails `char_is_digit` and the parse errors out. Note that the sing-box
path spells the same flags the other way round — `amneziawg-go` reads them from
UAPI with `strconv.ParseBool`, which takes `true`/`false`/`1`/`0` but not
`on`/`off`. `config.Generate` writes `on`, `awg3endpoint` writes JSON `true`.

**An unknown key is fatal, not ignored.** `process_line` in `config.c` falls
through to `goto error` on a key it does not know, so `awg setconf` fails for the
*whole file*. A config carrying `RandomTrailers` handed to a 3.0 tool does not
come up with the flag quietly dropped — the tunnel does not come up at all. This
is why the module and the tool have to ship together, and why the UI gates the
3.1 switches on a loaded module of 3.1 or newer (`supportsAwg31`).

Build it with the Keenetic SDK: `keenetic-sdk/package/net/amneziawg-tools`, one
build per arch (`./configure.sh <MODEL>` then
`make package/net/amneziawg-tools/compile`; KN-1810 for mipsel, KN-2010 for mips,
KN-1812 for aarch64). Drop the `sed` that the package Makefile used to run over
`src/uapi/linux/linux/wireguard.h`: it renamed the generic netlink family to
"wireguard", and the tool then talked to the kernel's own module instead of
`amneziawg`.

Replacing the tool ahead of the modules is safe: it negotiates on the netlink
family version the kernel reports (2 for the AWG 2.0 modules, 3 for awg3) and
falls back to the old on-the-wire form. It does send the awg3 attributes
unconditionally, but an older module ignores them — which is why the UI gates
the awg3 editor on the loaded module version. See `../kmod/README.md` for the
combined cut-over (including the `ExpectedKmodVersion` bump).
