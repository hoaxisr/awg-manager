// Package config owns persisted settings and their safety invariants.
package config

const DefaultWANCheckURL = "https://ya.ru/robots.txt"
const DefaultAddsPerHour = 50
const MaxAddsPerHour = 500

// Config contains only settings supported by 0.10.0-1. ResolveVia is either
// empty (use sing-box dns.final) or sb:<tag>. Legacy resolver modes are never
// used for coverage.
type Config struct {
	AutoAdd           bool     `json:"autoAdd"`
	TargetInterface   string   `json:"targetInterface,omitempty"`
	ResolveVia        string   `json:"resolveVia,omitempty"`
	WANCheckURL       string   `json:"wanCheckUrl"`
	WANMonitorEnabled bool     `json:"wanMonitorEnabled"`
	TunnelProbe       bool     `json:"tunnelProbe"`
	UDPEnabled        bool     `json:"udpEnabled"`
	Verbose           bool     `json:"verbose"`
	AddsPerHour       int      `json:"addsPerHour"`
	ExcludeDomains    []string `json:"excludeDomains,omitempty"`
	ExcludeRuleSets   []string `json:"excludeRuleSets,omitempty"`
}

func Default() Config {
	return Config{
		WANCheckURL: DefaultWANCheckURL,
		TunnelProbe: true,
		AddsPerHour: DefaultAddsPerHour,
	}
}

func clone(cfg Config) Config {
	cfg.ExcludeDomains = append([]string(nil), cfg.ExcludeDomains...)
	cfg.ExcludeRuleSets = append([]string(nil), cfg.ExcludeRuleSets...)
	return cfg
}
