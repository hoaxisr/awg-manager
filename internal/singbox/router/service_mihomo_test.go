package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"gopkg.in/yaml.v3"
)

type fakeSubscriptionOutboundSource struct {
	outbounds []map[string]any
}

func (f *fakeSubscriptionOutboundSource) SubscriptionOutbounds() []map[string]any {
	return append([]map[string]any(nil), f.outbounds...)
}

// GenerateMihomoConfig is a test helper for router package unit tests.
// It exercises CompileMihomoConfig and writes the resulting config.yaml for test assertions.
// In production runtime, all configuration writing is exclusively performed by ApplyCoordinator.
func (s *ServiceImpl) GenerateMihomoConfig() error {
	res, err := s.CompileMihomoConfig(context.Background())
	if err != nil {
		return err
	}
	configDir := s.deps.MihomoConfigDir
	if configDir == "" && s.deps.Engine != nil {
		configDir = s.deps.Engine.ConfigDir()
	}
	if configDir == "" {
		return fmt.Errorf("Mihomo config directory is unavailable")
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create mihomo config directory: %w", err)
	}
	cfgPath := filepath.Join(configDir, "config.yaml")
	if res.Mode == mihomo.RuntimeOff {
		_ = os.Remove(cfgPath)
		return nil
	}
	return os.WriteFile(cfgPath, res.ConfigYAML, 0644)
}

func newMihomoConfigTestService(t *testing.T, routingEngine string) (*ServiceImpl, string) {
	t.Helper()

	singboxDir := t.TempDir()
	mihomoDir := t.TempDir()
	orch := orchestrator.New(singboxDir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatalf("register SlotRouter: %v", err)
	}
	if err := orch.SetEnabledSilent(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("enable SlotRouter: %v", err)
	}

	cfg := NewEmptyConfig()
	cfg.Outbounds = []Outbound{
		{Type: "selector", Tag: "route-group", Outbounds: []string{"sub-a", "direct"}},
		{Type: "direct", Tag: "direct"},
	}
	cfg.Route.Rules = []Rule{
		{DomainSuffix: []string{"example.com"}, Outbound: "route-group"},
	}
	cfg.Route.Final = "direct"
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	if err := orch.SaveSilent(orchestrator.SlotRouter, data); err != nil {
		t.Fatalf("SaveSilent SlotRouter: %v", err)
	}

	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingMode:   "tproxy",
		RoutingEngine: routingEngine,
		DeviceMode:    "all",
		WANAutoDetect: true,
	})

	svc := &ServiceImpl{deps: Deps{
		Settings: store,
		Engine:   &fakeSingbox{dir: mihomoDir},
		Orch:     orch,
		SubscriptionComposites: NewSubscriptionCompositesAdapter(&fakeSubscriptionOutboundSource{
			outbounds: []map[string]any{
				{
					"type":        "vless",
					"tag":         "sub-a",
					"server":      "sub.example",
					"server_port": 443,
					"uuid":        "11111111-1111-1111-1111-111111111111",
				},
			},
		}),
		DeviceProxyInstances: func() []DeviceProxyInstance {
			return []DeviceProxyInstance{{
				ID:               "dev1",
				Port:             1099,
				SelectedOutbound: "route-group",
				Enabled:          true,
			}}
		},
	}}
	return svc, mihomoDir
}

func attachNativeBridge(t *testing.T, svc *ServiceImpl, port int) *mihomonative.Store {
	t.Helper()
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := native.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#Native", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: port, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	}); err != nil {
		t.Fatal(err)
	}
	svc.deps.MihomoNativeProxies = native
	return native
}

func TestGenerateMihomoConfig_WritesConfigForMihomoEngine(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}

	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if cfg.TProxyPort != 51271 || cfg.RedirPort != 51272 {
		t.Fatalf("unexpected ports: tproxy=%d redir=%d", cfg.TProxyPort, cfg.RedirPort)
	}
	if len(cfg.Proxies) != 1 || cfg.Proxies[0]["name"] != "sub-a" {
		t.Fatalf("subscription proxy not materialized correctly: %#v", cfg.Proxies)
	}
	if len(cfg.ProxyGroups) == 0 || cfg.ProxyGroups[0].Name != "route-group" {
		t.Fatalf("route-group not materialized: %#v", cfg.ProxyGroups)
	}
	if len(cfg.Listeners) != 0 {
		t.Fatalf("unexpected Mihomo listeners: %#v", cfg.Listeners)
	}
	rules := strings.Join(cfg.Rules, "\n")
	for _, want := range []string{
		"DOMAIN-SUFFIX,example.com,route-group",
		"MATCH,DIRECT",
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("generated config missing rule %q:\n%s", want, rules)
		}
	}
}

