// Package monitoring provides observability-only multi-target probes through
// running tunnels. The matrix view in the UI renders the resulting cells.
//
// This package does NOT influence pingcheck restart logic — it is purely a
// visual extension. The base target list is hardcoded; dynamic targets are
// derived from each tunnel's configured pingcheck target (so the user can
// see how the "active" target performs alongside the base ones).
package monitoring

// Target is a single monitoring probe target.
type Target struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Name string `json:"name"`
}

// Tunnel is a running tunnel relevant for monitoring (subset of the full
// tunnel record). Built per scheduler tick from the TunnelLister + storage.
type Tunnel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	IfaceName       string `json:"ifaceName"`
	PingcheckTarget string `json:"pingcheckTarget"` // empty when restart pingcheck disabled
	SelfTarget      string `json:"selfTarget"`      // host the connectivity-check probes; empty when method=disabled/handshake
	SelfMethod      string `json:"selfMethod"`      // "http", "ping", "handshake", "disabled"
}

// BaseTargets is the hardcoded base list. Extend by code change only — there
// is no CRUD UI for targets.
var BaseTargets = []Target{
	{ID: "cf-1.1.1.1", Host: "1.1.1.1", Name: "Cloudflare DNS"},
	{ID: "g-8.8.8.8", Host: "8.8.8.8", Name: "Google DNS"},
	{ID: "q-9.9.9.9", Host: "9.9.9.9", Name: "Quad9 DNS"},
}

// EffectiveTargets returns BaseTargets ∪ unique pingcheck targets ∪ unique
// self-check targets from tunnels. Synthesised entries get id "pc-<host>"
// (restart pingcheck target) or "cc-<host>" (connectivity-check self
// target). Base order is preserved; dynamic entries appended in tunnel-
// iteration order, deduplicated by Host.
func EffectiveTargets(tunnels []Tunnel) []Target {
	seen := make(map[string]bool, len(BaseTargets))
	for _, t := range BaseTargets {
		seen[t.Host] = true
	}
	out := make([]Target, 0, len(BaseTargets)+len(tunnels)*2)
	out = append(out, BaseTargets...)
	for _, tun := range tunnels {
		if tun.PingcheckTarget != "" && !seen[tun.PingcheckTarget] {
			seen[tun.PingcheckTarget] = true
			out = append(out, Target{
				ID:   "pc-" + tun.PingcheckTarget,
				Host: tun.PingcheckTarget,
				Name: tun.PingcheckTarget,
			})
		}
		if tun.SelfTarget != "" && !seen[tun.SelfTarget] {
			seen[tun.SelfTarget] = true
			out = append(out, Target{
				ID:   "cc-" + tun.SelfTarget,
				Host: tun.SelfTarget,
				Name: tun.SelfTarget,
			})
		}
	}
	return out
}
