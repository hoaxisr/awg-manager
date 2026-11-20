//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// Explicitly opt in on a real router. This test never activates a policy or
// changes the store, NDMS, routing, keys, endpoints, or interface state.
func TestRouterNativeUplinkReadOnly(t *testing.T) {
	dir := os.Getenv("UPLINK_ROUTER_READONLY_DATA")
	if dir == "" {
		t.Skip("set UPLINK_ROUTER_READONLY_DATA on the test router")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tr := transport.NewWithURL("http://127.0.0.1:79/rci", transport.NewSemaphore(2))
	defer tr.Close()
	op := nwg.NewOperator(nil, nil, tr, nil)
	catalogue, err := op.NativeUplinkPeerMarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "tunnels", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no tunnel records: %v", err)
	}
	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var stored storage.AWGTunnel
		if err = json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Backend != "nativewg" || !stored.Enabled {
			continue
		}
		mark, err := op.NativeUplinkPeerMark(ctx, &stored)
		if err != nil {
			t.Fatalf("%s peer runtime: %v", stored.ID, err)
		}
		iface := nwg.NewNWGNames(stored.NWGIndex).IfaceName
		if len(catalogue[iface]) != 1 || catalogue[iface][0] != mark {
			t.Fatalf("%s mark catalogue mismatch", stored.ID)
		}
		checked++
		t.Logf("%s: live peer and unique-catalogue source read; IPv4-only eligible=%t", stored.ID, len(stored.Peer.AllowedIPs) == 1 && stored.Peer.AllowedIPs[0] == "0.0.0.0/0")
		if len(stored.Peer.AllowedIPs) != 1 || stored.Peer.AllowedIPs[0] != "0.0.0.0/0" {
			continue
		}
		target := ManagedTarget{Ref: stored.ID, Interface: iface, Peer: stored.Peer.PublicKey, Endpoint: stored.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(stored.Interface.DNS), Enabled: true}
		manager, err := NewManaged(ManagedDeps{
			Directory:    t.TempDir(),
			Lookup:       func(context.Context, string) (ManagedTarget, error) { return target, nil },
			List:         func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil },
			Providers:    func() []uplink.Provider { return nil },
			WithLock:     func(_ context.Context, _ string, fn func() error) error { return fn() },
			ApplyBinding: func(context.Context, string, string, string) error { return fmt.Errorf("read-only test forbids apply") },
			PeerMark: func(ctx context.Context, _ ManagedTarget) (uint32, error) {
				return op.NativeUplinkPeerMark(ctx, &stored)
			},
			PeerMarks: op.NativeUplinkPeerMarks,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = manager.Validate(ctx, uplink.Target{Engine: "wireguard", Ref: stored.ID}); err != nil {
			t.Fatalf("%s production validation: %v", stored.ID, err)
		}
		t.Logf("%s: production manager Validate passed against real kernel and NDMS", stored.ID)
	}
	if checked == 0 {
		t.Fatal("no active native peers checked")
	}
	for _, family := range []string{"-4", "-6"} {
		for _, args := range [][]string{{family, "-j", "rule", "show"}, {family, "-j", "route", "show", "table", "all"}} {
			if _, err := defaultRunner(ctx, "ip", args...); err != nil {
				t.Fatalf("production legacy reader %v: %v", args, err)
			}
		}
	}
}
