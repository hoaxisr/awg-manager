# AmneziaWG kernel modules (kernel-mode backend)

These are the native `amneziawg` kernel modules loaded by the **kernel** backend
(`ip link add dev X type amneziawg` + `awg setconf`). One `.ko` per Keenetic
model; `internal/sys/kmod/loader.go` picks the right file, `scripts/build-ipk.sh`
bundles the arch-matching subset, and the bundled version marker comes from
`ExpectedKmodVersion` in `internal/sys/kmod/download.go`.

All current modules target Linux **4.9-ndm** (verify with
`strings amneziawg-KN-XXXX.ko | grep vermagic`). Arch split: MIPS32 LE
(mt7621 / mt7628), MIPS32 BE (en75xx), ARM aarch64 (mt7622 / mt7981 / mt7988).

## AWG 3.0 (awg3) rebuild

AWG 3.0 device params (HeaderProtectionKey, ContentPaddingAddition, and the
timing ranges RekeyAfterTime / RekeyTimeout / RejectAfterTime / KeepaliveTimeout
/ MaxHandshakeAttempts) are applied in kernel mode via `awg setconf`. They need
awg3-capable modules **and** an awg3-capable `awg` tool (see `../bin/README.md`).

Source: `amnezia-vpn/amneziawg-linux-kernel-module`, tag **v3.0.20260731**
(AWG 3.0 landed on master via PR #192; the `feat/awg3` branch no longer exists).
The kernel floor is 3.10, so no shipped model regresses.

Build with the Keenetic SDK — `keenetic-sdk/package/kernel/amneziawg/` carries
the recipe and the patch stack, and `keenetic-sdk/build-all-amneziawg.sh` sweeps
every model. The stock v3.0 tree does **not** build against 4.9-ndm as is;
patches 011–016 in that directory cover it:

| Patch | Why |
|---|---|
| 011 | `header_protection.c` uses the kernel 6.15 chacha API — reimplemented on the bundled zinc chacha20 |
| 013 | `awg_has_header_protection` is declared `inline` across translation units, and 4.9 maps `inline` to `always_inline` |
| 014 | `nla_put_uint()` only exists from kernel 6.6 |
| 015 | the new blake2s compat block pulls the kernel's `crypto/blake2s.h` into zinc's own translation units |
| 012, 016 | upstream defects: an inverted RekeyTimeout test that disables handshake rate limiting, and I4/I5 overwriting the I1 junk spec |

## Header protection needs S1–S4 ≥ 12

The module reads the ChaCha20 nonce from the front of the Sx junk padding (S1
for handshake initiation, S2 for response, S3 for cookie, S4 for transport), so
shorter padding leaves the two sides with different nonces and every packet is
dropped. `awg setconf` rejects S < 12 next to a key, but only for values present
in the same request: a config carrying no S values at all is accepted and the
tunnel comes up dead. `config.ValidateAWG3` enforces this on our side.

## Cut-over (do these together, never separately)

1. Drop the rebuilt awg3 `.ko` files into this directory (same names).
2. Drop the awg3 `awg` tool into `../bin/` (see that README).
3. Set `ExpectedKmodVersion` in `internal/sys/kmod/download.go` to the module's
   own version string, the one `modinfo` reports and the one that ends up in
   `/sys/module/amneziawg/version` (`3.0.20260731` for this batch). Any change
   to the string makes installed routers re-copy the modules, and keeping it
   equal to the real version means `kernelModuleVersion` and
   `kernelModuleLoadedVersion` in system info agree once the router reboots.
   While they differ, the disk holds a module the kernel has not loaded yet.

Do NOT bump the version before the awg3 binaries are in place: the bump forces a
re-copy, and a version marker ahead of the actual `.ko`/tool set would ship an
awg3-labelled build whose modules ignore the awg3 keys.

`EnsureModule` does not reload a module that is already in the kernel, so a
router keeps running the old one until it reboots. Until then the new `awg`
silently sends awg3 attributes that the old module drops — which is why the UI
gates the awg3 editor on the *loaded* module version, not on this marker.
