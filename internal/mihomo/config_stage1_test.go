package mihomo

import (
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"gopkg.in/yaml.v3"
)

func TestGenerateConfig_UnknownRuleTarget_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	_, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{{"type": "socks", "tag": "valid-proxy", "server": "1.1.1.1", "server_port": 1080}},
		[]Rule{
			{Domain: []string{"example.com"}, Outbound: "typo-proxy"},
		},
		"valid-proxy",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want unknown rule target compile error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "rule" || compileErr.Resource != "typo-proxy" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}
}

func TestGenerateConfig_UnknownGroupMember_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{
		RoutingMode: "tproxy",
		ProxyGroups: []storage.ProxyGroup{
			{
				Name:    "my-group",
				Type:    "selector",
				Proxies: []string{"valid-proxy", "non-existent-proxy"},
			},
		},
	}
	_, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{{"type": "socks", "tag": "valid-proxy", "server": "1.1.1.1", "server_port": 1080}},
		nil,
		"my-group",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want unknown group member compile error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "proxy-group" || compileErr.Resource != "my-group" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}
	if !strings.Contains(compileErr.Message, "non-existent-proxy") {
		t.Fatalf("error message %q should mention missing proxy member", compileErr.Message)
	}
}

func TestGenerateConfig_UnknownGroupProvider_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	_, err := GenerateConfigWithResources(
		settings,
		"",
		nil,
		NativeResources{
			ProxyGroups: []ProxyGroup{
				{
					Name: "auto-group",
					Type: "url-test",
					Use:  []string{"missing-provider"},
				},
			},
		},
		nil,
		"auto-group",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want unknown group provider compile error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "proxy-group" || compileErr.Resource != "auto-group" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}
	if !strings.Contains(compileErr.Message, "missing-provider") {
		t.Fatalf("error message %q should mention missing provider", compileErr.Message)
	}
}

func TestGenerateConfig_GroupCycle_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	// Group A -> Group B -> Group A
	_, err := GenerateConfigWithResources(
		settings,
		"",
		nil,
		NativeResources{
			ProxyGroups: []ProxyGroup{
				{Name: "group-a", Type: "selector", Proxies: []string{"group-b"}},
				{Name: "group-b", Type: "selector", Proxies: []string{"group-a"}},
			},
		},
		nil,
		"group-a",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want circular group dependency error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "proxy-group" {
		t.Fatalf("unexpected compile error source: %+v", compileErr)
	}
	if !strings.Contains(compileErr.Message, "circular group dependency") {
		t.Fatalf("error message %q should mention circular dependency", compileErr.Message)
	}
}

func TestGenerateConfig_GroupSelfCycle_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	// Group A -> Group A
	_, err := GenerateConfigWithResources(
		settings,
		"",
		nil,
		NativeResources{
			ProxyGroups: []ProxyGroup{
				{Name: "self-loop", Type: "selector", Proxies: []string{"self-loop"}},
			},
		},
		nil,
		"self-loop",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want self-loop circular group dependency error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if !strings.Contains(compileErr.Message, "circular group dependency") {
		t.Fatalf("error message %q should mention circular dependency", compileErr.Message)
	}
}

func TestGenerateConfig_DanglingRuleSet_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	_, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{{"type": "socks", "tag": "proxy-1", "server": "1.1.1.1", "server_port": 1080}},
		[]Rule{
			{RuleSet: []string{"unregistered-ruleset"}, Outbound: "proxy-1"},
		},
		"proxy-1",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want dangling rule-set compile error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "rule" || compileErr.Resource != "unregistered-ruleset" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}
}