func TestGenerateMihomoConfig_CompilesDNSServersAndRules(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	// Update router config with dns-direct and dns-tunnel
	ctx := context.Background()
	cfg, err := svc.loadRouterConfig()
	if err != nil {
		t.Fatalf("loadRouterConfig: %v", err)
	}
	cfg.DNS.Servers = []DNSServer{
		{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"},
		{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9", Detour: "route-group"},
	}
	cfg.DNS.Rules = []DNSRule{
		{Domain: []string{"example.com"}, Server: "dns-tunnel"},
	}
	if err := svc.persistConfigDirect(ctx, cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}

	var parsed struct {
		DNS struct {
			Nameserver       []string          `yaml:"nameserver"`
			Fallback         []string          `yaml:"fallback"`
			NameserverPolicy map[string]string `yaml:"nameserver-policy"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	foundDirect := false
	for _, ns := range parsed.DNS.Nameserver {
		if strings.Contains(ns, "77.88.8.8") {
			foundDirect = true
			break
		}
	}
	if !foundDirect {
		t.Errorf("expected 77.88.8.8 in nameserver, got %v", parsed.DNS.Nameserver)
	}

	foundFallback := false
	for _, fb := range parsed.DNS.Fallback {
		if strings.Contains(fb, "9.9.9.9#route-group") {
			foundFallback = true
			break
		}
	}
	if !foundFallback {
		t.Errorf("expected 9.9.9.9#route-group in fallback, got %v", parsed.DNS.Fallback)
	}

	if policy := parsed.DNS.NameserverPolicy["example.com"]; policy != "9.9.9.9#route-group" {
		t.Errorf("expected nameserver-policy for example.com to be 9.9.9.9#route-group, got %q", policy)
	}
}

func TestGenerateMihomoConfig_MigratesLegacyGroupsAndRulesToNativeStore(t *testing.T) {
	svc, _ := newMihomoConfigTestService(t, "mihomo")
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.deps.MihomoNativeProxies = native
	err = svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.SingboxRouter.ProxyGroups = []storage.ProxyGroup{{Name: "Block", Type: "select", Proxies: []string{"block", "direct"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MigrateLegacyMihomoResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatal(err)
	}
	if !native.HasGroups() || !native.HasRules() {
		t.Fatal("native routing ownership was not activated")
	}
	groups := native.ListGroups()
	if len(groups) != 1 || groups[0].Name != "Block" || groups[0].Proxies[0] != "REJECT" {
		t.Fatalf("groups=%#v", groups)
	}
	rules := native.ListRules()
	if len(rules) != 1 || rules[0].Type != "DOMAIN-SUFFIX" || rules[0].Payload != "example.com" {
		t.Fatalf("rules=%#v", rules)
	}
}

func TestGenerateMihomoConfig_LoadsAWGDirectOutboundsFromSlot(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}
	raw := []byte(`{"outbounds":[{"type":"direct","tag":"awg-awg20","bind_interface":"nwg1"}]}`)
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotAwg, raw); err != nil {
		t.Fatalf("SaveSilent SlotAwg: %v", err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	for _, proxy := range cfg.Proxies {
		if proxy["name"] == "awg-awg20" {
			if proxy["type"] != "direct" || proxy["interface-name"] != "nwg1" {
				t.Fatalf("AWG proxy = %#v", proxy)
			}
			return
		}
	}
	t.Fatalf("AWG proxy missing: %#v", cfg.Proxies)
}

func TestGenerateMihomoConfig_LoadsDisabledSubscriptionSlot(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotSubscriptions, Filename: "40-subscriptions.json",
	}); err != nil {
		t.Fatalf("register SlotSubscriptions: %v", err)
	}
	raw := []byte(`{"outbounds":[{"type":"vless","tag":"sub-a","server":"sub.example","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"},{"type":"vless","tag":"tt-node","server":"vpn.example","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"},{"type":"selector","tag":"ttUS","outbounds":["tt-node"]}]}`)
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotSubscriptions, raw); err != nil {
		t.Fatalf("SaveSilent SlotSubscriptions: %v", err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotSubscriptions, false); err != nil {
		t.Fatalf("disable SlotSubscriptions: %v", err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	foundProxy, foundGroup := false, false
	for _, proxy := range cfg.Proxies {
		foundProxy = foundProxy || proxy["name"] == "tt-node"
	}
	for _, group := range cfg.ProxyGroups {
		foundGroup = foundGroup || group.Name == "ttUS"
	}
	if !foundProxy || !foundGroup {
		t.Fatalf("subscription slot not materialized: proxies=%#v groups=%#v", cfg.Proxies, cfg.ProxyGroups)
	}
}

func TestGenerateMihomoConfig_LoadsStandaloneTunnelSlot(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotTunnels: %v", err)
	}
	raw := []byte(`{"outbounds":[{"type":"vless","tag":"ttUS","server":"vpn.example","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}]}`)
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotTunnels, raw); err != nil {
		t.Fatalf("SaveSilent SlotTunnels: %v", err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	for _, proxy := range cfg.Proxies {
		if proxy["name"] == "ttUS" {
			return
		}
	}
	t.Fatalf("standalone tunnel missing: %#v", cfg.Proxies)
}

func TestGenerateMihomoConfig_SkipsWriteWhenEngineIsSingbox(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "sing-box")

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	_, err := os.Stat(filepath.Join(mihomoDir, "config.yaml"))
	if !os.IsNotExist(err) {
		t.Fatalf("config.yaml existence error = %v, want not exist", err)
	}
}

func TestGenerateMihomoConfig_SingboxPrimaryWritesExportsOnlySidecar(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "sing-box")
	attachNativeBridge(t, svc, 12007)

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ExternalCtl != "127.0.0.1:9090" || !cfg.DNS.Enable || cfg.DNS.Listen != "" {
		t.Fatalf("sidecar controller/DNS=%q %#v", cfg.ExternalCtl, cfg.DNS)
	}
	if cfg.TProxyPort != 0 || cfg.RedirPort != 0 || cfg.MixedPort != 0 || cfg.Port != 0 || cfg.SocksPort != 0 || cfg.Tun != nil {
		t.Fatalf("sidecar exposes routing/global listeners: %#v", cfg)
	}
	hasBridge := false
	for _, l := range cfg.Listeners {
		if l.Name == "routing-tproxy" {
			t.Fatalf("sidecar has routing-tproxy listener: %#v", cfg.Listeners)
		}
		if l.Port == 12007 {
			hasBridge = true
		}
	}
	if !hasBridge {
		t.Fatalf("sidecar missing bridge listener 12007: %#v", cfg.Listeners)
	}
}

func TestGenerateMihomoConfig_PrimaryWithNDMSExportOffOmitsBridgeListener(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	attachNativeBridge(t, svc, 12008)
	err := svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.CreateNDMSProxyForSingbox = false
		s.SingboxRouter.Enabled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, listener := range cfg.Listeners {
		if strings.HasPrefix(listener.Name, "mihomo-native-") {
			t.Fatalf("NDMS export disabled but bridge listener remains: %#v", cfg.Listeners)
		}
	}
}

func TestGenerateMihomoConfig_PicksTunInterfaceForPolicyTun(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	err := svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingMode = statePolicyTun
		s.OpkgTun = &storage.OpkgTunState{
			Mode:        statePolicyTun,
			Provisioned: true,
			Index:       3,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Settings.Update: %v", err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Tun == nil || !cfg.Tun.Enable {
		t.Fatal("Tun must be enabled for policy-tun mode")
	}
	if cfg.Tun.Device != tunIfaceName(3) {
		t.Fatalf("Tun.Device = %q, want %q", cfg.Tun.Device, tunIfaceName(3))
	}
}

type testAWGTags struct {
	tags   []AWGTag
	err    error
	called *bool
}

func (t *testAWGTags) ListTags(_ context.Context) ([]AWGTag, error) {
	if t.called != nil {
		*t.called = true
	}
	return t.tags, t.err
}

func TestGenerateMihomoConfig_FreshInstall_AbsentOptionalSlots_Succeeds(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	// Register optional slots with orchestrator, but do not write any files (empty / fresh install)
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotSubscriptions, Filename: "40-subscriptions.json",
	}); err != nil {
		t.Fatalf("register SlotSubscriptions: %v", err)
	}
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotTunnels: %v", err)
	}
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}

	svc.deps.AWGTags = &testAWGTags{tags: nil}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig failed on fresh install: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.TProxyPort != 51271 {
		t.Fatalf("expected tproxy port 51271, got %d", cfg.TProxyPort)
	}
}

func TestGenerateMihomoConfig_CorruptedRouterConfig_FailsClosedAndPreservesExisting(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	cfgPath := filepath.Join(mihomoDir, "config.yaml")
	canary := []byte("PREVIOUS_VALID_MIHOMO_CONFIG_DATA")
	if err := os.WriteFile(cfgPath, canary, 0644); err != nil {
		t.Fatalf("Write canary config.yaml: %v", err)
	}

	// Corrupt SlotRouter in orchestrator
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotRouter, []byte("{broken json:")); err != nil {
		t.Fatalf("SaveSilent corrupted SlotRouter: %v", err)
	}

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected error on corrupted router config, got nil")
	}
	if !strings.Contains(err.Error(), "load router config") {
		t.Fatalf("expected 'load router config' in error, got: %v", err)
	}

	// Verify previous config.yaml is byte-for-byte untouched
	current, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("ReadFile(config.yaml): %v", readErr)
	}
	if !bytes.Equal(current, canary) {
		t.Fatalf("config.yaml was overwritten or corrupted! got: %s", string(current))
	}
}

