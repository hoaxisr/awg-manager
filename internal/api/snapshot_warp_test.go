package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

type warpTestTunnelSvc struct {
	stubTunnelSvc
	items []service.TunnelWithStatus
}

func (s *warpTestTunnelSvc) List(context.Context) ([]service.TunnelWithStatus, error) {
	return append([]service.TunnelWithStatus(nil), s.items...), nil
}

type warpTestSystemSvc struct {
	items []ndms.SystemWireguardTunnel
}

func (s *warpTestSystemSvc) List(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return append([]ndms.SystemWireguardTunnel(nil), s.items...), nil
}

func (s *warpTestSystemSvc) ListFresh(context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return append([]ndms.SystemWireguardTunnel(nil), s.items...), nil
}

func (s *warpTestSystemSvc) Get(_ context.Context, name string) (*ndms.SystemWireguardTunnel, error) {
	for i := range s.items {
		if s.items[i].ID == name {
			cp := s.items[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *warpTestSystemSvc) GetASCParams(context.Context, string) (json.RawMessage, error) {
	return nil, nil
}

func (s *warpTestSystemSvc) SetASCParams(context.Context, string, json.RawMessage) error {
	return nil
}

func TestTunnelsAndSnapshot_WarpAutopilotOverlay(t *testing.T) {
	watched := map[string]ndms.WarpAutopilotInfo{
		"kernel:awg1":       {ProfileID: "wp1", ProfileName: "WARP Kernel", State: "ok"},
		"native:awg2":       {ProfileID: "wp2", ProfileName: "WARP Native", State: "swapping"},
		"kernel:raw1":       {ProfileID: "wp3", ProfileName: "Ignored Raw", State: "ok"},
		"system:Wireguard0": {ProfileID: "wp4", ProfileName: "WARP HEL", State: "ok"},
	}
	provider := func() map[string]ndms.WarpAutopilotInfo { return watched }

	tunSvc := &warpTestTunnelSvc{
		items: []service.TunnelWithStatus{
			{ID: "awg1", Name: "K1", Backend: "kernel", State: tunnel.StateRunning},
			{ID: "awg2", Name: "N2", Backend: "nativewg", State: tunnel.StateStopped},
			{ID: "raw1", Name: "R1", Backend: "wdtt-raw", State: tunnel.StateRunning},
		},
	}
	tunSvc.getFn = func(_ context.Context, id string) (*service.TunnelWithStatus, error) {
		for i := range tunSvc.items {
			if tunSvc.items[i].ID == id {
				cp := tunSvc.items[i]
				return &cp, nil
			}
		}
		return nil, nil
	}

	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg1", Name: "K1", Backend: "kernel", Enabled: true})
	seedTunnel(t, store, &storage.AWGTunnel{ID: "awg2", Name: "N2", Backend: "nativewg"})
	seedTunnel(t, store, &storage.AWGTunnel{ID: "raw1", Name: "R1", Backend: "wdtt-raw", Enabled: true})

	th := NewTunnelsHandler(tunSvc, store, nil)
	th.SetWarpAutopilotProvider(provider)

	sysSvc := &warpTestSystemSvc{
		items: []ndms.SystemWireguardTunnel{{ID: "Wireguard0", InterfaceName: "nwg0"}},
	}
	sh := NewSystemTunnelsHandler(sysSvc, storage.NewSettingsStore(t.TempDir()), nil, nil)
	sh.SetWarpAutopilotProvider(provider)

	b := NewTunnelsSnapshotBuilder()
	b.SetTunnelsHandler(th)
	b.SetSystemTunnelsHandler(sh)
	b.SetWarpAutopilotProvider(provider)

	snap1 := b.Build(context.Background())
	tunnels1 := snap1["tunnels"].([]tunnelItem)
	if len(tunnels1) != 3 {
		t.Fatalf("expected 3 managed tunnels, got %d", len(tunnels1))
	}
	if tunnels1[0].WarpAutopilot == nil || tunnels1[0].WarpAutopilot.ProfileName != "WARP Kernel" {
		t.Fatalf("awg1 warpAutopilot = %+v, want WARP Kernel", tunnels1[0].WarpAutopilot)
	}
	if tunnels1[1].WarpAutopilot == nil || tunnels1[1].WarpAutopilot.State != "swapping" {
		t.Fatalf("awg2 warpAutopilot = %+v, want swapping", tunnels1[1].WarpAutopilot)
	}
	if tunnels1[2].WarpAutopilot != nil {
		t.Fatalf("wdtt-raw must not resolve warpAutopilot, got %+v", tunnels1[2].WarpAutopilot)
	}

	sys1 := snap1["system"].([]ndms.SystemWireguardTunnel)
	if len(sys1) != 1 || sys1[0].WarpAutopilot == nil || sys1[0].WarpAutopilot.ProfileName != "WARP HEL" {
		t.Fatalf("system warpAutopilot = %+v, want WARP HEL", sys1)
	}

	// GET /api/tunnels?id=awg1 returns warpAutopilot.
	recGet := httptest.NewRecorder()
	th.Get(recGet, httptest.NewRequest(http.MethodGet, "/api/tunnels/get?id=awg1", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("TunnelsHandler.Get code=%d body=%s", recGet.Code, recGet.Body.String())
	}
	var envGet struct {
		Success bool `json:"success"`
		Data    struct {
			WarpAutopilot *ndms.WarpAutopilotInfo `json:"warpAutopilot"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &envGet); err != nil || !envGet.Success {
		t.Fatalf("TunnelsHandler.Get body: %s", recGet.Body.String())
	}
	if envGet.Data.WarpAutopilot == nil || envGet.Data.WarpAutopilot.ProfileName != "WARP Kernel" {
		t.Fatalf("TunnelsHandler.Get warpAutopilot = %+v, want WARP Kernel", envGet.Data.WarpAutopilot)
	}

	// GET /api/system-tunnels/get?name=Wireguard0 returns warpAutopilot.
	recSys := httptest.NewRecorder()
	sh.Get(recSys, httptest.NewRequest(http.MethodGet, "/api/system-tunnels/get?name=Wireguard0", nil))
	if recSys.Code != http.StatusOK {
		t.Fatalf("SystemTunnelsHandler.Get code=%d body=%s", recSys.Code, recSys.Body.String())
	}
	var envSys struct {
		Success bool                       `json:"success"`
		Data    ndms.SystemWireguardTunnel `json:"data"`
	}
	if err := json.Unmarshal(recSys.Body.Bytes(), &envSys); err != nil || !envSys.Success {
		t.Fatalf("SystemTunnelsHandler.Get body: %s", recSys.Body.String())
	}
	if envSys.Data.WarpAutopilot == nil || envSys.Data.WarpAutopilot.ProfileName != "WARP HEL" {
		t.Fatalf("SystemTunnelsHandler.Get warpAutopilot = %+v, want WARP HEL", envSys.Data.WarpAutopilot)
	}

	// Clearing provider state clears badges in subsequent snapshot build.
	watched = nil
	snap2 := b.Build(context.Background())
	tunnels2 := snap2["tunnels"].([]tunnelItem)
	sys2 := snap2["system"].([]ndms.SystemWireguardTunnel)
	if tunnels2[0].WarpAutopilot != nil || sys2[0].WarpAutopilot != nil {
		t.Fatalf("expected cleared warpAutopilot, got tunnel=%+v system=%+v", tunnels2[0].WarpAutopilot, sys2[0].WarpAutopilot)
	}
}