func TestGenerateConfig_UnknownListenerTarget_FailsClosed(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	_, err := GenerateConfig(
		settings,
		"",
		nil,
		nil,
		"direct",
		[]DeviceProxyListener{
			{ID: "dev1", Port: 1080, SelectedOutbound: "ghost-proxy", Enabled: true},
		},
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want unknown listener target error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "listener" {
		t.Fatalf("unexpected compile error source: %+v", compileErr)
	}
	if !strings.Contains(compileErr.Message, "ghost-proxy") {
		t.Fatalf("error message %q should mention ghost-proxy", compileErr.Message)
	}
}

func TestGenerateConfig_AWGDirectInterfaceBound_Preserved(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	// Real direct outbound with tag and bind_interface
	awgOutbound := map[string]any{
		"type":           "direct",
		"tag":            "awg-sys-Wireguard1",
		"bind_interface": "nwg0",
	}
	yamlBytes, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{awgOutbound},
		[]Rule{
			{Domain: []string{"my.site"}, Outbound: "awg-sys-Wireguard1"},
		},
		"direct",
		nil,
	)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v, want success for real interface-bound AWG direct", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal error: %v", err)
	}

	found := false
	for _, p := range cfg.Proxies {
		if p["name"] == "awg-sys-Wireguard1" {
			found = true
			if p["type"] != "direct" || p["interface-name"] != "nwg0" {
				t.Fatalf("unexpected proxy definition: %#v", p)
			}
		}
	}
	if !found {
		t.Fatal("expected awg-sys-Wireguard1 to be present in cfg.Proxies")
	}
}

func TestGenerateConfig_AWGTypoInRule_DoesNotSynthesizeInterface(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	// A rule mentions awg-sys-NonExistent, but it is not passed as an outbound
	_, err := GenerateConfig(
		settings,
		"",
		nil,
		[]Rule{
			{Domain: []string{"bad.site"}, Outbound: "awg-sys-NonExistent"},
		},
		"direct",
		nil,
	)
	if err == nil {
		t.Fatal("GenerateConfig() error = nil, want failure when referencing non-existent awg interface proxy")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Resource != "awg-sys-NonExistent" {
		t.Fatalf("unexpected resource in compile error: %+v", compileErr)
	}
}

func TestGenerateConfig_LegacyGeodataNormalization_BeforeValidation(t *testing.T) {
	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	proxy := map[string]any{"type": "socks", "tag": "p1", "server": "1.1.1.1", "server_port": 1080}
	yamlBytes, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{proxy},
		[]Rule{
			{RuleSet: []string{"geosite-telegram", "geoip-private"}, Outbound: "p1"},
		},
		"p1",
		nil,
	)
	if err != nil {
		t.Fatalf("GenerateConfig() error = %v, want legacy geosite/geoip rules to normalize and succeed", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(yamlBytes, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal error: %v", err)
	}

	rulesText := strings.Join(cfg.Rules, "\n")
	if !strings.Contains(rulesText, "GEOSITE,telegram,p1") {
		t.Fatalf("expected normalized GEOSITE,telegram,p1 in rules, got:\n%s", rulesText)
	}
	if !strings.Contains(rulesText, "GEOIP,private,p1,no-resolve") {
		t.Fatalf("expected normalized GEOIP,private,p1,no-resolve in rules, got:\n%s", rulesText)
	}
}

func TestGenerateSidecarConfig_ValidatesListenerAndProviderTargets(t *testing.T) {
	// 1. Valid listener pointing to proxy and group
	validSidecar, err := GenerateSidecarConfig(NativeResources{
		Proxies: []Proxy{{"name": "NodeA", "type": "socks5", "server": "1.1.1.1", "port": 1080}},
		ProxyGroups: []ProxyGroup{
			{Name: "GroupA", Type: "selector", Proxies: []string{"NodeA"}},
		},
		Listeners: []Listener{
			{Name: "b1", Port: 12001, Proxy: "NodeA"},
			{Name: "b2", Port: 12002, Proxy: "GroupA"},
		},
	})
	if err != nil {
		t.Fatalf("valid sidecar failed: %v", err)
	}
	if len(validSidecar) == 0 {
		t.Fatal("empty sidecar bytes")
	}

	// 2. Listener referencing non-existent proxy
	_, err = GenerateSidecarConfig(NativeResources{
		Listeners: []Listener{{Name: "b1", Port: 12001, Proxy: "missing-node"}},
	})
	if err == nil {
		t.Fatal("GenerateSidecarConfig() error = nil, want missing listener target error")
	}
	var compileErr *MihomoCompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "listener" || compileErr.Resource != "b1" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}

	// 3. Group referencing non-existent provider in sidecar
	_, err = GenerateSidecarConfig(NativeResources{
		ProxyGroups: []ProxyGroup{
			{Name: "g1", Type: "url-test", Use: []string{"no-provider"}},
		},
	})
	if err == nil {
		t.Fatal("GenerateSidecarConfig() error = nil, want missing provider error")
	}
	if !errors.As(err, &compileErr) {
		t.Fatalf("expected MihomoCompileError, got %T: %v", err, err)
	}
	if compileErr.Source != "proxy-group" || compileErr.Resource != "g1" {
		t.Fatalf("unexpected compile error fields: %+v", compileErr)
	}
}

func TestCompileError_WrappingAndErrorsAs(t *testing.T) {
	orig := NewCompileError("rule", "target-proxy", "DOMAIN,test.com,target-proxy", "unknown target")
	wrapped := errors.Join(errors.New("wrapper 1"), orig)

	var target *MihomoCompileError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As failed to unwrap MihomoCompileError")
	}
	if target.Source != "rule" || target.Resource != "target-proxy" {
		t.Fatalf("unexpected unwrapped error: %+v", target)
	}
}

