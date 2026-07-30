package wdtt

import (
	"context"
	"path/filepath"
	"testing"
)

type stubAccessManager struct {
	natCalls int
}

func (s *stubAccessManager) ApplyNATModeToInterface(context.Context, string, string, string) (string, error) {
	s.natCalls++
	return "", nil
}
func (s *stubAccessManager) ApplyPolicyToInterface(context.Context, string, string) error { return nil }
func (s *stubAccessManager) ApplyLANSegmentsToInterface(context.Context, string, string, string, []string) error {
	return nil
}
func (s *stubAccessManager) EnsureInterfaceFirewallPermit(context.Context, string) error { return nil }
func (s *stubAccessManager) KernelIfaceName(context.Context, string) string             { return "" }
func (s *stubAccessManager) ResolveLANSegmentCIDRs(context.Context, []string) ([]string, error) {
	return nil, nil
}
func (s *stubAccessManager) DefaultGatewayNDMS(context.Context) (string, error) { return "", nil }

func TestApplyServerAccessWithoutAccessManager(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	cfg := DefaultServerConfig()
	cfg.NatMode = "full"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess without accessMgr: %v", err)
	}
}

func TestApplyServerAccessCallsNDMSWhenWired(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	stub := &stubAccessManager{}
	svc.SetAccessManager(stub)
	cfg := DefaultServerConfig()
	cfg.NatMode = "full"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess: %v", err)
	}
	if stub.natCalls != 1 {
		t.Fatalf("NDMS NAT calls = %d, want 1", stub.natCalls)
	}
}
