package deviceproxy

import (
	"context"
	"errors"
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
	running      bool
	tags         []string
	lastSpec     *ExternalSpec
	lastSelector string
	lastMember   string
}

func (f *fakeSingboxOperator) ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error {
	f.lastSpec = &spec
	return nil
}
func (f *fakeSingboxOperator) TunnelTags() []string { return f.tags }
func (f *fakeSingboxOperator) IsRunning() bool      { return f.running }
func (f *fakeSingboxOperator) SetSelectorDefault(_ context.Context, selector, member string) error {
	f.lastSelector, f.lastMember = selector, member
	return nil
}

type fakeNDMSQuery struct{ addr string }

func (f *fakeNDMSQuery) GetInterfaceAddress(_ context.Context, _ string) (string, error) {
	return f.addr, nil
}

func TestService_SelectOutbound_HotSwitch(t *testing.T) {
	sb := &fakeSingboxOperator{running: true, tags: []string{"VLESS-RU"}}
	ndms := &fakeNDMSQuery{addr: "10.10.10.1"}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "direct"})

	s := NewService(Deps{Store: store, Singbox: sb, NDMSQuery: ndms})

	if err := s.SelectOutbound(context.Background(), "VLESS-RU"); err != nil {
		t.Fatalf("SelectOutbound: %v", err)
	}
	if sb.lastSelector != "device-proxy-selector" || sb.lastMember != "VLESS-RU" {
		t.Fatalf("selector switch not called: %+v", sb)
	}
	if store.Get().SelectedOutbound != "VLESS-RU" {
		t.Fatalf("storage not updated: %#v", store.Get())
	}
	// ApplyDeviceProxy must be called so config.json's selector.default stays in sync.
	if sb.lastSpec == nil {
		t.Fatalf("ApplyDeviceProxy was not called after SelectOutbound")
	}
}

func TestService_SelectOutbound_UnknownTag(t *testing.T) {
	sb := &fakeSingboxOperator{running: true}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	_ = store.Save(Config{Enabled: true, ListenAll: true, Port: 1099})
	s := NewService(Deps{Store: store, Singbox: sb})

	err := s.SelectOutbound(context.Background(), "nope")
	if err == nil || !errors.Is(err, ErrOutboundUnavailable) {
		t.Fatalf("got %v, want ErrOutboundUnavailable", err)
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
