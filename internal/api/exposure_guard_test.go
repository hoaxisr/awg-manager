package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── fakes ────────────────────────────────────────────────────────

type fakeStaticNAT struct {
	entries []ndmsquery.StaticNATEntry
	err     error
}

func (f fakeStaticNAT) List(context.Context) ([]ndmsquery.StaticNATEntry, error) {
	return f.entries, f.err
}

type fakeHTTPProxy struct {
	entries []ndmsquery.HTTPProxyEntry
	err     error
}

func (f fakeHTTPProxy) List(context.Context) ([]ndmsquery.HTTPProxyEntry, error) {
	return f.entries, f.err
}

type fakeIfaces struct {
	byName map[string]ndms.Interface
	err    error
}

func (f fakeIfaces) Get(_ context.Context, name string) (*ndms.Interface, error) {
	if f.err != nil {
		return nil, f.err
	}
	iface, ok := f.byName[name]
	if !ok {
		return nil, nil
	}
	return &iface, nil
}

func (f fakeIfaces) List(context.Context) ([]ndms.Interface, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]ndms.Interface, 0, len(f.byName))
	for _, iface := range f.byName {
		out = append(out, iface)
	}
	return out, nil
}

// routerIfaces mirrors the stand: a public WAN and a private LAN bridge.
func routerIfaces() fakeIfaces {
	return fakeIfaces{byName: map[string]ndms.Interface{
		"PPPoE0": {ID: "PPPoE0", SecurityLevel: "public", Address: "91.144.142.72"},
		"Home":   {ID: "Home", SecurityLevel: "private", Address: "192.168.0.1"},
	}}
}

func guardWithPort(t *testing.T, port int, static fakeStaticNAT, proxies fakeHTTPProxy, ifaces fakeIfaces) (*ExposureGuard, *storage.SettingsStore) {
	t.Helper()
	tmp := t.TempDir()
	raw := `{"schemaVersion":2,"authEnabled":false,"server":{"port":` + strconv.Itoa(port) + `}}`
	if err := os.WriteFile(filepath.Join(tmp, "settings.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	store := storage.NewSettingsStore(tmp)
	if _, err := store.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	return NewExposureGuard(store, static, proxies, ifaces, nil), store
}

func authEnabled(t *testing.T, store *storage.SettingsStore) bool {
	t.Helper()
	s, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return s.AuthEnabled
}

// ── port forwards ────────────────────────────────────────────────

// Seeds are the shapes verified on a live router 2026-08-20.
func TestExposureGuard_PortForward(t *testing.T) {
	cases := []struct {
		name  string
		entry ndmsquery.StaticNATEntry
		want  bool
	}{
		{
			name:  "explicit to-port onto our port",
			entry: ndmsquery.StaticNATEntry{Interface: "PPPoE0", Protocol: "tcp", Port: "18022", ToPort: "2222", ToAddress: "192.168.0.1"},
			want:  true,
		},
		{
			// "this Keenetic" in the router UI: to-address is loopback and
			// to-port is omitted because it equals port.
			name:  "loopback, to-port omitted",
			entry: ndmsquery.StaticNATEntry{Interface: "PPPoE0", Protocol: "tcp", Port: "2222", ToAddress: "127.0.0.1"},
			want:  true,
		},
		{
			name:  "udp does not reach an HTTP server",
			entry: ndmsquery.StaticNATEntry{Interface: "PPPoE0", Protocol: "udp", Port: "2222", ToAddress: "127.0.0.1"},
			want:  false,
		},
		{
			name:  "forward to a LAN device that happens to use our port",
			entry: ndmsquery.StaticNATEntry{Interface: "PPPoE0", Protocol: "tcp", Port: "2222", ToAddress: "192.168.0.50"},
			want:  false,
		},
		{
			name:  "forward from a private interface opens nothing",
			entry: ndmsquery.StaticNATEntry{Interface: "Home", Protocol: "tcp", Port: "2222", ToAddress: "127.0.0.1"},
			want:  false,
		},
		{
			name:  "other port",
			entry: ndmsquery.StaticNATEntry{Interface: "PPPoE0", Protocol: "tcp", Port: "8080", ToPort: "9090", ToAddress: "127.0.0.1"},
			want:  false,
		},
		{
			// Plain static NAT rows share the table and carry no ports.
			name:  "static NAT row is not a forward",
			entry: ndmsquery.StaticNATEntry{Interface: "Wireguard4", ToInterface: "PPPoE0"},
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, store := guardWithPort(t, 2222,
				fakeStaticNAT{entries: []ndmsquery.StaticNATEntry{c.entry}},
				fakeHTTPProxy{}, routerIfaces())
			g.Check(context.Background())
			if got := authEnabled(t, store); got != c.want {
				t.Errorf("authEnabled = %v, want %v", got, c.want)
			}
		})
	}
}

// ── ip http proxy ────────────────────────────────────────────────

func TestExposureGuard_HTTPProxy(t *testing.T) {
	cases := []struct {
		name  string
		entry ndmsquery.HTTPProxyEntry
		want  bool
	}{
		{
			name:  "public without auth",
			entry: ndmsquery.HTTPProxyEntry{Name: "awgm", UpstreamPort: "2222", Public: true},
			want:  true,
		},
		{
			// The router holds authentication in front of us.
			name:  "public with auth",
			entry: ndmsquery.HTTPProxyEntry{Name: "awgm", UpstreamPort: "2222", Public: true, Auth: true},
			want:  false,
		},
		{
			name:  "private without auth",
			entry: ndmsquery.HTTPProxyEntry{Name: "awgm", UpstreamPort: "2222"},
			want:  false,
		},
		{
			name:  "another application",
			entry: ndmsquery.HTTPProxyEntry{Name: "transmission", UpstreamPort: "9091", Public: true},
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, store := guardWithPort(t, 2222, fakeStaticNAT{},
				fakeHTTPProxy{entries: []ndmsquery.HTTPProxyEntry{c.entry}}, routerIfaces())
			g.Check(context.Background())
			if got := authEnabled(t, store); got != c.want {
				t.Errorf("authEnabled = %v, want %v", got, c.want)
			}
		})
	}
}

