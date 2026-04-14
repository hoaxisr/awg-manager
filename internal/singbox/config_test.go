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