func TestCompileError_NoSecretsInMessage(t *testing.T) {
	secretUUID := "99999999-dead-beef-cafe-000000000000"
	secretPassword := "super-secret-password-12345"

	settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
	proxy := map[string]any{
		"type":        "vless",
		"tag":         "p1",
		"server":      "example.com",
		"server_port": 443,
		"uuid":        secretUUID,
		"password":    secretPassword,
	}

	_, err := GenerateConfig(
		settings,
		"",
		[]map[string]any{proxy},
		[]Rule{{Domain: []string{"example.com"}, Outbound: "unknown-group"}},
		"p1",
		nil,
	)
	if err == nil {
		t.Fatal("expected error for unknown outbound")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, secretUUID) {
		t.Fatalf("error message contains secret UUID %q: %s", secretUUID, errMsg)
	}
	if strings.Contains(errMsg, secretPassword) {
		t.Fatalf("error message contains secret password %q: %s", secretPassword, errMsg)
	}
}

func TestCompileRuleGrammar_Comprehensive(t *testing.T) {
	proxy := map[string]any{"type": "socks", "tag": "p1", "server": "1.1.1.1", "server_port": 1080}
	ruleProviders := map[string]map[string]interface{}{
		"my-ruleset": {"type": "http", "behavior": "domain", "url": "https://example.com/rs.yaml", "path": "rs.yaml"},
	}

	tests := []struct {
		name      string
		rules     []string
		wantError bool
	}{
		{
			name:      "MATCH DIRECT",
			rules:     []string{"MATCH,DIRECT"},
			wantError: false,
		},
		{
			name:      "DOMAIN-SUFFIX with group/proxy",
			rules:     []string{"DOMAIN-SUFFIX,example.org,p1"},
			wantError: false,
		},
		{
			name:      "IP-CIDR with no-resolve",
			rules:     []string{"IP-CIDR,10.0.0.0/8,p1,no-resolve"},
			wantError: false,
		},
		{
			name:      "RULE-SET with provider",
			rules:     []string{"RULE-SET,my-ruleset,p1"},
			wantError: false,
		},
		{
			name:      "RULE-SET missing provider",
			rules:     []string{"RULE-SET,missing-rs,p1"},
			wantError: true,
		},
		{
			name:      "AND rule with sub-rules",
			rules:     []string{"AND,((DOMAIN,example.com),(NETWORK,tcp)),p1"},
			wantError: false,
		},
		{
			name:      "malformed 1-field rule",
			rules:     []string{"DOMAIN"},
			wantError: true,
		},
		{
			name:      "malformed 2-field rule for DOMAIN",
			rules:     []string{"DOMAIN,example.com"},
			wantError: true,
		},
		{
			name:      "empty target with trailing comma",
			rules:     []string{"DOMAIN,example.com,"},
			wantError: true,
		},
		{
			name:      "empty payload with whitespace",
			rules:     []string{"DOMAIN,  ,p1"},
			wantError: true,
		},
		{
			name:      "unknown rule type",
			rules:     []string{"STRANGE-UNKNOWN-TYPE,payload,p1"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := storage.SingboxRouterSettings{RoutingMode: "tproxy"}
			_, err := GenerateConfigWithResources(
				settings,
				"",
				[]map[string]any{proxy},
				NativeResources{RuleProviders: ruleProviders, Rules: tt.rules, RulesAuthoritative: true},
				nil,
				"p1",
				nil,
			)
			if tt.wantError && err == nil {
				t.Fatalf("expected error for rules: %v, got nil", tt.rules)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected error for rules %v: %v", tt.rules, err)
			}
		})
	}
}

