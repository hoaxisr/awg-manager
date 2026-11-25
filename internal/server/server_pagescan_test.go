package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/pagescan"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testutil"
)

type pageScanFixture struct {
	srv         *Server
	settings    *storage.SettingsStore
	inboundPort int
	devicePort  int
	tunnelHits  *atomic.Int32
}

func listenPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// newPageScanFixture поднимает fircd-заглушку с заданными туннелями, слушающий
// вход FIRC и слушающий device-proxy при работающем sing-box.
func newPageScanFixture(t *testing.T, engine string, tunnels []firc.Tunnel, ifaces []firc.SystemInterface) pageScanFixture {
	t.Helper()
	hits := &atomic.Int32{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/tunnels":
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"tunnels": tunnels})
		case "/api/v1/system/interfaces":
			_ = json.NewEncoder(w).Encode(map[string]any{"interfaces": ifaces})
		case "/api/v1/system/netfilter":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mock.Close)

	settings := storage.NewSettingsStore(t.TempDir())
	if err := settings.Update(func(st *storage.Settings) error {
		st.SingboxRouter.RoutingEngine = engine
		return nil
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	inboundPort, devicePort := listenPort(t), listenPort(t)
	tunnels = append(tunnels, firc.Tunnel{ID: "main", Device: "tunvless9", Description: "Main", Enable: true,
		Inbounds: []firc.TunnelInbound{{Port: uint16(inboundPort), Listen: "lo"}}})
	fircOp := firc.NewOperator(firc.Options{ConfigDir: t.TempDir(), InboundPort: uint16(inboundPort)})
	fircOp.SetClient(firc.NewHTTPClient(mock.URL, mock.Client()))

	store := deviceproxy.NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	if err := store.SaveInstance(deviceproxy.Instance{
		ID: "default", Name: "LAN", Enabled: true, ListenAll: true, Port: devicePort,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	// Живой sing-box: pid-файл указывает на сам тестовый процесс. Пишем его после
	// NewOperator, иначе тот примет «живой процесс» и заведёт чтение его логов.
	sbDir := t.TempDir()
	sb := singbox.NewOperator(singbox.OperatorDeps{Dir: sbDir, Binary: os.Args[0]})
	if err := os.WriteFile(filepath.Join(sbDir, "sing-box.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("pid file: %v", err)
	}
	if running, _ := sb.IsRunning(); !running {
		t.Fatal("fixture: sing-box must look running")
	}

	return pageScanFixture{
		srv: &Server{
			settings:       settings,
			fircHandler:    api.NewFircHandler(fircOp),
			deviceProxySvc: deviceproxy.NewService(deviceproxy.Deps{Store: store}),
			singboxOp:      sb,
		},
		settings:    settings,
		inboundPort: inboundPort,
		devicePort:  devicePort,
		tunnelHits:  hits,
	}
}

func loopbackURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

func proxyURLs(items []pagescan.ProxyOption) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.URL)
	}
	return out
}

func TestPageScanProxyURLProvider(t *testing.T) {
	ctx := context.Background()

	provide := func(fx pageScanFixture) string {
		return fx.srv.pageScanProxyURLProvider(fx.srv.pageScanFircTunnels())(ctx)
	}

	fx := newPageScanFixture(t, "firc", nil, nil)
	if got, want := provide(fx), loopbackURL(fx.inboundPort); got != want {
		t.Fatalf("engine=firc: provider = %q, want %q", got, want)
	}

	// Движок не FIRC: живой device-proxy не становится выходом режима auto.
	fx = newPageScanFixture(t, "sing-box", nil, nil)
	if got := provide(fx); got != "" {
		t.Fatalf("engine=sing-box: provider = %q, want empty", got)
	}

	// FIRC выбран, но не запущен — то же самое.
	fx = newPageScanFixture(t, "firc", nil, nil)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	fx.srv.fircHandler.Operator().SetClient(firc.NewHTTPClient(dead.URL, dead.Client()))
	if got := provide(fx); got != "" {
		t.Fatalf("fircd is down: provider = %q, want empty", got)
	}
}

// ifaceWithIPv4 — поднятый интерфейс с адресом IPv4, кроме loopback.
func ifaceWithIPv4(t *testing.T) (name, ip string) {
	t.Helper()
	all, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	for _, iface := range all {
		if ip := resolveInterfaceIPv4(iface.Name); ip != "" && iface.Flags&net.FlagUp != 0 {
			return iface.Name, ip
		}
	}
	t.Skip("нет интерфейса с IPv4, кроме loopback")
	return "", ""
}

// fircd слушает только адрес интерфейса из listen: 127.0.0.1 тогда закрыт или
// принадлежит другой программе (тот же 1099 — умолчание прокси для LAN).
func TestPageScanFircInbound_UsesListenAddress(t *testing.T) {
	ctx := context.Background()
	iface, ip := ifaceWithIPv4(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	foreign, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("порт %d занят на loopback: %v", port, err)
	}
	t.Cleanup(func() { _ = foreign.Close() })

	inbound := func(in firc.TunnelInbound, enable bool) *Server {
		in.Port = uint16(port)
		srv := newPageScanFixture(t, "firc", []firc.Tunnel{{ID: "t1", Device: "tunvless0", Enable: enable, Inbounds: []firc.TunnelInbound{in}}}, nil).srv
		op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir(), InboundPort: uint16(port)})
		op.SetClient(srv.fircHandler.Operator().Client())
		srv.fircHandler = api.NewFircHandler(op)
		return srv
	}
	want := "http://" + net.JoinHostPort(ip, strconv.Itoa(port))

	srv := inbound(firc.TunnelInbound{Listen: iface}, true)
	if got := srv.pageScanProxyURLProvider(srv.pageScanFircTunnels())(ctx); got != want {
		t.Errorf("provider = %q, want %q", got, want)
	}
	urls := proxyURLs(srv.pageScanProxyLister(srv.pageScanFircTunnels())(ctx))
	if !slices.Contains(urls, want) || slices.Contains(urls, loopbackURL(port)) {
		t.Errorf("proxies = %v, want %s without loopback", urls, want)
	}
	match := srv.pageScanRouterProxyMatcher(srv.pageScanFircTunnels())
	if !match(ctx, ip, strconv.Itoa(port)) || match(ctx, "127.0.0.1", strconv.Itoa(port)) {
		t.Errorf("matcher must accept %s:%d and refuse loopback", ip, port)
	}

	for name, srv := range map[string]*Server{
		"вход с паролем":   inbound(firc.TunnelInbound{Listen: iface, User: "u", Password: "p"}, true),
		"туннель выключен": inbound(firc.TunnelInbound{Listen: iface}, false),
		"интерфейса нет":   inbound(firc.TunnelInbound{Listen: "awgm-absent0"}, true),
	} {
		if got := srv.pageScanProxyURLProvider(srv.pageScanFircTunnels())(ctx); got != "" {
			t.Errorf("%s: provider = %q, want empty", name, got)
		}
	}
}