func TestGenerateMihomoConfig_CorruptedSubscriptionSlot_FailsClosed(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	cfgPath := filepath.Join(mihomoDir, "config.yaml")
	canary := []byte("PREVIOUS_VALID_MIHOMO_CONFIG_DATA")
	if err := os.WriteFile(cfgPath, canary, 0644); err != nil {
		t.Fatalf("Write canary config.yaml: %v", err)
	}

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotSubscriptions, Filename: "40-subscriptions.json",
	}); err != nil {
		t.Fatalf("register SlotSubscriptions: %v", err)
	}
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotSubscriptions, []byte("{invalid subscription json")); err != nil {
		t.Fatalf("SaveSilent corrupted SlotSubscriptions: %v", err)
	}

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected error on corrupted SlotSubscriptions, got nil")
	}
	if !strings.Contains(err.Error(), "decode subscription outbound slot") {
		t.Fatalf("expected 'decode subscription outbound slot' in error, got: %v", err)
	}

	current, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("ReadFile(config.yaml): %v", readErr)
	}
	if !bytes.Equal(current, canary) {
		t.Fatalf("config.yaml was overwritten! got: %s", string(current))
	}
}

func TestGenerateMihomoConfig_CorruptedTunnelSlot_FailsClosed(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	cfgPath := filepath.Join(mihomoDir, "config.yaml")
	canary := []byte("PREVIOUS_VALID_MIHOMO_CONFIG_DATA")
	if err := os.WriteFile(cfgPath, canary, 0644); err != nil {
		t.Fatalf("Write canary config.yaml: %v", err)
	}

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotTunnels: %v", err)
	}
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotTunnels, []byte("{invalid tunnel json")); err != nil {
		t.Fatalf("SaveSilent corrupted SlotTunnels: %v", err)
	}

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected error on corrupted SlotTunnels, got nil")
	}
	if !strings.Contains(err.Error(), "decode tunnel outbound slot") {
		t.Fatalf("expected 'decode tunnel outbound slot' in error, got: %v", err)
	}

	current, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("ReadFile(config.yaml): %v", readErr)
	}
	if !bytes.Equal(current, canary) {
		t.Fatalf("config.yaml was overwritten! got: %s", string(current))
	}
}

