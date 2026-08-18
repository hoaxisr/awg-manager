package service

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Kernel tunnels are visible in NDMS as OpkgTunN — Get/List must say so,
// иначе потребители (политики доступа, мастер «Вывести трафик») не находят
// интерфейс туннеля.
func TestGet_KernelTunnel_HasNDMSName(t *testing.T) {
	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg18")

	got, err := svc.Get(context.Background(), "awg18")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.NDMSName != "OpkgTun18" {
		t.Errorf("NDMSName = %q, want OpkgTun18", got.NDMSName)
	}
	if got.InterfaceName != "opkgtun18" {
		t.Errorf("InterfaceName = %q, want opkgtun18", got.InterfaceName)
	}
}

func TestList_KernelTunnel_HasNDMSName(t *testing.T) {
	svc, store, _, _ := testService(t)
	saveTunnel(t, store, "awg18")

	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].NDMSName != "OpkgTun18" {
		t.Errorf("NDMSName = %q, want OpkgTun18", list[0].NDMSName)
	}
}

// OS4 (awgmN) и wdtt-raw именуются иначе — OpkgTunN им подставлять нельзя.
func TestStoredIfaceNames_NonKernelBackends(t *testing.T) {
	tests := []struct {
		name      string
		stored    storage.AWGTunnel
		wantIface string
		wantNDMS  string
	}{
		{
			name:      "os4 awgm has no NDMS name",
			stored:    storage.AWGTunnel{ID: "awgm0"},
			wantIface: "awgm0",
			wantNDMS:  "",
		},
		{
			name: "wdtt-raw uses live iface names",
			stored: storage.AWGTunnel{
				ID:             "wdttraw-default",
				Backend:        "wdtt-raw",
				RawKernelIface: "wdttraw0",
				RawNdmsIface:   "OpkgTun17",
			},
			wantIface: "wdttraw0",
			wantNDMS:  "OpkgTun17",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			iface, ndms := storedIfaceNames(&tc.stored)
			if iface != tc.wantIface {
				t.Errorf("iface = %q, want %q", iface, tc.wantIface)
			}
			if ndms != tc.wantNDMS {
				t.Errorf("ndms = %q, want %q", ndms, tc.wantNDMS)
			}
		})
	}
}