func TestPageScanProxyLister(t *testing.T) {
	openPort, authPort, offPort := listenPort(t), listenPort(t), listenPort(t)
	closedPort := testutil.FreePort(t)

	fx := newPageScanFixture(t, "firc", []firc.Tunnel{
		{ID: "t1", Device: "tunvless0", Description: "DE", Enable: true, Inbounds: []firc.TunnelInbound{
			{Port: uint16(openPort), Listen: "lo"},
			{Port: uint16(authPort), Listen: "lo", User: "u", Password: "p"},
			{Port: uint16(closedPort), Listen: "lo"},
		}},
		{ID: "t2", Device: "tunvless1", Enable: false, Inbounds: []firc.TunnelInbound{{Port: uint16(offPort), Listen: "lo"}}},
	}, nil)
	list := fx.srv.pageScanProxyLister(fx.srv.pageScanFircTunnels())

	got := proxyURLs(list(context.Background()))
	want := []string{loopbackURL(openPort), loopbackURL(fx.inboundPort), loopbackURL(fx.devicePort)}
	if !slices.Equal(got, want) {
		t.Fatalf("engine=firc: proxies = %v, want %v", got, want)
	}

	// На другом движке первым идёт device-proxy.
	if err := fx.settings.Update(func(st *storage.Settings) error {
		st.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	if got := proxyURLs(list(context.Background())); len(got) != 3 || got[0] != loopbackURL(fx.devicePort) {
		t.Fatalf("engine=sing-box: proxies = %v", got)
	}
}

func TestPageScanRouterProxyMatcher(t *testing.T) {
	const tunnelPort, authPort, offPort, foreignPort = 41001, 41002, 41003, 41004
	fx := newPageScanFixture(t, "firc", []firc.Tunnel{
		{ID: "t1", Device: "tunvless0", Enable: true, Inbounds: []firc.TunnelInbound{
			{Port: tunnelPort, Listen: "lo"},
			{Port: authPort, Listen: "127.0.0.1", User: "u", Password: "p"},
			{Port: foreignPort, Listen: "192.0.2.1"},
		}},
		{ID: "t2", Device: "tunvless1", Enable: false, Inbounds: []firc.TunnelInbound{{Port: offPort, Listen: "lo"}}},
	}, nil)
	match := fx.srv.pageScanRouterProxyMatcher(fx.srv.pageScanFircTunnels())

	for _, tc := range []struct {
		host string
		port string
		want bool
	}{
		{"127.0.0.1", strconv.Itoa(fx.inboundPort), true},
		{"127.0.0.1", strconv.Itoa(fx.devicePort), true},
		{"127.0.0.1", strconv.Itoa(tunnelPort), true},
		{"127.0.0.1", strconv.Itoa(authPort), true},
		{"127.0.0.1", strconv.Itoa(offPort), false},
		{"127.0.0.1", strconv.Itoa(foreignPort), false},
		{"127.0.0.1", "79", false},
		{"127.0.0.1", "2222", false},
		{"127.0.0.1", "0", false},
		{"127.0.0.1", "http", false},
		// Чужой адрес с известным портом — не прокси роутера.
		{"192.0.2.1", strconv.Itoa(fx.devicePort), false},
		{"192.0.2.1", strconv.Itoa(foreignPort), false},
	} {
		if got := match(context.Background(), tc.host, tc.port); got != tc.want {
			t.Errorf("match(%s, %s) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestPageScanInterfaceLister(t *testing.T) {
	all, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	up := ""
	for _, iface := range all {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			up = iface.Name
			break
		}
	}
	if up == "" {
		t.Skip("нет поднятого интерфейса, кроме loopback")
	}

	fx := newPageScanFixture(t, "firc", []firc.Tunnel{
		{ID: "t1", Device: up, Description: "DE", Enable: true},
		{ID: "t2", Device: "lo", Enable: true},
		{ID: "t3", Device: "awgm-absent0", Enable: true},
	}, []firc.SystemInterface{{ID: "lo", Role: "tunnel"}})
	got := fx.srv.pageScanInterfaceLister(fx.srv.pageScanFircTunnels())(context.Background())
	if len(got) != 1 || got[0].Name != up || got[0].Label != "DE ("+up+")" {
		t.Fatalf("interfaces = %+v, want only %s", got, up)
	}
}

func TestBuildPageScan_OptionsReadsFircTunnelsOnce(t *testing.T) {
	fx := newPageScanFixture(t, "firc", []firc.Tunnel{{ID: "t1", Device: "tunvless0", Enable: true}}, nil)
	rec := httptest.NewRecorder()
	fx.srv.buildPageScan(nil).GetOptions(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics/page-scan/options", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), loopbackURL(fx.devicePort)) {
		t.Fatalf("options: %d %s", rec.Code, rec.Body.String())
	}
	if got := fx.tunnelHits.Load(); got != 1 {
		t.Fatalf("fircd /tunnels requested %d times, want 1", got)
	}
}
