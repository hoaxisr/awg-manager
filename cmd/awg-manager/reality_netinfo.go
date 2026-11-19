package main

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// realityNetworkInfo detects whether the router has a public ("white") IPv4 on
// its WAN and which host clients should use for the direct REALITY entry.
func (a *app) realityNetworkInfo(ctx context.Context) vlessserver.RealityNetworkInfo {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var info vlessserver.RealityNetworkInfo
	keenDNSDirect := ""
	var wanKernel string
	if a.ndmsQueries != nil {
		if a.ndmsQueries.KeenDNS != nil {
			if kd, err := a.ndmsQueries.KeenDNS.Get(ctx); err == nil && kd != nil && kd.DirectAccess() {
				keenDNSDirect = strings.TrimSpace(kd.Domain)
			}
		}
		if addr, err := a.ndmsQueries.WANInterfaceAddress(ctx); err == nil {
			info.WANAddress = strings.TrimSpace(addr)
		}
		if a.ndmsQueries.Routes != nil && a.ndmsQueries.Interfaces != nil {
			if name, err := a.ndmsQueries.Routes.GetDefaultGatewayInterface(ctx); err == nil && name != "" {
				wanKernel = a.ndmsQueries.Interfaces.ResolveSystemName(ctx, name)
			}
		}
	}
	if ext, err := testing.GetWANIPBound(ctx, wanKernel, nil); err == nil {
		info.ExternalIP = strings.TrimSpace(ext)
	}
	info.KeenDNSDomain = keenDNSDirect

	wanIP := net.ParseIP(info.WANAddress)
	extIP := net.ParseIP(info.ExternalIP)
	switch {
	case wanIP == nil && extIP == nil:
		info.Reason = "не удалось определить адрес WAN и внешний IP"
	case wanIP == nil:
		info.Reason = "адрес WAN-интерфейса неизвестен"
	case !isPublicUnicast(wanIP):
		info.Reason = "на WAN-интерфейсе серый (приватный / CGNAT) адрес " + info.WANAddress
	case extIP == nil:
		info.Reason = "не удалось проверить внешний IP (нет ответа сервисов проверки)"
		info.WhiteIP = true
	case !wanIP.Equal(extIP):
		info.Reason = "внешний IP " + info.ExternalIP + " не совпадает с адресом WAN " + info.WANAddress + " (NAT у провайдера)"
	default:
		info.WhiteIP = true
	}

	switch {
	case keenDNSDirect != "":
		info.PublicHost = keenDNSDirect
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
