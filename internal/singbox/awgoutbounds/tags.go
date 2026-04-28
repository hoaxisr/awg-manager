// internal/singbox/awgoutbounds/tags.go
package awgoutbounds

import "strings"

const (
	managedPrefix = "awg-"
	systemPrefix  = "awg-sys-"
)

// ManagedTag returns the canonical sing-box outbound tag for a managed
// AWG tunnel.
func ManagedTag(tunnelID string) string { return managedPrefix + tunnelID }

// SystemTag returns the canonical sing-box outbound tag for a system
// (NDMS-managed, e.g. NativeWG / OpkgTun) tunnel.
func SystemTag(tunnelID string) string { return systemPrefix + tunnelID }

// IsAWGTag reports whether tag belongs to the awg-* namespace owned by
// the awgoutbounds package.
func IsAWGTag(tag string) bool { return strings.HasPrefix(tag, managedPrefix) }

// IsSystemTag is the stricter variant — true only for awg-sys-* tags.
func IsSystemTag(tag string) bool { return strings.HasPrefix(tag, systemPrefix) }
