package main

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// probeExternalIP — подменяется в тестах: проверка ходит в интернет.
var probeExternalIP = testing.GetWANIPBound

// realityNetworkInfo detects whether the router has a public ("white") IPv4 on
// its WAN and which host clients should use for the direct REALITY entry.
func (a *app) realityNetworkInfo(ctx context.Context) vlessserver.RealityNetworkInfo {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wanAddr, extIP, keenDNSDirect, wanKernel string
	if a.ndmsQueries != nil {
		if a.ndmsQueries.KeenDNS != nil {
			if kd, err := a.ndmsQueries.KeenDNS.Get(ctx); err == nil && kd != nil && kd.DirectAccess() {
				keenDNSDirect = strings.TrimSpace(kd.Domain)
			}
		}
		// Канал провайдера, а не default-маршрут: тот может идти через VPN, и
		// адрес туннеля выглядел бы серым WAN.
		if iface := serveringress.ResolveWANInterface(ctx, a.ndmsQueries); iface != nil {
			wanAddr = strings.TrimSpace(iface.Address)
			wanKernel = a.ndmsQueries.Interfaces.ResolveSystemName(ctx, iface.ID)
		}
	}
	// Без имени ядра проба ушла бы по default-маршруту, возможно в туннель.
	if shouldProbeExternalIP(net.ParseIP(wanAddr)) && (wanAddr == "" || wanKernel != "") {
		if ext, err := probeExternalIP(ctx, wanKernel, nil); err == nil {
			extIP = strings.TrimSpace(ext)
		}
	}
	return classifyRealityNetworkInfo(wanAddr, extIP, keenDNSDirect)
}

func shouldProbeExternalIP(wanIP net.IP) bool {
	return wanIP == nil || isPublicUnicast(wanIP)
}

func classifyRealityNetworkInfo(wanAddr, extIP, keenDNSDirect string) vlessserver.RealityNetworkInfo {
	info := vlessserver.RealityNetworkInfo{
		WANAddress:    strings.TrimSpace(wanAddr),
		ExternalIP:    strings.TrimSpace(extIP),
		KeenDNSDomain: strings.TrimSpace(keenDNSDirect),
	}

	wanIP := net.ParseIP(info.WANAddress)
	parsedExtIP := net.ParseIP(info.ExternalIP)
	switch {
	case wanIP == nil && parsedExtIP == nil:
		info.Reason = "не удалось определить адрес WAN и внешний IP"
	case wanIP == nil:
		info.Reason = "адрес WAN-интерфейса неизвестен"
	case !isPublicUnicast(wanIP):
		info.GrayIP = true
		info.Reason = "на WAN-интерфейсе серый (приватный / CGNAT) адрес " + info.WANAddress
	case parsedExtIP == nil:
		info.Reason = "не удалось проверить внешний IP (нет ответа сервисов проверки)"
		info.WhiteIP = true
	case !wanIP.Equal(parsedExtIP):
		info.GrayIP = true
		info.Reason = "внешний IP " + info.ExternalIP + " не совпадает с адресом WAN " + info.WANAddress + " (NAT у провайдера)"
	default:
		info.WhiteIP = true
	}

	switch {
	case info.KeenDNSDomain != "":
		info.PublicHost = info.KeenDNSDomain
	case info.WhiteIP && info.ExternalIP != "":
		info.PublicHost = info.ExternalIP
	case info.WhiteIP && info.WANAddress != "":
		info.PublicHost = info.WANAddress
	case info.ExternalIP != "":
		info.PublicHost = info.ExternalIP
	}
	return info
}

func isPublicUnicast(ip net.IP) bool {
	if ip == nil || ip.To4() == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return !cgnat.Contains(ip)
}
