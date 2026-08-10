package wdtt

import "fmt"

// ServerListenAddrs returns UDP bind addresses wdtt-server opens (-listen, -listen-raw, …).
func (c ServerConfig) ServerListenAddrs() []string {
	cfg := normalizeServerConfig(c)
	out := []string{cfg.Listen, cfg.EffectiveRawListen()}
	if d := cfg.EffectiveDirectListen(); d != "" && d != cfg.Listen {
		out = append(out, d)
	}
	if cfg.WgPort > 0 {
		out = append(out, fmt.Sprintf("127.0.0.1:%d", cfg.WgPort))
	}
	return out
}
