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
produce 10 files here — and those 10 hold only **9 distinct binaries**, because
`KN-1212` and `KN-1710` are byte-identical in `.text`, `.data` and `.rodata`
(true of the 3.0 batch as well as 3.1). They are kept as two names rather than
aliased, so that `modelAlias` does not have to change on a version bump; the cost
is one duplicate file. Within a group `.text`, `.data`, `.rodata`, `.modinfo` and
vermagic are identical, `__versions` is empty (no CONFIG_MODVERSIONS, so no
per-kernel symbol CRCs), and the undefined-symbol sets match. What separates the
groups is kernel configuration the hwnat patch keys off, which is why KN-1011
stands apart from its mt7621 neighbours.

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

## AWG 3.x (awg3) rebuild

AWG 3.0 device params (HeaderProtectionKey, ContentPaddingAddition, and the
timing ranges RekeyAfterTime / RekeyTimeout / RejectAfterTime / KeepaliveTimeout
/ MaxHandshakeAttempts) are applied in kernel mode via `awg setconf`. AWG 3.1
adds two boolean device flags on top: **RandomTrailers** and **DisableCookies**.
All of them need awg3-capable modules **and** a matching `awg` tool (see
`../bin/README.md`) — the tool aborts the whole `setconf` on a key it does not
know, so module and tool ship together.

`RandomTrailers` is not negotiated on the wire: with it on, handshake and cookie
messages carry a random tail outside the encryption, and a peer on 3.0 drops them
on its length check without a word. Both ends have to be on 3.1, which is why the
UI gates the switch on a *loaded* module of 3.1 or newer, not on the marker below.

Source: `amnezia-vpn/amneziawg-linux-kernel-module`, tag **v3.1.20260906**
(AWG 3.0 landed on master via PR #192; the `feat/awg3` branch no longer exists).
The kernel floor is 3.10, so no shipped model regresses.

Upstream 3.1 took over three of the patches we used to carry, and they are gone
from `kmod/amneziawg/patches/`: **013** (`awg_has_header_protection` deleted
outright, which also drops a `down_read`/`up_read` from every inbound packet —
a measurable win on mips), **018** (`has_protection` now computed after the
`IS_ERR(wg)` check) and **022** (`le32_to_cpu` added on all four message-type
comparisons). 3.1.20260906 took **019** as well (`init_rwsem` on
`header_protection.lock`, hunk for hunk, plus `down_write` in `set_key` where
the key used to be written under a read lock). Ten patches remain; 010 and 017
were regenerated for the new `trailer` argument of
`wg_socket_send_buffer_to_peer()`, the rest apply unchanged.

What 3.1.20260906 changes on the wire, on top of 3.1.20260812: random trailers
are no longer appended to I1–I5 and junk packets, `DisableCookies` switches off
the whole under-load path instead of only the cookie reply (handshakes without
MAC2 were dropped under load before), and `ContentPaddingAddition` is capped by
the peer's observed UDP window rather than the MTU. The netlink UAPI is
unchanged, so the tool stays on v3.1.20260812. Upstream did not bump
`src/version.h` for the tags after 20260812, so a stock build of this tag calls
itself `3.1.20260812`; the recipe passes `WIREGUARD_VERSION=$(PKG_VERSION)` to
Kbuild, which overrides the header, and `modinfo` reports the tag.

The recipe and the patch stack live in `kmod/amneziawg/` in this repo; copy that
directory to `keenetic-sdk/package/kernel/amneziawg/` and build there. The stock
tree does **not** build against 4.9-ndm as is, and these upstream bugs are fatal
on it:

| Patch | Why |
|---|---|
| 011 | `header_protection.c` uses the kernel 6.15 chacha API, reimplemented on the bundled zinc chacha20 |
| 015 | the new blake2s compat block pulls the kernel's `crypto/blake2s.h` into zinc's own translation units |
| 017 | Jmin is never checked against Jmax, so the junk packet size can run past the jmax-sized buffer |
| 021 | the crypt workers call `cond_resched()` inside the SIMD region, so an arm64 worker can sleep with NEON still held |

The former 019 (now upstream, see above) is the one that put mipsel routers in
a boot loop while aarch64 was fine: `wg_newlink()` never initialised
`header_protection.lock`, and MIPS builds use `CONFIG_RWSEM_GENERIC_SPINLOCK`,
where `__down_read()` reads a zeroed `wait_list` as "has waiters" and
dereferences NULL, while `CONFIG_RWSEM_XCHGADD_ALGORITHM` on aarch64 reads the
same zeroes as an unlocked rwsem. 017 and 019 were both reproduced on a KN-1810
stand, each against a build that carries the patch and one that does not. 017
is still not fixed upstream.

021 is the arm64 counterpart: `CONFIG_PREEMPT_COUNT` is not set on 4.9-ndm, so
the `preempt_disable()` inside `kernel_neon_begin()` is a bare `barrier()` and
the worker really does sleep with `kernel_neon_busy` set. The next NEON user on
that CPU — IPsec ESP through cryptd — dies on `BUG_ON(!may_use_simd())`. Two
NC-1812 carrying an ESP tunnel next to a busy AmneziaWG interface rebooted every
5-6 hours; with the patch both ran 48 hours clean. The stock keenetic
`wireguard.ko` holds NEON just as long but has no `cond_resched()` in the
region, which is why only our module shows this.

Big endian, fixed upstream in 3.1 — kept here as history, since it explains why
the 3.0.20260805 batch shipped a patch that no longer exists. Before 3.1,
`awg_determine_type_and_padding()` matched the raw `__le32` type field against
the host-order H1-H4 ranges without converting it, so an en7512 / en7516 router
read `01 00 00 00` as `0x01000000`, missed even the `{1,1}` default set in
`wg_newlink()`, and dropped every packet of every type. Kernel mode simply could
not work on KN-2010, KN-2110, KN-2112, KN-2410, KN-2510 or KN-3610, whatever the
configuration; NativeWG was unaffected, which is why the hole stayed invisible.
We carried it as patch 022 (a single hunk lifted from `feat/awg-3.1`, `d6d7342f`);
3.1 merged `le32_to_cpu()` on all four comparisons, so the patch is gone and the
fix now comes from upstream code.

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
   `/sys/module/amneziawg/version` (`3.1.20260906` for this batch). Any change
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
