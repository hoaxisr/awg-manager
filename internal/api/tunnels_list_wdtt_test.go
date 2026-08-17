package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// listSvcStub serves a fixed tunnel list; every other TunnelService method is
// unused by List and left inert.
type listSvcStub struct {
	tunnels []service.TunnelWithStatus
}

func (s *listSvcStub) List(context.Context) ([]service.TunnelWithStatus, error) {
	return s.tunnels, nil
}
func (s *listSvcStub) Get(context.Context, string) (*service.TunnelWithStatus, error) {
	return nil, nil
}
func (s *listSvcStub) Create(context.Context, string, string, tunnel.Config, *storage.AWGTunnel) error {
	return nil
}
func (s *listSvcStub) Update(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
	return nil
}
func (s *listSvcStub) Delete(context.Context, string) error                   { return nil }
func (s *listSvcStub) Start(context.Context, string) error                    { return nil }
func (s *listSvcStub) Stop(context.Context, string) error                     { return nil }
func (s *listSvcStub) Restart(context.Context, string) error                  { return nil }
func (s *listSvcStub) CheckAddressConflicts(context.Context, string) []string { return nil }
func (s *listSvcStub) GetState(context.Context, string) tunnel.StateInfo {
	return tunnel.StateInfo{}
}
func (s *listSvcStub) SetEnabled(context.Context, string, bool) error      { return nil }
func (s *listSvcStub) SetDefaultRoute(context.Context, string, bool) error { return nil }
func (s *listSvcStub) Import(context.Context, string, string, string) (*service.TunnelWithStatus, error) {
	return nil, nil
}
func (s *listSvcStub) ReplaceConfig(context.Context, string, string, string) error { return nil }
func (s *listSvcStub) WANModel() *wan.Model                                        { return nil }
func (s *listSvcStub) GetResolvedISP(string) string                                { return "" }
func (s *listSvcStub) SetSelfCreateGate(tunnel.SelfCreateGater)                    {}

// TestList_ExposesWdttClientID pins the list response contract: the WDTT link
// is carried by wdttClientId, so clients need not guess it from the endpoint.
func TestList_ExposesWdttClientID(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID:           "awg-wd",
		Name:         "WD",
		WdttClientID: "client-a",
		Interface:    storage.AWGInterface{Address: "10.0.0.2/32"},
		Peer:         storage.AWGPeer{Endpoint: "127.0.0.1:9005"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{
		store: store,
		svc:   &listSvcStub{tunnels: []service.TunnelWithStatus{{ID: "awg-wd", Name: "WD"}}},
	}

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/tunnels/list", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Data []struct {
			ID           string `json:"id"`
			WdttClientID string `json:"wdttClientId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].WdttClientID != "client-a" {
		t.Fatalf("wdttClientId = %q, want %q", resp.Data[0].WdttClientID, "client-a")
	}
}
