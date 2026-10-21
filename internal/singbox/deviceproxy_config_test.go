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
	// 00-base.json and would collide if duplicated here. dns is allowed
	// only as dns.servers (concatenated across slots, F702): its scalars
	// are first-file-wins and owned elsewhere.
	for _, k := range []string{"log", "experimental"} {
		if _, ok := raw[k]; ok {
			t.Errorf("slot output must not contain %q key: %s", k, s)
		}
	}
	if dns, ok := raw["dns"].(map[string]any); ok {
		for k := range dns {
			if k != "servers" {
				t.Errorf("slot dns must carry only servers, got %q: %s", k, s)
			}
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
	rules := cfg.routeRules()[3:] // [0..1] — отказ loopback, [2] — LAN-имена
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
	first, _ := cfg.routeRules()[2].(map[string]any)
	if first["outbound"] != "device-proxy-kids-lan" || first["domain_regex"] == nil {
		t.Errorf("first rule = %v, want outbound device-proxy-kids-lan", first)
	}
	if !hasOutboundTag(cfg, "device-proxy-kids-lan") {
		t.Fatalf("device-proxy-kids-lan missing: %v", cfg.outbounds())
	}
	if srv := deviceProxyDNSServers(cfg); len(srv) != 1 || srv[0]["tag"] != "device-proxy-kids-lan-dns" {
		t.Errorf("dns servers = %v, want device-proxy-kids-lan-dns", srv)
	}
	cfg.RemoveAllDeviceProxyInstances()
	if hasOutboundTag(cfg, "device-proxy-kids-lan") {
		t.Error("device-proxy-kids-lan survived RemoveAllDeviceProxyInstances")
	}
	if srv := deviceProxyDNSServers(cfg); len(srv) != 0 {
		t.Errorf("dns servers after RemoveAllDeviceProxyInstances = %v", srv)
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

// F702 (хвост): LAN-имена через прокси для устройств (SOCKS5h/HTTP передают
// имя) — правило по имени ведёт в device-proxy-lan, а тот резолвит их своим
// локальным DNS (127.0.0.1 роутера): публичный резолвер их не знает, а
// my.keenetic.net резолвится в собственный, но не частный адрес роутера —
// ip_is_private его не поймал бы. Стенд 21.10: 302 за 22 мс.
func TestBuildDeviceProxyConfig_LANNamesViaLocalDNS(t *testing.T) {
	data, err := BuildDeviceProxyConfig(DeviceProxySpec{
		Enabled: true, ListenAddr: "0.0.0.0", Port: 1099, SelectedTag: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	rules := cfg.routeRules()[2:] // [0..1] — отказ loopback
	if len(rules) != 3 {
		t.Fatalf("rules = %v, want lan-names, private, binding", rules)
	}
	names, _ := rules[0].(map[string]any)
	if names["inbound"] != "device-proxy-in" || names["outbound"] != "device-proxy-lan" {
		t.Errorf("rule 0 = %v, want LAN names → device-proxy-lan", names)
	}
	got, _ := json.Marshal(map[string]any{"domain_suffix": names["domain_suffix"], "domain_regex": names["domain_regex"]})
	// Узко (решение владельца 21.10): без .local (mDNS) и .internal —
	// корпоративные имена должны уходить в выбранный туннель.
	want := `{"domain_regex":["^[^.]+$"],"domain_suffix":["lan","home.arpa","my.keenetic.net","my.netcraze.net"]}`
	if string(got) != want {
		t.Errorf("LAN names matcher = %s, want %s", got, want)
	}
	if r, _ := rules[1].(map[string]any); r["ip_is_private"] != true {
		t.Errorf("rule 1 = %v, want ip_is_private", r)
	}

	var lan map[string]any
	for _, v := range cfg.outbounds() {
		if ob, _ := v.(map[string]any); ob["tag"] == "device-proxy-lan" {
			lan = ob
		}
	}
	if lan["domain_resolver"] != "device-proxy-lan-dns" {
		t.Errorf("device-proxy-lan domain_resolver = %v, want device-proxy-lan-dns", lan["domain_resolver"])
	}
	// udp на резолвер роутера, а не type:local — тот уводит .local в mDNS
	// мультикастом во все интерфейсы, включая WAN. Адрес отправителя не нужен:
	// к loopback-назначению sing-box сокет к интерфейсу не привязывает
	// (sing/common/control/bind.go, N.IsVirtual).
	got, _ = json.Marshal(deviceProxyDNSServers(cfg))
	if want := `[{"server":"127.0.0.1","tag":"device-proxy-lan-dns","type":"udp"}]`; string(got) != want {
		t.Errorf("dns servers = %s, want %s", got, want)
	}

	cfg.RemoveDeviceProxy()
	if srv := deviceProxyDNSServers(cfg); len(srv) != 0 {
		t.Errorf("dns servers after RemoveDeviceProxy = %v", srv)
	}
}

func deviceProxyDNSServers(c *Config) []map[string]any {
	var out []map[string]any
	dns, _ := c.raw["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	for _, v := range servers {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ip_is_private в sing-box включает loopback и unspecified: без явного отказа
// правило LAN отдавало бы их клиентам прокси. Первые правила инстанса — отказ
// по адресу и по имени localhost.
func TestBuildDeviceProxyConfig_RejectsLoopbackFirst(t *testing.T) {
	data, err := BuildDeviceProxyConfig(DeviceProxySpec{
		Enabled: true, ListenAddr: "0.0.0.0", Port: 1099, SelectedTag: "direct",
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
		`{"action":"reject","inbound":"device-proxy-in","ip_cidr":["127.0.0.0/8","::1/128","0.0.0.0/32","::/128"]}`,
		`{"action":"reject","domain_suffix":["localhost"],"inbound":"device-proxy-in"}`,
	}
	if len(rules) < len(want) {
		t.Fatalf("rules = %v", rules)
	}
	for i, w := range want {
		got, _ := json.Marshal(rules[i])
		if string(got) != w {
			t.Errorf("rule %d = %s, want %s", i, got, w)
		}
	}
	cfg.RemoveDeviceProxy()
	if n := len(cfg.routeRules()); n != 0 {
		t.Errorf("after RemoveDeviceProxy %d rules left", n)
	}
}
