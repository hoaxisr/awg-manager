package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// EditTunnel — та же правка, что POST /tunnels/update, без HTTP: её
// вызывает MCP. Отказ приходит типизированным, со статусом и кодом REST,
// а успешная правка ложится в стор.
func TestEditTunnel_RefusalCarriesStatusAndCode(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID: "awg1", Name: "t1", Enabled: true, Locked: true,
		Interface: storage.AWGInterface{Address: "10.0.0.2/32"},
	})
	seedTunnel(t, store, &storage.AWGTunnel{
		ID: "awg2", Name: "t2", Enabled: true,
		Interface: storage.AWGInterface{Address: "10.0.0.3/32", MTU: 1420},
		Peer:      storage.AWGPeer{Endpoint: "1.2.3.4:51820", PublicKey: "pub"},
	})

	_, err := h.EditTunnel(context.Background(), "awg1", storage.AWGTunnel{Name: "x"})
	var editErr *TunnelEditError
	if !errors.As(err, &editErr) || editErr.Status != http.StatusForbidden || editErr.Code != "TUNNEL_LOCKED" {
		t.Fatalf("err = %#v", err)
	}
	_, err = h.EditTunnel(context.Background(), "absent", storage.AWGTunnel{Name: "x"})
	if !errors.As(err, &editErr) || editErr.Status != http.StatusBadRequest || editErr.Code != "NOT_FOUND" {
		t.Fatalf("err = %#v", err)
	}

	edited, err := h.EditTunnel(context.Background(), "awg2", storage.AWGTunnel{Name: "renamed"})
	if err != nil || edited.Raw != nil {
		t.Fatalf("edited = %+v, err = %v", edited, err)
	}
	if saved, _ := store.Get("awg2"); saved.Name != "renamed" || saved.Interface.MTU != 1420 {
		t.Fatalf("saved = %+v", saved)
	}
}
