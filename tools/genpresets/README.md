# genpresets — unified preset catalog generator (DEV TOOL)

Regenerates `internal/presets/defaults.json` by merging the two existing
catalogs (`internalpresets.All()` + the frontend `SERVICE_PRESETS`) into unified
per-engine entries, reconciling DNS domains by decompiling each `.srs` with a
host sing-box, and adding the new presets in `catalog.go`'s `additions` table.

**Not** run on the router and **not** in CI. Needs network (downloads `.srs`)
and a host sing-box.

## Run

```bash
# 1) export the frontend DNS catalog
cd frontend && node scripts/export-service-presets.mjs > /tmp/service-presets.json && cd ..

# 2) get a host sing-box pinned to the project's runtime version
ver="$(sed -n 's/^const RequiredVersion = "\(.*\)"/\1/p' internal/singbox/installer/embedded.go)"
curl -fsSL -o /tmp/sb.tgz "https://github.com/SagerNet/sing-box/releases/download/v${ver}/sing-box-${ver}-linux-amd64.tar.gz"
tar -xzf /tmp/sb.tgz -C /tmp
SB=$(find /tmp -type f -name sing-box -path "*${ver}-linux-amd64*" | head -1)

# 3) generate, then review the diff and commit internal/presets/defaults.json
go run ./tools/genpresets -singbox "$SB" -service-presets /tmp/service-presets.json
```

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
