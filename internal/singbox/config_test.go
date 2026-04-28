// internal/singbox/config_test.go
package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_NewEmpty(t *testing.T) {
	c := NewConfig()
	if len(c.Tunnels()) != 0 {
		t.Error("expected 0 tunnels")
	}
}

func TestConfig_AddTunnel_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := NewConfig()

	ob := json.RawMessage(`{"type":"vless","tag":"Germany","server":"de.tld","server_port":443,"uuid":"u"}`)
	if err := c.AddTunnel("Germany", "vless", "de.tld", 443, ob); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	list := loaded.Tunnels()
	if len(list) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(list))
	}
	if list[0].Tag != "Germany" || list[0].Protocol != "vless" || list[0].ListenPort != 1080 {
		t.Errorf("tunnel: %+v", list[0])
	}
	if list[0].ProxyInterface != "Proxy0" {
		t.Errorf("proxy iface: %s", list[0].ProxyInterface)
	}
}

func TestConfig_AddTunnel_TagConflict(t *testing.T) {
	c := NewConfig()
	ob := json.RawMessage(`{"type":"vless","tag":"X"}`)
	if err := c.AddTunnel("X", "vless", "h", 1, ob); err != nil {
		t.Fatal(err)
	}
	if err := c.AddTunnel("X", "vless", "h", 1, ob); err == nil {
		t.Error("expected tag conflict")
	}
}

func TestConfig_RemoveTunnel(t *testing.T) {
	c := NewConfig()
	c.AddTunnel("A", "vless", "h", 1, json.RawMessage(`{"type":"vless","tag":"A"}`))
	c.AddTunnel("B", "vless", "h", 2, json.RawMessage(`{"type":"vless","tag":"B"}`))
	if err := c.RemoveTunnel("A"); err != nil {
		t.Fatal(err)
	}
	list := c.Tunnels()
	if len(list) != 1 || list[0].Tag != "B" {
		t.Errorf("after remove: %+v", list)
	}
	// Port 1080 should now be free; next add reuses it
	c.AddTunnel("C", "vless", "h", 3, json.RawMessage(`{"type":"vless","tag":"C"}`))
	list = c.Tunnels()
	var gotC TunnelInfo
	for _, ti := range list {
		if ti.Tag == "C" {
			gotC = ti
		}
	}
	if gotC.ListenPort != 1080 {
		t.Errorf("port reuse: got %d, want 1080", gotC.ListenPort)
	}
}

func TestConfig_ProxyInterface_StableAcrossRemove(t *testing.T) {
	c := NewConfig()
	c.AddTunnel("A", "vless", "h", 1, json.RawMessage(`{"type":"vless","tag":"A"}`))
	c.AddTunnel("B", "vless", "h", 2, json.RawMessage(`{"type":"vless","tag":"B"}`))
	c.AddTunnel("C", "vless", "h", 3, json.RawMessage(`{"type":"vless","tag":"C"}`))

	// Before: A=Proxy0, B=Proxy1, C=Proxy2
	var cBefore TunnelInfo
	for _, ti := range c.Tunnels() {
		if ti.Tag == "C" {
			cBefore = ti
		}
	}
	if cBefore.ProxyInterface != "Proxy2" {
		t.Fatalf("C before remove: ProxyInterface=%q, want Proxy2", cBefore.ProxyInterface)
	}

	// Remove B — C's ProxyInterface must stay "Proxy2" (tied to port 1082)
	if err := c.RemoveTunnel("B"); err != nil {
		t.Fatal(err)
	}
	var cAfter TunnelInfo
	for _, ti := range c.Tunnels() {
		if ti.Tag == "C" {
			cAfter = ti
		}
	}
	if cAfter.ProxyInterface != "Proxy2" {
		t.Errorf("C after remove: ProxyInterface=%q, want Proxy2 (must stay stable)", cAfter.ProxyInterface)
	}

	// Add D — reuses port 1081 = Proxy1
	c.AddTunnel("D", "vless", "h", 4, json.RawMessage(`{"type":"vless","tag":"D"}`))
	var d TunnelInfo
	for _, ti := range c.Tunnels() {
		if ti.Tag == "D" {
			d = ti
		}
	}
	if d.ProxyInterface != "Proxy1" {
		t.Errorf("D: ProxyInterface=%q, want Proxy1 (slot freed by B)", d.ProxyInterface)
	}
}

