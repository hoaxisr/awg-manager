package mihomonative

import (
	"path/filepath"
	"testing"
)

func TestCompileManualTrustTunnel(t *testing.T) {
	node, err := CompileManual(ManualProxyInput{
		Name: "TT", Protocol: "trusttunnel", Server: "tt.example", Port: 443,
		EnginePreference: EngineMihomo,
		Config:           map[string]interface{}{"username": "user", "password": "secret", "quic": true, "sni": "edge.example"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if node.Transport != "quic" || node.NativeConfig["type"] != "trusttunnel" {
		t.Fatalf("node=%#v", node)
	}
}

func TestManualProxyAppearsInNativeSelectGroup(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateManual(ManualProxyInput{
		Name: "HY2", Protocol: "hysteria2", Server: "hy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	groups := store.ConfigProviderGroups()
	if len(groups) != 1 || groups[0]["name"] != "Mihomo: Native" {
		t.Fatalf("groups=%#v", groups)
	}
	members, ok := groups[0]["proxies"].([]string)
	if !ok || len(members) != 1 || members[0] != "HY2" {
		t.Fatalf("members=%#v", groups[0]["proxies"])
	}
}

func TestCompileManualRejectsUnknownAndMissingFields(t *testing.T) {
	base := ManualProxyInput{Name: "X", Protocol: "anytls", Server: "a.example", Port: 443, EnginePreference: EngineMihomo}
	base.Config = map[string]interface{}{"password": "secret", "reality-opts": map[string]interface{}{"public-key": "x"}}
	if _, err := CompileManual(base, EngineMihomo); err == nil {
		t.Fatal("expected unknown field rejection")
	}
	base.Config = map[string]interface{}{}
	if _, err := CompileManual(base, EngineMihomo); err == nil {
		t.Fatal("expected missing password rejection")
	}
}