func TestGenerateMihomoConfig_CorruptedAwgSlot_FailsClosed(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	cfgPath := filepath.Join(mihomoDir, "config.yaml")
	canary := []byte("PREVIOUS_VALID_MIHOMO_CONFIG_DATA")
	if err := os.WriteFile(cfgPath, canary, 0644); err != nil {
		t.Fatalf("Write canary config.yaml: %v", err)
	}

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotAwg, []byte("{invalid awg json")); err != nil {
		t.Fatalf("SaveSilent corrupted SlotAwg: %v", err)
	}

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected error on corrupted SlotAwg, got nil")
	}
	if !strings.Contains(err.Error(), "decode AWG outbound slot") {
		t.Fatalf("expected 'decode AWG outbound slot' in error, got: %v", err)
	}

	current, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("ReadFile(config.yaml): %v", readErr)
	}
	if !bytes.Equal(current, canary) {
		t.Fatalf("config.yaml was overwritten! got: %s", string(current))
	}
}

func TestGenerateMihomoConfig_AbsentAwgSlot_UsesLiveCatalog(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	// Register SlotAwg but do NOT populate it (LoadEffective returns (nil, nil))
	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}

	catalogCalled := false
	svc.deps.AWGTags = &testAWGTags{
		tags:   []AWGTag{{Tag: "awg-live", Iface: "nwg10"}},
		called: &catalogCalled,
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	if !catalogCalled {
		t.Fatal("expected live catalog to be called when SlotAwg is absent")
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	foundLive := false
	for _, proxy := range cfg.Proxies {
		if proxy["name"] == "awg-live" {
			foundLive = true
			if proxy["type"] != "direct" || proxy["interface-name"] != "nwg10" {
				t.Fatalf("unexpected proxy payload for awg-live: %#v", proxy)
			}
		}
	}
	if !foundLive {
		t.Fatalf("expected awg-live in proxies, got: %#v", cfg.Proxies)
	}
}

func TestGenerateMihomoConfig_PopulatedAwgSlot_DoesNotCallLiveCatalog(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}
	rawSlot := []byte(`{"outbounds":[{"type":"direct","tag":"slot-awg","bind_interface":"nwg20"}]}`)
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotAwg, rawSlot); err != nil {
		t.Fatalf("SaveSilent SlotAwg: %v", err)
	}

	catalogCalled := false
	svc.deps.AWGTags = &testAWGTags{
		tags:   []AWGTag{{Tag: "live-awg", Iface: "nwg30"}},
		called: &catalogCalled,
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	if catalogCalled {
		t.Fatal("live catalog should NOT be called when SlotAwg is populated")
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config.yaml): %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	foundSlot, foundLive := false, false
	for _, proxy := range cfg.Proxies {
		if proxy["name"] == "slot-awg" {
			foundSlot = true
		}
		if proxy["name"] == "live-awg" {
			foundLive = true
		}
	}
	if !foundSlot {
		t.Fatalf("expected slot-awg in proxies, got: %#v", cfg.Proxies)
	}
	if foundLive {
		t.Fatal("live-awg must NOT be in proxies when SlotAwg is populated")
	}
}

