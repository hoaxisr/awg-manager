// Package monitoring provides observability-only multi-target probes through
// running tunnels. The matrix view in the UI renders the resulting cells.
//
// This package does NOT influence pingcheck restart logic — it is purely a
// visual extension. The base target list is hardcoded; dynamic targets are
// derived from each tunnel's configured pingcheck target (so the user can
// see how the "active" target performs alongside the base ones).
package monitoring

// Target is a single monitoring probe target.
//
// URL is the HTTPS endpoint used by sing-box rows (Clash API
// /proxies/<tag>/delay). HTTP is unsafe — sing-box upstream
// forces HTTPS in this endpoint (sagernet/sing-box#3604) — so
// callers must pass HTTPS URLs only. AWG rows ignore URL and
// probe Host directly via curl bound to the tunnel interface.
type Target struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
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

	// Source identifies which lister produced this tunnel: "awg",
	// "system", "singbox". Drives row visual hints in the matrix UI.
	Source string `json:"source,omitempty"`
	// SingboxTag is the sing-box outbound tag (e.g. "veesp") for
	// Source=="singbox" tunnels; empty otherwise. Lets the frontend
	// reach into the per-member latency history map keyed by tag.
	SingboxTag string `json:"singboxTag,omitempty"`
	// ClashDelay is the last-recorded sing-box urltest delay (ms) for
	// this tunnel. 0 means: not a urltest member, or no delay recorded
	// yet, or Clash unreachable.
	ClashDelay int `json:"clashDelay,omitempty"`
	// UrltestGroup is the tag of the urltest composite group this
	// tunnel belongs to (when ClashDelay is non-zero). Empty otherwise.
	UrltestGroup string `json:"urltestGroup,omitempty"`
}

// BaseTargets is the hardcoded base list. Extend by code change only — there
// is no CRUD UI for targets.
var BaseTargets = []Target{
	{ID: "cf-1.1.1.1", Host: "1.1.1.1", Name: "Cloudflare DNS", URL: "https://1.1.1.1/"},
	{ID: "g-8.8.8.8", Host: "8.8.8.8", Name: "Google DNS", URL: "https://8.8.8.8/"},
	{ID: "q-9.9.9.9", Host: "9.9.9.9", Name: "Quad9 DNS", URL: "https://9.9.9.9/"},
}

// EffectiveTargets returns BaseTargets ∪ unique pingcheck targets.
// Synthesised entries get id "pc-<host>" (restart pingcheck target).
// Base order is preserved; dynamic entries appended in tunnel iteration
// order, deduplicated by Host.
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
	}
	return out
}
