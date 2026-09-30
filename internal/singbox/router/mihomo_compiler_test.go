package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestCompileMihomoConfigFromInput_PureNoWrites(t *testing.T) {
	input := &MihomoCompileInput{
		RouterSettings: storage.SingboxRouterSettings{
			RoutingEngine:   "mihomo",
			Enabled:         true,
			MihomoMixedPort: 1099,
		},
		TunIface:      "netaid-tun",
		FinalOutbound: "DIRECT",
		Mode:          mihomo.RuntimeEnforced,
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("CompileMihomoConfigFromInput failed: %v", err)
	}

	if res == nil {
		t.Fatal("expected non-nil CompileResult")
	}

	if len(res.ConfigYAML) == 0 {
		t.Fatal("expected non-empty ConfigYAML")
	}

	if res.ConfigDigest == "" {
		t.Fatal("expected non-empty ConfigDigest")
	}

	if res.InputDigest == "" {
		t.Fatal("expected non-empty InputDigest")
	}

	// Verify required listeners include 1099
	has1099TCP := false
	has1099UDP := false
	for _, l := range res.RequiredListeners {
		if l.Port == 1099 && l.Protocol == "tcp" {
			has1099TCP = true
		}
		if l.Port == 1099 && l.Protocol == "udp" {
			has1099UDP = true
		}
	}
	if !has1099TCP || !has1099UDP {
		t.Fatalf("expected 1099 TCP and UDP required listeners: %+v", res.RequiredListeners)
	}
}