func TestGenerateMihomoConfig_AbsentAwgSlot_EmptyLiveCatalog_Succeeds(t *testing.T) {
	svc, _ := newMihomoConfigTestService(t, "mihomo")

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}

	catalogCalled := false
	svc.deps.AWGTags = &testAWGTags{
		tags:   []AWGTag{},
		called: &catalogCalled,
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig should succeed with absent slot and empty catalog: %v", err)
	}
	if !catalogCalled {
		t.Fatal("expected live catalog to be called")
	}
}

func TestGenerateMihomoConfig_AwgSlotReadError_FailsClosedWithoutCallingLiveCatalog(t *testing.T) {
	svc, _ := newMihomoConfigTestService(t, "mihomo")

	if err := svc.deps.Orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotAwg, Filename: "15-awg.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register SlotAwg: %v", err)
	}

	// Create a directory at configDir/15-awg.json so os.ReadFile will fail with an I/O error (EISDIR)
	slotPath := filepath.Join(svc.deps.Orch.ConfigDir(), "15-awg.json")
	if err := os.MkdirAll(slotPath, 0755); err != nil {
		t.Fatalf("MkdirAll slotPath: %v", err)
	}

	catalogCalled := false
	svc.deps.AWGTags = &testAWGTags{
		tags:   []AWGTag{{Tag: "live-awg", Iface: "nwg30"}},
		called: &catalogCalled,
	}

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected error on slot read failure, got nil")
	}
	if !strings.Contains(err.Error(), "load AWG outbound slot") {
		t.Fatalf("expected 'load AWG outbound slot' in error, got: %v", err)
	}
	if catalogCalled {
		t.Fatal("live catalog must NOT be called when slot load fails (must fail closed)")
	}
}

type fakeMihomoNativeProxySource struct {
	validateErr   error
	proxies       []map[string]interface{}
	providers     map[string]map[string]interface{}
	groups        []map[string]interface{}
	listeners     []mihomonative.BridgeListener
	rules         []string
	hasGroups     bool
	hasRules      bool
	ruleProviders map[string]map[string]interface{}
}

func (f *fakeMihomoNativeProxySource) ValidateRuntimeRules() error             { return f.validateErr }
func (f *fakeMihomoNativeProxySource) ConfigProxies() []map[string]interface{} { return f.proxies }
func (f *fakeMihomoNativeProxySource) ConfigProviders() map[string]map[string]interface{} {
	return f.providers
}
func (f *fakeMihomoNativeProxySource) ConfigProviderGroups() []map[string]interface{} {
	return f.groups
}
func (f *fakeMihomoNativeProxySource) ConfigBridgeListeners() []mihomonative.BridgeListener {
	return f.listeners
}
func (f *fakeMihomoNativeProxySource) ConfigRules() []string { return f.rules }
func (f *fakeMihomoNativeProxySource) HasGroups() bool       { return f.hasGroups }
func (f *fakeMihomoNativeProxySource) HasRules() bool        { return f.hasRules }
func (f *fakeMihomoNativeProxySource) ConfigRuleProviders() map[string]map[string]interface{} {
	return f.ruleProviders
}
func (f *fakeMihomoNativeProxySource) ImportLegacyGroups([]storage.ProxyGroup) error { return nil }
func (f *fakeMihomoNativeProxySource) ImportLegacyRules([]string) error              { return nil }
func (f *fakeMihomoNativeProxySource) ListBridges() []mihomonative.BridgeRef         { return nil }

