package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type fakeMihomoDiagnosticStore struct {
	bridges []mihomonative.BridgeRef
}

func (f fakeMihomoDiagnosticStore) ListBridges() []mihomonative.BridgeRef {
	return append([]mihomonative.BridgeRef(nil), f.bridges...)
}

type fakeMihomoDiagnosticProxyLookup struct {
	description string
	exists      bool
	err         error
	lookups     int
}

func (f *fakeMihomoDiagnosticProxyLookup) LookupProxy(context.Context, int) (string, bool, error) {
	f.lookups++
	return f.description, f.exists, f.err
}

type fakeMihomoDiagnosticKernelResolver struct {
	iface    string
	resolved string
}

func (f *fakeMihomoDiagnosticKernelResolver) ResolveSystemName(_ context.Context, name string) string {
	f.resolved = name
	return f.iface
}

func testMihomoDiagnosticResolver(lookup *fakeMihomoDiagnosticProxyLookup, kernel *fakeMihomoDiagnosticKernelResolver) *mihomoDiagnosticResolver {
	const id = "native-1"
	return &mihomoDiagnosticResolver{
		store: fakeMihomoDiagnosticStore{bridges: []mihomonative.BridgeRef{{
			Kind: "proxy", ID: id, Enabled: true,
			Bridge: mihomonative.ProxyBridge{
				ProxyIndex: 7, ProxyInterface: "Proxy999", KernelInterface: "forged0", ListenPort: 12007,
			},
		}}},
		proxies:        lookup,
		kernel:         kernel,
		exportsEnabled: func() bool { return true },
		runtimeReady:   func() bool { return true },
	}
}

func TestMihomoDiagnosticResolverUsesVerifiedNDMSAllocation(t *testing.T) {
	const id = "native-1"
	lookup := &fakeMihomoDiagnosticProxyLookup{
		description: mihomonative.BridgeOwnershipDescription("proxy", id),
		exists:      true,
	}
	kernel := &fakeMihomoDiagnosticKernelResolver{iface: "t2s7"}
	resolver := testMihomoDiagnosticResolver(lookup, kernel)

	iface, err := resolver.ResolveMihomoDiagnosticInterface(context.Background(), "proxy", id)
	if err != nil {
		t.Fatalf("ResolveMihomoDiagnosticInterface() error = %v", err)
	}
	if iface != "t2s7" {
		t.Fatalf("iface = %q, want server-resolved t2s7", iface)
	}
	if kernel.resolved != "Proxy7" {
		t.Fatalf("resolved NDMS name = %q, want derived Proxy7", kernel.resolved)
	}
}

func TestMihomoDiagnosticResolverRejectsToggleOffBeforeNDMSLookup(t *testing.T) {
	lookup := &fakeMihomoDiagnosticProxyLookup{}
	resolver := testMihomoDiagnosticResolver(lookup, &fakeMihomoDiagnosticKernelResolver{iface: "t2s7"})
	resolver.exportsEnabled = func() bool { return false }

	if _, err := resolver.ResolveMihomoDiagnosticInterface(context.Background(), "proxy", "native-1"); err == nil {
		t.Fatal("toggle-off bridge unexpectedly resolved")
	}
	if lookup.lookups != 0 {
		t.Fatalf("NDMS lookups = %d, want 0 while exports are disabled", lookup.lookups)
	}
}

func TestMihomoDiagnosticResolverRejectsStaleOrTakenOverProxy(t *testing.T) {
	tests := []struct {
		name        string
		description string
		exists      bool
		err         error
	}{
		{name: "missing", exists: false},
		{name: "foreign owner", description: "user proxy", exists: true},
		{name: "lookup failure", err: errors.New("ndms unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := &fakeMihomoDiagnosticProxyLookup{description: tt.description, exists: tt.exists, err: tt.err}
			resolver := testMihomoDiagnosticResolver(lookup, &fakeMihomoDiagnosticKernelResolver{iface: "t2s7"})
			if _, err := resolver.ResolveMihomoDiagnosticInterface(context.Background(), "proxy", "native-1"); err == nil {
				t.Fatal("stale bridge unexpectedly resolved")
			}
		})
	}
}

func TestMihomoDiagnosticResolverRejectsClosedRuntimeGate(t *testing.T) {
	lookup := &fakeMihomoDiagnosticProxyLookup{}
	resolver := testMihomoDiagnosticResolver(lookup, &fakeMihomoDiagnosticKernelResolver{iface: "t2s7"})
	resolver.runtimeReady = func() bool { return false }

	if _, err := resolver.ResolveMihomoDiagnosticInterface(context.Background(), "proxy", "native-1"); err == nil {
		t.Fatal("closed runtime gate unexpectedly resolved")
	}
	if lookup.lookups != 0 {
		t.Fatalf("NDMS lookups = %d, want 0 while runtime gate is closed", lookup.lookups)
	}
}
