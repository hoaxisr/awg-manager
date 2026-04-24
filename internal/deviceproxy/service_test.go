package deviceproxy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func TestService_GetConfig_ReturnsDefault(t *testing.T) {
	s := newTestService(t)
	got := s.GetConfig()
	if got.Enabled {
		t.Fatalf("default should not be enabled")
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	return NewService(Deps{Store: store})
}

func TestService_ValidateConfig_PortConflict(t *testing.T) {
	s := newTestService(t)

	bad := Config{Enabled: true, ListenAll: true, Port: 1080}
	s.withTunnelInboundPorts([]int{1080, 1081}) // helper

	err := s.ValidateConfig(bad)
	if err == nil {
		t.Fatalf("expected port conflict error")
	}
}

func TestService_ValidateConfig_EmptyAuthUsername(t *testing.T) {
	s := newTestService(t)
	bad := Config{
		Enabled: true, ListenAll: true, Port: 1099,
		Auth: AuthSpec{Enabled: true, Username: "", Password: "p"},
	}
	err := s.ValidateConfig(bad)
	if err == nil {
		t.Fatalf("expected empty-username error")
	}
}

func TestService_SaveConfig_AppliesToSingbox(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	cfg := Config{
		Enabled:          true,
		ListenAll:        false,
		ListenInterface:  "Bridge0",
		Port:             1099,
		SelectedOutbound: "direct",
	}
	if err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("singbox spec was not applied")
	}
	if sb.lastSpec.ListenAddr != "10.10.10.1" {
		t.Fatalf("listen resolved to %q, want 10.10.10.1", sb.lastSpec.ListenAddr)
	}
	if sb.lastSpec.SelectedTag != "direct" {
		t.Fatalf("selected = %q", sb.lastSpec.SelectedTag)
	}

	// Persisted to storage
	got := store.Get()
	if got != cfg {
		t.Fatalf("stored != saved:\n got=%#v\nwant=%#v", got, cfg)
	}
}

type fakeSingboxOperator struct {
	running       bool
	tags          []string
	lastSpec      *ExternalSpec
	lastSpecNR    *ExternalSpec // ApplyDeviceProxyNoReload call
	lastSelector  string
	lastMember    string
	runtimeActive string // what GetSelectorActive returns
}

func (f *fakeSingboxOperator) ApplyDeviceProxy(_ context.Context, spec ExternalSpec) error {
	f.lastSpec = &spec
	return nil
}
func (f *fakeSingboxOperator) ApplyDeviceProxyNoReload(_ context.Context, spec ExternalSpec) error {
	f.lastSpecNR = &spec
	return nil
}
func (f *fakeSingboxOperator) TunnelTags() []string { return f.tags }
func (f *fakeSingboxOperator) IsRunning() bool      { return f.running }
func (f *fakeSingboxOperator) SetSelectorDefault(_ context.Context, selector, member string) error {
	f.lastSelector, f.lastMember = selector, member
	return nil
}
func (f *fakeSingboxOperator) GetSelectorActive(_ context.Context, _ string) (string, error) {
	if !f.running {
		return "", fmt.Errorf("not running")
	}
	return f.runtimeActive, nil
}

type fakeNDMSQuery struct{ addr string }

func (f *fakeNDMSQuery) GetInterfaceAddress(_ context.Context, _ string) (string, error) {
	return f.addr, nil
}

func TestService_SelectRuntimeOutbound_OnlyClashAPI(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, tags: []string{"VLESS-RU"}}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	if err := s.SelectRuntimeOutbound(context.Background(), "VLESS-RU"); err != nil {
		t.Fatalf("SelectRuntimeOutbound: %v", err)
	}
	if sb.lastSelector != "device-proxy-selector" || sb.lastMember != "VLESS-RU" {
		t.Fatalf("selector switch not called with expected args: %+v", sb)
	}
	// Storage must NOT be mutated — the switch is ephemeral.
	if got := store.Get().SelectedOutbound; got != "direct" {
		t.Fatalf("storage was written: SelectedOutbound=%q, want 'direct'", got)
	}
	// ApplyDeviceProxy must NOT have been called.
	if sb.lastSpec != nil {
		t.Fatalf("ApplyDeviceProxy called but shouldn't have been: %+v", sb.lastSpec)
	}
	if sb.lastSpecNR != nil {
		t.Fatalf("ApplyDeviceProxyNoReload called but shouldn't have been: %+v", sb.lastSpecNR)
	}
}

