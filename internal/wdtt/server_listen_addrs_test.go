package wdtt

import "testing"

func TestServerListenAddrs(t *testing.T) {
	cfg := ServerConfig{
		Listen:       "0.0.0.0:56002",
		DirectListen: "0.0.0.0:56010",
		RawListen:    "0.0.0.0:56013",
		WgPort:       56001,
	}
	got := cfg.ServerListenAddrs()
	want := []string{
		"0.0.0.0:56002",
		"0.0.0.0:56013",
		"0.0.0.0:56010",
		"127.0.0.1:56001",
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestServerListenAddrsSkipsDirectSameAsDTLS(t *testing.T) {
	cfg := ServerConfig{
		Listen:       "0.0.0.0:56002",
		DirectListen: "0.0.0.0:56002",
		WgPort:       56001,
	}
	got := cfg.ServerListenAddrs()
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	if got[1] != "0.0.0.0:56003" {
		t.Fatalf("raw auto: got %q", got[1])
	}
}
