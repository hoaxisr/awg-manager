package wdtt

import "strings"

const (
	ConnModeWG  = "wg"
	ConnModeRaw = "raw"
)

// normalizeConnMode returns wg or raw (default wg).
func normalizeConnMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ConnModeRaw:
		return ConnModeRaw
	default:
		return ConnModeWG
	}
}

func (c ClientConfig) UsesWireGuard() bool {
	return normalizeConnMode(c.ConnMode) == ConnModeWG
}

func (c ServerConfig) UsesWireGuardRelay() bool {
	return normalizeConnMode(c.RelayMode) == ConnModeWG
}
