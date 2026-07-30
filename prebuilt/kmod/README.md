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
/ MaxHandshakeAttempts) are applied in kernel mode via `awg setconf`. They only
take effect with awg3-capable modules **and** an awg3-capable `awg` tool
(see `../bin/README.md`). The awg-manager side (storage, .conf gen, classify,
UI) already emits them; the last piece is rebuilding these binaries.

Source (pin exact commits — `feat/awg3` is a moving, unmerged branch):

- Module: `amnezia-vpn/amneziawg-linux-kernel-module` branch `feat/awg3`
  - Verified working at `bb3cd56` (2026-07-30, "feat: implement awg3 timings and
    content addition"). All 7 params are parsed in `netlink.c wg_set_device()`
    and consumed in `send.c` / `receive.c` / `timers.c`.
  - Kernel floor is `Linux >= 3.10` (`compat/compat.h`) — satisfied by 4.9-ndm,
    so no model regresses.

Build against the Keenetic SDK (OpenWrt-based, kernel 4.9-ndm):
https://github.com/keenetic/keenetic-sdk — branch `5.00`, targets
`en7512 / en7516 / en7528` (MIPS-BE), `mt7621 / mt7628` (MIPS-LE),
`mt7622 / mt7981 / mt7988` (ARM). Produce one `.ko` per model matching the
existing filenames here (`amneziawg-KN-XXXX.ko`, aliases in `loader.go`), i.e.
the same set/arch/vermagic as the files currently committed.

## Cut-over (do these together, never separately)

1. Drop the rebuilt awg3 `.ko` files into this directory (same names).
2. Drop the awg3 `awg` tool into `../bin/` (see that README).
3. Bump `ExpectedKmodVersion` in `internal/sys/kmod/download.go` so installed
   routers re-copy the new modules (loader.go re-installs on version change).

Do NOT bump the version before the awg3 binaries are in place: the bump forces a
re-copy, and a version marker ahead of the actual `.ko`/tool set would ship an
awg3-labelled build whose `awg setconf` rejects the awg3 keys.
