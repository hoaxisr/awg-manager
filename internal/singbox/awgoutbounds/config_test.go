// internal/singbox/awgoutbounds/config_test.go
package awgoutbounds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveFile_AtomicAndContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")

	entries := []AWGEntry{
		{Tag: "awg-tunnel-a", Label: "A", Kind: "managed", Iface: "t2s0"},
		{Tag: "awg-sys-Wireguard0", Label: "W0", Kind: "system", Iface: "nwg0"},
	}
	if err := saveFile(path, entries); err != nil {
		t.Fatalf("saveFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 2 {
		t.Fatalf("want 2 outbounds, got %d", len(got.Outbounds))
	}
	first := got.Outbounds[0]
	if first["type"] != "direct" || first["tag"] != "awg-tunnel-a" || first["bind_interface"] != "t2s0" {
		t.Errorf("first outbound shape wrong: %+v", first)
	}
}

func TestSaveFile_EmptyEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")
	if err := saveFile(path, nil); err != nil {
		t.Fatalf("saveFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" {
		t.Fatalf("expected non-empty file")
	}
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 0 {
		t.Errorf("want 0 outbounds, got %d", len(got.Outbounds))
	}
}

func TestSaveFile_OverwriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")
	if err := os.WriteFile(path, []byte(`{"old":"junk"}`), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := saveFile(path, []AWGEntry{{Tag: "awg-x", Iface: "t2s0"}}); err != nil {
		t.Fatalf("saveFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 1 {
		t.Errorf("expected file to be replaced, got %d outbounds", len(got.Outbounds))
	}
}

// Каждый outbound обязан резолвить домен через свой DNS-сервер, а тот —
// ходить detour'ом через сам туннель (#846).
func TestMarshalEntries_DomainResolverAndDNSServers(t *testing.T) {
	raw, err := marshalEntries([]AWGEntry{
		{Tag: "awg-tunA", Kind: "managed", Iface: "t2s0", Resolver: "10.8.0.1"},
		{Tag: "awg-sys-Wireguard0", Kind: "system", Iface: "nwg0"},
	})
	if err != nil {
		t.Fatalf("marshalEntries: %v", err)
	}
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
		DNS       struct {
			Servers []map[string]any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 2 || len(got.DNS.Servers) != 2 {
		t.Fatalf("want 2 outbounds and 2 dns servers, got %d/%d: %s", len(got.Outbounds), len(got.DNS.Servers), raw)
	}

	resolver, ok := got.Outbounds[0]["domain_resolver"].(map[string]any)
	if !ok {
		t.Fatalf("outbound 0 has no domain_resolver object: %+v", got.Outbounds[0])
	}
	if resolver["server"] != "dns-awg-tunA" {
		t.Errorf("domain_resolver.server = %v, want dns-awg-tunA", resolver["server"])
	}

	// Туннель со своим DNS: первый его адрес, detour на собственный outbound.
	s0 := got.DNS.Servers[0]
	if s0["type"] != "udp" || s0["tag"] != "dns-awg-tunA" || s0["server"] != "10.8.0.1" || s0["detour"] != "awg-tunA" {
		t.Errorf("dns server 0 wrong: %+v", s0)
	}
	// Туннель без DNS (системный): fallback-адрес, detour всё равно свой.
	s1 := got.DNS.Servers[1]
	if s1["server"] != "1.1.1.1" {
		t.Errorf("dns server 1 server = %v, want fallback 1.1.1.1", s1["server"])
	}
	if s1["tag"] != "dns-awg-sys-Wireguard0" || s1["detour"] != "awg-sys-Wireguard0" {
		t.Errorf("dns server 1 wrong: %+v", s1)
	}
}

// Пустой слот всё равно объявляет обе секции пустыми массивами (не null),
// иначе sing-box не сольёт config.d.
func TestMarshalEntries_EmptyKeepsDNSSection(t *testing.T) {
	raw, err := marshalEntries(nil)
	if err != nil {
		t.Fatalf("marshalEntries: %v", err)
	}
	var got struct {
		Outbounds *[]map[string]any `json:"outbounds"`
		DNS       *struct {
			Servers *[]map[string]any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Outbounds == nil || len(*got.Outbounds) != 0 {
		t.Errorf("outbounds must be an empty array, got %v: %s", got.Outbounds, raw)
	}
	if got.DNS == nil || got.DNS.Servers == nil || len(*got.DNS.Servers) != 0 {
		t.Errorf("dns.servers must be an empty array: %s", raw)
	}
}
