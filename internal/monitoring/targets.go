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
// probe Host directly via HTTP bound to the tunnel interface.
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
	// Backend is the AWG backend kind for managed tunnels: "kernel" or
	// "nativewg". Empty for non-AWG rows.
	Backend string `json:"backend,omitempty"`
	// AWGVersion is derived from the managed tunnel interface obfuscation
	// params: "awg2.0" | "awg1.5" | "awg1.0" | "wg". Empty for non-AWG rows.
	AWGVersion string `json:"awgVersion,omitempty"`
	// DefaultRoute marks managed AWG tunnels configured as default route.
	DefaultRoute bool `json:"defaultRoute,omitempty"`
	// Subscription marks sing-box rows sourced from subscription members.
	Subscription bool `json:"subscription,omitempty"`
	// Sing-box protocol/security/transport hints used by monitoring badges.
	Protocol  string `json:"protocol,omitempty"`
	Security  string `json:"security,omitempty"`
	Transport string `json:"transport,omitempty"`
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

// BaseTargets is empty: cross-target probing (Cloudflare/Google/Quad9 against
// every tunnel) was removed together with the matrix UI. The scheduler now
// probes only each tunnel's self-check cell.
var BaseTargets = []Target{}

// EffectiveTargets returns one connectivity-check (self) target per unique
// SelfTarget host. Cross-target probing was removed with the matrix UI;
// only the self-check cell feeds the per-tunnel connectivity indicator.
func EffectiveTargets(tunnels []Tunnel) []Target {
	seen := make(map[string]bool)
	out := make([]Target, 0, len(tunnels))
	for _, tun := range tunnels {
		if tun.SelfTarget == "" || seen[tun.SelfTarget] {
			continue
		}
		seen[tun.SelfTarget] = true
		out = append(out, Target{
			ID:   "cc-" + tun.SelfTarget,
			Host: tun.SelfTarget,
			Name: tun.SelfTarget,
		})
	}
	return out
}
