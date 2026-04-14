package singbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// fakeNDMS implements ndms.Client with Proxy tracking; all other methods are no-ops.
type fakeNDMS struct {
	creates []string
	deletes []string
	ups     []string
	downs   []string
	shows   map[string]bool // name -> exists
}

func newFakeNDMS() *fakeNDMS {
	return &fakeNDMS{shows: map[string]bool{}}
}

// --- Proxy methods (tracked) ---

func (f *fakeNDMS) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	f.creates = append(f.creates, name)
	f.shows[name] = true
	return nil
}
func (f *fakeNDMS) DeleteProxy(ctx context.Context, name string) error {
	f.deletes = append(f.deletes, name)
	delete(f.shows, name)
	return nil
}
func (f *fakeNDMS) ProxyUp(ctx context.Context, name string) error {
	f.ups = append(f.ups, name)
	return nil
}
func (f *fakeNDMS) ProxyDown(ctx context.Context, name string) error {
	f.downs = append(f.downs, name)
	return nil
}
func (f *fakeNDMS) ShowProxy(ctx context.Context, name string) (*ndms.ProxyInfo, error) {
	exists := f.shows[name]
	return &ndms.ProxyInfo{Name: name, Exists: exists, Up: exists}, nil
}

// --- All other ndms.Client methods: stub with zero values ---

func (f *fakeNDMS) CreateOpkgTun(ctx context.Context, name, description string) error  { return nil }
func (f *fakeNDMS) SetIPGlobal(ctx context.Context, name string) error                 { return nil }
func (f *fakeNDMS) DeleteOpkgTun(ctx context.Context, name string) error               { return nil }
func (f *fakeNDMS) OpkgTunExists(ctx context.Context, name string) bool                { return false }
func (f *fakeNDMS) ShowInterface(ctx context.Context, name string) (string, error)     { return "", nil }
func (f *fakeNDMS) SetAddress(ctx context.Context, name, address string) error         { return nil }
func (f *fakeNDMS) SetIPv6Address(ctx context.Context, name, address string) error     { return nil }
func (f *fakeNDMS) ClearIPv6Address(ctx context.Context, name string)                  {}
func (f *fakeNDMS) SetMTU(ctx context.Context, name string, mtu int) error             { return nil }
func (f *fakeNDMS) SetDNS(ctx context.Context, name string, servers []string) error    { return nil }
func (f *fakeNDMS) ClearDNS(ctx context.Context, name string, servers []string) error  { return nil }
func (f *fakeNDMS) SetDescription(ctx context.Context, name, description string) error { return nil }
func (f *fakeNDMS) InterfaceUp(ctx context.Context, name string) error                 { return nil }
func (f *fakeNDMS) InterfaceDown(ctx context.Context, name string) error               { return nil }
func (f *fakeNDMS) SetDefaultRoute(ctx context.Context, name string) error             { return nil }
func (f *fakeNDMS) RemoveDefaultRoute(ctx context.Context, name string) error          { return nil }
func (f *fakeNDMS) RemoveHostRoute(ctx context.Context, host string) error             { return nil }
func (f *fakeNDMS) SetIPv6DefaultRoute(ctx context.Context, name string) error         { return nil }
func (f *fakeNDMS) RemoveIPv6DefaultRoute(ctx context.Context, name string)            {}
func (f *fakeNDMS) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	return "", "", nil
}
func (f *fakeNDMS) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return "", nil
}
func (f *fakeNDMS) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	return nil, nil
}
func (f *fakeNDMS) QueryAllInterfaces(ctx context.Context) ([]ndms.AllInterface, error) {
	return nil, nil
}
func (f *fakeNDMS) DumpIPv4Routes(ctx context.Context) string                 { return "" }
func (f *fakeNDMS) HasWANIPv6(ctx context.Context, ifaceName string) bool     { return false }
func (f *fakeNDMS) GetSystemName(ctx context.Context, ndmsName string) string { return ndmsName }
func (f *fakeNDMS) Save(ctx context.Context) error                            { return nil }
func (f *fakeNDMS) RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeNDMS) RCIGet(ctx context.Context, path string) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeNDMS) ShowObjectGroupFQDN(ctx context.Context) ([]ndms.ObjectGroupFQDN, error) {
	return nil, nil
}
func (f *fakeNDMS) ShowDnsProxyRoute(ctx context.Context) ([]ndms.DnsProxyRoute, error) {
	return nil, nil
}
func (f *fakeNDMS) ListWireguardInterfaces(ctx context.Context) ([]ndms.WireguardInterfaceInfo, error) {
	return nil, nil
}
func (f *fakeNDMS) ListSystemWireguardTunnels(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (f *fakeNDMS) GetSystemWireguardTunnel(ctx context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	return nil, nil
}
func (f *fakeNDMS) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeNDMS) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	return nil
}
func (f *fakeNDMS) GetWireguardServer(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	return nil, nil
}
func (f *fakeNDMS) GetWireguardServerConfig(ctx context.Context, name string) (*ndms.WireguardServerConfig, error) {
	return nil, nil
}
func (f *fakeNDMS) ListAllWireguardServers(ctx context.Context) ([]ndms.WireguardServer, error) {
	return nil, nil
}
func (f *fakeNDMS) FindFreeWireguardIndex(ctx context.Context) (int, error) { return 0, nil }
func (f *fakeNDMS) ConfigurePingCheck(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
	return nil
}
func (f *fakeNDMS) RemovePingCheck(ctx context.Context, profile, ifaceName string) error { return nil }
func (f *fakeNDMS) ShowPingCheck(ctx context.Context, profile string) (*ndms.PingCheckStatus, error) {
	return nil, nil
}

// Compile-time check that fakeNDMS satisfies ndms.Client.
var _ ndms.Client = (*fakeNDMS)(nil)

func TestOperator_ListTunnels_NoConfig(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{
		Dir:  dir,
		NDMS: newFakeNDMS(),
	})
	list, err := op.ListTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty, got %d", len(list))
	}
}

func TestOperator_ConfigPaths(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir, NDMS: newFakeNDMS()})
	if op.configPath != filepath.Join(dir, "config.json") {
		t.Errorf("configPath: %s", op.configPath)
	}
	if op.pidPath != filepath.Join(dir, "sing-box.pid") {
		t.Errorf("pidPath: %s", op.pidPath)
	}
}

func TestParseProxyIdx(t *testing.T) {
	cases := []struct {
		in      string
		wantIdx int
		wantErr bool
	}{
		{"Proxy0", 0, false},
		{"Proxy42", 42, false},
		{"", 0, true},
		{"Proxy", 0, true},
		{"WrongPrefix0", 0, true},
		{"Proxy-1", -1, false}, // Sscanf accepts negative — that's OK, semantic validation elsewhere
	}
	for _, c := range cases {
		got, err := parseProxyIdx(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if err == nil && got != c.wantIdx {
			t.Errorf("%q: got=%d want=%d", c.in, got, c.wantIdx)
		}
	}
}