// auth:true on the proxy protects only the proxy path — a port forward
// lands on our socket directly, with no authentication in front of it.
func TestExposureGuard_ProxyAuthDoesNotExcuseForward(t *testing.T) {
	g, store := guardWithPort(t, 2222,
		fakeStaticNAT{entries: []ndmsquery.StaticNATEntry{
			{Interface: "PPPoE0", Protocol: "tcp", Port: "2222", ToAddress: "127.0.0.1"},
		}},
		fakeHTTPProxy{entries: []ndmsquery.HTTPProxyEntry{
			{Name: "awgm", UpstreamPort: "2222", Public: true, Auth: true},
		}}, routerIfaces())
	g.Check(context.Background())
	if !authEnabled(t, store) {
		t.Error("authEnabled = false, want true (forward bypasses the router's proxy auth)")
	}
}

// ── fail-open ────────────────────────────────────────────────────

func TestExposureGuard_FailOpen(t *testing.T) {
	boom := errors.New("ndms unreachable")
	cases := []struct {
		name    string
		static  fakeStaticNAT
		proxies fakeHTTPProxy
		ifaces  fakeIfaces
	}{
		{name: "ip static unreadable", static: fakeStaticNAT{err: boom}, ifaces: routerIfaces()},
		{name: "ip http unreadable", proxies: fakeHTTPProxy{err: boom}, ifaces: routerIfaces()},
		{
			name: "interfaces unreadable",
			static: fakeStaticNAT{entries: []ndmsquery.StaticNATEntry{
				{Interface: "PPPoE0", Protocol: "tcp", Port: "2222", ToAddress: "192.168.0.1"},
			}},
			ifaces: fakeIfaces{err: boom},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, store := guardWithPort(t, 2222, c.static, c.proxies, c.ifaces)
			g.Check(context.Background())
			if authEnabled(t, store) {
				t.Error("authEnabled = true, want false (unknown state must not flip the flag)")
			}
		})
	}
}

// The guard never turns authentication off.
func TestExposureGuard_NeverDisables(t *testing.T) {
	g, store := guardWithPort(t, 2222, fakeStaticNAT{}, fakeHTTPProxy{}, routerIfaces())
	if _, err := store.SetAuthEnabled(true); err != nil {
		t.Fatalf("SetAuthEnabled: %v", err)
	}
	g.Check(context.Background())
	if !authEnabled(t, store) {
		t.Error("authEnabled = false, want true (guard must not disable auth)")
	}
}
