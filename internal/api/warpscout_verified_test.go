package api

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

type warpVerifiedStubSvc struct {
	TunnelService
	mu             sync.Mutex
	store          *storage.AWGTunnelStore
	states         map[string]tunnel.State
	importFn       func(context.Context, string, string, string) (*service.TunnelWithStatus, error)
	deleteFn       func(context.Context, string) error
	stopFn         func(context.Context, string) error
	replaceFn      func(context.Context, string, string, string) error
	restartCalls   int
	restartCtxErrs []error
}

func (s *warpVerifiedStubSvc) Get(_ context.Context, id string) (*service.TunnelWithStatus, error) {
	t, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	st := s.states[id]
	s.mu.Unlock()
	if st == 0 {
		st = tunnel.StateRunning
	}
	return &service.TunnelWithStatus{
		ID:            t.ID,
		Name:          t.Name,
		Enabled:       t.Enabled,
		Backend:       t.Backend,
		InterfaceName: "if-" + t.ID,
		State:         st,
	}, nil
}

func (s *warpVerifiedStubSvc) Update(_ context.Context, _, newStored *storage.AWGTunnel) error {
	return s.store.Update(newStored.ID, func(t *storage.AWGTunnel) error {
		*t = *newStored
		return nil
	})
}

func (s *warpVerifiedStubSvc) SetEnabled(_ context.Context, id string, enabled bool) error {
	return s.store.Update(id, func(t *storage.AWGTunnel) error {
		t.Enabled = enabled
		return nil
	})
}

func (s *warpVerifiedStubSvc) Start(_ context.Context, id string) error {
	s.mu.Lock()
	s.states[id] = tunnel.StateRunning
	s.mu.Unlock()
	return nil
}

func (s *warpVerifiedStubSvc) Stop(ctx context.Context, id string) error {
	if s.stopFn != nil {
		return s.stopFn(ctx, id)
	}
	return nil
}

func (s *warpVerifiedStubSvc) Restart(ctx context.Context, id string) error {
	s.mu.Lock()
	s.restartCalls++
	s.restartCtxErrs = append(s.restartCtxErrs, ctx.Err())
	s.mu.Unlock()
	return nil
}

func (s *warpVerifiedStubSvc) ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error {
	if s.replaceFn != nil {
		return s.replaceFn(ctx, tunnelID, confContent, newName)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := config.Parse(confContent)
	if err != nil {
		return err
	}
	return s.store.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.Interface = parsed.Interface
		t.Peer = parsed.Peer
		if newName != "" {
			t.Name = newName
		}
		return nil
	})
}

func newWarpVerifiedHarness(t *testing.T, states map[string]tunnel.State) (*WarpTunnelOps, *warpVerifiedStubSvc, *storage.AWGTunnelStore) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	svc := &warpVerifiedStubSvc{store: store, states: states}
	var ops *WarpTunnelOps
	var mu sync.Mutex
	var prevRx, prevTx uint64 = 1000, 500
	ops = &WarpTunnelOps{
		Svc:   svc,
		Store: store,
		RotateKernelPort: func(context.Context, string) error {
			return nil
		},
		Runtime: func(_ context.Context, ref *warpscout.TunnelRef) (warpRuntime, error) {
			mu.Lock()
			defer mu.Unlock()
			rx, tx := prevRx, prevTx
			prevRx += uint64(3*128*1024 + 1024)
			prevTx += 2048
			return warpRuntime{
				PublicKey: ref.LocalPublicKey,
				PeerKey:   ref.PeerPublicKey,
				Endpoint:  ref.Endpoint,
				Rx:        rx,
				Tx:        tx,
			}, nil
		},
	}
	return ops, svc, store
}

func seedWarpVerifiedTunnel(t *testing.T, store *storage.AWGTunnelStore, id, backend string) {
	t.Helper()
	if err := store.Create(&storage.AWGTunnel{
		ID:      id,
		Name:    "WARP HEL",
		Enabled: true,
		Backend: backend,
		Interface: storage.AWGInterface{
			PrivateKey: "gZwP6wviWSsSUhp4AbX0iLuzKKVVTwlB5asMAQ+BWBE=",
			Address:    "172.16.0.2/32",
			MTU:        1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  warpscout.WarpPeerPublicKey,
			Endpoint:   "162.159.192.70:1014",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func warpValidMeasurement() *warpscout.VerificationResult {
	return &warpscout.VerificationResult{
		VerificationVersion: 3,
		Bytes:               3 * 128 * 1024,
		Samples:             []float64{10, 10, 10},
		DownloadMbps:        10,
		PingMs:              50,
		Trace:               warpscout.TraceResult{Warp: "on", Colo: "HEL", Loc: "FI", LatencyMs: 50, LatencyKind: "tcp"},
	}
}

func TestWarpVerifiedSwapRollsBackOnPartialTraffic(t *testing.T) {
	ops, svc, store := newWarpVerifiedHarness(t, map[string]tunnel.State{"awg11": tunnel.StateRunning})
	seedWarpVerifiedTunnel(t, store, "awg11", "kernel")
	old, _ := store.Get("awg11")
	before := config.GenerateForExport(old)
	ops.Verify = func(_ context.Context, iface string, full bool) (*warpscout.VerificationResult, error) {
		if iface != "if-awg11" || !full {
			t.Fatalf("wrong verification target %s %v", iface, full)
		}
		current, _ := store.Get("awg11")
		if current.Peer.Endpoint != "162.159.192.88:2408" {
			t.Fatal("tested stale endpoint")
		}
		r := warpValidMeasurement()
		r.Bytes--
		return r, nil
	}
	_, err := ops.SwapVerified(context.Background(), warpscout.TargetKernel, "awg11", "162.159.192.88:2408", warpscout.SwapOptions{}, warpscout.Filters{})
	if err == nil {
		t.Fatal("accepted truncated transfer")
	}
	after, _ := store.Get("awg11")
	if config.GenerateForExport(after) != before || after.ID != "awg11" {
		t.Fatal("original config/ID not restored")
	}
	if svc.restartCalls != 1 {
		t.Fatal("original tunnel not restarted")
	}
}
