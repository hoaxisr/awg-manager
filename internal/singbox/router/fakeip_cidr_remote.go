package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Exec/IO seams (mirror ruleSetMatchExec / inlineRuleSetCompileExec) so the
// network + binary integration is replaced in tests. Production paths call the
// default* implementations against the real cache + sing-box.
var ruleSetDownload = func(ctx context.Context, url, format string) (string, error) {
	return defaultRuleSetDownload(ctx, url, format)
}
var ruleSetDecompileExec = func(binary, srsPath string) ([]byte, error) {
	return defaultRuleSetDecompile(binary, srsPath)
}

// remoteCIDRCache is a process-wide on-disk cache for the Tier-2 download path.
// It shares the same sha256-by-URL layout (and default $TMPDIR/awgm-router-rulesets
// dir) as the inspector's cache, so a .srs fetched by either path is reused by the
// other. Lazily constructed; newRuleSetCache itself touches no disk.
var (
	remoteCIDRCacheOnce sync.Once
	remoteCIDRCache     *ruleSetCache
)

func sharedRemoteCIDRCache() *ruleSetCache {
	remoteCIDRCacheOnce.Do(func() { remoteCIDRCache = newRuleSetCache("") })
	return remoteCIDRCache
}

// defaultRuleSetDownload fetches (and caches) a remote rule-set, returning the
// local file path. Reuses ruleSetCache.getOrDownload — the same machinery the
// inspector uses. format influences only the cache filename extension. emit is
// nil here (no Inspect progress channel during reconcile); tag is best-effort.
func defaultRuleSetDownload(_ context.Context, url, format string) (string, error) {
	if format == "" {
		format = inferFormat(url)
	}
	return sharedRemoteCIDRCache().getOrDownload(url, format, nil, "")
}

// defaultRuleSetDecompile runs `sing-box rule-set decompile --output <tmp.json>
// <srs>` (the genpresets form) and returns the decompiled source JSON bytes.
// sing-box writes to the --output file rather than stdout, so we point it at a
// temp file, read it back, and remove it. An empty binary (dev box without
// sing-box) yields an error the caller logs + skips.
func defaultRuleSetDecompile(binary, srsPath string) ([]byte, error) {
	if binary == "" {
		return nil, fmt.Errorf("no sing-box binary for decompile")
	}
	tmp, err := os.CreateTemp("", "awgm-decompile-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	jsonPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(jsonPath)

	cmd := exec.Command(binary, "rule-set", "decompile", "--output", jsonPath, srsPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sing-box decompile: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(jsonPath)
}

// singboxBinary returns the configured sing-box binary path, or "" on a dev box
// without one wired (defaultRuleSetDecompile then errors and the set is skipped).
func (s *ServiceImpl) singboxBinary() string {
	if s.deps.Singbox != nil {
		return s.deps.Singbox.Binary()
	}
	return ""
}

// remoteTunCIDRs downloads + decompiles each remote rule-set referenced by a
// LOOP-SAFE proxy route-rule and returns the normalized v4/v6 ip_cidr it contains.
// Gating on loopSafeProxyRule (not just isProxyRoute) is the loop-safety contract:
// a remote set's IPs may only be routed to the tun if the referencing rule's only
// matchers are ip_cidr/rule_set, so a by-IP packet to those CIDRs is guaranteed to
// proxy and never fall through to route.final=direct (which would loop back to the
// tun). Best-effort: any per-set failure is skipped (logged, not fatal).
func (s *ServiceImpl) remoteTunCIDRs(ctx context.Context, cfg *RouterConfig) (v4 []string, v6 []string) {
	if cfg == nil {
		return nil, nil
	}
	byTag := make(map[string]RuleSet, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		byTag[rs.Tag] = rs
	}
	want := map[string]RuleSet{}
	for _, r := range cfg.Route.Rules {
		if !loopSafeProxyRule(r) {
			continue
		}
		for _, tag := range r.RuleSet {
			if rs, ok := byTag[tag]; ok && rs.Type == "remote" && rs.URL != "" {
				want[tag] = rs
			}
		}
	}
	seen := map[string]bool{}
	add := func(c string) {
		norm, is4, ok := normalizeCIDR(c)
		if !ok || seen[norm] {
			return
		}
		seen[norm] = true
		if is4 {
			v4 = append(v4, norm)
		} else {
			v6 = append(v6, norm)
		}
	}
	for _, rs := range want {
		path, err := ruleSetDownload(ctx, rs.URL, rs.Format)
		if err != nil {
			s.appLog.Warn("fakeip-cidr-remote", rs.Tag, "download: "+err.Error())
			continue
		}
		var raw []byte
		if strings.HasSuffix(path, ".json") || rs.Format == "source" {
			raw, err = os.ReadFile(path)
		} else {
			raw, err = ruleSetDecompileExec(s.singboxBinary(), path)
		}
		if err != nil {
			s.appLog.Warn("fakeip-cidr-remote", rs.Tag, "read/decompile: "+err.Error())
			continue
		}
		var src inlineRuleSetSource
		if e := json.Unmarshal(raw, &src); e != nil {
			s.appLog.Warn("fakeip-cidr-remote", rs.Tag, "parse: "+e.Error())
			continue
		}
		for _, c := range ruleSetCIDRs(RuleSet{Rules: src.Rules}) {
			add(c)
		}
	}
	return v4, v6
}
