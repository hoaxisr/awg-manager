package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
	nettesting "github.com/hoaxisr/awg-manager/internal/testing"
)

func TestSingboxHandler_StatusSmoke(t *testing.T) {
	// NewSingboxHandler requires a real *singbox.Operator; we can't easily build one in this unit test
	// without a full NDMS mock. Skip for now — operator behaviour is covered by singbox package tests.
	// This file exists so future CRUD tests have a place to land.
	t.Skip("operator-dependent tests live in singbox package; HTTP surface covered in Task 16+")
}

func TestSingboxHandler_MethodNotAllowed_ListTunnels(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodDelete, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.ListTunnels(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_AddTunnels(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.AddTunnels(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_GetTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/get?tag=foo", nil)
	w := httptest.NewRecorder()
	h.GetTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_UpdateTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels?tag=foo", nil)
	w := httptest.NewRecorder()
	h.UpdateTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_RenameTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/rename", nil)
	w := httptest.NewRecorder()
	h.RenameTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_DeleteTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels?tag=foo", nil)
	w := httptest.NewRecorder()
	h.DeleteTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_GetTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/get", nil)
	w := httptest.NewRecorder()
	h.GetTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_UpdateTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodPut, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.UpdateTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_DeleteTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodDelete, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.DeleteTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_DelayCheck_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/delay-check?tag=A", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_DelayCheck_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/delay-check", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/test/connectivity?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/connectivity", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_OperatorNotWired(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/connectivity?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/test/ip?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/ip", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_OperatorNotWired(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/ip?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_IfaceOverride_UsesIfaceWithoutOperator(t *testing.T) {
	old := checkIPByInterface
	t.Cleanup(func() { checkIPByInterface = old })
	var gotIface, gotService string
	checkIPByInterface = func(_ context.Context, iface, service string) (*nettesting.IPResult, error) {
		gotIface, gotService = iface, service
		return &nettesting.IPResult{}, nil
	}
	h := NewSingboxHandler(nil, nil, nil, nil)
	rr := perform(h.CheckIP, http.MethodGet, "/api/singbox/tunnels/test/ip?iface=t2s1&service=https://example.invalid/ip", "")
	if rr.Code != 200 || gotIface != "t2s1" || gotService != "https://example.invalid/ip" {
		t.Fatalf("code=%d iface=%q service=%q body=%s", rr.Code, gotIface, gotService, rr.Body.String())
	}
}

func TestSingboxHandler_CheckConnectivity_IfaceOverride_UsesIfaceWithoutOperator(t *testing.T) {
	old := checkConnectivityByInterfaceURL
	t.Cleanup(func() { checkConnectivityByInterfaceURL = old })
	var gotIface, gotURL string
	checkConnectivityByInterfaceURL = func(_ context.Context, iface, url string) *nettesting.ConnectivityResult {
		gotIface, gotURL = iface, url
		return &nettesting.ConnectivityResult{}
	}
	h := NewSingboxHandler(nil, nil, nil, nil)
	rr := perform(h.CheckConnectivity, http.MethodGet, "/api/singbox/tunnels/test/connectivity?iface=t2s1", "")
	if rr.Code != 200 || gotIface != "t2s1" || gotURL != storage.DefaultConnectivityCheckURL {
		t.Fatalf("code=%d iface=%q url=%q body=%s", rr.Code, gotIface, gotURL, rr.Body.String())
	}
}

func TestSingboxNetworkProbeSeams_DefaultToTestingPackage(t *testing.T) {
	if reflect.ValueOf(checkIPByInterface).Pointer() != reflect.ValueOf(nettesting.CheckIPByInterface).Pointer() {
		t.Fatal("checkIPByInterface по умолчанию обязан быть testing.CheckIPByInterface")
	}
	if reflect.ValueOf(checkConnectivityByInterfaceURL).Pointer() != reflect.ValueOf(nettesting.CheckConnectivityByInterfaceURL).Pointer() {
		t.Fatal("checkConnectivityByInterfaceURL по умолчанию обязан быть testing.CheckConnectivityByInterfaceURL")
	}
}

func TestSingboxStatusData_MapsNewFields(t *testing.T) {
	src := singbox.Status{
		InstallState:  "outdated_no_space",
		RequiredBytes: 32_000_000,
		FreeBytes:     8_000_000,
	}
	got := singboxStatusData(src)
	if got.InstallState != "outdated_no_space" {
		t.Fatalf("InstallState=%q", got.InstallState)
	}
	if got.RequiredBytes != 32_000_000 || got.FreeBytes != 8_000_000 {
		t.Fatalf("Required/FreeBytes mismatch: %+v", got)
	}
}

func TestResolveTunnelInterfaceFromList_Found(t *testing.T) {
	iface, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: "t2s1"},
		{Tag: "B", KernelInterface: "t2s2"},
	}, "B")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if iface != "t2s2" {
		t.Fatalf("expected t2s2, got %q", iface)
	}
}

func TestResolveTunnelInterfaceFromList_NotFound(t *testing.T) {
	_, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: "t2s1"},
	}, "missing")
	if !errors.Is(err, singbox.ErrTunnelNotFound) {
		t.Fatalf("expected ErrTunnelNotFound, got %v", err)
	}
}

