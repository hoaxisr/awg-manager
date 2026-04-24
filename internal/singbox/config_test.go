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

func TestConfig_NewDNSBootstrap(t *testing.T) {
	// The fresh skeleton ships an explicit DNS block with a UDP
	// bootstrap (IP literal, no hostname resolution needed) and a DoH
	// upstream that points domain_resolver at the bootstrap tag. Both
	// detour="direct" so DNS never loops through a tunnel that itself
	// needs DNS to start.
	c := NewConfig()
	dns, ok := c.raw["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns block missing or not an object: %#v", c.raw["dns"])
	}
	if dns["strategy"] != "ipv4_only" {
		t.Errorf("strategy: want ipv4_only (Keenetic has no IPv6 egress by default), got %v", dns["strategy"])
	}
	servers, ok := dns["servers"].([]any)
	if !ok || len(servers) != 2 {
		t.Fatalf("servers: want 2 (bootstrap + DoH), got %#v", dns["servers"])
	}

	bootstrap := servers[0].(map[string]any)
	if bootstrap["tag"] != "dns-bootstrap" || bootstrap["type"] != "udp" {
		t.Errorf("bootstrap server: %#v", bootstrap)
	}
	// sing-box 1.13 native schema uses `server`, not `address`. If we
	// ever regress to the legacy key, `sing-box check` will reject the
	// config with "unknown field 'address'".
	if _, hasLegacyAddress := bootstrap["address"]; hasLegacyAddress {
		t.Errorf("bootstrap must NOT use legacy `address` field, got %#v", bootstrap)
	}
	if bootstrap["server"] != "1.1.1.1" {
		t.Errorf("bootstrap server must be an IP literal (no hostname to resolve), got %v", bootstrap["server"])
	}
	// Bootstrap must NOT carry detour=direct — sing-box 1.13 FATALs on
	// "detour to an empty direct outbound makes no sense" at startup.
	// When M2+ adds tunnel routes we'll pin bootstrap via route rules.
	if _, hasDetour := bootstrap["detour"]; hasDetour {
		t.Errorf("bootstrap must not set detour (fails sing-box 1.13 startup), got %v", bootstrap["detour"])
	}

	doh := servers[1].(map[string]any)
	if doh["tag"] != "dns-doh" || doh["type"] != "https" {
		t.Errorf("DoH server: %#v", doh)
	}
	if _, hasLegacyAddress := doh["address"]; hasLegacyAddress {
		t.Errorf("DoH must NOT use legacy `address` field, got %#v", doh)
	}
	if doh["server"] != "cloudflare-dns.com" {
		t.Errorf("DoH server hostname: want cloudflare-dns.com, got %v", doh["server"])
	}
	if doh["domain_resolver"] != "dns-bootstrap" {
		t.Errorf("DoH must point domain_resolver at the bootstrap tag to avoid chicken-and-egg, got %v", doh["domain_resolver"])
	}
	// DoH must NOT set detour: following route.final lets DNS flow
	// through whichever tunnel the user later makes default, giving
	// automatic leak protection once routing is wired up.
	if _, hasDetour := doh["detour"]; hasDetour {
		t.Errorf("DoH must not pin a detour; found %v", doh["detour"])
	}
	if dns["final"] != "dns-doh" {
		t.Errorf("final: want dns-doh, got %v", dns["final"])
	}

	// sing-box 1.12+ requires route.default_domain_resolver; without
	// it outbound hostname resolution is deprecated and emits a FATAL
	// on `sing-box check`. Pinning it to dns-bootstrap stays safe
	// even when the user later routes everything through a tunnel.
	route := c.raw["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("route.default_domain_resolver: want dns-bootstrap, got %v", route["default_domain_resolver"])
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
