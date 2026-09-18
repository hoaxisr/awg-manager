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

// RouterLANIP — LAN-адрес роутера на интерфейсе iface ("" если не определился).
// Отдельная функция поверх FirstIPv4 — ради шва: подменяется в тесте целиком,
// иначе проверка зависела бы от сети машины, на которой запущена.
//
// Имя интерфейса ДАЁТ ВЫЗЫВАЮЩИЙ (storage.DefaultInterface): пакет
// namespace-нейтрален и о настройках демона не знает, а литерал здесь был бы
// вторым определением одного и того же — разошлись бы молча.
var RouterLANIP = func(iface string) string { return FirstIPv4(iface) }
