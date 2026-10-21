// internal/singbox/deviceproxy_config_test.go
package singbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildDeviceProxyConfigEnabled(t *testing.T) {
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "192.168.1.1",
		Port:        1080,
		SelectedTag: "awg-vpn0",
		AWGTags:     []string{"awg-vpn0"},
	}
	data, err := BuildDeviceProxyConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "tag") {
		t.Fatalf("output looks empty: %s", s)
	}
	// Must contain selector outbound + inbound + route rule.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inbounds, _ := raw["inbounds"].([]any)
	if len(inbounds) == 0 {
		t.Errorf("missing/empty inbounds: %s", s)
	}
	outbounds, _ := raw["outbounds"].([]any)
	if len(outbounds) == 0 {
		t.Errorf("missing/empty outbounds: %s", s)
	}
	route, ok := raw["route"].(map[string]any)
	if !ok {
		t.Fatalf("missing route: %s", s)
	}
	rules, _ := route["rules"].([]any)
	if len(rules) == 0 {
		t.Errorf("missing/empty route.rules: %s", s)
	}

	// Inbound tag must be the device-proxy one.
	ib, _ := inbounds[0].(map[string]any)
	if tag, _ := ib["tag"].(string); tag != deviceProxyInboundTag {
		t.Errorf("first inbound tag = %q, want %q", tag, deviceProxyInboundTag)
	}

	// Selector outbound must reference the selected member.
	var selector map[string]any
	for _, v := range outbounds {
		ob, _ := v.(map[string]any)
		if tag, _ := ob["tag"].(string); tag == deviceProxySelectorTag {
			selector = ob
		}
	}
	if selector == nil {
		t.Fatalf("selector outbound %q missing in: %s", deviceProxySelectorTag, s)
	}
	if def, _ := selector["default"].(string); def != "awg-vpn0" {
		t.Errorf("selector default = %q, want awg-vpn0", def)
	}

	// Slot file must NOT carry base-config keys — those live in
	// 00-base.json and would collide if duplicated here.
	for _, k := range []string{"log", "dns", "experimental"} {
		if _, ok := raw[k]; ok {
			t.Errorf("slot output must not contain %q key: %s", k, s)
		}
	}
}

func TestBuildDeviceProxyConfigDisabled(t *testing.T) {
	spec := DeviceProxySpec{Enabled: false}
	data, err := BuildDeviceProxyConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// When disabled, output is a well-formed but contentless config.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No inbound or selector outbound from device-proxy.
	if inbounds, ok := raw["inbounds"].([]any); ok && len(inbounds) > 0 {
		t.Errorf("expected no inbounds: %v", inbounds)
	}
	// Outbounds may include the NewConfig() defaults but must not include
	// the device-proxy selector tag.
	outbounds, _ := raw["outbounds"].([]any)
	for _, v := range outbounds {
		ob, _ := v.(map[string]any)
		if tag, _ := ob["tag"].(string); tag == deviceProxySelectorTag {
			t.Errorf("unexpected device-proxy selector outbound: %v", ob)
		}
	}
}

// F701: слот прокси для устройств стоит раньше правил маршрутизатора, и
// системное `ip_is_private → direct` маршрутизатора до его входа больше не
// доходит. Своё правило — перед привязкой: иначе LAN-адреса через прокси
// уходят в выбранный туннель. Снятие инстанса уносит оба правила.
func TestBuildDeviceProxyConfig_PrivateDirectBeforeBinding(t *testing.T) {
	data, err := BuildDeviceProxyConfig(DeviceProxySpec{
		Enabled: true, ListenAddr: "192.168.1.1", Port: 1080,
		SelectedTag: "awg-vpn0", AWGTags: []string{"awg-vpn0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	rules := cfg.routeRules()
	want := []string{
		`{"inbound":"device-proxy-in","ip_is_private":true,"outbound":"device-proxy-lan"}`,
		`{"inbound":"device-proxy-in","outbound":"device-proxy-selector"}`,
	}
	if len(rules) != len(want) {
		t.Fatalf("rules = %v, want %v", rules, want)
	}
	for i, r := range rules {
		got, _ := json.Marshal(r)
		if string(got) != want[i] {
			t.Errorf("rule %d = %s, want %s", i, got, want[i])
		}
	}

	// F702: под auto_detect_interface (включённый маршрутизатор) sing-box
	// привязывает сокет к интерфейсу, владеющему адресом, и на ядре Keenetic
	// соединение к собственному адресу роутера висит до таймаута. Любой
	// *_bind_address отключает автопривязку (common/dialer/default.go,
	// disableDefaultBind) — стенд 21.10: 192.168.0.1/WAN/nwg1 открываются.
	var lan map[string]any
	for _, v := range cfg.outbounds() {
		if ob, _ := v.(map[string]any); ob["tag"] == "device-proxy-lan" {
			lan = ob
		}
	}
	if lan == nil {
		t.Fatalf("outbound device-proxy-lan missing: %v", cfg.outbounds())
	}
	if lan["type"] != "direct" || lan["inet4_bind_address"] != "0.0.0.0" {
		t.Errorf("device-proxy-lan = %v, want direct bound to 0.0.0.0", lan)
	}

	cfg.RemoveDeviceProxy()
	if n := len(cfg.routeRules()); n != 0 {
		t.Errorf("after RemoveDeviceProxy %d rules left: %v", n, cfg.routeRules())
	}
	for _, v := range cfg.outbounds() {
		if ob, _ := v.(map[string]any); ob["tag"] == "device-proxy-lan" {
			t.Errorf("device-proxy-lan survived RemoveDeviceProxy")
		}
	}
}

// Именованный инстанс получает свой LAN-outbound; снятие всех инстансов
// уносит и его.
func TestDeviceProxyInstance_NamedLANOutbound(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.EnsureDeviceProxyInstance(DeviceProxyInstanceSpec{
		ID: "kids", Enabled: true, ListenAddr: "0.0.0.0", Port: 1100, SelectedTag: "direct",
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := cfg.routeRules()[0].(map[string]any)
	if first["outbound"] != "device-proxy-kids-lan" {
		t.Errorf("first rule = %v, want outbound device-proxy-kids-lan", first)
	}
	if !hasOutboundTag(cfg, "device-proxy-kids-lan") {
		t.Fatalf("device-proxy-kids-lan missing: %v", cfg.outbounds())
	}
	cfg.RemoveAllDeviceProxyInstances()
	if hasOutboundTag(cfg, "device-proxy-kids-lan") {
		t.Error("device-proxy-kids-lan survived RemoveAllDeviceProxyInstances")
	}
}

func hasOutboundTag(c *Config, tag string) bool {
	for _, v := range c.outbounds() {
		if ob, _ := v.(map[string]any); ob["tag"] == tag {
			return true
		}
	}
	return false
}
