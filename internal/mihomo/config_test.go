package mihomo

import (
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"gopkg.in/yaml.v3"
)

func TestGenerateConfig_TProxyMode(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
		ProxyGroups: []storage.ProxyGroup{
			{
				Name:     "auto",
				Type:     "url-test",
				URL:      "https://cp.cloudflare.com/generate_204",
				Interval: 300,
				Lazy:     true,
				Proxies:  []string{"direct", "sub-a", "block"},
			},
		},
	}

	yamlBytes, err := GenerateConfigWithResources(
		settings,
		"",
		[]map[string]any{
			{
				"type":        "vless",
				"tag":         "sub-a",
				"server":      "example.com",
				"server_port": 443,
				"uuid":        "11111111-1111-1111-1111-111111111111",
				"tls": map[string]any{
					"enabled":     true,
					"server_name": "example.com",
				},
			},
			{
				"type":      "selector",
				"tag":       "sub-selector",
				"outbounds": []any{"sub-a", "direct", "block"},
			},
		},
		NativeResources{
			RuleProviders: map[string]map[string]interface{}{
				"custom-rs": {
					"type":     "http",
					"behavior": "domain",
					"url":      "https://example.com/rs.yaml",
					"path":     "custom-rs.yaml",
				},
			},
		},
		[]Rule{
			{DomainSuffix: []string{"example.org"}, Outbound: "direct"},
			{RuleSet: []string{"geosite:google", "geoip:ru", "custom-rs"}, Outbound: "block"},
		},
		"direct",
		[]DeviceProxyListener{
			{ID: "lan1", Port: 1099, SelectedOutbound: "block", Enabled: true},
		},
	)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}

	if cfg.TProxyPort != 51271 {
		t.Fatalf("TProxyPort = %d, want 51271", cfg.TProxyPort)
	}
	if len(cfg.Listeners) != 1 || cfg.Listeners[0].Name != "device-proxy-lan1" {
		t.Fatalf("system tproxy must use tproxy-port and preserve resource listeners: %#v", cfg.Listeners)
	}
	if cfg.RedirPort != 51272 {
		t.Fatalf("RedirPort = %d, want 51272", cfg.RedirPort)
	}
	if cfg.DNS.Listen != "127.0.0.1:1053" {
		t.Fatalf("DNS.Listen = %q, want 127.0.0.1:1053", cfg.DNS.Listen)
	}
	if cfg.DNS.Enhanced != "redir-host" {
		t.Fatalf("DNS.Enhanced = %q, want redir-host", cfg.DNS.Enhanced)
	}
	if cfg.Tun != nil {
		t.Fatal("Tun must stay nil in tproxy mode")
	}

	if len(cfg.Proxies) != 1 {
		t.Fatalf("len(Proxies) = %d, want 1", len(cfg.Proxies))
	}
	if got := cfg.Proxies[0]["name"]; got != "sub-a" {
		t.Fatalf("proxy name = %v, want sub-a", got)
	}

	foundSelector := false
	foundSettingsGroup := false
	for _, pg := range cfg.ProxyGroups {
		switch pg.Name {
		case "sub-selector":
			foundSelector = true
			if strings.Join(pg.Proxies, ",") != "sub-a,DIRECT,REJECT" {
				t.Fatalf("sub-selector proxies = %v, want [sub-a DIRECT REJECT]", pg.Proxies)
			}
		case "auto":
			foundSettingsGroup = true
			if strings.Join(pg.Proxies, ",") != "DIRECT,sub-a,REJECT" {
				t.Fatalf("auto proxies = %v, want [DIRECT sub-a REJECT]", pg.Proxies)
			}
		}
	}
	if !foundSelector {
		t.Fatal("converted selector proxy group not found")
	}
	if !foundSettingsGroup {
		t.Fatal("settings proxy group not found")
	}

	if len(cfg.Listeners) != 1 {
		t.Fatalf("len(Listeners) = %d, want device proxy only", len(cfg.Listeners))
	}
	if cfg.Listeners[0].Proxy != "REJECT" {
		t.Fatalf("device listener proxy = %q, want REJECT", cfg.Listeners[0].Proxy)
	}

	rules := strings.Join(cfg.Rules, "\n")
	for _, want := range []string{
		"DOMAIN-SUFFIX,example.org,DIRECT",
		"GEOSITE,google,REJECT",
		"GEOIP,ru,REJECT,no-resolve",
		"RULE-SET,custom-rs,REJECT",
		"MATCH,DIRECT",
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("generated rules missing %q:\n%s", want, rules)
		}
	}
}

