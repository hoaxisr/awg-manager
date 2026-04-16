package routing

import (
	"context"
	"strings"
	"testing"
)

func TestGetKernelIfaceName_NativeWG(t *testing.T) {
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg10": {Backend: "nativewg", NWGIndex: 0},
	}}
	cat := NewCatalog(provider, nil, store)

	got, err := cat.GetKernelIfaceName(context.Background(), "awg10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "nwg0" {
		t.Errorf("got %q, want nwg0", got)
	}
}

func TestGetKernelIfaceName_WANPrefix(t *testing.T) {
	cat := NewCatalog(&mockTunnelProvider{}, nil, &mockStoreClient{entries: map[string]StoreEntry{}})

	got, err := cat.GetKernelIfaceName(context.Background(), "wan:ppp0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ppp0" {
		t.Errorf("got %q, want ppp0", got)
	}
}

func TestGetKernelIfaceName_UnknownIDReturnsError(t *testing.T) {
	// Regression: previously, passing a non-tunnel string like a policy name
	// silently returned "opkgtun0" (because extractTunnelNum falls back to "0"
	// when no digit is found). That garbage interface name ended up in
	// HydraRoute's domain.conf, breaking routing.
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{}}
	cat := NewCatalog(provider, nil, store)

	got, err := cat.GetKernelIfaceName(context.Background(), "HydraRoute")
	if err == nil {
		t.Fatalf("expected error for unknown tunnel ID, got %q", got)
	}
	if !strings.Contains(err.Error(), "HydraRoute") {
		t.Errorf("error should mention the offending ID, got: %v", err)
	}
}

func TestGetKernelIfaceName_ManagedKernelFromStore(t *testing.T) {
	// Managed (non-nativewg) tunnel that exists in storage resolves via
	// tunnel.NewNames. On OS5 this yields opkgtunN where N is parsed from ID.
	provider := &mockTunnelProvider{}
	store := &mockStoreClient{entries: map[string]StoreEntry{
		"awg5": {Backend: "userspace"},
	}}
	cat := NewCatalog(provider, nil, store)

	got, err := cat.GetKernelIfaceName(context.Background(), "awg5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "opkgtun5" {
		t.Errorf("got %q, want opkgtun5", got)
	}
}
