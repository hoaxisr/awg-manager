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

func normalizeRelayMode(mode string) string {
	return normalizeConnMode(mode)
}

func (c ClientConfig) usesWireGuard() bool {
	return normalizeConnMode(c.ConnMode) == ConnModeWG
}

func (c ServerConfig) usesWireGuardRelay() bool {
	return normalizeRelayMode(c.RelayMode) == ConnModeWG
}