func TestGenerateConfig_SnifferFollowsRouterSetting(t *testing.T) {
	yamlBytes, err := GenerateConfig(storage.SingboxRouterSettings{
		RoutingMode:    "tproxy",
		SnifferEnabled: true,
	}, "", nil, nil, "direct", nil)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if cfg.Sniffer == nil || !cfg.Sniffer.Enable || !cfg.Sniffer.ForceDNSMapping || !cfg.Sniffer.ParsePureIP {
		t.Fatalf("Sniffer = %#v, want enabled pure-IP sniffing", cfg.Sniffer)
	}
	if cfg.Sniffer.OverrideDestination {
		t.Fatal("global override-destination must stay false")
	}
	if got := cfg.Sniffer.Sniff.TLS.Ports; len(got) != 2 {
		t.Fatalf("TLS sniff ports = %#v, want 443 and 8443", got)
	}

	yamlBytes, err = GenerateConfig(storage.SingboxRouterSettings{RoutingMode: "tproxy"}, "", nil, nil, "direct", nil)
	if err != nil {
		t.Fatalf("GenerateConfig(disabled) error = %v", err)
	}
	cfg = Config{}
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal(disabled) error = %v", err)
	}
	if cfg.Sniffer != nil {
		t.Fatalf("Sniffer = %#v, want omitted when disabled", cfg.Sniffer)
	}
}

func TestGenerateConfig_TunModesUseFakeIP(t *testing.T) {
	for _, mode := range []string{"fakeip-tun", "policy-tun"} {
		t.Run(mode, func(t *testing.T) {
			settings := storage.SingboxRouterSettings{
				RoutingMode: mode,
				FakeIPPool4: "198.18.0.0/15",
			}

			yamlBytes, err := GenerateConfig(settings, "mihomo0", nil, nil, "", nil)
			if err != nil {
				t.Fatalf("GenerateConfig() error = %v", err)
			}

			var cfg Config
			if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", err)
			}

			if cfg.DNS.Enhanced != "fake-ip" {
				t.Fatalf("DNS.Enhanced = %q, want fake-ip", cfg.DNS.Enhanced)
			}
			if cfg.DNS.FakeIPRange != "198.18.0.0/15" {
				t.Fatalf("DNS.FakeIPRange = %q, want 198.18.0.0/15", cfg.DNS.FakeIPRange)
			}
			if cfg.Tun == nil || !cfg.Tun.Enable {
				t.Fatal("Tun must be enabled in tun modes")
			}
			if cfg.Tun.Device != "mihomo0" {
				t.Fatalf("Tun.Device = %q, want mihomo0", cfg.Tun.Device)
			}
		})
	}
}

func TestConvertSingboxRuleToMihomo_SkipsNonRouteActions(t *testing.T) {
	got := ConvertSingboxRuleToMihomo(Rule{
		Action:       "sniff",
		DomainSuffix: []string{"example.com"},
		Outbound:     "direct",
	})
	if len(got) != 0 {
		t.Fatalf("ConvertSingboxRuleToMihomo() = %v, want nil", got)
	}
}

