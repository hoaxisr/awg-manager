package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

func TestHostRouteHolders(t *testing.T) {
	const ip = "192.0.2.10"
	on := func(id, backend string, obf *storage.Obfuscator) storage.AWGTunnel {
		return storage.AWGTunnel{ID: id, Backend: backend, Enabled: true, ResolvedEndpointIP: ip, Obfuscator: obf}
	}
	off := on("awg11", "kernel", nil)
	off.Enabled = false
	for _, tc := range []struct {
		name string
		list []storage.AWGTunnel
		want netutil.RouteHolders
	}{
		{"только сам", []storage.AWGTunnel{on("awg10", "kernel", nil)}, netutil.RouteHolders{}},
		{"выключенный сосед", []storage.AWGTunnel{off}, netutil.RouteHolders{}},
		{"kernel", []storage.AWGTunnel{on("awg11", "kernel", nil)}, netutil.RouteHolders{Kernel: true, Any: true}},
		{"kernel без поля backend", []storage.AWGTunnel{on("awg11", "", nil)}, netutil.RouteHolders{Kernel: true, Any: true}},
		{"обычный nativewg", []storage.AWGTunnel{on("awg20", "nativewg", nil)}, netutil.RouteHolders{Any: true}},
		{"обфусцированный", []storage.AWGTunnel{on("awg20", "nativewg", &storage.Obfuscator{})}, netutil.RouteHolders{Obfuscated: true, Any: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostRouteHolders(tc.list, "awg10", ip); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if got := hostRouteHolders([]storage.AWGTunnel{{ID: "awg11", Enabled: true}}, "awg10", ""); got != (netutil.RouteHolders{}) {
		t.Errorf("пустой адрес: %+v", got)
	}
}
