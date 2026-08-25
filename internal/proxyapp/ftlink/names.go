package ftlink

import "strings"

// TunnelNameFromClient — метка AWG-туннеля, связанного с freeturn-клиентом.
// Принимает ИМЯ, а не инстанс: в новом мире имя живёт в Record.Name (тот же
// разворот, что у wdttlink.TunnelNameFromClient).
func TunnelNameFromClient(name string) string {
	name = strings.TrimSpace(name)
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
