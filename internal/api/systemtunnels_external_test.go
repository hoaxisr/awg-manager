package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
)

func TestMarkExternalSystemTunnels(t *testing.T) {
	detects := 0
	detect := func(f obfuscator.Foreign) func() obfuscator.Foreign {
		return func() obfuscator.Foreign { detects++; return f }
	}

	in := []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Description: "Phobos-router"}, {ID: "Wireguard1", Description: "Office"}}
	markExternal(in, detect(obfuscator.Foreign{InitScript: true}))
	if in[0].External != "phobos" || in[1].External != "" {
		t.Fatalf("%+v", in)
	}
	// Q16: префикс И признак установки. Один префикс (переименованный чужой
	// интерфейс) бейджа не даёт.
	in2 := []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Description: "Phobos-router"}}
	markExternal(in2, detect(obfuscator.Foreign{}))
	if in2[0].External != "" {
		t.Fatalf("%+v", in2)
	}
	if detects != 2 {
		t.Fatalf("детект по префиксу зван %d раз, ожидалось 2", detects)
	}

	// Списка без префикса достаточно, чтобы отказаться от поиска следа
	// установки: он читает /proc целиком, а список опрашивают каждые 5 с.
	detects = 0
	in3 := []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Description: "Office"}, {ID: "Wireguard1"}}
	markExternal(in3, detect(obfuscator.Foreign{InitScript: true, ProcessAlive: true}))
	if detects != 0 {
		t.Fatalf("детект зван без единого префикса: %d", detects)
	}
	if in3[0].External != "" || in3[1].External != "" {
		t.Fatalf("%+v", in3)
	}
}
