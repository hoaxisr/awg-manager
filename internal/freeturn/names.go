package freeturn

import "strings"

// TunnelNameFromClient builds AWG tunnel label for a FreeTurn client instance.
func TunnelNameFromClient(inst ClientInstance) string {
	name := strings.TrimSpace(inst.Name)
	if name == "" {
		name = "FreeTurn"
	}
	const suffix = " ft"
	if !strings.HasSuffix(strings.ToLower(name), suffix) {
		name += " FT"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}
