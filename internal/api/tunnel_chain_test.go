package api

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F806: цепочка «через туннель» — отказ на себя, на цикл и на неизвестного
// родителя; обычная цепочка и не-цепочка проходят.
func TestValidateTunnelChain(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg10", ISPInterface: "eth3"},
		{ID: "awg11", ISPInterface: "tunnel:awg10"},
		{ID: "awg12", ISPInterface: "tunnel:awg11"},
		// nativewg «через awg12»: хранит NDMS-имя, не tunnel:
		{ID: "awg20", Backend: "nativewg", NWGIndex: 3, ISPInterface: "OpkgTun12"},
	} {
		if err := store.Create(tun); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		id, isp, code string
	}{
		{"awg11", "tunnel:awg10", ""},
		{"awg11", "eth3", ""},
		{"awg13", "tunnel:awg12", ""},
		{"awg11", "tunnel:awg11", "TUNNEL_CHAIN_CYCLE"},
		{"awg10", "tunnel:awg12", "TUNNEL_CHAIN_CYCLE"},
		{"awg11", "tunnel:awg99", "TUNNEL_CHAIN_PARENT_NOT_FOUND"},
		{"awg10", "tunnel:awg20", "TUNNEL_CHAIN_CYCLE"}, // awg10→awg20(nwg)→awg12→awg11→awg10
		{"awg13", "tunnel:awg20", ""},
	}
	for _, c := range cases {
		err := validateTunnelChain(store, c.id, c.isp)
		var te *TunnelEditError
		got := ""
		if errors.As(err, &te) {
			got = te.Code
		} else if err != nil {
			t.Fatalf("%s→%s: неожиданная ошибка %v", c.id, c.isp, err)
		}
		if got != c.code {
			t.Errorf("%s→%s: код %q, ожидали %q", c.id, c.isp, got, c.code)
		}
	}
}