func TestService_SelectRuntimeOutbound_UnknownTag(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099})
	s := NewService(Deps{Store: store, Singbox: sb})

	err := s.SelectRuntimeOutbound(context.Background(), "nope")
	if err == nil || !errors.Is(err, ErrOutboundUnavailable) {
		t.Fatalf("got %v, want ErrOutboundUnavailable", err)
	}
}

// fakeSystemTunnelQuery is a test double for SystemTunnelQuery.
type fakeSystemTunnelQuery struct {
	tunnels []SystemTunnel
}

func (f *fakeSystemTunnelQuery) List(_ context.Context) ([]SystemTunnel, error) {
	return f.tunnels, nil
}

func TestService_ListOutbounds_IncludesSystemTunnels(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	sysTunnels := &fakeSystemTunnelQuery{
		tunnels: []SystemTunnel{
			{ID: "Wireguard0", InterfaceName: "nwg0", Description: "My VPN"},
		},
	}
	s := NewService(Deps{Store: store, SystemTunnels: sysTunnels})

	out := s.ListOutbounds(context.Background())

	found := false
	for _, ob := range out {
		if ob.Tag == "awg-sys-Wireguard0" {
			found = true
			if ob.Kind != "awg" {
				t.Fatalf("expected kind=awg, got %q", ob.Kind)
			}
			if ob.Label != "My VPN" {
				t.Fatalf("expected label=My VPN, got %q", ob.Label)
			}
			if ob.Detail != "nwg0" {
				t.Fatalf("expected detail=nwg0, got %q", ob.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("awg-sys-Wireguard0 not found in outbounds: %+v", out)
	}
}

func TestService_SaveConfig_AppliesToSingbox_SystemTunnels(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	sysTunnels := &fakeSystemTunnelQuery{
		tunnels: []SystemTunnel{
			{ID: "Wireguard0", InterfaceName: "nwg0", Description: "My VPN"},
		},
	}
	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms, SystemTunnels: sysTunnels})

	cfg := Config{
		Enabled:          true,
		ListenAll:        false,
		ListenInterface:  "Bridge0",
		Port:             1099,
		SelectedOutbound: "awg-sys-Wireguard0",
	}
	if err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if sb.lastSpec == nil {
		t.Fatalf("singbox spec was not applied")
	}

	found := false
	for _, a := range sb.lastSpec.AWGTargets {
		if a.TunnelID == "sys-Wireguard0" && a.KernelIface == "nwg0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sys-Wireguard0 AWGTarget not found in spec: %+v", sb.lastSpec.AWGTargets)
	}
}

func TestService_GetRuntimeState_Alive(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, runtimeActive: "VLESS-RU"}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})
	s := NewService(Deps{Store: store, Singbox: sb})

	got := s.GetRuntimeState(context.Background())
	if !got.Alive || got.ActiveTag != "VLESS-RU" || got.DefaultTag != "direct" {
		t.Fatalf("runtime = %#v", got)
	}
}

func TestService_GetRuntimeState_Dead(t *testing.T) {
	sb := &fakeSingboxOperator{running: false}
	store := NewStore(filepath.Join(t.TempDir(), "d.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})
	s := NewService(Deps{Store: store, Singbox: sb})

	got := s.GetRuntimeState(context.Background())
	if got.Alive || got.ActiveTag != "" {
		t.Fatalf("runtime = %#v, want Alive=false ActiveTag=''", got)
	}
	if got.DefaultTag != "direct" {
		t.Fatalf("DefaultTag = %q, want 'direct'", got.DefaultTag)
	}
}

func TestService_Reconcile_MissingTargetDisables(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{
		Enabled:          true,
		ListenAll:        true,
		Port:             1099,
		SelectedOutbound: "awg-ghost", // tunnel that no longer exists
	})

	bus := events.NewBus()
	_, evCh, unsub := bus.Subscribe()
	defer unsub()

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms, Bus: bus})
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := store.Get()
	if got.Enabled {
		t.Fatalf("expected disabled, got %#v", got)
	}
	if got.SelectedOutbound != "" {
		t.Fatalf("expected cleared SelectedOutbound, got %q", got.SelectedOutbound)
	}

	// Drain events, check that missing-target was published.
	sawMissing := false
	// Non-blocking read loop.
	for {
		select {
		case ev := <-evCh:
			if ev.Type == "deviceproxy:missing-target" {
				sawMissing = true
			}
		default:
			if !sawMissing {
				t.Fatalf("missing-target event was not published")
			}
			return
		}
	}
}
