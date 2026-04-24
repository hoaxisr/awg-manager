package deviceproxy

import (
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
