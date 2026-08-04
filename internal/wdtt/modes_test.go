package wdtt

import "testing"

func TestNormalizeConnMode(t *testing.T) {
	if got := normalizeConnMode(""); got != ConnModeWG {
		t.Fatalf("empty: got %q", got)
	}
	if got := normalizeConnMode("RAW"); got != ConnModeRaw {
		t.Fatalf("raw: got %q", got)
	}
	if got := normalizeConnMode("wg"); got != ConnModeWG {
		t.Fatalf("wg: got %q", got)
	}
}

func TestClientUsesWireGuard(t *testing.T) {
	wg := ClientConfig{ConnMode: "wg"}
	if !wg.UsesWireGuard() {
		t.Fatal("wg must use wireguard")
	}
	raw := ClientConfig{ConnMode: "raw"}
	if raw.UsesWireGuard() {
		t.Fatal("raw must not use wireguard")
	}
}

func TestServerUsesWireGuardRelay(t *testing.T) {
	wg := ServerConfig{RelayMode: "wg"}
	if !wg.UsesWireGuardRelay() {
		t.Fatal("wg relay expected")
	}
	raw := ServerConfig{RelayMode: "raw"}
	if raw.UsesWireGuardRelay() {
		t.Fatal("raw relay must skip wg path")
	}
}
