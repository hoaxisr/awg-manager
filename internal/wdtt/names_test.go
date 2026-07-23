package wdtt

import "testing"

func TestTunnelNameFromClient(t *testing.T) {
	got := TunnelNameFromClient(ClientInstance{Name: "Германия"})
	if got != "Германия wdtt" {
		t.Fatalf("got %q", got)
	}
	got = TunnelNameFromClient(ClientInstance{Name: "Германия wdtt"})
	if got != "Германия wdtt" {
		t.Fatalf("duplicate suffix: got %q", got)
	}
	got = TunnelNameFromClient(ClientInstance{Name: ""})
	if got != "WDTT wdtt" {
		t.Fatalf("empty name: got %q", got)
	}
}

func TestEnsureUniqueListenAddr(t *testing.T) {
	listens := []string{"127.0.0.1:9000", "127.0.0.1:9000"}
	got := ensureUniqueListenAddr(listens, 1, listens[1], nil, 9000, 9200)
	if got == "127.0.0.1:9000" {
		t.Fatalf("expected reassigned port, got %q", got)
	}
}
