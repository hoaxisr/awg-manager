package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/ipinspect"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/pagescan"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// buildPageScan собирает сканер ресурсов страницы: откуда брать маршруты,
// прокси и туннели роутера.
func (s *Server) buildPageScan(ipInspect *ipinspect.Service) *api.PageScanHandler {
	fircTunnels := s.pageScanFircTunnels()
	return api.NewPageScanHandler(pagescan.NewService(pagescan.Options{
		IPInspect:          ipInspect,
		RouteChecker:       s.pageScanRouteChecker(),
		ProxyURLProvider:   s.pageScanProxyURLProvider(fircTunnels),
		InterfaceLister:    s.pageScanInterfaceLister(fircTunnels),
		ProxyLister:        s.pageScanProxyLister(fircTunnels),
		RouterProxyMatcher: s.pageScanRouterProxyMatcher(fircTunnels),
	}))
}

func (s *Server) pageScanRouteChecker() pagescan.RouteChecker {
	if s == nil || s.fircHandler == nil || s.fircHandler.Operator() == nil {
		return nil
	}
	fircOp := s.fircHandler.Operator()
	return func(ctx context.Context, domains []string) (map[string]pagescan.RouteInfo, error) {
		if s.settings != nil {
			st, err := s.settings.Get()
			if err != nil || st == nil || strings.TrimSpace(st.SingboxRouter.RoutingEngine) != "firc" {
				return nil, nil
			}
		}
		if !fircOp.IsRunning(ctx) {
			return nil, nil
		}
		checked := fircOp.CheckDomainsRouting(ctx, domains)
		out := make(map[string]pagescan.RouteInfo, len(checked))
		for dom, res := range checked {
			if res == nil {
				continue
			}
			out[dom] = pagescan.RouteInfo{
				Routed:        res.Routed,
				FakeIP:        res.FakeIP,
				RealIP:        res.RealIP,
				GroupID:       res.GroupID,
				GroupName:     res.GroupName,
				Outbound:      res.Outbound,
				OutboundLabel: res.OutboundLabel,
				Error:         res.Error,
			}
		}
		return out, nil
	}
}

func probeLocalTCP(host string, port int) bool {
	if host == "" || port <= 0 || port > 65535 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func isNetIfaceUp(name string) bool {
	iface, err := net.InterfaceByName(strings.TrimSpace(name))
	return err == nil && iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0
}

func resolveInterfaceIPv4(ifaceName string) string {
	iface, err := net.InterfaceByName(strings.TrimSpace(ifaceName))
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		v4 := ipNet.IP.To4()
		if v4 == nil || v4.IsLoopback() {
			continue
		}
		return v4.String()
	}
	return ""
}

