// internal/singbox/awgoutbounds/service_test.go
package awgoutbounds

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fakeSingbox struct {
	dir         string
	reloadCalls int
	reloadErr   error
}

func (f *fakeSingbox) ConfigDir() string { return f.dir }
func (f *fakeSingbox) Reload() error {
	f.reloadCalls++
	return f.reloadErr
}

func newSvcWithIface(t *testing.T, awgStore AWGTunnelStore, sysStore SystemTunnelQuery, ifaces ...string) (*ServiceImpl, *fakeSingbox) {
	t.Helper()
	cfgDir := t.TempDir()
	netRoot := t.TempDir()
	for _, n := range ifaces {
		if err := os.MkdirAll(filepath.Join(netRoot, n), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
	sb := &fakeSingbox{dir: cfgDir}
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels:    awgStore,
			SystemTunnels: sysStore,
			Singbox:       sb,
		},
		sysClassNet: netRoot,
	}
	return s, sb
}

func TestSync_WritesFileAndReloads(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("SyncAWGOutbounds: %v", err)
	}
	if sb.reloadCalls != 1 {
		t.Errorf("want 1 reload call, got %d", sb.reloadCalls)
	}
	raw, err := os.ReadFile(filepath.Join(sb.dir, "15-awg.json"))
	if err != nil {
		t.Fatalf("read 15-awg.json: %v", err)
	}
	var got fileShape
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 1 {
		t.Errorf("want 1 outbound, got %d", len(got.Outbounds))
	}
}

func TestReconcile_NoReload(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sb.reloadCalls != 0 {
		t.Errorf("want 0 reload calls (Reconcile is reload-free), got %d", sb.reloadCalls)
	}
	if _, err := os.Stat(filepath.Join(sb.dir, "15-awg.json")); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}

func TestSync_Idempotent(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	for i := 0; i < 3; i++ {
		if err := s.SyncAWGOutbounds(context.Background()); err != nil {
			t.Fatalf("Sync iteration %d: %v", i, err)
		}
	}
	if sb.reloadCalls != 3 {
		t.Errorf("want 3 reload calls (one per Sync), got %d", sb.reloadCalls)
	}
}

func TestListTags_MatchesEnumerate(t *testing.T) {
	s, _ := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		&fakeSystemStore{tunnels: []SystemTunnelInfo{{ID: "Wireguard0", InterfaceName: "nwg0", Description: "W0"}}},
		"t2s0", "nwg0",
	)
	tags, err := s.ListTags(context.Background())
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("want 2 tags, got %d", len(tags))
	}
	if tags[0].Tag != "awg-a" || tags[0].Kind != "managed" {
		t.Errorf("tag 0 wrong: %+v", tags[0])
	}
	if tags[1].Tag != "awg-sys-Wireguard0" || tags[1].Kind != "system" {
		t.Errorf("tag 1 wrong: %+v", tags[1])
	}
}

func TestSync_NilSingbox_DoesNotReload(t *testing.T) {
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: nil},
		},
	}
	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("Sync with nil Singbox should be safe: %v", err)
	}
}