func TestConfig_AllocPort_Exhausted(t *testing.T) {
	c := NewConfig()
	inbounds := make([]any, 0, 65536-firstPort+1)
	for p := firstPort; p <= 65535; p++ {
		inbounds = append(inbounds, map[string]any{
			"type":        "mixed",
			"tag":         fmt.Sprintf("t%d-in", p),
			"listen":      "127.0.0.1",
			"listen_port": p,
		})
	}
	c.raw["inbounds"] = inbounds
	_, err := c.allocPort()
	if err == nil {
		t.Fatal("expected error on exhausted port range")
	}
}

func TestConfig_AtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Pre-populate with garbage
	os.WriteFile(path, []byte("existing"), 0644)
	c := NewConfig()
	c.AddTunnel("X", "vless", "h", 1, json.RawMessage(`{"type":"vless","tag":"X"}`))
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if len(b) < 10 || b[0] != '{' {
		t.Errorf("save output: %s", b)
	}
}

func TestConfig_Tunnels_KernelInterface(t *testing.T) {
	c := NewConfig()
	c.AddTunnel("A", "vless", "h", 1, json.RawMessage(`{"type":"vless","tag":"A"}`))
	c.AddTunnel("B", "vless", "h", 2, json.RawMessage(`{"type":"vless","tag":"B"}`))

	got := map[string]string{}
	for _, ti := range c.Tunnels() {
		got[ti.Tag] = ti.KernelInterface
	}
	if got["A"] != "t2s0" {
		t.Errorf("A: KernelInterface=%q, want t2s0", got["A"])
	}
	if got["B"] != "t2s1" {
		t.Errorf("B: KernelInterface=%q, want t2s1", got["B"])
	}
}

func TestConfig_EnsureDeviceProxy_Full(t *testing.T) {
	c := NewConfig()

	// Seed a sing-box user outbound so EnsureDeviceProxy has an sb tag to include.
	ob := json.RawMessage(`{"type":"vless","server":"x","server_port":443}`)
	if err := c.AddTunnel("VLESS-RU", "vless", "x", 443, ob); err != nil {
		t.Fatalf("seed AddTunnel: %v", err)
	}

	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "10.10.10.1",
		Port:        1099,
		Auth:        DeviceProxyAuth{Enabled: true, Username: "u", Password: "p"},
		SelectedTag: "awg-tun123",
		AWGTags:     []string{"awg-tun123"},
		SBTags:      []string{"VLESS-RU"},
	}
	if err := c.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("EnsureDeviceProxy: %v", err)
	}

	// Inbound has users
	var inbound map[string]any
	for _, v := range c.inbounds() {
		ib := v.(map[string]any)
		if ib["tag"] == "device-proxy-in" {
			inbound = ib
		}
	}
	users, _ := inbound["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users len = %d, want 1", len(users))
	}
	u := users[0].(map[string]any)
	if u["username"] != "u" || u["password"] != "p" {
		t.Fatalf("users = %v", users)
	}

	// Selector outbound present with correct members and default
	var selector map[string]any
	for _, v := range c.outbounds() {
		ob := v.(map[string]any)
		if ob["tag"] == "device-proxy-selector" {
			selector = ob
		}
	}
	if selector == nil {
		t.Fatalf("device-proxy-selector missing; outbounds=%v", c.outbounds())
	}
	if selector["type"] != "selector" {
		t.Fatalf("selector type = %v", selector["type"])
	}
	members, _ := selector["outbounds"].([]any)
	want := []string{"direct", "VLESS-RU", "awg-tun123"}
	if len(members) != len(want) {
		t.Fatalf("members = %v, want %v", members, want)
	}
	for i, w := range want {
		if members[i] != w {
			t.Fatalf("members[%d] = %v, want %q", i, members[i], w)
		}
	}
	if selector["default"] != "awg-tun123" {
		t.Fatalf("default = %v, want awg-tun123", selector["default"])
	}

	// AWG-direct outbound must NOT be written by EnsureDeviceProxy —
	// it now lives in 15-awg.json owned by awgoutbounds.
	for _, v := range c.outbounds() {
		ob := v.(map[string]any)
		tag, _ := ob["tag"].(string)
		obType, _ := ob["type"].(string)
		if obType == "direct" && tag == "awg-tun123" {
			t.Fatalf("EnsureDeviceProxy must NOT write awg-* direct outbounds; found awg-tun123")
		}
	}

	// Route rule at front
	rules := c.routeRules()
	if len(rules) == 0 {
		t.Fatalf("no route rules")
	}
	first := rules[0].(map[string]any)
	if first["inbound"] != "device-proxy-in" || first["outbound"] != "device-proxy-selector" {
		t.Fatalf("first rule = %v", first)
	}
}

