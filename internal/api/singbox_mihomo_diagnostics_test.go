package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMihomoDiagnosticsResolver struct {
	iface      string
	err        error
	kind       string
	resourceID string
	calls      int
}

func (f *fakeMihomoDiagnosticsResolver) ResolveMihomoDiagnosticInterface(_ context.Context, kind, resourceID string) (string, error) {
	f.calls++
	f.kind = kind
	f.resourceID = resourceID
	return f.iface, f.err
}

func TestResolveMihomoDiagnosticInterfaceIgnoresForgedClientIface(t *testing.T) {
	resolver := &fakeMihomoDiagnosticsResolver{iface: "t2s7"}
	h := &SingboxHandler{mihomoDiagnostics: resolver}
	req := httptest.NewRequest(http.MethodGet,
		"/api/singbox/tunnels/test/connectivity?mihomoKind=proxy&mihomoId=native-1&iface=t2s999", nil)

	iface, requested, err := h.resolveMihomoDiagnosticInterface(context.Background(), req)
	if err != nil {
		t.Fatalf("resolveMihomoDiagnosticInterface() error = %v", err)
	}
	if !requested || iface != "t2s7" {
		t.Fatalf("requested=%v iface=%q, want true and server-resolved t2s7", requested, iface)
	}
	if resolver.calls != 1 || resolver.kind != "proxy" || resolver.resourceID != "native-1" {
		t.Fatalf("resolver call = %d kind=%q id=%q", resolver.calls, resolver.kind, resolver.resourceID)
	}
}

func TestMihomoDiagnosticsEndpointsShareBridgeUnavailableContract(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*SingboxHandler, http.ResponseWriter, *http.Request)
	}{
		{
			name: "connectivity",
			path: "/api/singbox/tunnels/test/connectivity?mihomoKind=proxy&mihomoId=native-1&iface=t2s999",
			call: (*SingboxHandler).CheckConnectivity,
		},
		{
			name: "ip",
			path: "/api/singbox/tunnels/test/ip?mihomoKind=proxy&mihomoId=native-1&iface=t2s999",
			call: (*SingboxHandler).CheckIP,
		},
		{
			name: "speed",
			path: "/api/singbox/tunnels/test/speed/stream?mihomoKind=proxy&mihomoId=native-1&iface=t2s999&server=iperf.example&port=5201",
			call: (*SingboxHandler).SpeedTestStream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeMihomoDiagnosticsResolver{err: errors.New("bridge publication is stale")}
			h := &SingboxHandler{mihomoDiagnostics: resolver}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)

			tt.call(h, rec, req)

			if rec.Code != http.StatusPreconditionFailed {
				t.Fatalf("status = %d, want 412; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"code":"BRIDGE_UNAVAILABLE"`) {
				t.Fatalf("body does not contain BRIDGE_UNAVAILABLE: %s", rec.Body.String())
			}
			if resolver.calls != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolver.calls)
			}
		})
	}
}