var _ MihomoNativeProxySource = (*fakeMihomoNativeProxySource)(nil)

func TestGenerateMihomoConfig_FailsClosedOnUnsupportedNativeRule(t *testing.T) {
	svc, _ := newMihomoConfigTestService(t, "mihomo")
	fake := &fakeMihomoNativeProxySource{
		validateErr: errors.New("unsupported active rule SUB-RULE found"),
	}
	svc.deps.MihomoNativeProxies = fake

	err := svc.GenerateMihomoConfig()
	if err == nil {
		t.Fatal("expected GenerateMihomoConfig to fail when ValidateRuntimeRules returns error, got nil")
	}
	if !strings.Contains(err.Error(), "validate mihomo native rules") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGenerateMihomoConfig_DynamicCloudCIDRs(t *testing.T) {
	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")
	svc.setDynamicCloudCIDRs([]string{"192.0.2.1/32", "198.51.100.0/24"})

	err := svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.SingboxRouter.KeeneticCloudTunnel = true
		s.SingboxRouter.KeeneticCloudOutbound = "DIRECT"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(mihomoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile config.yaml: %v", err)
	}
	var cfg mihomo.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	rules := strings.Join(cfg.Rules, "\n")
	if !strings.Contains(rules, "192.0.2.1/32") || !strings.Contains(rules, "198.51.100.0/24") {
		t.Fatalf("generated config missing dynamic cloud CIDRs:\n%s", rules)
	}
}

type acceptanceManifest struct {
	Version string `json:"version"`
	Binary  struct {
		AssetName          string `json:"assetName"`
		URL                string `json:"url"`
		CompressedSha256   string `json:"compressedSha256"`
		UncompressedSha256 string `json:"uncompressedSha256"`
	} `json:"binary"`
	Geodata map[string]struct {
		ReleaseTag string `json:"releaseTag"`
		URL        string `json:"url"`
		Sha256     string `json:"sha256"`
	} `json:"geodata"`
}

type validatedFixtures struct {
	BinaryPath   string
	GeodataDir   string
	GeodataFiles map[string]string
}

func validateMihomoAcceptanceFixtures(dir string, manifest acceptanceManifest) (validatedFixtures, error) {
	if dir == "" {
		return validatedFixtures{}, errors.New("fixture directory is empty")
	}
	binPath := filepath.Join(dir, "mihomo")
	binData, err := os.ReadFile(binPath)
	if err != nil {
		return validatedFixtures{}, fmt.Errorf("read binary %s: %w", binPath, err)
	}
	h := sha256.Sum256(binData)
	binSha := hex.EncodeToString(h[:])
	if !strings.EqualFold(binSha, manifest.Binary.UncompressedSha256) {
		return validatedFixtures{}, fmt.Errorf("binary checksum mismatch: expected %s, got %s", manifest.Binary.UncompressedSha256, binSha)
	}

	geoFiles := make(map[string]string, len(manifest.Geodata))
	for name, meta := range manifest.Geodata {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			return validatedFixtures{}, fmt.Errorf("read geodata %s: %w", p, err)
		}
		gh := sha256.Sum256(data)
		actualSha := hex.EncodeToString(gh[:])
		if !strings.EqualFold(actualSha, meta.Sha256) {
			return validatedFixtures{}, fmt.Errorf("geodata %s checksum mismatch: expected %s, got %s", name, meta.Sha256, actualSha)
		}
		geoFiles[name] = p
	}

	return validatedFixtures{
		BinaryPath:   binPath,
		GeodataDir:   dir,
		GeodataFiles: geoFiles,
	}, nil
}

var awgmVersionFormatRegex = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
var bannerTokenRegex = regexp.MustCompile(`(?i)(?:^|\s)Mihomo(?:\s+Meta)?\s+([^\s]+)`)

func matchMihomoVersion(versionOutput, expectedVersion string) bool {
	trimmedExpected := strings.TrimSpace(expectedVersion)
	if !awgmVersionFormatRegex.MatchString(trimmedExpected) {
		return false
	}
	normExpected := strings.TrimPrefix(trimmedExpected, "v")

	matches := bannerTokenRegex.FindAllStringSubmatch(versionOutput, -1)
	if len(matches) != 1 {
		// Unambiguous: exactly one banner token required
		return false
	}

	rawToken := matches[0][1]
	if !awgmVersionFormatRegex.MatchString(rawToken) {
		return false
	}

	normCandidate := strings.TrimPrefix(rawToken, "v")
	return normCandidate == normExpected
}