func TestConfig_EnsureDeviceProxy_Idempotent(t *testing.T) {
	c := NewConfig()
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "0.0.0.0",
		Port:        1099,
		SelectedTag: "direct",
	}
	_ = c.EnsureDeviceProxy(spec)
	snap1, _ := json.Marshal(c.raw)
	_ = c.EnsureDeviceProxy(spec)
	snap2, _ := json.Marshal(c.raw)
	if string(snap1) != string(snap2) {
		t.Fatalf("non-idempotent:\n%s\nvs\n%s", snap1, snap2)
	}
}

func TestConfig_RemoveDeviceProxy_ClearsEverything(t *testing.T) {
	c := NewConfig()
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "0.0.0.0",
		Port:        1099,
		SelectedTag: "awg-x",
		AWGTags:     []string{"awg-x"},
	}
	_ = c.EnsureDeviceProxy(spec)
	c.RemoveDeviceProxy()

	for _, v := range c.inbounds() {
		if v.(map[string]any)["tag"] == "device-proxy-in" {
			t.Fatalf("inbound not removed")
		}
	}
	for _, v := range c.outbounds() {
		tag, _ := v.(map[string]any)["tag"].(string)
		if tag == "device-proxy-selector" {
			t.Fatalf("outbound not removed: %s", tag)
		}
	}
	for _, v := range c.routeRules() {
		r := v.(map[string]any)
		if r["inbound"] == "device-proxy-in" {
			t.Fatalf("route rule not removed")
		}
	}
}

// Regression: Tunnels() must not surface the device-proxy-selector as a
// user tunnel. userOutbounds filters by type; selector was not in the
// original exclusion list.
func TestConfig_EnsureDeviceProxy_SelectorNotInUserOutbounds(t *testing.T) {
	c := NewConfig()
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "0.0.0.0",
		Port:        1099,
		SelectedTag: "direct",
	}
	_ = c.EnsureDeviceProxy(spec)
	for _, t2 := range c.Tunnels() {
		if t2.Tag == "device-proxy-selector" || t2.Tag == "" {
			t.Fatalf("device-proxy-selector leaked into Tunnels(): %+v", t2)
		}
	}
}

// Regression: Tunnels() must not surface awg-<id> tags as user tunnels.
// EnsureDeviceProxy no longer writes awg-* direct outbounds, but if they
// were present (e.g. written by awgoutbounds into another file) they should
// not be surfaced through the device-proxy selector either.
func TestConfig_EnsureDeviceProxy_AWGDirectNotInUserOutbounds(t *testing.T) {
	c := NewConfig()
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "0.0.0.0",
		Port:        1099,
		SelectedTag: "awg-tun123",
		AWGTags:     []string{"awg-tun123"},
	}
	_ = c.EnsureDeviceProxy(spec)
	// EnsureDeviceProxy must not write an awg-tun123 direct outbound into
	// the top-level outbounds list (those now live in 15-awg.json).
	for _, v := range c.outbounds() {
		ob := v.(map[string]any)
		tag, _ := ob["tag"].(string)
		obType, _ := ob["type"].(string)
		if obType == "direct" && tag == "awg-tun123" {
			t.Fatalf("awg-tun123 direct outbound must not be written by EnsureDeviceProxy: %+v", ob)
		}
	}
}

