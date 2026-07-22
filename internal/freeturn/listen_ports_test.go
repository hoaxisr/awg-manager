package freeturn

import "testing"

func TestNextClientListen_SkipsReserved(t *testing.T) {
	clients := []ClientInstance{
		{Config: ClientConfig{Listen: "127.0.0.1:9000"}},
	}
	reserved := map[int]bool{9001: true}
	got := nextClientListen(clients, reserved)
	if got != "127.0.0.1:9002" {
		t.Fatalf("got %q want 127.0.0.1:9002", got)
	}
}

func TestNextClientListen_NoReserved(t *testing.T) {
	clients := []ClientInstance{
		{Config: ClientConfig{Listen: "127.0.0.1:9000"}},
	}
	got := nextClientListen(clients, nil)
	if got != "127.0.0.1:9001" {
		t.Fatalf("got %q want 127.0.0.1:9001", got)
	}
}

func TestLocalListenPortMatch(t *testing.T) {
	if !LocalListenPortMatch("127.0.0.1:9001", "localhost:9001") {
		t.Fatal("same localhost port must match")
	}
	if LocalListenPortMatch("127.0.0.1:9001", "127.0.0.1:9000") {
		t.Fatal("different ports must not match")
	}
	if LocalListenPortMatch("127.0.0.1:9001", "1.2.3.4:9001") {
		t.Fatal("non-localhost endpoint must not match")
	}
}

func TestNormalizeBrowser(t *testing.T) {
	cases := map[string]string{
		"chromium": "chrome",
		"Chrome":   "chrome",
		"firefox":  "firefox",
		"safari":   "safari",
		"":         "chrome",
		"edge":     "chrome",
	}
	for in, want := range cases {
		if got := normalizeBrowser(in); got != want {
			t.Fatalf("normalizeBrowser(%q) = %q want %q", in, got, want)
		}
	}
}