func validateMihomoVersion(binPath, expectedVersion string) (string, error) {
	cmd := exec.Command(binPath, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("execute %s -v: %w (output: %s)", binPath, err, string(out))
	}
	versionOutput := strings.TrimSpace(string(out))
	if !matchMihomoVersion(versionOutput, expectedVersion) {
		return versionOutput, fmt.Errorf("version mismatch: expected %s in output, got %q", expectedVersion, versionOutput)
	}
	return versionOutput, nil
}

func TestValidateMihomoAcceptanceFixtures_MissingFileReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := acceptanceManifest{
		Version: "v1.19.29",
	}
	manifest.Binary.UncompressedSha256 = "dummy"
	_, err := validateMihomoAcceptanceFixtures(tmpDir, manifest)
	if err == nil || !strings.Contains(err.Error(), "read binary") {
		t.Fatalf("expected read binary error, got %v", err)
	}
}

func TestValidateMihomoAcceptanceFixtures_ChecksumMismatchReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "mihomo")
	if err := os.WriteFile(binPath, []byte("invalid content"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := acceptanceManifest{
		Version: "v1.19.29",
	}
	manifest.Binary.UncompressedSha256 = "expected-sha"
	_, err := validateMihomoAcceptanceFixtures(tmpDir, manifest)
	if err == nil || !strings.Contains(err.Error(), "binary checksum mismatch") {
		t.Fatalf("expected binary checksum mismatch error, got %v", err)
	}
}

func TestValidateMihomoVersion_ExactMatching(t *testing.T) {
	cases := []struct {
		output   string
		expected string
	}{
		{"Mihomo v1.19.29 linux amd64", "v1.19.29"},
		{"Mihomo 1.19.29 linux amd64", "v1.19.29"},
		{"Mihomo v1.19.29 linux amd64", "1.19.29"},
		{"Mihomo Meta v1.19.29 linux amd64", "v1.19.29"},
		{"Mihomo v0.1.0 linux amd64", "v0.1.0"},
		{"Mihomo v1.19.29-beta.1 linux amd64", "v1.19.29-beta.1"},
	}

	for _, tc := range cases {
		t.Run(tc.output+"_vs_"+tc.expected, func(t *testing.T) {
			if !matchMihomoVersion(tc.output, tc.expected) {
				t.Fatalf("expected output %q to match version %q", tc.output, tc.expected)
			}
		})
	}
}

func TestValidateMihomoVersion_Mismatches(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		expected string
	}{
		{"different version", "Mihomo v1.18.0 linux amd64", "v1.19.29"},
		{"substring near match suffix", "Mihomo v1.19.290 linux amd64", "v1.19.29"},
		{"prefix match", "Mihomo v1.19.2 linux amd64", "v1.19.29"},
		{"major mismatch", "Mihomo v2.19.29 linux amd64", "v1.19.29"},
		{"prerelease mismatch", "Mihomo v1.19.29-alpha linux amd64", "v1.19.29"},
		{"leading zero in major", "Mihomo v01.19.29 linux amd64", "v1.19.29"},
		{"leading zero in minor", "Mihomo v1.019.29 linux amd64", "v1.19.29"},
		{"leading zero in patch", "Mihomo v1.19.029 linux amd64", "v1.19.29"},
		{"ambiguous multiple banners", "Mihomo v1.18.0 Mihomo v1.19.29 linux amd64", "v1.19.29"},
		{"build metadata unpermitted", "Mihomo v1.19.29+build linux amd64", "v1.19.29"},
		{"expected version has build metadata", "Mihomo v1.19.29 linux amd64", "v1.19.29+build"},
		{"banner missing", "v1.19.29 linux amd64", "v1.19.29"},
		{"empty output", "", "v1.19.29"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if matchMihomoVersion(tc.output, tc.expected) {
				t.Fatalf("expected output %q to NOT match version %q", tc.output, tc.expected)
			}
		})
	}
}

func TestValidateMihomoVersion_ExecutionScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping POSIX shell script execution test on Windows")
	}
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock-mihomo")
	scriptContent := "#!/bin/sh\necho 'Mihomo v1.18.0 linux amd64'\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := validateMihomoVersion(scriptPath, "v1.19.29")
	if err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("expected version mismatch error, got %v", err)
	}
}