func TestCompileMihomoConfigFromInput_RuntimeOff(t *testing.T) {
	input := &MihomoCompileInput{
		RouterSettings: storage.SingboxRouterSettings{
			RoutingEngine: "singbox", // Not mihomo
			Enabled:       false,
		},
		Sidecar: true, // Sidecar with 0 bridge listeners -> RuntimeOff (P0-4)
		NativeResources: mihomo.NativeResources{
			Listeners: nil, // 0 listeners
		},
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Mode != mihomo.RuntimeOff {
		t.Fatalf("expected RuntimeOff, got %s", res.Mode)
	}
	if res.ConfigYAML != nil {
		t.Fatalf("expected nil ConfigYAML for RuntimeOff")
	}
}

type fakeNativeStoreMigration struct {
	groupsImported bool
	rulesImported  bool
}

func (f *fakeNativeStoreMigration) ImportLegacyGroups(groups []storage.ProxyGroup) error {
	f.groupsImported = true
	return nil
}

func (f *fakeNativeStoreMigration) ImportLegacyRules(lines []string) error {
	f.rulesImported = true
	return nil
}

func (f *fakeNativeStoreMigration) HasGroups() bool {
	return f.groupsImported
}

func (f *fakeNativeStoreMigration) HasRules() bool {
	return f.rulesImported
}

func TestMigrateLegacyMihomoResources(t *testing.T) {
	store := &fakeNativeStoreMigration{}
	groups := []storage.ProxyGroup{{Name: "Group1"}}
	rules := []mihomo.Rule{{Domain: []string{"example.com"}, Outbound: "DIRECT"}}

	err := MigrateLegacyMihomoResources(context.Background(), store, groups, rules)
	if err != nil {
		t.Fatalf("MigrateLegacyMihomoResources failed: %v", err)
	}

	if !store.groupsImported || !store.rulesImported {
		t.Fatalf("expected legacy groups and rules imported: groups=%v rules=%v", store.groupsImported, store.rulesImported)
	}

	// Re-run migration: should be a clean no-op
	err = MigrateLegacyMihomoResources(context.Background(), store, groups, rules)
	if err != nil {
		t.Fatalf("second migration call failed: %v", err)
	}
}

func TestCompileMihomoConfig_PrimaryTProxyListeners(t *testing.T) {
	input := &MihomoCompileInput{
		RouterSettings: storage.SingboxRouterSettings{
			RoutingEngine:   "mihomo",
			Enabled:         true,
			RoutingMode:     "tproxy",
			MihomoMixedPort: 1099,
		},
		FinalOutbound: "DIRECT",
		Mode:          mihomo.RuntimeEnforced,
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var has51271TCP, has51271UDP, has51272TCP, has1099, has9090 bool
	for _, l := range res.RequiredListeners {
		if l.Port == 51271 && l.Network == "tcp" {
			has51271TCP = true
		}
		if l.Port == 51271 && l.Network == "udp" {
			has51271UDP = true
		}
		if l.Port == 51272 && l.Network == "tcp" {
			has51272TCP = true
		}
		if l.Port == 1099 {
			has1099 = true
		}
		if l.Port == 9090 && l.Purpose == "external-controller" {
			has9090 = true
		}
	}

	if !has51271TCP || !has51271UDP || !has51272TCP || !has1099 || !has9090 {
		t.Fatalf("missing required listener in primary TProxy: 51271(tcp=%v,udp=%v), 51272(%v), 1099(%v), 9090(%v)",
			has51271TCP, has51271UDP, has51272TCP, has1099, has9090)
	}
}

func TestCompileMihomoConfig_PolicyTunListeners(t *testing.T) {
	input := &MihomoCompileInput{
		RouterSettings: storage.SingboxRouterSettings{
			RoutingEngine:   "mihomo",
			Enabled:         true,
			RoutingMode:     "policy-tun",
			MihomoMixedPort: 1099,
			FakeIPPool4:     "198.18.0.0/15",
		},
		TunIface:      "netaid-tun",
		FinalOutbound: "DIRECT",
		Mode:          mihomo.RuntimeEnforced,
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, l := range res.RequiredListeners {
		if l.Port == 51271 || l.Port == 51272 {
			t.Fatalf("policy-tun must not include transparent tproxy/redir listeners, found: %+v", l)
		}
	}

	var has1099, has9090 bool
	for _, l := range res.RequiredListeners {
		if l.Port == 1099 {
			has1099 = true
		}
		if l.Port == 9090 {
			has9090 = true
		}
	}
	if !has1099 || !has9090 {
		t.Fatalf("policy-tun should include 1099(%v) and 9090(%v)", has1099, has9090)
	}
}

func TestCompileMihomoConfig_SidecarOnlyListeners(t *testing.T) {
	input := &MihomoCompileInput{
		Sidecar: true,
		Mode:    mihomo.RuntimePermissive,
		NativeResources: mihomo.NativeResources{
			Listeners: []mihomo.Listener{
				{Name: "bridge-1", Port: 12005, Type: "mixed", Listen: "127.0.0.1", Proxy: "DIRECT"},
			},
		},
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, l := range res.RequiredListeners {
		if l.Port == 1099 {
			t.Fatalf("sidecar config must NOT include mixed port 1099: %+v", l)
		}
		if l.Port == 51271 || l.Port == 51272 {
			t.Fatalf("sidecar config must NOT include tproxy/redir ports: %+v", l)
		}
	}

	var hasBridge12005, has9090 bool
	for _, l := range res.RequiredListeners {
		if l.Port == 12005 {
			hasBridge12005 = true
		}
		if l.Port == 9090 {
			has9090 = true
		}
	}
	if !hasBridge12005 || !has9090 {
		t.Fatalf("sidecar should include bridge 12005(%v) and controller 9090(%v)", hasBridge12005, has9090)
	}
}

func TestCompileMihomoConfig_PortZeroDisabled(t *testing.T) {
	input := &MihomoCompileInput{
		RouterSettings: storage.SingboxRouterSettings{
			RoutingEngine:   "mihomo",
			Enabled:         true,
			RoutingMode:     "tproxy",
			MihomoMixedPort: 0, // Explicitly disabled
		},
		FinalOutbound: "DIRECT",
		Mode:          mihomo.RuntimeEnforced,
	}

	res, err := CompileMihomoConfigFromInput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, l := range res.RequiredListeners {
		if l.Port == 1099 {
			t.Fatalf("port 0 must disable listener, but found 1099: %+v", l)
		}
	}
}
