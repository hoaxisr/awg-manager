# AmneziaWG kernel modules (kernel-mode backend)

These are the native `amneziawg` kernel modules loaded by the **kernel** backend
(`ip link add dev X type amneziawg` + `awg setconf`). One `.ko` per Keenetic
model; `internal/sys/kmod/loader.go` picks the right file, `scripts/build-ipk.sh`
bundles the arch-matching subset, and the bundled version marker comes from
`ExpectedKmodVersion` in `internal/sys/kmod/download.go`.

All current modules target Linux **4.9-ndm** (verify with
`strings amneziawg-KN-XXXX.ko | grep vermagic`). Arch split: MIPS32 LE
(mt7621 / mt7628), MIPS32 BE (en75xx), ARM aarch64 (mt7622 / mt7981 / mt7988).

## One file per group, not per model

The sweep builds a module for every model, but the results collapse: 28 models
produce 10 distinct binaries. Within a group `.text`, `.data`, `.rodata`,
`.modinfo` and vermagic are identical, `__versions` is empty (no
CONFIG_MODVERSIONS, so no per-kernel symbol CRCs), and the undefined-symbol sets
match. What separates the groups is kernel configuration the hwnat patch keys
off, which is why KN-1011 stands apart from its mt7621 neighbours.

So this directory holds one file per group and `modelAlias` in
`internal/sys/kmod/loader.go` points the rest at it. The mipsel IPK carries
0.7 MB of modules instead of 5 MB.

Regrouping after a rebuild:

```
for f in amneziawg-*.ko; do
  echo "$(readelf -x .text "$f" | sha256sum | cut -c1-8) $(readelf -p .modinfo "$f" | grep -o 'vermagic=.*') $f"
done | sort
```

Models that fall outside the built set keep whatever module is already on the
router: `selectBundledModule` finds no matching file, copies nothing and leaves
the version marker alone. A fresh install on such a model gets no kernel module
at all, so kernel mode is unavailable there and only NativeWG remains.

## AWG 3.0 (awg3) rebuild

AWG 3.0 device params (HeaderProtectionKey, ContentPaddingAddition, and the
timing ranges RekeyAfterTime / RekeyTimeout / RejectAfterTime / KeepaliveTimeout
/ MaxHandshakeAttempts) are applied in kernel mode via `awg setconf`. They need
awg3-capable modules **and** an awg3-capable `awg` tool (see `../bin/README.md`).

Source: `amnezia-vpn/amneziawg-linux-kernel-module`, tag **v3.0.20260805**
(AWG 3.0 landed on master via PR #192; the `feat/awg3` branch no longer exists).
The kernel floor is 3.10, so no shipped model regresses.

The recipe and the patch stack live in `kmod/amneziawg/` in this repo; copy that
directory to `keenetic-sdk/package/kernel/amneziawg/` and build there. The stock
tree does **not** build against 4.9-ndm as is, and these upstream bugs are fatal
on it:

| Patch | Why |
|---|---|
| 011 | `header_protection.c` uses the kernel 6.15 chacha API, reimplemented on the bundled zinc chacha20 |
| 013 | `awg_has_header_protection` is declared `inline` across translation units, and 4.9 maps `inline` to `always_inline` |
| 015 | the new blake2s compat block pulls the kernel's `crypto/blake2s.h` into zinc's own translation units |
| 017 | Jmin is never checked against Jmax, so the junk packet size can run past the jmax-sized buffer |
| 018 | `wg_set_device()` calls `awg_has_header_protection(wg)` before the `IS_ERR(wg)` check, taking a rwsem inside an ERR_PTR |
| 019 | `wg_newlink()` never initialises `header_protection.lock`, and a zeroed rwsem kills every MIPS target on the first `awg setconf` |
| 021 | the crypt workers call `cond_resched()` inside the SIMD region, so an arm64 worker can sleep with NEON still held |
| 022 | the message type is matched without `le32_to_cpu()`, so a big endian target drops every packet it receives |

019 is the one that put mipsel routers in a boot loop while aarch64 was fine:
MIPS builds use `CONFIG_RWSEM_GENERIC_SPINLOCK`, where `__down_read()` reads a
zeroed `wait_list` as "has waiters" and dereferences NULL, while
`CONFIG_RWSEM_XCHGADD_ALGORITHM` on aarch64 reads the same zeroes as an unlocked
rwsem. All three bugs are reproduced on a KN-1810 stand, each against a build
that carries the patch and one that does not. Upstream has none of them fixed as
of 3.0.20260805.

021 is the arm64 counterpart: `CONFIG_PREEMPT_COUNT` is not set on 4.9-ndm, so
the `preempt_disable()` inside `kernel_neon_begin()` is a bare `barrier()` and
the worker really does sleep with `kernel_neon_busy` set. The next NEON user on
that CPU — IPsec ESP through cryptd — dies on `BUG_ON(!may_use_simd())`. Two
NC-1812 carrying an ESP tunnel next to a busy AmneziaWG interface rebooted every
5-6 hours; with the patch both ran 48 hours clean. The stock keenetic
`wireguard.ko` holds NEON just as long but has no `cond_resched()` in the
region, which is why only our module shows this.

022 is the big endian counterpart, and it is total rather than intermittent:
`awg_determine_type_and_padding()` matches the raw `__le32` type field against
the host-order H1-H4 ranges without converting it, so an en7512 / en7516 router
reads `01 00 00 00` as `0x01000000`, misses even the `{1,1}` default set in
`wg_newlink()`, and drops every packet of every type. Kernel mode simply cannot
work on KN-2010, KN-2110, KN-2112, KN-2410, KN-2510 and KN-3610 without this
patch, whatever the configuration; NativeWG is unaffected, which is why the hole
stayed invisible. On the little endian and aarch64 targets `le32_to_cpu()` is the
identity, so their modules are unchanged and were not rebuilt. Fixed upstream on
`feat/awg-3.1` (`d6d7342f`), unmerged as of v3.0.20260805; 022 is that hunk alone.

The -02 tag carries three fixes we reported: I4/I5 no longer overwrite the I1
junk spec, the inverted RekeyTimeout test is corrected, and a header protection
key is refused when any Sx is below 12. -03 made that last check actually fail
the request with `-EINVAL` instead of reporting success while dropping the key;
`config.ValidateAWG3` still covers the request that carries no Sx values at all.

## Header protection needs S1–S4 ≥ 12

The module reads the ChaCha20 nonce from the front of the Sx junk padding (S1
for handshake initiation, S2 for response, S3 for cookie, S4 for transport), so
shorter padding leaves the two sides with different nonces and every packet is
dropped. Since -03 `awg setconf` fails with `-EINVAL` on S < 12 next to a key,
but only for values present in the same request: a config carrying no S values
at all is accepted and the tunnel comes up dead. `config.ValidateAWG3` enforces
this on our side.

## Cut-over (do these together, never separately)

1. Drop the rebuilt awg3 `.ko` files into this directory (same names).
2. Drop the awg3 `awg` tool into `../bin/` (see that README).
3. Set `ExpectedKmodVersion` in `internal/sys/kmod/download.go` to the module's
   own version string, the one `modinfo` reports and the one that ends up in
   `/sys/module/amneziawg/version` (`3.0.20260805` for this batch). Any change
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