func TestProviderProxyValidation(t *testing.T) {
	cfgBase := func() Config {
		return Config{
			Proxies: []Proxy{{"name": "p1", "type": "direct"}},
			Rules:   []string{"MATCH,DIRECT"},
		}
	}

	// Non-string proxy in proxy-provider
	cfgNonString := cfgBase()
	cfgNonString.ProxyProvider = map[string]map[string]interface{}{
		"provider1": {"proxy": 12345},
	}
	if err := validateCompiledConfig(&cfgNonString); err == nil {
		t.Fatal("expected error for non-string proxy in proxy-provider, got nil")
	}

	// Unknown target in proxy-provider
	cfgUnknownTarget := cfgBase()
	cfgUnknownTarget.ProxyProvider = map[string]map[string]interface{}{
		"provider1": {"proxy": "non-existent"},
	}
	if err := validateCompiledConfig(&cfgUnknownTarget); err == nil {
		t.Fatal("expected error for unknown target in proxy-provider, got nil")
	}

	// Valid target in proxy-provider
	cfgValid := cfgBase()
	cfgValid.ProxyProvider = map[string]map[string]interface{}{
		"provider1": {"proxy": "p1"},
	}
	if err := validateCompiledConfig(&cfgValid); err != nil {
		t.Fatalf("unexpected error for valid proxy target: %v", err)
	}

	// Normalization of "direct" and "reject" in provider proxy
	cfgNorm := cfgBase()
	cfgNorm.ProxyProvider = map[string]map[string]interface{}{
		"p-direct": {"proxy": "direct"},
		"p-reject": {"proxy": "reject"},
	}
	cfgNorm.RuleProvider = map[string]map[string]interface{}{
		"r-block": {"proxy": "block"},
	}
	if err := normalizeCompiledConfig(&cfgNorm); err != nil {
		t.Fatalf("normalizeCompiledConfig failed: %v", err)
	}
	if cfgNorm.ProxyProvider["p-direct"]["proxy"] != "DIRECT" {
		t.Fatalf("expected DIRECT, got %v", cfgNorm.ProxyProvider["p-direct"]["proxy"])
	}
	if cfgNorm.ProxyProvider["p-reject"]["proxy"] != "REJECT" {
		t.Fatalf("expected REJECT, got %v", cfgNorm.ProxyProvider["p-reject"]["proxy"])
	}
	if cfgNorm.RuleProvider["r-block"]["proxy"] != "REJECT" {
		t.Fatalf("expected REJECT, got %v", cfgNorm.RuleProvider["r-block"]["proxy"])
	}
}

