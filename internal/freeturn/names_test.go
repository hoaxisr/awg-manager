package freeturn

import "testing"

func TestTunnelNameFromClient(t *testing.T) {
	got := TunnelNameFromClient(ClientInstance{Name: "Клиент"})
	if got != "Клиент FT" {
		t.Fatalf("got %q", got)
	}
	got = TunnelNameFromClient(ClientInstance{Name: "Клиент FT"})
	if got != "Клиент FT" {
		t.Fatalf("duplicate suffix: got %q", got)
	}
	got = TunnelNameFromClient(ClientInstance{Name: ""})
	if got != "FreeTurn FT" {
		t.Fatalf("empty name: got %q", got)
	}
}
