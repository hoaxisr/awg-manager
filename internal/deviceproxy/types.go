// Package deviceproxy owns the user-facing proxy server running on top
// of sing-box. A single mixed inbound accepts LAN-side client
// connections; a selector outbound routes them through one of the
// tunnels (sing-box outbounds by tag, AWG tunnels via direct+bind_interface)
// or directly out WAN. See docs/superpowers/specs/2026-04-24-device-proxy-design.md.
package deviceproxy

// Config is the single persisted record describing the user-facing
// proxy server. Exactly one Config exists per router; a nil/missing
// storage file is treated as a disabled zero-value Config.
type Config struct {
	Enabled          bool     `json:"enabled"`
	ListenAll        bool     `json:"listenAll"`        // true → bind 0.0.0.0
	ListenInterface  string   `json:"listenInterface"`  // NDMS interface id when ListenAll=false, e.g. "Bridge0"
	Port             int      `json:"port"`
	Auth             AuthSpec `json:"auth"`
	SelectedOutbound string   `json:"selectedOutbound"` // sing-box tag of the active member
}

// AuthSpec captures the optional username/password gate on the inbound.
type AuthSpec struct {
	Enabled  bool   `json:"enabled"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// defaultConfig is used when deviceproxy.json does not exist yet.
// Port 1099 is chosen to sit above the tunnel-inbound range (1080+slot)
// used by singbox/config.go so a fresh install doesn't trip the
// port-conflict validator.
func defaultConfig() Config {
	return Config{
		Enabled:   false,
		ListenAll: true,
		Port:      1099,
	}
}
