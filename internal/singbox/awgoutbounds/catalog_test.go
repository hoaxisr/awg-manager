// internal/singbox/awgoutbounds/catalog_test.go
package awgoutbounds

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeAWGStore is the in-test stand-in for storage.AWGTunnelStore.
type fakeAWGStore struct {
	tunnels []AWGTunnelInfo
	err     error
}

func (f *fakeAWGStore) List(ctx context.Context) ([]AWGTunnelInfo, error) {
	return f.tunnels, f.err
}

// fakeSystemStore is the stand-in for SystemTunnelQuery.
type fakeSystemStore struct {
	tunnels []SystemTunnelInfo
	err     error
}

func (f *fakeSystemStore) List(ctx context.Context) ([]SystemTunnelInfo, error) {
	return f.tunnels, f.err
}

// fakeIfaceResolver mocks the kernel-iface lookup. Returns "" for unknown ids.
type fakeIfaceResolver struct {
	byID map[string]string
}

func (f *fakeIfaceResolver) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	v, ok := f.byID[tunnelID]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

// makeIfacePresent creates /sys/class/net/<name>-style stubs in a temp
// dir, then redirects ifaceExists' root via test override.
func makeIfacePresent(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
	return root
}

func TestEnumerate_ManagedOnly(t *testing.T) {
	root := makeIfacePresent(t, "t2s0", "t2s1")
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: []AWGTunnelInfo{
				{ID: "tunA", Name: "Home", BackendIface: "t2s0"},
				{ID: "tunB", Name: "Work", BackendIface: "t2s1"},
			}},
		},
		sysClassNet: root,
	}
	got, err := s.enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d (%+v)", len(got), got)
	}
	if got[0].Tag != "awg-tunA" || got[0].Iface != "t2s0" || got[0].Kind != "managed" {
		t.Errorf("entry 0 wrong: %+v", got[0])
	}
}

func TestEnumerate_SkipsMissingIface(t *testing.T) {
	root := makeIfacePresent(t, "t2s0") // t2s1 deliberately missing
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: []AWGTunnelInfo{
				{ID: "tunA", Name: "Home", BackendIface: "t2s0"},
				{ID: "tunB", Name: "Work", BackendIface: "t2s1"},
			}},
		},
		sysClassNet: root,
	}
	got, _ := s.enumerate(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 entry (only present iface), got %d", len(got))
	}
	if got[0].Iface != "t2s0" {
		t.Errorf("expected t2s0, got %q", got[0].Iface)
	}
}

func TestEnumerate_SkipsEmptyIface(t *testing.T) {
	root := makeIfacePresent(t)
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: []AWGTunnelInfo{
				{ID: "tunA", Name: "Home", BackendIface: ""},
			}},
		},
		sysClassNet: root,
	}
	got, _ := s.enumerate(context.Background())
	if len(got) != 0 {
		t.Errorf("want 0 entries, got %d", len(got))
	}
}

func TestEnumerate_SystemOnly(t *testing.T) {
	root := makeIfacePresent(t, "Wireguard0")
	s := &ServiceImpl{
		deps: Deps{
			SystemTunnels: &fakeSystemStore{tunnels: []SystemTunnelInfo{
				{ID: "Wireguard0", InterfaceName: "Wireguard0", Description: "Office VPN"},
			}},
		},
		sysClassNet: root,
	}
	got, _ := s.enumerate(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].Tag != "awg-sys-Wireguard0" || got[0].Kind != "system" || got[0].Label != "Office VPN" {
		t.Errorf("system entry wrong: %+v", got[0])
	}
}

func TestEnumerate_DedupBySystemAlsoManaged(t *testing.T) {
	root := makeIfacePresent(t, "nwg0")
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: []AWGTunnelInfo{
				{ID: "tunA", Name: "NWG-managed", BackendIface: "nwg0"},
			}},
			SystemTunnels: &fakeSystemStore{tunnels: []SystemTunnelInfo{
				{ID: "Wireguard0", InterfaceName: "nwg0", Description: "Same iface"},
			}},
		},
		sysClassNet: root,
	}
	got, _ := s.enumerate(context.Background())
	if len(got) != 1 {
		t.Fatalf("want 1 (managed wins, system deduped), got %d (%+v)", len(got), got)
	}
	if got[0].Kind != "managed" {
		t.Errorf("expected managed entry to win dedup, got %+v", got[0])
	}
}