func isLocalRouterHostIP(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		if ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (s *Server) resolveRouterProxyHost(ctx context.Context, ifaceOrAddr string) string {
	listen := strings.TrimSpace(ifaceOrAddr)
	switch {
	case listen == "", strings.EqualFold(listen, "lo"), listen == "127.0.0.1", listen == "0.0.0.0":
		return "127.0.0.1"
	case net.ParseIP(listen) != nil:
		return listen
	}
	if ip := resolveInterfaceIPv4(listen); ip != "" {
		return ip
	}
	if s != nil && s.ndmsQueries != nil && s.ndmsQueries.Interfaces != nil {
		if sysName := s.ndmsQueries.Interfaces.ResolveSystemName(ctx, listen); sysName != "" {
			if ip := resolveInterfaceIPv4(sysName); ip != "" {
				return ip
			}
		}
		if info, err := s.ndmsQueries.Interfaces.Get(ctx, listen); err == nil && info != nil {
			ip := strings.TrimSpace(strings.Split(info.Address, "/")[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return ""
}

func (s *Server) isFircRoutingSelected() bool {
	if s == nil || s.settings == nil {
		return false
	}
	st, err := s.settings.Get()
	return err == nil && st != nil && strings.TrimSpace(st.SingboxRouter.RoutingEngine) == "firc"
}

// pageScanProxyURLProvider — прокси режима auto: только вход FIRC. Прокси для
// LAN-устройств пользователь выбирает сам из списка, молча в него не уходим.
func (s *Server) pageScanProxyURLProvider(fircTunnels func(context.Context) []firc.Tunnel) pagescan.ProxyURLProvider {
	return func(ctx context.Context) string {
		if s == nil || s.fircHandler == nil || s.fircHandler.Operator() == nil {
			return ""
		}
		if s.settings != nil && !s.isFircRoutingSelected() {
			return ""
		}
		fircOp := s.fircHandler.Operator()
		port := fircOp.InboundPort()
		if port == 0 || !fircOp.IsRunning(ctx) {
			return ""
		}
		// fircd слушает только адрес интерфейса из listen (put_line в tunrun.c):
		// 127.0.0.1 — лишь при listen «lo».
		for _, t := range fircTunnels(ctx) {
			if !t.Enable {
				continue
			}
			for _, inb := range t.Inbounds {
				if inb.Port != port || inb.User != "" || inb.Password != "" {
					continue
				}
				if host := s.resolveRouterProxyHost(ctx, inb.Listen); probeLocalTCP(host, int(port)) {
					return "http://" + net.JoinHostPort(host, strconv.Itoa(int(port)))
				}
			}
		}
		return ""
	}
}

// pageScanFircTunnels отдаёт список туннелей fircd, общий для всех списков
// сканера: короткая память, чтобы один запрос не ходил за ним дважды.
func (s *Server) pageScanFircTunnels() func(context.Context) []firc.Tunnel {
	var (
		mu  sync.Mutex
		at  time.Time
		mem []firc.Tunnel
	)
	return func(ctx context.Context) []firc.Tunnel {
		if s == nil || s.fircHandler == nil || s.fircHandler.Operator() == nil {
			return nil
		}
		fircOp := s.fircHandler.Operator()
		client := fircOp.Client()
		if client == nil {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < 2*time.Second {
			return mem
		}
		tunnels, err := client.GetTunnels(ctx)
		if err != nil {
			tunnels, _ = fircOp.LoadTunnelsFile()
		}
		at, mem = time.Now(), tunnels
		return mem
	}
}

func (s *Server) pageScanInterfaceLister(fircTunnels func(context.Context) []firc.Tunnel) func(context.Context) []pagescan.InterfaceOption {
	return func(ctx context.Context) []pagescan.InterfaceOption {
		if s == nil {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		out := make([]pagescan.InterfaceOption, 0)
		seen := make(map[string]struct{})
		add := func(name, label string) {
			name = strings.TrimSpace(name)
			if name == "" || !isNetIfaceUp(name) {
				return
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			label = strings.TrimSpace(label)
			if label == "" {
				label = name
			} else if !strings.EqualFold(label, name) && !strings.Contains(label, name) {
				label = fmt.Sprintf("%s (%s)", label, name)
			}
			out = append(out, pagescan.InterfaceOption{
				Name:  name,
				Label: label,
			})
		}

		if s.fircHandler != nil && s.fircHandler.Operator() != nil {
			fircOp := s.fircHandler.Operator()
			if client := fircOp.Client(); client != nil {
				tunnels := fircTunnels(probeCtx)
				tunnelDesc := make(map[string]string, len(tunnels))
				for _, t := range tunnels {
					dev := strings.TrimSpace(t.Device)
					if dev != "" {
						tunnelDesc[dev] = strings.TrimSpace(t.Description)
					}
				}
				if ifaces, err := client.GetInterfaces(probeCtx); err == nil {
					var ownedProxies map[string]bool
					hasT2S := false
					for _, iface := range ifaces {
						if strings.HasPrefix(strings.ToLower(strings.TrimSpace(iface.ID)), "t2s") {
							hasT2S = true
							break
						}
					}
					if hasT2S && s.singboxOp != nil {
						ownedProxies = api.OwnedProxySet(s.singboxOp.ListOwnedProxies(probeCtx))
					}
					for _, opt := range filterPageScanFircInterfaces(ifaces, tunnelDesc, ownedProxies) {
						add(opt.Name, opt.Label)
					}
				}
				for _, t := range tunnels {
					if t.Enable {
						add(t.Device, t.Description)
					}
				}
			}
		}

		if s.tunnelService != nil {
			if list, err := s.tunnelService.List(probeCtx); err == nil {
				for _, t := range list {
					if t.State != tunnel.StateRunning {
						continue
					}
					add(t.InterfaceName, t.Name)
				}
			}
		}

		return out
	}
}

func filterPageScanFircInterfaces(ifaces []firc.SystemInterface, tunnelDesc map[string]string, ownedProxies map[string]bool) []pagescan.InterfaceOption {
	out := make([]pagescan.InterfaceOption, 0, len(ifaces))
	for _, iface := range ifaces {
		id := strings.TrimSpace(iface.ID)
		if id == "" {
			continue
		}
		// ownedProxies nil — владение не прочитано: t2s* прячем все (см. firc_handler).
		if strings.HasPrefix(strings.ToLower(id), "t2s") && (ownedProxies == nil || ownedProxies[strings.ToLower(id)]) {
			continue
		}
		_, isFircTunnel := tunnelDesc[id]
		role := strings.TrimSpace(iface.Role)
		isTunnelRole := strings.EqualFold(role, "tunnel") ||
			(strings.EqualFold(role, "wan") && ndmsquery.IsNonISPInterface(id))
		if !isTunnelRole && !isFircTunnel {
			continue
		}
		label := tunnelDesc[id]
		if label == "" {
			label = strings.TrimSpace(iface.Name)
		}
		if label == "" {
			label = strings.TrimSpace(iface.NDMSName)
		}
		out = append(out, pagescan.InterfaceOption{Name: id, Label: label})
	}
	return out
}

func (s *Server) pageScanProxyLister(fircTunnels func(context.Context) []firc.Tunnel) func(context.Context) []pagescan.ProxyOption {
	return func(ctx context.Context) []pagescan.ProxyOption {
		if s == nil {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		out := make([]pagescan.ProxyOption, 0)
		seen := make(map[string]struct{})
		add := func(rawURL, label string) {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				return
			}
			key := strings.ToLower(rawURL)
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			out = append(out, pagescan.ProxyOption{
				URL:   rawURL,
				Label: strings.TrimSpace(label),
			})
		}

		addFircProxies := func() {
			if s.fircHandler == nil || s.fircHandler.Operator() == nil {
				return
			}
			fircOp := s.fircHandler.Operator()
			if !fircOp.IsRunning(probeCtx) {
				return
			}
			for _, t := range fircTunnels(probeCtx) {
				if !t.Enable {
					continue
				}
				tName := strings.TrimSpace(t.Description)
				if tName == "" {
					tName = strings.TrimSpace(t.Device)
				}
				for _, inb := range t.Inbounds {
					if inb.Port == 0 || inb.User != "" || inb.Password != "" {
						continue
					}
					host := s.resolveRouterProxyHost(probeCtx, inb.Listen)
					if host != "" && probeLocalTCP(host, int(inb.Port)) {
						proxyURL := "http://" + net.JoinHostPort(host, strconv.Itoa(int(inb.Port)))
						label := fmt.Sprintf("AWGM Route %s (:%d)", tName, inb.Port)
						if tName == "" {
							label = fmt.Sprintf("AWGM Route inbound (%s:%d)", host, inb.Port)
						}
						add(proxyURL, label)
					}
				}
			}
		}

		addDeviceProxies := func() {
			if s.deviceProxySvc == nil {
				return
			}
			if s.singboxOp != nil {
				if running, _ := s.singboxOp.IsRunning(); !running {
					return
				}
			}
			for _, inst := range s.deviceProxySvc.GetSnapshot().Instances {
				if !inst.Enabled || inst.Auth.Enabled || inst.Port <= 0 {
					continue
				}
				host := "127.0.0.1"
				if !inst.ListenAll && inst.ListenInterface != "" && !strings.EqualFold(inst.ListenInterface, "lo") {
					host = s.resolveRouterProxyHost(probeCtx, inst.ListenInterface)
				}
				if host == "" || !probeLocalTCP(host, inst.Port) {
					continue
				}
				addr := net.JoinHostPort(host, strconv.Itoa(inst.Port))
				name := strings.TrimSpace(inst.Name)
				if name == "" {
					name = "Device proxy"
				}
				label := fmt.Sprintf("%s (%s)", name, addr)
				if outTag := strings.TrimSpace(inst.SelectedOutbound); outTag != "" {
					label = fmt.Sprintf("%s -> %s (%s)", name, outTag, addr)
				}
				add("http://"+addr, label)
			}
		}

		if s.settings == nil || s.isFircRoutingSelected() {
			addFircProxies()
			addDeviceProxies()
		} else {
			addDeviceProxies()
			addFircProxies()
		}

		return out
	}
}

func (s *Server) pageScanRouterProxyMatcher(fircTunnels func(context.Context) []firc.Tunnel) func(context.Context, string, string) bool {
	return func(ctx context.Context, host, portStr string) bool {
		if s == nil {
			return false
		}
		portNum, err := strconv.Atoi(strings.TrimSpace(portStr))
		if err != nil || portNum <= 0 || portNum > 65535 {
			return false
		}
		if !isLocalRouterHostIP(host) {
			return false
		}
		if ctx == nil {
			ctx = context.Background()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		matchesListen := func(listen string, all bool) bool {
			trimmed := strings.TrimSpace(listen)
			if all || trimmed == "" || trimmed == "0.0.0.0" {
				return true
			}
			resolved := s.resolveRouterProxyHost(probeCtx, trimmed)
			return resolved != "" && strings.EqualFold(resolved, host)
		}

		if s.fircHandler != nil && s.fircHandler.Operator() != nil {
			for _, t := range fircTunnels(probeCtx) {
				if !t.Enable {
					continue
				}
				for _, inb := range t.Inbounds {
					if int(inb.Port) == portNum && matchesListen(inb.Listen, false) {
						return true
					}
				}
			}
		}

		if s.deviceProxySvc != nil {
			for _, inst := range s.deviceProxySvc.GetSnapshot().Instances {
				if inst.Enabled && inst.Port == portNum && matchesListen(inst.ListenInterface, inst.ListenAll) {
					return true
				}
			}
		}

		return false
	}
}
