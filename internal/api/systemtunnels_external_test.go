package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
)

func TestMarkExternalSystemTunnels(t *testing.T) {
	in := []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Description: "Phobos-router"}, {ID: "Wireguard1", Description: "Office"}}
	markExternal(in, obfuscator.Foreign{InitScript: true})
	if in[0].External != "phobos" || in[1].External != "" {
		t.Fatalf("%+v", in)
	}
	// Q16: префикс И признак установки. Один префикс (переименованный чужой
	// интерфейс) бейджа не даёт.
	in2 := []ndms.SystemWireguardTunnel{{ID: "Wireguard0", Description: "Phobos-router"}}
	markExternal(in2, obfuscator.Foreign{})
	if in2[0].External != "" {
		t.Fatalf("%+v", in2)
	}
}
