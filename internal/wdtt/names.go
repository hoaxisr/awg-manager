package wdtt

import "strings"

// TunnelNameFromClient builds AWG tunnel label for a WDTT client instance.
func TunnelNameFromClient(inst ClientInstance) string {
	name := strings.TrimSpace(inst.Name)
	if name == "" {
		name = "WDTT"
	}
	const suffix = " wdtt"
	if !strings.HasSuffix(strings.ToLower(name), suffix) {
		name += suffix
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}