func TestListenerConflictValidation(t *testing.T) {
	// Duplicate listener names
	cfgDupName := Config{
		Listeners: []Listener{
			{Name: "l1", Port: 8001, Proxy: "DIRECT"},
			{Name: "l1", Port: 8002, Proxy: "DIRECT"},
		},
		Rules: []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgDupName); err == nil {
		t.Fatal("expected error for duplicate listener names, got nil")
	}

	// Port out of range
	cfgBadPort := Config{
		Listeners: []Listener{
			{Name: "l1", Port: 70000, Proxy: "DIRECT"},
		},
		Rules: []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgBadPort); err == nil {
		t.Fatal("expected error for listener port > 65535, got nil")
	}

	// Listener vs Listener port conflict with overlapping address and protocol
	cfgListenerConflict := Config{
		Listeners: []Listener{
			{Name: "l1", Type: "mixed", Port: 8001, Listen: "0.0.0.0", Proxy: "DIRECT"},
			{Name: "l2", Type: "mixed", Port: 8001, Listen: "127.0.0.1", Proxy: "DIRECT"},
		},
		Rules: []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgListenerConflict); err == nil {
		t.Fatal("expected error for overlapping listener ports, got nil")
	}

	// Listener vs Listener on different specific IPs (no wildcard) does NOT conflict
	cfgDifferentIPs := Config{
		Listeners: []Listener{
			{Name: "l1", Type: "mixed", Port: 8001, Listen: "127.0.0.1", Proxy: "DIRECT"},
			{Name: "l2", Type: "mixed", Port: 8001, Listen: "192.168.1.1", Proxy: "DIRECT"},
		},
		Rules: []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgDifferentIPs); err != nil {
		t.Fatalf("unexpected error for different specific IPs: %v", err)
	}

	// Listener vs Global port conflict
	cfgGlobalConflict := Config{
		TProxyPort: 51271,
		Listeners: []Listener{
			{Name: "l1", Type: "mixed", Port: 51271, Listen: "127.0.0.1", Proxy: "DIRECT"},
		},
		Rules: []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgGlobalConflict); err == nil {
		t.Fatal("expected error for listener port conflicting with global tproxy-port, got nil")
	}

	// Global port vs Global port conflict
	cfgGlobalPortConflict := Config{
		TProxyPort: 1099,
		MixedPort:  1099,
		Rules:      []string{"MATCH,DIRECT"},
	}
	if err := validateCompiledConfig(&cfgGlobalPortConflict); err == nil {
		t.Fatal("expected error for global tproxy-port conflicting with mixed-port, got nil")
	}
}

func TestErrorSanitization_UTF8(t *testing.T) {
	cyrillic := "Тестовая ошибка с очень длинным описанием на русском языке, которое превышает лимит рун..."
	longMessage := strings.Repeat(cyrillic, 10)

	err := CompileErrorf("source\r\n\twith_controls", "resource\x00_test", "rule\twith\nspaces", "%s", longMessage)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	// Verify whitespace collapsing and control char stripping
	if strings.Contains(err.Source, "\r") || strings.Contains(err.Source, "\n") || strings.Contains(err.Source, "\t") {
		t.Fatalf("Source contains unstripped whitespace: %q", err.Source)
	}
	if strings.Contains(err.Resource, "\x00") {
		t.Fatalf("Resource contains control character: %q", err.Resource)
	}

	// Verify rune-safe truncation: rune count of Message must be <= 512
	runeCount := len([]rune(err.Message))
	if runeCount > 512 {
		t.Fatalf("Message rune count %d exceeds 512", runeCount)
	}

	// Verify no panic or invalid UTF-8 byte truncation
	str := err.Error()
	if !strings.HasPrefix(str, "mihomo compile error [") {
		t.Fatalf("unexpected error format: %s", str)
	}
}

