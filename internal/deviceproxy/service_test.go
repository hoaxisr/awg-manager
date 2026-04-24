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
