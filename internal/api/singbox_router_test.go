package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// mockRouterSvc satisfies router.Service with controllable return values.
type mockRouterSvc struct {
	enableErr  error
	bindMAC    string
	bindPolicy string
	bindErr    error
}

func (m *mockRouterSvc) Enable(ctx context.Context) error  { return m.enableErr }
func (m *mockRouterSvc) Disable(ctx context.Context) error { return nil }
func (m *mockRouterSvc) Reconcile(ctx context.Context) error { return nil }
func (m *mockRouterSvc) GetStatus(ctx context.Context) (router.Status, error) {
	return router.Status{}, nil
}
func (m *mockRouterSvc) GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error) {
	return storage.SingboxRouterSettings{}, nil
}
func (m *mockRouterSvc) UpdateSettings(ctx context.Context, s storage.SingboxRouterSettings) error {
	return nil
}
func (m *mockRouterSvc) ListRules(ctx context.Context) ([]router.Rule, error) { return nil, nil }
func (m *mockRouterSvc) AddRule(ctx context.Context, rule router.Rule) error  { return nil }
func (m *mockRouterSvc) UpdateRule(ctx context.Context, index int, rule router.Rule) error {
	return nil
}
func (m *mockRouterSvc) DeleteRule(ctx context.Context, index int) error           { return nil }
func (m *mockRouterSvc) MoveRule(ctx context.Context, from, to int) error          { return nil }
func (m *mockRouterSvc) ListRuleSets(ctx context.Context) ([]router.RuleSet, error) { return nil, nil }
func (m *mockRouterSvc) AddRuleSet(ctx context.Context, rs router.RuleSet) error   { return nil }
func (m *mockRouterSvc) DeleteRuleSet(ctx context.Context, tag string, force bool) error {
	return nil
}
func (m *mockRouterSvc) RefreshRuleSet(ctx context.Context, tag string) error { return nil }
func (m *mockRouterSvc) ListCompositeOutbounds(ctx context.Context) ([]router.Outbound, error) {
	return nil, nil
}
func (m *mockRouterSvc) AddCompositeOutbound(ctx context.Context, o router.Outbound) error {
	return nil
}
func (m *mockRouterSvc) UpdateCompositeOutbound(ctx context.Context, tag string, o router.Outbound) error {
	return nil
}
func (m *mockRouterSvc) DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	return nil
}
func (m *mockRouterSvc) ApplyPreset(ctx context.Context, presetID, outboundTag string) error {
	return nil
}
func (m *mockRouterSvc) ListPolicies(ctx context.Context) ([]router.PolicyInfo, error) {
	return []router.PolicyInfo{}, nil
}
func (m *mockRouterSvc) CreatePolicy(ctx context.Context, description string) (router.PolicyInfo, error) {
	return router.PolicyInfo{Name: "Policy0", Description: description}, nil
}
func (m *mockRouterSvc) ListPolicyDevices(ctx context.Context, policyName string) ([]router.PolicyDevice, error) {
	return []router.PolicyDevice{}, nil
}
func (m *mockRouterSvc) BindDevice(ctx context.Context, mac, policyName string) error {
	m.bindMAC = mac
	m.bindPolicy = policyName
	return m.bindErr
}
func (m *mockRouterSvc) UnbindDevice(ctx context.Context, mac string) error { return nil }
func (m *mockRouterSvc) ListDNSServers(ctx context.Context) ([]router.DNSServer, error) {
	return nil, nil
}
func (m *mockRouterSvc) AddDNSServer(ctx context.Context, s router.DNSServer) error { return nil }
func (m *mockRouterSvc) UpdateDNSServer(ctx context.Context, tag string, s router.DNSServer) error {
	return nil
}
func (m *mockRouterSvc) DeleteDNSServer(ctx context.Context, tag string, force bool) error {
	return nil
}
func (m *mockRouterSvc) ListDNSRules(ctx context.Context) ([]router.DNSRule, error) { return nil, nil }
func (m *mockRouterSvc) AddDNSRule(ctx context.Context, r router.DNSRule) error      { return nil }
func (m *mockRouterSvc) UpdateDNSRule(ctx context.Context, index int, r router.DNSRule) error {
	return nil
}
func (m *mockRouterSvc) DeleteDNSRule(ctx context.Context, index int) error          { return nil }
func (m *mockRouterSvc) MoveDNSRule(ctx context.Context, from, to int) error          { return nil }
func (m *mockRouterSvc) GetDNSGlobals(ctx context.Context) (string, string, error)   { return "", "", nil }
func (m *mockRouterSvc) SetDNSGlobals(ctx context.Context, final, strategy string) error {
	return nil
}

func newMockRouterHandler(svc *mockRouterSvc) *SingboxRouterHandler {
	return &SingboxRouterHandler{svc: svc}
}

func TestRouterEnable_PolicyNotConfigured_Returns400(t *testing.T) {
	svc := &mockRouterSvc{enableErr: router.ErrPolicyNotConfigured}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/enable", nil)
	rr := httptest.NewRecorder()
	h.Enable(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestRouterEnable_PolicyMissing_Returns400(t *testing.T) {
	svc := &mockRouterSvc{enableErr: router.ErrPolicyMissing}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/enable", nil)
	rr := httptest.NewRecorder()
	h.Enable(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestRouterEnable_MethodNotAllowed(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/enable", nil)
	rr := httptest.NewRecorder()
	h.Enable(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

func TestRouterBindDevice_DelegatesToService(t *testing.T) {
	svc := &mockRouterSvc{}
	h := newMockRouterHandler(svc)
	body := strings.NewReader(`{"mac":"aa:bb:cc:dd:ee:ff","policyName":"Policy0"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/policy-devices/bind", body)
	rr := httptest.NewRecorder()
	h.BindDevice(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if svc.bindMAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("bind MAC wrong: got %q", svc.bindMAC)
	}
	if svc.bindPolicy != "Policy0" {
		t.Errorf("bind policy wrong: got %q", svc.bindPolicy)
	}
}

func TestRouterListPolicies_Returns200(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/policies", nil)
	rr := httptest.NewRecorder()
	h.PoliciesCollection(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestRouterListPolicyDevices_MissingName_Returns400(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/policy-devices", nil)
	rr := httptest.NewRecorder()
	h.ListPolicyDevices(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}