func TestCompositeRules_NestedTrailingTokensRejected(t *testing.T) {
	testCases := []struct {
		name string
		rule string
	}{
		{
			name: "NOT with trailing unexpected token",
			rule: "AND,((NOT,((DOMAIN,example.com)),unexpected),(NETWORK,tcp)),DIRECT",
		},
		{
			name: "AND with trailing extra token",
			rule: "OR,((AND,((DOMAIN,a),(NETWORK,tcp)),extra),(NETWORK,udp)),REJECT",
		},
		{
			name: "NOT with multiple extra trailing tokens",
			rule: "AND,((NOT,((DOMAIN,a)),extra1,extra2),(NETWORK,tcp)),DIRECT",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				Mode:  "rule",
				Rules: []string{tc.rule},
			}
			err := validateCompiledConfig(&cfg)
			if err == nil {
				t.Fatalf("expected error for composite rule with trailing tokens, got nil (rule=%q)", tc.rule)
			}
			if !strings.Contains(err.Error(), "must have exactly 2 fields") {
				t.Fatalf("expected 'must have exactly 2 fields' error, got: %v", err)
			}
		})
	}
}

func TestListenerValidation_TypesAndUDPCapabilities(t *testing.T) {
	proxy := Proxy{"type": "socks5", "name": "p1", "server": "1.1.1.1", "port": 1080}

	t.Run("unknown listener type rejected fail-closed", func(t *testing.T) {
		cfg := Config{
			Mode:    "rule",
			Proxies: []Proxy{proxy},
			Rules:   []string{"MATCH,DIRECT"},
			Listeners: []Listener{
				{Name: "bad-listener", Type: "unknown-proto", Port: 8888, Proxy: "DIRECT"},
			},
		}
		err := validateCompiledConfig(&cfg)
		if err == nil {
			t.Fatal("expected error for unknown listener type, got nil")
		}
		if !strings.Contains(err.Error(), "unsupported type") {
			t.Fatalf("expected 'unsupported type' error, got: %v", err)
		}
	})

	t.Run("empty listener type rejected", func(t *testing.T) {
		cfg := Config{
			Mode:    "rule",
			Proxies: []Proxy{proxy},
			Rules:   []string{"MATCH,DIRECT"},
			Listeners: []Listener{
				{Name: "empty-listener", Type: "", Port: 8888, Proxy: "DIRECT"},
			},
		}
		err := validateCompiledConfig(&cfg)
		if err == nil {
			t.Fatal("expected error for empty listener type, got nil")
		}
		if !strings.Contains(err.Error(), "empty type") {
			t.Fatalf("expected 'empty type' error, got: %v", err)
		}
	})

	t.Run("case-insensitivity of valid listener types", func(t *testing.T) {
		cfg := Config{
			Mode:    "rule",
			Proxies: []Proxy{proxy},
			Rules:   []string{"MATCH,DIRECT"},
			Listeners: []Listener{
				{Name: "mixed-listener", Type: "MIXED", Port: 8888, Proxy: "DIRECT"},
				{Name: "http-listener", Type: "Http", Port: 8889, Proxy: "DIRECT"},
			},
		}
		err := validateCompiledConfig(&cfg)
		if err != nil {
			t.Fatalf("unexpected error for uppercase listener types: %v", err)
		}
	})

	t.Run("redir UDP flag affects collision detection", func(t *testing.T) {
		cfgTCPConflict := Config{
			Mode:       "rule",
			Proxies:    []Proxy{proxy},
			Rules:      []string{"MATCH,DIRECT"},
			TProxyPort: 8888, // global tproxy is tcp+udp
			Listeners: []Listener{
				// redir with UDP=false conflicts on TCP
				{Name: "redir-listener", Type: "redir", Port: 8888, Proxy: "DIRECT", UDP: false},
			},
		}
		err := validateCompiledConfig(&cfgTCPConflict)
		if err == nil {
			t.Fatal("expected TCP conflict with global tproxy, got nil")
		}
	})
}
