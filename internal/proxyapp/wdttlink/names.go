package wdttlink

import "strings"

// TunnelNameFromClient — метка AWG-туннеля, связанного с wdtt-клиентом.
// Копия wdtt.TunnelNameFromClient (names.go:5) с одной адаптацией: на вход
// имя, а не ClientInstance — в новом мире имя инстанса живёт в Record.Name.
func TunnelNameFromClient(instanceName string) string {
	name := strings.TrimSpace(instanceName)
	if name == "" {
		name = "WDTT"
	}
	const suffix = " wdtt"
	if !strings.HasSuffix(strings.ToLower(name), suffix) {
		name += suffix
	}
	// По рунам, а не по байтам: срез на 60 байтах рвёт кириллицу пополам и
	// оставляет в имени туннеля битый UTF-8.
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	return name
}