func TestGenerateConfig_UnsupportedOutboundFailsClosed(t *testing.T) {
	_, err := GenerateConfig(
		storage.SingboxRouterSettings{RoutingMode: "tproxy"},
		"",
		[]map[string]any{{
			"type": "ssh",
			"tag":  "private-hop",
		}},
		nil,
		"private-hop",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want unsupported outbound failure")
	}
	for _, want := range []string{"private-hop", "ssh"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("GenerateConfig() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestGenerateConfig_MalformedOutboundFailsClosed(t *testing.T) {
	_, err := GenerateConfig(
		storage.SingboxRouterSettings{RoutingMode: "tproxy"},
		"",
		[]map[string]any{{"type": "vless"}},
		nil,
		"direct",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want malformed outbound failure")
	}
	if !strings.Contains(err.Error(), "non-empty type and tag") {
		t.Fatalf("GenerateConfig() error = %q, want malformed outbound detail", err)
	}
}

func TestGenerateConfig_AWGDirectOutboundBindsInterface(t *testing.T) {
	yamlBytes, err := GenerateConfig(
		storage.SingboxRouterSettings{RoutingMode: "tproxy"},
		"",
		[]map[string]any{{
			"type":           "direct",
			"tag":            "awg-awg20",
			"bind_interface": "nwg20",
		}},
		nil,
		"awg-awg20",
		nil,
	)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("Proxies = %#v, want one AWG direct proxy", cfg.Proxies)
	}
	p := cfg.Proxies[0]
	if p["name"] != "awg-awg20" || p["type"] != "direct" || p["interface-name"] != "nwg20" {
		t.Fatalf("AWG direct proxy = %#v", p)
	}
}

func TestGenerateConfig_DeduplicatesProxyGroups(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
		ProxyGroups: []storage.ProxyGroup{{
			Name: "Block", Type: "select", Proxies: []string{"block"},
		}},
	}
	yamlBytes, err := GenerateConfig(settings, "", []map[string]any{{
		"type": "selector", "tag": "Block", "outbounds": []any{"direct"},
	}}, nil, "direct", nil)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.ProxyGroups) != 1 || cfg.ProxyGroups[0].Name != "Block" {
		t.Fatalf("ProxyGroups = %#v, want one Block group", cfg.ProxyGroups)
	}
	if strings.Join(cfg.ProxyGroups[0].Proxies, ",") != "REJECT" {
		t.Fatalf("Block proxies = %#v, want authoritative settings definition", cfg.ProxyGroups[0].Proxies)
	}
}

func TestConvertSingboxProxyGroup_NormalizesSelectorType(t *testing.T) {
	group := ConvertSingboxToMihomoProxyGroup(map[string]any{
		"type": "selector", "tag": "choice", "outbounds": []any{"direct"},
	})
	if group == nil || group.Type != "select" {
		t.Fatalf("group = %#v, want Mihomo type select", group)
	}
}

func TestGenerateConfig_NormalizesLegacySettingsSelectorType(t *testing.T) {
	yamlBytes, err := GenerateConfig(storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
		ProxyGroups: []storage.ProxyGroup{{
			Name: "legacy", Type: "selector", Proxies: []string{"direct"},
		}},
	}, "", nil, nil, "direct", nil)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.ProxyGroups) != 1 || cfg.ProxyGroups[0].Type != "select" {
		t.Fatalf("ProxyGroups = %#v, want normalized select", cfg.ProxyGroups)
	}
}

func TestGenerateConfig_ConvertsSocksOutbound(t *testing.T) {
	yamlBytes, err := GenerateConfig(storage.SingboxRouterSettings{RoutingMode: "tproxy"}, "", []map[string]any{{
		"type": "socks", "tag": "ttRU", "server": "proxy.example", "server_port": 1080,
		"username": "user", "password": "pass",
	}}, nil, "ttRU", nil)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.Proxies) != 1 || cfg.Proxies[0]["type"] != "socks5" || cfg.Proxies[0]["name"] != "ttRU" {
		t.Fatalf("Proxies = %#v, want ttRU socks5", cfg.Proxies)
	}
}

func TestGenerateConfig_ConvertsTrustTunnelOutbound(t *testing.T) {
	yamlBytes, err := GenerateConfig(storage.SingboxRouterSettings{RoutingMode: "tproxy"}, "", []map[string]any{{
		"type":        "trusttunnel",
		"tag":         "NetherlTT2364",
		"server":      "candy-wise-kick-9a4ace.relay7ai.net",
		"server_port": 443,
		"username":    "candy-wise-kick-9a4ace",
		"password":    "2AiZEeLn5uJCkCKyLb7jsts82gH3X0O_OGhlD2pur2s",
		"quic":        true,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "nl.ttnl.top",
		},
	}}, nil, "NetherlTT2364", nil)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("Proxies = %#v, want 1 proxy", cfg.Proxies)
	}
	p := cfg.Proxies[0]
	if p["name"] != "NetherlTT2364" || p["type"] != "trusttunnel" || p["sni"] != "nl.ttnl.top" || p["quic"] != true {
		t.Fatalf("Proxy = %#v, want NetherlTT2364 trusttunnel with sni and quic", p)
	}
}

