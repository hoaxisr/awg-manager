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

func TestEnsureUniqueServerListenAddr_SkipsWdttWgPort(t *testing.T) {
	got := ensureUniqueServerListenAddr([]string{"0.0.0.0:56002"}, 0, "0.0.0.0:56001", map[int]bool{56001: true}, 56000, 56100)
	if got == "0.0.0.0:56001" {
		t.Fatalf("expected reassigned listen, got %q", got)
	}
	gotWg := ensureUniqueWgPort(56001, got, map[int]bool{56001: true}, 56000, 56100)
	if gotWg == 56001 {
		t.Fatalf("expected wg port != 56001, got %d", gotWg)
	}
}

func TestEnsureUniqueListenAddr_SkipsReservedCrossProxy(t *testing.T) {
	listens := []string{"127.0.0.1:9000"}
	reserved := map[int]bool{9000: true}
	got := ensureUniqueListenAddr(listens, 0, listens[0], reserved, 9000, 9200)
	if got != "127.0.0.1:9001" {
		t.Fatalf("got %q want 127.0.0.1:9001", got)
	}
}
