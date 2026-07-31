package freeturn

import (
	"testing"
	"unicode/utf8"
)

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

func TestTunnelNameFromClientTruncatesByRunes(t *testing.T) {
	long := ""
	for i := 0; i < 80; i++ {
		long += "я"
	}
	got := TunnelNameFromClient(ClientInstance{Name: long})
	if len([]rune(got)) != 60 {
		t.Fatalf("длина в рунах = %d, want 60", len([]rune(got)))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("обрезка порвала UTF-8: %q", got)
	}
}