func TestGenerateMihomoConfig_RepresentativeBinaryValidation(t *testing.T) {
	if os.Getenv("MIHOMO_ACCEPTANCE") != "1" {
		t.Skip("skipping acceptance binary validation; set MIHOMO_ACCEPTANCE=1 to run")
	}

	manifestPath := os.Getenv("MIHOMO_ACCEPTANCE_MANIFEST")
	if manifestPath == "" {
		_, thisFile, _, _ := runtime.Caller(0)
		manifestPath = filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../../../scripts/mihomo-acceptance-manifest.json"))
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read acceptance manifest %s: %v", manifestPath, err)
	}
	var manifest acceptanceManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("failed to parse acceptance manifest: %v", err)
	}

	fixtureDir := os.Getenv("MIHOMO_FIXTURE_DIR")
	if fixtureDir == "" {
		t.Fatal("MIHOMO_FIXTURE_DIR environment variable must be set in acceptance mode")
	}

	fixtures, err := validateMihomoAcceptanceFixtures(fixtureDir, manifest)
	if err != nil {
		t.Fatalf("acceptance fixtures validation failed: %v", err)
	}

	versionOutput, err := validateMihomoVersion(fixtures.BinaryPath, manifest.Version)
	if err != nil {
		t.Fatalf("acceptance version validation failed: %v", err)
	}
	t.Logf("Validated Mihomo acceptance binary: %s", versionOutput)

	svc, mihomoDir := newMihomoConfigTestService(t, "mihomo")

	// Copy verified geodata fixtures into test mihomoDir
	for name, srcPath := range fixtures.GeodataFiles {
		data, err := os.ReadFile(srcPath)
		if err != nil {
			t.Fatalf("read fixture %s: %v", srcPath, err)
		}
		if err := os.WriteFile(filepath.Join(mihomoDir, name), data, 0644); err != nil {
			t.Fatalf("write fixture %s to %s: %v", name, mihomoDir, err)
		}
	}

	// Populate representative DNS configuration (IPv4, IPv6, DoT, DoH, DHCP, RCODE, SYSTEM)
	slotCfg := NewEmptyConfig()
	slotCfg.Outbounds = []Outbound{
		{Type: "selector", Tag: "route-group", Outbounds: []string{"direct"}},
		{Type: "direct", Tag: "direct"},
	}
	slotCfg.Route.Rules = []Rule{
		{DomainSuffix: []string{"example.com"}, Outbound: "route-group"},
	}
	slotCfg.Route.Final = "direct"
	slotCfg.DNS.Servers = []DNSServer{
		{Tag: "dns-v4", Type: "udp", Server: "8.8.8.8", Detour: "direct"},
		{Tag: "dns-v6", Type: "udp", Server: "2606:4700:4700::1111", Detour: "direct"},
		{Tag: "dns-tls", Type: "tls", Server: "1.1.1.1", ServerPort: 853, Detour: "direct"},
		{Tag: "dns-https", Type: "https", Server: "1.1.1.1", Detour: "direct"},
		{Tag: "dns-dhcp", Type: "dhcp", Server: "dhcp://system", Detour: "direct"},
		{Tag: "dns-rcode", Type: "rcode", Server: "rcode://success", Detour: "direct"},
		{Tag: "dns-system", Type: "system", Server: "system", Detour: "direct"},
	}
	slotData, err := json.MarshalIndent(slotCfg, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotRouter, slotData); err != nil {
		t.Fatalf("SaveSilent SlotRouter with representative DNS: %v", err)
	}

	// Local classical rule-set payload
	dummyRuleSet := "payload:\n  - DOMAIN,example.com\n"
	_ = os.WriteFile(filepath.Join(mihomoDir, "dummy.yaml"), []byte(dummyRuleSet), 0644)

	ruleProviders := map[string]map[string]interface{}{
		"provider-name": {
			"type":     "file",
			"behavior": "classical",
			"path":     filepath.Join(mihomoDir, "dummy.yaml"),
		},
	}

	var allRules []string
	for _, spec := range mihomo.AllRuleSpecs() {
		if spec.Type == "MATCH" {
			allRules = append(allRules, "MATCH,DIRECT")
		} else if spec.Type == "RULE-SET" {
			allRules = append(allRules, "RULE-SET,provider-name,DIRECT")
		} else {
			allRules = append(allRules, fmt.Sprintf("%s,%s,DIRECT", spec.Type, spec.SamplePayload))
		}
	}

	fakeSource := &fakeMihomoNativeProxySource{
		hasRules:      true,
		rules:         allRules,
		ruleProviders: ruleProviders,
	}
	svc.deps.MihomoNativeProxies = fakeSource

	if err := svc.GenerateMihomoConfig(); err != nil {
		t.Fatalf("GenerateMihomoConfig failed: %v", err)
	}

	cfgPath := filepath.Join(mihomoDir, "config.yaml")
	cmd := exec.Command(fixtures.BinaryPath, "-t", "-f", cfgPath, "-d", mihomoDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo config validation (-t) failed: %v\nOutput:\n%s", err, string(out))
	}
	t.Logf("mihomo -t passed successfully: %s", strings.TrimSpace(string(out)))
}
