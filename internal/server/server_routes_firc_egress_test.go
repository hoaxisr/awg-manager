package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestServerListFircEgressTunnels(t *testing.T) {
	var hits atomic.Int32
	mockFirc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/tunnels":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tunnels": []firc.Tunnel{
					{
						ID:          "t1",
						Device:      "tunvless0",
						Description: "Vless DE",
						Enable:      true,
					},
					{
						ID:          "t2",
						Device:      "tunvless1",
						Description: "Disabled NL",
						Enable:      false,
					},
					{
						ID:     "t3",
						Device: "tunvless2",
						Enable: true,
					},
					{
						ID:          "t4",
						Device:      "   ",
						Description: "Missing device",
						Enable:      true,
					},
				},
			})
		case "/api/v1/tunnels/state":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tunnels": []firc.TunnelState{
					{
						ID:     "t1",
						Device: "tunvless0",
						Status: "UP",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockFirc.Close()

	settings := storage.NewSettingsStore(t.TempDir())
	if err := settings.Update(func(st *storage.Settings) error {
		st.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatalf("settings.Update(sing-box): %v", err)
	}

	fircOp := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
	fircOp.SetClient(firc.NewHTTPClient(mockFirc.URL, mockFirc.Client()))
	srv := &Server{
		settings:    settings,
		fircHandler: api.NewFircHandler(fircOp),
	}

	ctx := context.Background()
	if got := srv.listFircEgressTunnels(ctx); got != nil {
		t.Fatalf("expected nil entries when routingEngine=sing-box, got %+v", got)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls when routingEngine=sing-box, got %d", hits.Load())
	}

	if err := settings.Update(func(st *storage.Settings) error {
		st.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatalf("settings.Update(firc): %v", err)
	}

	got := srv.listFircEgressTunnels(ctx)
	want := []routing.TunnelEntry{
		{
			ID:        "firc:tunvless0",
			Name:      "Vless DE",
			Iface:     "tunvless0",
			Type:      "firc",
			Status:    "up",
			Available: true,
		},
		{
			ID:        "firc:tunvless1",
			Name:      "Disabled NL",
			Iface:     "tunvless1",
			Type:      "firc",
			Status:    "off",
			Available: false,
		},
		{
			ID:        "firc:tunvless2",
			Name:      "tunvless2",
			Iface:     "tunvless2",
			Type:      "firc",
			Status:    "unknown",
			Available: false,
		},
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] mismatch:\n  got:  %+v\n  want: %+v", i, got[i], want[i])
		}
	}
}

func TestFircEgressState_ForeignFirc(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, "opt/lib/opkg/info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := storage.NewSettingsStore(t.TempDir())
	if err := settings.Update(func(st *storage.Settings) error {
		st.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer daemon.Close()
	op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir(), Foreign: firc.NewForeignDetector(root)})
	op.SetClient(firc.NewHTTPClient(daemon.URL, daemon.Client()))
	srv := &Server{settings: settings, fircHandler: api.NewFircHandler(op)}
	if ok, why := srv.fircEgressState(context.Background()); !ok || why != "" {
		t.Fatalf("без стороннего: %v %q", ok, why)
	}
	if err := os.WriteFile(filepath.Join(info, "firc.list"), []byte("/opt/bin/fircd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, why := srv.fircEgressState(context.Background()); ok || why != firc.ErrForeignFirc.Error() {
		t.Fatalf("при стороннем: %v %q", ok, why)
	}
}