func TestConfig_EnsureDeviceProxy_InboundOnly(t *testing.T) {
	c := NewConfig()

	spec := DeviceProxySpec{
		Enabled:    true,
		ListenAddr: "0.0.0.0",
		Port:       1099,
	}
	if err := c.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("EnsureDeviceProxy: %v", err)
	}

	// Inbound present
	var found map[string]any
	for _, v := range c.inbounds() {
		ib, _ := v.(map[string]any)
		if ib["tag"] == "device-proxy-in" {
			found = ib
			break
		}
	}
	if found == nil {
		t.Fatalf("inbound device-proxy-in not found; inbounds=%v", c.inbounds())
	}
	if found["type"] != "mixed" {
		t.Fatalf("inbound type = %v, want mixed", found["type"])
	}
	if found["listen"] != "0.0.0.0" {
		t.Fatalf("listen = %v, want 0.0.0.0", found["listen"])
	}
	if port, _ := toInt(found["listen_port"]); port != 1099 {
		t.Fatalf("listen_port = %v, want 1099", found["listen_port"])
	}
	if _, hasUsers := found["users"]; hasUsers {
		t.Fatalf("users should be absent when auth disabled")
	}
}

func TestEnsureDeviceProxy_NoLongerWritesAWGOutbounds(t *testing.T) {
	c := NewConfig()
	spec := DeviceProxySpec{
		Enabled:    true,
		ListenAddr: "127.0.0.1",
		Port:       1080,
		AWGTags:    []string{"awg-x", "awg-sys-Wireguard0"},
	}
	if err := c.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("EnsureDeviceProxy: %v", err)
	}
	// Verify: selector contains both AWG tags as members
	for _, ob := range c.outbounds() {
		obMap := ob.(map[string]any)
		if obMap["tag"] == deviceProxySelectorTag {
			members := obMap["outbounds"].([]any)
			memberStrs := make([]string, 0, len(members))
			for _, m := range members {
				memberStrs = append(memberStrs, m.(string))
			}
			has := func(s string) bool {
				for _, m := range memberStrs {
					if m == s {
						return true
					}
				}
				return false
			}
			if !has("awg-x") || !has("awg-sys-Wireguard0") {
				t.Errorf("selector members missing AWG tags: %v", memberStrs)
			}
		}
	}
	// Verify: no awg-* direct outbound in top-level outbounds
	for _, ob := range c.outbounds() {
		obMap := ob.(map[string]any)
		tag, _ := obMap["tag"].(string)
		obType, _ := obMap["type"].(string)
		if obType == "direct" && len(tag) >= 4 && tag[:4] == "awg-" {
			t.Errorf("EnsureDeviceProxy must NOT write awg-* outbounds anymore, found: %s", tag)
		}
	}
}

func TestEnsureDeviceProxy_StripsLegacyAWGOutbounds(t *testing.T) {
	c := NewConfig()
	// Seed config with legacy awg-* outbound (as old version would have written)
	c.setOutbounds([]any{
		map[string]any{"type": "direct", "tag": "awg-legacy", "bind_interface": "t2s0"},
		map[string]any{"type": "direct", "tag": "direct"},
	})
	spec := DeviceProxySpec{
		Enabled: true, ListenAddr: "127.0.0.1", Port: 1080,
	}
	if err := c.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("EnsureDeviceProxy: %v", err)
	}
	// awg-legacy must be gone
	for _, ob := range c.outbounds() {
		if obMap := ob.(map[string]any); obMap["tag"] == "awg-legacy" {
			t.Errorf("legacy awg-* outbound was not stripped")
		}
	}
}
