package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

type wdttListStub struct {
	cfg wdtt.Config
	st  wdtt.Status
}

func (s *wdttListStub) GetConfig() (wdtt.Config, error) { return s.cfg, nil }
func (s *wdttListStub) Status() wdtt.Status             { return s.st }

func TestAppendWdttRawListItems_NoDuplicate(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	clientID := "default"
	tid := wdtt.RawTunnelID(clientID)
	if err := store.Save(&storage.AWGTunnel{
		ID:           tid,
		Name:         "WDTT Raw",
		Backend:      wdtt.BackendWdttRaw,
		WdttClientID: clientID,
		Interface:    storage.AWGInterface{Address: "10.70.1.2/32", MTU: 1300},
		Peer:         storage.AWGPeer{Endpoint: "1.2.3.4:56013"},
	}); err != nil {
		t.Fatal(err)
	}

	h := &TunnelsHandler{store: store}
	h.wdttSvc = &wdttListStub{
		cfg: wdtt.Config{Clients: []wdtt.ClientInstance{{ID: clientID, Config: wdtt.ClientConfig{
			RawIface:    "wdttraw0",
			NdmsIface:   "OpkgTun17",
			RawClientIP: "10.70.1.2",
		}}}},
		st: wdtt.Status{Clients: []wdtt.InstanceStatus{{ID: clientID, Status: wdtt.ProcessStatus{
			Running: true, RawIface: "wdttraw0", NdmsIface: "OpkgTun17", RawClientIP: "10.70.1.2",
		}}}},
	}

	out := h.appendWdttRawListItems(context.Background(), nil)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].NDMSName != "OpkgTun17" {
		t.Fatalf("ndmsName = %q", out[0].NDMSName)
	}

	// Second pass must not duplicate when id is already present.
	out2 := h.appendWdttRawListItems(context.Background(), out)
	if len(out2) != 1 {
		t.Fatalf("after dedupe len = %d, want 1", len(out2))
	}
}