func TestResolveTunnelInterfaceFromList_NoInterface(t *testing.T) {
	_, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: ""},
	}, "A")
	if !errors.Is(err, errTunnelNoInterface) {
		t.Fatalf("expected errTunnelNoInterface, got %v", err)
	}
}

func TestSingboxStatusData_MapsAllFields(t *testing.T) {
	in := singbox.Status{
		Installed:        true,
		Version:          "1.13.11",
		Running:          false,
		PID:              4242,
		TunnelCount:      3,
		ProxyComponent:   true,
		NDMSProxyEnabled: false,
		Features:         []string{"with_quic", "with_gvisor"},
		LastError:        "+0000 2026-05-14 21:45:56 FATAL[0000] failed to initialize",
		CurrentVersion:   "1.13.10",
		RequiredVersion:  "1.13.11",
		CurrentSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequiredSHA256:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		UpdateAvailable:  true,
	}

	got := singboxStatusData(in)

	if got.Installed != in.Installed {
		t.Fatalf("Installed = %v, want %v", got.Installed, in.Installed)
	}
	if got.Version != in.Version {
		t.Fatalf("Version = %q, want %q", got.Version, in.Version)
	}
	if got.Running != in.Running {
		t.Fatalf("Running = %v, want %v", got.Running, in.Running)
	}
	if got.PID != in.PID {
		t.Fatalf("PID = %d, want %d", got.PID, in.PID)
	}
	if got.TunnelCount != in.TunnelCount {
		t.Fatalf("TunnelCount = %d, want %d", got.TunnelCount, in.TunnelCount)
	}
	if got.ProxyComponent != in.ProxyComponent {
		t.Fatalf("ProxyComponent = %v, want %v", got.ProxyComponent, in.ProxyComponent)
	}
	if got.NDMSProxyEnabled != in.NDMSProxyEnabled {
		t.Fatalf("NDMSProxyEnabled = %v, want %v", got.NDMSProxyEnabled, in.NDMSProxyEnabled)
	}
	if !reflect.DeepEqual(got.Features, in.Features) {
		t.Fatalf("Features = %v, want %v", got.Features, in.Features)
	}
	if got.LastError != in.LastError {
		t.Fatalf("LastError = %q, want %q", got.LastError, in.LastError)
	}
	if got.CurrentVersion != in.CurrentVersion {
		t.Fatalf("CurrentVersion = %q, want %q", got.CurrentVersion, in.CurrentVersion)
	}
	if got.RequiredVersion != in.RequiredVersion {
		t.Fatalf("RequiredVersion = %q, want %q", got.RequiredVersion, in.RequiredVersion)
	}
	if got.CurrentSHA256 != in.CurrentSHA256 {
		t.Fatalf("CurrentSHA256 = %q, want %q", got.CurrentSHA256, in.CurrentSHA256)
	}
	if got.RequiredSHA256 != in.RequiredSHA256 {
		t.Fatalf("RequiredSHA256 = %q, want %q", got.RequiredSHA256, in.RequiredSHA256)
	}
	if got.UpdateAvailable != in.UpdateAvailable {
		t.Fatalf("UpdateAvailable = %v, want %v", got.UpdateAvailable, in.UpdateAvailable)
	}
}

func TestSingboxHandler_AddTunnels_BindValidation(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	h.SetBindValidator(func(ctx context.Context, name string) error {
		if name != "eth3" {
			return fmt.Errorf("interface %q not allowed", name)
		}
		return nil
	})

	body := `{"links":"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@1.2.3.4:443?security=tls&bind_interface=bad0#test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.AddTunnels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid bind_interface") {
		t.Fatalf("expected error message to contain invalid bind_interface, got %s", w.Body.String())
	}
}

func TestSingboxHandler_UpdateTunnel_BindValidation(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	h.SetBindValidator(func(ctx context.Context, name string) error {
		if name != "eth3" {
			return fmt.Errorf("interface %q not allowed", name)
		}
		return nil
	})

	body := `{"outbound":{"type":"vless","server":"1.2.3.4","server_port":443,"bind_interface":"bad0"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/singbox/tunnels?tag=t1", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.UpdateTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid bind_interface") {
		t.Fatalf("expected error message to contain invalid bind_interface, got %s", w.Body.String())
	}
}
