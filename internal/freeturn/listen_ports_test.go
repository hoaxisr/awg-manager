package freeturn

import "testing"

func TestEnsureUniqueServerListenAddr_SkipsReservedCrossProxy(t *testing.T) {
	listens := []string{"0.0.0.0:56001"}
	reserved := map[int]bool{56001: true}
	got := ensureUniqueServerListenAddr(listens, 0, listens[0], reserved, 56000, 56100)
	if got == "0.0.0.0:56001" {
		t.Fatalf("expected reassigned port, got %q", got)
	}
}

func TestEnsureUniqueListenAddr_SkipsReservedCrossProxy(t *testing.T) {
	listens := []string{"127.0.0.1:9000"}
	reserved := map[int]bool{9000: true} // e.g. WDTT client on same port
	got := ensureUniqueListenAddr(listens, 0, listens[0], reserved, 9000, 9200)
	if got == "127.0.0.1:9000" {
		t.Fatalf("expected reassigned port, got %q", got)
	}
	if got != "127.0.0.1:9001" {
		t.Fatalf("got %q want 127.0.0.1:9001", got)
	}
}

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

func TestLocalListenPort(t *testing.T) {
	if port, ok := LocalListenPort("localhost:9001"); !ok || port != 9001 {
		t.Fatalf("LocalListenPort(localhost:9001) = %d, %v", port, ok)
	}
	if _, ok := LocalListenPort("1.2.3.4:9001"); ok {
		t.Fatal("non-localhost endpoint must not match")
	}
}

func TestNormalizePlatform(t *testing.T) {
	cases := map[string]string{
		"mobile":  "mobile",
		"Mobile":  "mobile",
		"desktop": "desktop",
		"":        "desktop",
		"edge":    "desktop",
	}
	for in, want := range cases {
		if got := normalizePlatform(in); got != want {
			t.Fatalf("normalizePlatform(%q) = %q want %q", in, got, want)
		}
	}
}
