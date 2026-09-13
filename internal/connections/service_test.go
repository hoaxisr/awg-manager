package connections

import (
	"context"
	"net"
	"testing"
)

// enrichSvc собирает Service с инжектированными адресами интерфейсов и
// марк-провайдером — без NDMS и каталога (они в classify не участвуют).
func enrichSvc(addrs map[string][]string, mark string) *Service {
	s := &Service{
		ifaceAddrs: func(name string) ([]net.Addr, error) {
			var out []net.Addr
			for _, a := range addrs[name] {
				_, ipn, _ := net.ParseCIDR(a)
				ip, _, _ := net.ParseCIDR(a)
				ipn.IP = ip
				out = append(out, ipn)
			}
			return out, nil
		},
	}
	if mark != "" {
		s.sbMarkProvider = func(ctx context.Context) (string, bool) { return mark, true }
	}
	return s
}

func TestClassify_IfwTunnel(t *testing.T) {
	s := enrichSvc(nil, "")
	tunnels := map[string]tunnelInfo{"nwg2": {id: "tun_1", name: "VPS"}}
	c := s.classify(rawConn{Connection: Connection{Src: "192.168.0.5", Dst: "1.2.3.4"}, ifw: 32},
		map[int]string{32: "nwg2"}, tunnels, s.tunnelLocalIPs(tunnels), nil, 0, false)
	if c.RouteClass != "tunnel" || c.TunnelID != "tun_1" {
		t.Errorf("routeClass/tunnelId = %q/%q, want tunnel/tun_1", c.RouteClass, c.TunnelID)
	}
}

func TestClassify_NoIfw_SrcIsTunnelLocalIP(t *testing.T) {
	// Эгресс sing-box, привязанный к nwg1: src = локальный IP nwg1.
	s := enrichSvc(map[string][]string{"nwg1": {"172.16.6.1/24"}}, "")
	tunnels := map[string]tunnelInfo{"nwg1": {id: "tun_nwg1", name: "NWG"}}
	c := s.classify(rawConn{Connection: Connection{Src: "172.16.6.1", Dst: "8.8.8.8"}},
		nil, tunnels, s.tunnelLocalIPs(tunnels), nil, 0, false)
	if c.RouteClass != "tunnel" || c.TunnelID != "tun_nwg1" {
		t.Errorf("routeClass/tunnelId = %q/%q, want tunnel/tun_nwg1", c.RouteClass, c.TunnelID)
	}
	if c.Interface != "nwg1" {
		t.Errorf("Interface = %q, want nwg1", c.Interface)
	}
}

func TestClassify_NoIfw_ReplyDstSNAT(t *testing.T) {
	// Policy-routing SNAT: reply-dst = локальный IP туннеля (живой кейс со стенда).
	s := enrichSvc(map[string][]string{"nwg2": {"10.8.1.3/32"}}, "")
	tunnels := map[string]tunnelInfo{"nwg2": {id: "tun_2", name: "VPS2"}}
	c := s.classify(rawConn{Connection: Connection{Src: "172.16.6.4", Dst: "172.217.114.4"}, replyDst: "10.8.1.3"},
		nil, tunnels, s.tunnelLocalIPs(tunnels), nil, 0, false)
	if c.RouteClass != "tunnel" || c.TunnelID != "tun_2" {
		t.Errorf("routeClass/tunnelId = %q/%q, want tunnel/tun_2", c.RouteClass, c.TunnelID)
	}
}

func TestClassify_NoIfw_ReplyDstWAN(t *testing.T) {
	// Форвардный SNAT в WAN без ifw: трафик клиента напрямую, НЕ «Локально».
	s := enrichSvc(map[string][]string{"eth3": {"203.0.113.72/24"}}, "")
	wanIPs := s.wanLocalIPs([]string{"eth3"})
	c := s.classify(rawConn{Connection: Connection{Src: "192.168.0.54", Dst: "198.51.100.232"}, replyDst: "203.0.113.72"},
		nil, nil, nil, wanIPs, 0, false)
	if c.RouteClass != "direct" || c.Interface != "eth3" {
		t.Errorf("routeClass/iface = %q/%q, want direct/eth3", c.RouteClass, c.Interface)
	}
}

func TestClassify_NoIfw_SingboxMark(t *testing.T) {
	// tproxy-нога LAN-клиента: connmark == PolicyMark sb-router (0xffffaaa = 268434090... используем 0xff = 255).
	s := enrichSvc(nil, "0xff")
	c := s.classify(rawConn{Connection: Connection{Src: "192.168.0.54", Dst: "1.1.1.1"}, mark: 0xff},
		nil, nil, nil, nil, 0xff, true)
	if c.RouteClass != "singbox" || c.TunnelName != "sing-box" {
		t.Errorf("routeClass/name = %q/%q, want singbox/sing-box", c.RouteClass, c.TunnelName)
	}
}

func TestClassify_NoIfw_Local(t *testing.T) {
	s := enrichSvc(nil, "")
	c := s.classify(rawConn{Connection: Connection{Src: "203.0.113.72", Dst: "9.9.9.9"}},
		nil, nil, nil, nil, 0, false)
	if c.RouteClass != "local" || c.TunnelName != "Локально" {
		t.Errorf("routeClass/name = %q/%q, want local/Локально", c.RouteClass, c.TunnelName)
	}
}

func TestApplyFilters_StateAndRouteClass(t *testing.T) {
	conns := []Connection{
		{Src: "a", State: "ESTABLISHED", RouteClass: "direct"},
		{Src: "b", State: "TIME_WAIT", RouteClass: "singbox"},
		{Src: "c", State: "ESTABLISHED", RouteClass: "local"},
	}
	got := applyFilters(conns, ListParams{Tunnel: "all", Protocol: "all", State: "ESTABLISHED"})
	if len(got) != 2 {
		t.Fatalf("state filter: got %d, want 2", len(got))
	}
	got = applyFilters(conns, ListParams{Tunnel: "singbox", Protocol: "all"})
	if len(got) != 1 || got[0].Src != "b" {
		t.Fatalf("tunnel=singbox: got %d, want 1 (b)", len(got))
	}
	got = applyFilters(conns, ListParams{Tunnel: "local", Protocol: "all"})
	if len(got) != 1 || got[0].Src != "c" {
		t.Fatalf("tunnel=local: got %d, want 1 (c)", len(got))
	}
	got = applyFilters(conns, ListParams{Tunnel: "direct", Protocol: "all"})
	if len(got) != 1 || got[0].Src != "a" {
		t.Fatalf("tunnel=direct: got %d, want 1 (a)", len(got))
	}
}