func TestGenerateConfigWithNative_PreservesXHTTP(t *testing.T) {
	b, err := GenerateConfigWithNative(storage.SingboxRouterSettings{}, "", nil, []Proxy{{
		"name": "native-xhttp", "type": "vless", "server": "example.com", "port": 443,
		"uuid": "id", "network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/x"},
	}}, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "name: native-xhttp") || !strings.Contains(text, "network: xhttp") || !strings.Contains(text, "xhttp-opts:") {
		t.Fatalf("yaml missing native xhttp proxy:\n%s", text)
	}
}

func TestGenerateConfigWithResources_AddsProxyProvider(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{}, "", nil, NativeResources{
		ProxyProviders: map[string]map[string]interface{}{"mnp-test": {
			"type": "http", "url": "https://example.test/provider.yaml", "path": "./providers/mnp-test.yaml",
		}},
		ProxyGroups: []ProxyGroup{{Name: "Mihomo: Test", Type: "select", Use: []string{"mnp-test"}}},
	}, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"proxy-providers:", "mnp-test:", "url: https://example.test/provider.yaml", "use:", "- mnp-test"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestGenerateConfigWithResources_NativeGroupsAndRulesAreAuthoritative(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{ProxyGroups: []storage.ProxyGroup{{Name: "legacy", Type: "select", Proxies: []string{"direct"}}}}, "", nil, NativeResources{
		ProxyGroups: []ProxyGroup{{Name: "native", Type: "select", Proxies: []string{"DIRECT"}}},
		Rules:       []string{"DOMAIN-SUFFIX,example.com,native"}, GroupsAuthoritative: true, RulesAuthoritative: true,
	}, []Rule{{DomainSuffix: []string{"legacy.test"}, Outbound: "legacy"}}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "name: legacy") || strings.Contains(text, "legacy.test") {
		t.Fatalf("legacy resources leaked:\n%s", text)
	}
	for _, want := range []string{"name: native", "DOMAIN-SUFFIX,example.com,native", "MATCH,DIRECT"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateConfigWithResources_NativeResourcesOverlayLegacyFallback(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{ProxyGroups: []storage.ProxyGroup{
		{Name: "Block", Type: "select", Proxies: []string{"direct"}},
		{Name: "legacy-only", Type: "select", Proxies: []string{"direct"}},
	}}, "", nil, NativeResources{
		ProxyGroups: []ProxyGroup{{Name: "Block", Type: "select", Proxies: []string{"REJECT"}}},
		Rules:       []string{"DOMAIN-SUFFIX,new.example,Block"},
	}, []Rule{{DomainSuffix: []string{"old.example"}, Outbound: "legacy-only"}}, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"name: legacy-only", "DOMAIN-SUFFIX,new.example,Block", "DOMAIN-SUFFIX,old.example,legacy-only"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "name: Block") != 1 || !strings.Contains(text, "- REJECT") {
		t.Fatalf("native group did not override legacy definition:\n%s", text)
	}
	if strings.Index(text, "DOMAIN-SUFFIX,new.example,Block") > strings.Index(text, "DOMAIN-SUFFIX,old.example,legacy-only") {
		t.Fatalf("native rules must precede legacy fallback:\n%s", text)
	}
}

func TestGenerateConfigWithResources_AddsRuleProvider(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{}, "", nil, NativeResources{RuleProviders: map[string]map[string]interface{}{"youtube": {"type": "http", "url": "https://example.test/youtube.mrs", "behavior": "domain", "format": "mrs"}}}, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"rule-providers:", "youtube:", "behavior: domain", "format: mrs"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateConfigWithResources_AdvancedProxyGroupFields(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{}, "", nil, NativeResources{ProxyGroups: []ProxyGroup{{
		Name: "Auto NL", Type: "url-test", IncludeAllProviders: true, Filter: "(?i)nl", ExcludeType: "Http",
		Tolerance: 80, Timeout: 4000, MaxFailedTimes: 3, ExpectedStatus: "204", DisableUDP: true,
	}}}, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"include-all-providers: true", "filter: (?i)nl", "exclude-type: Http", "tolerance: 80", "timeout: 4000", "max-failed-times: 3", "expected-status: \"204\"", "disable-udp: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateConfig_PersistsRuntimeSelections(t *testing.T) {
	b, err := GenerateConfig(storage.SingboxRouterSettings{}, "", nil, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "profile:") || !strings.Contains(text, "store-selected: true") {
		t.Fatalf("runtime selection persistence missing:\n%s", text)
	}
}

func TestGenerateConfig_MihomoLocalListeners(t *testing.T) {
	b, err := GenerateConfig(storage.SingboxRouterSettings{
		MihomoMixedPort: 7890, MihomoHTTPPort: 7891, MihomoSOCKSPort: 7892,
	}, "", nil, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"mixed-port: 7890", "port: 7891", "socks-port: 7892"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestConvertSingboxRuleToMihomo_ConvertsHyphenatedGeoTags(t *testing.T) {
	rules := ConvertSingboxRuleToMihomo(Rule{
		RuleSet: []string{"geosite-telegram", "geoip-ru"}, Outbound: "Block",
	})
	want := []string{"GEOSITE,telegram,Block", "GEOIP,ru,Block,no-resolve"}
	if strings.Join(rules, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules = %#v, want %#v", rules, want)
	}
}

func TestGenerateConfigWithResources_AddsLoopbackMixedBridgeListeners(t *testing.T) {
	b, err := GenerateConfigWithResources(storage.SingboxRouterSettings{RoutingMode: "tproxy"}, "", nil, NativeResources{
		Proxies: []Proxy{{"name": "Native node", "type": "socks5", "server": "1.1.1.1", "port": 1080}},
		Listeners: []Listener{{
			Name: "mihomo-native-p-0123456789abcdef", Type: "tproxy", Port: 12020,
			Listen: "0.0.0.0", Proxy: "Native node",
		}},
	}, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Listeners) != 1 {
		t.Fatalf("listeners=%#v, want only native bridge", cfg.Listeners)
	}
	bridge := cfg.Listeners[0]
	if bridge.Name != "mihomo-native-p-0123456789abcdef" || bridge.Type != "mixed" || bridge.Port != 12020 || bridge.Listen != "127.0.0.1" || bridge.Proxy != "Native node" || !bridge.UDP {
		t.Fatalf("bridge listener=%#v", bridge)
	}
}

func TestGenerateConfigWithResources_RejectsIncompleteBridgeListeners(t *testing.T) {
	tests := []struct {
		name     string
		listener Listener
	}{
		{name: "missing name", listener: Listener{Port: 12020, Proxy: "node"}},
		{name: "missing port", listener: Listener{Name: "bridge", Proxy: "node"}},
		{name: "missing proxy", listener: Listener{Name: "bridge", Port: 12020}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateConfigWithResources(storage.SingboxRouterSettings{}, "", nil, NativeResources{Listeners: []Listener{tt.listener}}, nil, "direct", nil)
			if err == nil || !strings.Contains(err.Error(), "listener requires name, port and proxy") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGenerateSidecarConfig_IsExportsOnlyButKeepsControllerAndResolver(t *testing.T) {
	raw, err := GenerateSidecarConfig(NativeResources{
		Proxies:   []Proxy{{"name": "Native", "type": "socks5", "server": "proxy.example", "port": 1080}},
		Listeners: []Listener{{Name: "mihomo-native-p-node", Port: 12000, Proxy: "Native"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"tproxy-port", "redir-port", "port", "socks-port", "mixed-port", "tun"} {
		if _, exists := doc[forbidden]; exists {
			t.Fatalf("exports-only config unexpectedly contains %q:\n%s", forbidden, raw)
		}
	}
	if got := doc["external-controller"]; got != "127.0.0.1:9090" {
		t.Fatalf("external-controller=%v, want loopback controller", got)
	}
	dns, ok := doc["dns"].(map[string]any)
	if !ok || dns["enable"] != true {
		t.Fatalf("internal resolver missing: %#v", doc["dns"])
	}
	if _, exists := dns["listen"]; exists {
		t.Fatalf("exports-only DNS must not bind a listener: %#v", dns)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Listeners) != 1 {
		t.Fatalf("listeners=%#v", cfg.Listeners)
	}
	listener := cfg.Listeners[0]
	if listener.Type != "mixed" || listener.Listen != "127.0.0.1" || listener.Port != 12000 || listener.Proxy != "Native" || !listener.UDP {
		t.Fatalf("bridge listener=%#v", listener)
	}
}

func TestGenerateConfig_KeeneticCloudDynamicCIDRs(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		KeeneticCloudTunnel:   true,
		KeeneticCloudOutbound: "CloudVPN",
		DynamicCloudCIDRs:     []string{"1.2.3.4/32", "5.6.7.8/32"},
	}
	subProxies := []map[string]any{
		{"type": "socks", "tag": "CloudVPN", "server": "1.1.1.1", "server_port": 1080},
	}
	raw, err := GenerateConfig(settings, "", subProxies, nil, "direct", nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	has1 := false
	has2 := false
	for _, r := range cfg.Rules {
		if r == "IP-CIDR,1.2.3.4/32,CloudVPN,no-resolve" {
			has1 = true
		}
		if r == "IP-CIDR,5.6.7.8/32,CloudVPN,no-resolve" {
			has2 = true
		}
	}
	if !has1 || !has2 {
		t.Fatalf("missing dynamic cloud CIDR rules in Mihomo rules: %+v", cfg.Rules)
	}
}

func TestGenerateConfig_SubscriptionGroupInNativeProxyGroup(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		MihomoMixedPort: 1099,
	}
	subProxies := []map[string]any{
		{"type": "hysteria2", "tag": "hyst-1", "server": "1.2.3.4", "server_port": 443},
		{"type": "urltest", "tag": "sub-5090797d", "outbounds": []string{"hyst-1"}},
	}
	native := NativeResources{
		ProxyGroups: []ProxyGroup{
			{Name: "Задний ход", Type: "fallback", Proxies: []string{"sub-5090797d", "DIRECT"}},
		},
	}

	raw, err := GenerateConfigWithResources(settings, "", subProxies, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources failed: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	var zhGroup *ProxyGroup
	for i := range cfg.ProxyGroups {
		if cfg.ProxyGroups[i].Name == "Задний ход" {
			zhGroup = &cfg.ProxyGroups[i]
			break
		}
	}
	if zhGroup == nil {
		t.Fatalf("group 'Задний ход' not found in config")
	}
	if len(zhGroup.Proxies) != 2 || zhGroup.Proxies[0] != "sub-5090797d" {
		t.Fatalf("expected sub-5090797d to be preserved in 'Задний ход', got %+v", zhGroup.Proxies)
	}
}

func TestGenerateConfig_AdaptiveEgress(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		MihomoMixedPort: 1099,
	}
	native := NativeResources{
		ProxyGroups: []ProxyGroup{
			{Name: "TargetGroup", Type: "fallback", Proxies: []string{"DIRECT"}},
		},
		AdaptiveEgress: &AdaptiveEgressConfig{
			Enabled:       true,
			Device:        "awgsus0",
			SelectedGroup: "TargetGroup",
		},
	}

	raw, err := GenerateConfigWithResources(settings, "", nil, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources failed: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	if cfg.Tun == nil || !cfg.Tun.Enable {
		t.Fatalf("expected Tun enabled, got %#v", cfg.Tun)
	}
	if cfg.Tun.Device != "awgsus0" {
		t.Errorf("expected Tun.Device awgsus0, got %s", cfg.Tun.Device)
	}
	if cfg.Tun.Stack != "system" {
		t.Errorf("expected Tun.Stack system, got %s", cfg.Tun.Stack)
	}
	if cfg.Tun.AutoRoute {
		t.Errorf("expected Tun.AutoRoute false")
	}
	if cfg.TProxyPort != 51271 || cfg.RedirPort != 51272 {
		t.Errorf("expected TProxyPort=51271 and RedirPort=51272 in tproxy mode, got %d, %d", cfg.TProxyPort, cfg.RedirPort)
	}
	if cfg.MixedPort != 1099 {
		t.Errorf("expected MixedPort 1099 preserved, got %d", cfg.MixedPort)
	}
	if len(cfg.Rules) == 0 || cfg.Rules[0] != "IN-TYPE,TUN,TargetGroup" {
		t.Fatalf("expected first rule IN-TYPE,TUN,TargetGroup, got: %v", cfg.Rules)
	}

	// Verify that in policy-tun mode, ports are zeroed out
	settingsPolicyTun := settings
	settingsPolicyTun.RoutingMode = "policy-tun"
	rawTun, err := GenerateConfigWithResources(settingsPolicyTun, "", nil, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources (policy-tun) failed: %v", err)
	}
	var cfgTun Config
	if err := yaml.Unmarshal(rawTun, &cfgTun); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	if cfgTun.TProxyPort != 0 || cfgTun.RedirPort != 0 {
		t.Errorf("expected TProxyPort=0 and RedirPort=0 in policy-tun mode, got %d, %d", cfgTun.TProxyPort, cfgTun.RedirPort)
	}
}

func TestGenerateSidecarConfig_AdaptiveEgress(t *testing.T) {
	native := NativeResources{
		ProxyGroups: []ProxyGroup{
			{Name: "Fastest", Type: "url-test", Proxies: []string{"DIRECT"}},
		},
		AdaptiveEgress: &AdaptiveEgressConfig{
			Enabled:       true,
			Device:        "awgsus0",
			SelectedGroup: "Fastest",
		},
	}

	raw, err := GenerateSidecarConfig(native)
	if err != nil {
		t.Fatalf("GenerateSidecarConfig failed: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal sidecar yaml: %v", err)
	}

	if cfg.Tun == nil || !cfg.Tun.Enable || cfg.Tun.Device != "awgsus0" {
		t.Fatalf("expected sidecar Tun enabled with awgsus0, got %#v", cfg.Tun)
	}
	if len(cfg.Rules) < 2 || cfg.Rules[0] != "IN-TYPE,TUN,Fastest" || cfg.Rules[1] != "MATCH,DIRECT" {
		t.Fatalf("unexpected sidecar rules: %v", cfg.Rules)
	}
}

func TestGenerateConfig_SusaninOption(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		MihomoMixedPort: 1099,
		SusaninEnabled:  true,
		SusaninOutbound: "BackdoorProxy",
	}
	native := NativeResources{
		ProxyGroups: []ProxyGroup{
			{Name: "BackdoorProxy", Type: "fallback", Proxies: []string{"DIRECT"}},
		},
	}

	raw, err := GenerateConfigWithResources(settings, "", nil, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources failed: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	// 1. Verify susanin rule-provider
	rp, exists := cfg.RuleProvider["susanin"]
	if !exists {
		t.Fatalf("expected rule-provider susanin to be present in cfg.RuleProvider")
	}
	if rp["type"] != "file" || rp["behavior"] != "classical" || rp["format"] != "yaml" {
		t.Errorf("unexpected susanin rule-provider spec: %#v", rp)
	}
	if rp["path"] != "./rules/susanin.yaml" {
		t.Errorf("expected path ./rules/susanin.yaml, got %v", rp["path"])
	}

	// 2. Verify susanin rule in rules list
	foundRule := false
	for _, r := range cfg.Rules {
		if r == "RULE-SET,susanin,BackdoorProxy" {
			foundRule = true
			break
		}
	}
	if !foundRule {
		t.Fatalf("expected rule RULE-SET,susanin,BackdoorProxy in cfg.Rules, got: %v", cfg.Rules)
	}

	// 3. Verify no separate awgsus0 Tun device created
	if cfg.Tun != nil && cfg.Tun.Enable {
		t.Errorf("expected no Tun device for susanin option, got %#v", cfg.Tun)
	}

	// 4. Verify standard ports preserved
	if cfg.TProxyPort != 51271 || cfg.RedirPort != 51272 || cfg.MixedPort != 1099 {
		t.Errorf("expected 51271/51272/1099 ports, got %d/%d/%d", cfg.TProxyPort, cfg.RedirPort, cfg.MixedPort)
	}

	// 5. Test when SusaninEnabled is false
	settingsOff := settings
	settingsOff.SusaninEnabled = false
	rawOff, err := GenerateConfigWithResources(settingsOff, "", nil, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources (off) failed: %v", err)
	}
	var cfgOff Config
	if err := yaml.Unmarshal(rawOff, &cfgOff); err != nil {
		t.Fatalf("unmarshal off yaml: %v", err)
	}
	if _, exists := cfgOff.RuleProvider["susanin"]; exists {
		t.Errorf("expected no susanin rule provider when disabled")
	}
	for _, r := range cfgOff.Rules {
		if strings.Contains(r, "susanin") {
			t.Errorf("found unexpected susanin rule when disabled: %s", r)
		}
	}
}

func TestGenerateConfigWithResources_DuplicateProxyNamesBetweenSubAndNative(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
	}

	// subProxies has a socks proxy with the same name as a native proxy (e.g. from 20-router.json)
	subProxies := []map[string]any{
		{
			"type":        "socks",
			"tag":         "FIN-Premium",
			"server":      "127.0.0.1",
			"server_port": 12001,
		},
		{
			"type":        "vless",
			"tag":         "Sub-Proxy-1",
			"server":      "vless.example.com",
			"server_port": 443,
			"uuid":        "a1b2c3d4-e5f6-7890-1234-567890abcdef",
		},
	}

	native := NativeResources{
		Proxies: []Proxy{
			{
				"name":   "FIN-Premium",
				"type":   "trusttunnel",
				"server": "fin.trutun.online",
				"port":   443,
			},
		},
	}

	raw, err := GenerateConfigWithResources(settings, "", subProxies, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources failed on duplicate proxy name: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	if len(cfg.Proxies) != 2 {
		t.Fatalf("len(cfg.Proxies) = %d, want 2 (FIN-Premium native and Sub-Proxy-1)", len(cfg.Proxies))
	}

	// Verify native proxy took precedence
	var finProxy Proxy
	for _, p := range cfg.Proxies {
		if p["name"] == "FIN-Premium" {
			finProxy = p
			break
		}
	}
	if finProxy == nil {
		t.Fatal("FIN-Premium proxy not found in config")
	}
	if finProxy["type"] != "trusttunnel" {
		t.Fatalf("FIN-Premium type = %v, want trusttunnel (native precedence)", finProxy["type"])
	}
}

func TestGenerateConfigWithResources_ExcludesIPCIDRFromNameserverPolicy(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
	}

	native := NativeResources{
		RuleProviders: map[string]map[string]interface{}{
			"telegram-cidr": {
				"type":     "http",
				"url":      "https://example.com/telegram.mrs",
				"behavior": "ipcidr",
				"format":   "mrs",
			},
			"telegram-domain": {
				"type":     "http",
				"url":      "https://example.com/telegram-domain.mrs",
				"behavior": "domain",
				"format":   "mrs",
			},
		},
		DNSServers: []DNSServerSpec{
			{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"},
			{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9", Detour: "awg-awg20"},
		},
		DNSRules: []DNSRuleSpec{
			{
				RuleSet: []string{"geosite-google-gemini", "telegram-cidr", "telegram-domain", "geoip-ru", "susanin"},
				Server:  "dns-tunnel",
			},
		},
	}

	raw, err := GenerateConfigWithResources(settings, "", nil, native, nil, "DIRECT", nil)
	if err != nil {
		t.Fatalf("GenerateConfigWithResources failed: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	// telegram-cidr, geoip-ru and susanin MUST NOT be in NameserverPolicy
	if _, exists := cfg.DNS.NameserverPolicy["geosite:telegram-cidr"]; exists {
		t.Errorf("expected geosite:telegram-cidr to NOT be in NameserverPolicy")
	}
	if _, exists := cfg.DNS.NameserverPolicy["rule-set:telegram-cidr"]; exists {
		t.Errorf("expected rule-set:telegram-cidr to NOT be in NameserverPolicy")
	}
	if _, exists := cfg.DNS.NameserverPolicy["geosite:geoip-ru"]; exists {
		t.Errorf("expected geosite:geoip-ru to NOT be in NameserverPolicy")
	}
	if _, exists := cfg.DNS.NameserverPolicy["geosite:susanin"]; exists {
		t.Errorf("expected geosite:susanin to NOT be in NameserverPolicy")
	}
	if _, exists := cfg.DNS.NameserverPolicy["rule-set:susanin"]; exists {
		t.Errorf("expected rule-set:susanin to NOT be in NameserverPolicy")
	}

	// geosite-google-gemini and telegram-domain MUST be present
	if target, exists := cfg.DNS.NameserverPolicy["geosite:google-gemini"]; !exists || !strings.Contains(target, "9.9.9.9#awg-awg20") {
		t.Errorf("expected geosite:google-gemini in NameserverPolicy with target 9.9.9.9#awg-awg20, got %q", target)
	}
	if target, exists := cfg.DNS.NameserverPolicy["rule-set:telegram-domain"]; !exists || !strings.Contains(target, "9.9.9.9#awg-awg20") {
		t.Errorf("expected rule-set:telegram-domain in NameserverPolicy with target 9.9.9.9#awg-awg20, got %q", target)
	}
}

