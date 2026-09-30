package mihomonative

import "testing"

func TestCompileSocks(t *testing.T) {
	raw := "socks5://user:secret@127.0.0.1:10890#Home-CDN-Socks"
	node, err := CompileSocks(raw, EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatalf("CompileSocks failed: %v", err)
	}

	if node.SelectedEngine != EngineMihomo {
		t.Fatalf("expected Mihomo engine, got %v", node.SelectedEngine)
	}
	if node.Name != "Home-CDN-Socks" {
		t.Fatalf("expected name Home-CDN-Socks, got %v", node.Name)
	}
	if node.NativeConfig["type"] != "socks5" {
		t.Fatalf("expected type socks5, got %v", node.NativeConfig["type"])
	}
	if node.NativeConfig["server"] != "127.0.0.1" {
		t.Fatalf("expected server 127.0.0.1, got %v", node.NativeConfig["server"])
	}
	if node.NativeConfig["port"] != 10890 {
		t.Fatalf("expected port 10890, got %v", node.NativeConfig["port"])
	}
	if node.NativeConfig["username"] != "user" {
		t.Fatalf("expected username user, got %v", node.NativeConfig["username"])
	}
	if node.NativeConfig["password"] != "secret" {
		t.Fatalf("expected password secret, got %v", node.NativeConfig["password"])
	}
}

func TestCompileSocksNoAuth(t *testing.T) {
	raw := "socks://127.0.0.1:10890"
	node, err := CompileSocks(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatalf("CompileSocks failed: %v", err)
	}
	if node.Name != "127.0.0.1:10890" {
		t.Fatalf("expected default name, got %v", node.Name)
	}
	if _, ok := node.NativeConfig["username"]; ok {
		t.Fatalf("unexpected username in config")
	}
}
