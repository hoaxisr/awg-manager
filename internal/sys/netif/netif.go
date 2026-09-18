// Package netif provides small network-interface helpers shared across the
// daemon, replacing several copy-pasted getBr0IP/getInterfaceIP functions.
package netif

import "net"

// FirstIPv4 returns the first IPv4 address (dotted-decimal) assigned to iface,
// or "" if the interface is absent or has no IPv4. Handles both *net.IPNet
// (the usual case) and *net.IPAddr address shapes.
func FirstIPv4(iface string) string {
	ni, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := ni.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	return ""
}

// RouterLANIP — LAN-адрес самого роутера ("" если не определился). Отдельная
// функция, а не FirstIPv4(«br0») по месту: интерфейс LAN выбирается в одном
// месте, и подменить её в тесте можно целиком — иначе проверка зависела бы от
// сети машины, на которой тест запущен.
//
// Имя интерфейса здесь литералом, а не storage.DefaultInterface: пакет
// namespace-нейтрален и о настройках демона не знает. Значение то же.
var RouterLANIP = func() string { return FirstIPv4("br0") }
