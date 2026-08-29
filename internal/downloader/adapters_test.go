package downloader

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestAWGTunnelIDFromTag(t *testing.T) {
	tests := []struct {
		tag     string
		wantID  string
		wantSys bool
	}{
		{"awg-awg11", "awg11", false},
		{"awg-sys-Wireguard0", "Wireguard0", true},
		{"direct", "", false},
	}
	for _, tc := range tests {
		id, system := awgTunnelIDFromTag(tc.tag)
		if id != tc.wantID || system != tc.wantSys {
			t.Fatalf("awgTunnelIDFromTag(%q) = (%q, %v), want (%q, %v)", tc.tag, id, system, tc.wantID, tc.wantSys)
		}
	}
}

func TestAWGStoreEgressAdapter_DNSForTag(t *testing.T) {
	dir := t.TempDir()
	lockDir := filepath.Join(dir, ".locks")
	store := storage.NewAWGTunnelStoreWithLockDir(dir, lockDir)
	if err := store.Create(&storage.AWGTunnel{
		ID:   "awg11",
		Name: "Work",
		Interface: storage.AWGInterface{
			DNS: "10.8.0.1, 1.1.1.1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	adp := NewAWGStoreEgressAdapter(store)
	got := adp.DNSForTag(context.Background(), "awg-awg11")
	if len(got) != 2 || got[0] != "10.8.0.1" || got[1] != "1.1.1.1" {
		t.Fatalf("DNSForTag = %+v", got)
	}
	if adp.DNSForTag(context.Background(), "awg-sys-Wireguard0") != nil {
		t.Fatal("system tag should not resolve managed DNS")
	}
}
