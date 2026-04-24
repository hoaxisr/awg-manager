package deviceproxy

import (
	"context"
	"path/filepath"
	"testing"
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
	running  bool
	lastSpec *ExternalSpec
}

func (f *fakeSingboxOperator) ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error {
	f.lastSpec = &spec
	return nil
}
func (f *fakeSingboxOperator) TunnelTags() []string { return nil }
func (f *fakeSingboxOperator) IsRunning() bool      { return f.running }
func (f *fakeSingboxOperator) SetSelectorDefault(_ context.Context, _, _ string) error {
	return nil
}

type fakeNDMSQuery struct{ addr string }

func (f *fakeNDMSQuery) GetInterfaceAddress(_ context.Context, _ string) (string, error) {
	return f.addr, nil
}
