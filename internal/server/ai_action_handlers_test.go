package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/aiassistant"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type mockMihomoOrch struct {
	restarted bool
	reloaded  bool
	verified  bool
}

func (m *mockMihomoOrch) Restart(ctx context.Context) error {
	m.restarted = true
	return nil
}

func (m *mockMihomoOrch) Reload(ctx context.Context) error {
	m.reloaded = true
	return nil
}

func (m *mockMihomoOrch) Verify(ctx context.Context) (*aiassistant.ActionVerification, error) {
	m.verified = true
	return &aiassistant.ActionVerification{Status: "passed", Summary: "mock verified"}, nil
}

func TestAIActionHandlers_RoutingEngineAndReapply(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	mockOrch := &mockMihomoOrch{}
	s := &Server{
		settings:   store,
		mihomoOrch: mockOrch,
	}

	handlers := s.buildAIActionHandlers(nil)
	if handlers.CurrentRoutingEngine == nil {
		t.Fatal("CurrentRoutingEngine handler is nil")
	}

	eng, err := handlers.CurrentRoutingEngine(context.Background())
	if err != nil || eng != "sing-box" {
		t.Fatalf("expected sing-box, got %q (err: %v)", eng, err)
	}

	// Switch to mihomo
	if err := handlers.SwitchRoutingEngine(context.Background(), "mihomo"); err != nil {
		t.Fatalf("SwitchRoutingEngine mihomo failed: %v", err)
	}
	eng, err = handlers.CurrentRoutingEngine(context.Background())
	if err != nil || eng != "mihomo" {
		t.Fatalf("expected mihomo, got %q (err: %v)", eng, err)
	}

	// ReapplyRouting should call ReloadMihomo
	if err := handlers.ReapplyRouting(context.Background()); err != nil {
		t.Fatalf("ReapplyRouting failed: %v", err)
	}
	if !mockOrch.reloaded {
		t.Fatal("expected mockOrch.Reload to be called")
	}

	// VerifyRouting should verify mihomo
	ver, err := handlers.VerifyRouting(context.Background())
	if err != nil || ver.Status != "passed" || !mockOrch.verified {
		t.Fatalf("VerifyRouting mihomo failed: ver=%+v, err=%v", ver, err)
	}

	// Switch back to sing-box
	if err := handlers.SwitchRoutingEngine(context.Background(), "singbox"); err != nil {
		t.Fatalf("SwitchRoutingEngine singbox failed: %v", err)
	}
	eng, err = handlers.CurrentRoutingEngine(context.Background())
	if err != nil || eng != "sing-box" {
		t.Fatalf("expected sing-box after singbox switch, got %q", eng)
	}

	// Invalid engine rejection
	if err := handlers.SwitchRoutingEngine(context.Background(), "invalid-engine"); err == nil {
		t.Fatal("expected error for invalid engine")
	}
}

func TestServer_Shutdown_StopsAIServices(t *testing.T) {
	dir := t.TempDir()
	cfgStore, err := aiassistant.NewConfigStore(filepath.Join(dir, "ai-cfg.json"))
	if err != nil {
		t.Fatal(err)
	}
	embeddedMgr := aiassistant.NewEmbeddedManager(cfgStore)
	memStore, err := aiassistant.NewMemoryStore(filepath.Join(dir, "mem.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := aiassistant.NewService(nil)
	sentinel := aiassistant.NewSentinel(svc, memStore, aiassistant.ToolSources{}, aiassistant.NewActionRegistry(aiassistant.ActionHandlers{}))
	sentinel.Start()

	s := &Server{
		aiSentinel:    sentinel,
		aiEmbeddedMgr: embeddedMgr,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if s.aiSentinel != nil {
		t.Fatal("expected aiSentinel to be cleared after shutdown")
	}
	if s.aiEmbeddedMgr != nil {
		t.Fatal("expected aiEmbeddedMgr to be cleared after shutdown")
	}
}

func TestAIActionHandlers_ExecKeeneticGuards(t *testing.T) {
	s := &Server{}
	handlers := s.buildAIActionHandlers(nil)

	// Banned commands touching Wireguard2, port 1099, force-reinstall, cleanup
	banned := []string{
		"no interface Wireguard2",
		"interface Wireguard 2 down",
		"interface Wireguard   2 down",
		"ip static tcp ISP 1099 192.168.1.1 1099",
		"opkg install --force-reinstall foo.ipk",
		"awg-manager --cleanup",
		"",
	}

	for _, cmd := range banned {
		err := handlers.ExecKeenetic(context.Background(), cmd)
		if err == nil || (!strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "empty")) {
			t.Fatalf("expected ExecKeenetic to reject %q with guard error, got: %v", cmd, err)
		}
	}
}

func TestAIActionHandlers_FlushDNS(t *testing.T) {
	s := &Server{}
	handlers := s.buildAIActionHandlers(nil)

	if handlers.FlushDNS == nil {
		t.Fatal("expected FlushDNS handler to be non-nil")
	}
	if handlers.VerifyDNS == nil {
		t.Fatal("expected VerifyDNS handler to be non-nil")
	}

	ver, err := handlers.VerifyDNS(context.Background())
	if err != nil || ver == nil || ver.Status != "passed" {
		t.Fatalf("VerifyDNS failed: ver=%+v, err=%v", ver, err)
	}
}
