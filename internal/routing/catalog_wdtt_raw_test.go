package routing

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

func TestGetKernelIfaceName_WdttRaw(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"wdttraw-default": {
			Backend:        wdtt.BackendWdttRaw,
			RawKernelIface: "opkgtun17",
			RawNdmsIface:   "OpkgTun17",
		},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store, nil)

	got, err := cat.GetKernelIfaceName(context.Background(), "wdttraw-default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "opkgtun17" {
		t.Fatalf("got %q, want opkgtun17", got)
	}

	ndms, err := cat.ResolveInterface(context.Background(), "wdttraw-default")
	if err != nil {
		t.Fatalf("ResolveInterface: %v", err)
	}
	if ndms != "OpkgTun17" {
		t.Fatalf("ResolveInterface = %q, want OpkgTun17", ndms)
	}
}

func TestResolveNDMSName_WdttRawSkipsOpkgTun0(t *testing.T) {
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"wdttraw-default": {Backend: wdtt.BackendWdttRaw, RawNdmsIface: "OpkgTun17"},
	}}
	cat := NewCatalog(&mockTunnelProvider{}, nil, store, nil)

	got := cat.resolveNDMSName(TunnelWithStatus{ID: "wdttraw-default", Backend: wdtt.BackendWdttRaw})
	if got != "OpkgTun17" {
		t.Fatalf("resolveNDMSName = %q, want OpkgTun17", got)
	}
}
