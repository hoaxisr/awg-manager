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
	// По рунам, а не по байтам: срез на 60 байтах рвёт кириллицу пополам и
	// оставляет в имени туннеля битый UTF-8.
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	return name
}
