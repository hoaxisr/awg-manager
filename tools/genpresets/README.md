# genpresets — unified preset catalog generator (DEV TOOL)

Maintains `internal/presets/defaults.json` by loading the committed catalog as
base, refreshing DNS for every sing-box preset by re-decompiling its `.srs` with
a host sing-box, and appending any new presets from the `additions` table in
`catalog.go`.

**Not** run on the router and **not** in CI. Needs network (downloads `.srs`)
and a host sing-box.

## Run

```bash
# 1) get a host sing-box pinned to the project's runtime version
ver="$(sed -n 's/^const RequiredVersion = "\(.*\)"/\1/p' internal/singbox/installer/embedded.go)"
curl -fsSL -o /tmp/sb.tgz "https://github.com/SagerNet/sing-box/releases/download/v${ver}/sing-box-${ver}-linux-amd64.tar.gz"
tar -xzf /tmp/sb.tgz -C /tmp
SB=$(find /tmp -type f -name sing-box -path "*${ver}-linux-amd64*" | head -1)

# 2) generate — base is read from internal/presets/defaults.json, then rewritten
go run ./tools/genpresets -singbox "$SB"

# 3) review the diff and commit internal/presets/defaults.json
git diff internal/presets/defaults.json
```

## Self-hosting flow

- **Base** = `internal/presets/defaults.json` (the committed catalog).
- The generator re-decompiles each sing-box preset's `.srs` to refresh its
  inlined DNS domains/subnets. Non-DNS fields of base presets are preserved as-is.
- Entries from the `additions` table in `catalog.go` that are not yet in the
  base are appended with freshly decompiled DNS.
- The file is rewritten in place; review the diff and commit it.

## Pinned sing-box

Use the project's single-source runtime version — `RequiredVersion` in
`internal/singbox/installer/embedded.go` (currently `1.14.0-alpha.25`). Verified:
that version's `rule-set decompile` reads the SagerNet rule-set-branch `.srs`
(source format v2).

## Notes

- DNS domains larger than 500 entries (composite categories like
  `category-ads-all`) are intentionally NOT inlined — the preset stays
  sing-box-only.
- `domain_keyword` / `domain_regex` rules cannot be expressed by the DNS engine;
  they are skipped and logged (`note: ... skipped ...`).
- Output is deterministic (sorted by category then id) so re-run diffs stay small.

## Updating the pin

`.srs` are decompiled from pinned commits (`sagerNetPinSHA` / `vernettePinSHA` in
`catalog.go`) so generation is deterministic and the release drift-check is stable.
To refresh from upstream:

1. Get new commit SHAs:
   `git ls-remote https://github.com/SagerNet/sing-geosite rule-set`
   `git ls-remote https://github.com/vernette/rulesets master`
2. Update `sagerNetPinSHA` / `vernettePinSHA` (with the date) in `catalog.go`.
3. `go run ./tools/genpresets -singbox <sb>` and review the `defaults.json` diff.
4. Commit `catalog.go` + `defaults.json`. The release `catalog-drift` check then
   passes against the new pin.
