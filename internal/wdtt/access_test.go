package wdtt

import (
	"context"
	"path/filepath"
	"testing"
)

type stubAccessManager struct {
	natCalls            int
	firewallPermitCalls int
	firewallPermitIface string
}

func (s *stubAccessManager) ApplyNATModeToInterface(context.Context, string, string, []string) ([]string, error) {
	s.natCalls++
	return nil, nil
}
func (s *stubAccessManager) ApplyPolicyToInterface(context.Context, string, string) error { return nil }
func (s *stubAccessManager) ApplyLANSegmentsToInterface(context.Context, string, string, string, []string) error {
	return nil
}
func (s *stubAccessManager) EnsureInterfaceFirewallPermit(_ context.Context, iface string) error {
	s.firewallPermitCalls++
	s.firewallPermitIface = iface
	return nil
}
func (s *stubAccessManager) KernelIfaceName(context.Context, string) string { return "" }
func (s *stubAccessManager) ResolveLANSegmentCIDRs(context.Context, []string) ([]string, error) {
	return nil, nil
}
func (s *stubAccessManager) DefaultGatewayNDMS(context.Context) (string, error) { return "", nil }

// ndmsServerConfig — конфиг на NDMS-пути: applyServerAccess отрабатывает
// целиком через accessMgr и не трогает iptables/ip, которых нет вне роутера.
func ndmsServerConfig() ServerConfig {
	cfg := DefaultServerConfig()
	cfg.NatMode = "full"
	cfg.NdmsIface = "OpkgTun17"
	cfg.WgIface = "opkgtun17"
	return cfg
}

func TestApplyServerAccessWithoutAccessManager(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	cfg := ndmsServerConfig()
	cfg.NatMode = "none"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess without accessMgr: %v", err)
	}
}

func TestApplyServerAccessCallsNDMSWhenWired(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	stub := &stubAccessManager{}
	svc.SetAccessManager(stub)
	cfg := ndmsServerConfig()
	cfg.NatMode = "none"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess: %v", err)
	}
	if stub.natCalls != 1 {
		t.Fatalf("NDMS NAT calls = %d, want 1", stub.natCalls)
	}
	if stub.firewallPermitCalls != 0 {
		t.Fatalf("firewall permit calls = %d, want 0 for natMode=none", stub.firewallPermitCalls)
	}
}

func TestApplyServerAccessSkipsFirewallPermitWhenNATDisabled(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	stub := &stubAccessManager{}
	svc.SetAccessManager(stub)
	cfg := ndmsServerConfig()
	cfg.NatMode = "none"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess: %v", err)
	}
	if stub.firewallPermitCalls != 0 {
		t.Fatalf("firewall permit calls = %d, want 0 for natMode=none", stub.firewallPermitCalls)
	}
}

func TestApplyServerAccessOpkgRawUsesNDMSAndEntware(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(dir, filepath.Join(dir, "run"), "", "")
	stub := &stubAccessManager{}
	svc.SetAccessManager(stub)
	cfg := ndmsServerConfig()
	cfg.RelayMode = ConnModeRaw
	cfg.NatMode = "none"
	if err := svc.applyServerAccess(context.Background(), "srv1", cfg); err != nil {
		t.Fatalf("applyServerAccess raw+opkg: %v", err)
	}
	if stub.natCalls != 1 {
		t.Fatalf("NDMS NAT calls = %d, want 1 (OpkgTun WG path)", stub.natCalls)
	}
}
