package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// mockRouterSvc satisfies router.Service with controllable return values.
type mockRouterSvc struct {
	enableErr     error
	bindMAC       string
	bindPolicy    string
	bindErr       error
	stagingStatus router.StagingStatus
	applyErr      error
	applyRes      orchestrator.ValidationResult
	discardErr    error
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
func (m *mockRouterSvc) SetRouteFinal(ctx context.Context, tag string) error       { return nil }
func (m *mockRouterSvc) ListRuleSets(ctx context.Context) ([]router.RuleSet, error) { return nil, nil }
func (m *mockRouterSvc) AddRuleSet(ctx context.Context, rs router.RuleSet) error { return nil }
func (m *mockRouterSvc) UpdateRuleSet(ctx context.Context, tag string, rs router.RuleSet) error {
	return nil
}
func (m *mockRouterSvc) DeleteRuleSet(ctx context.Context, tag string, force bool) error {
	return nil
}
func (m *mockRouterSvc) RefreshRuleSet(ctx context.Context, tag string) error { return nil }
func (m *mockRouterSvc) ListCompositeOutbounds(ctx context.Context) ([]router.CompositeOutboundView, error) {
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
func (m *mockRouterSvc) Inspect(ctx context.Context, input router.InspectInput) (router.InspectResult, error) {
	return router.InspectResult{Input: input.Domain, InputType: "domain", Destination: "direct", MatchedRule: -1, Matches: []router.RuleMatchResult{}, Final: "direct"}, nil
}

func (m *mockRouterSvc) StagingStatus(_ context.Context) router.StagingStatus {
	return m.stagingStatus
}

func (m *mockRouterSvc) ApplyStaging(_ context.Context) (orchestrator.ValidationResult, error) {
	return m.applyRes, m.applyErr
}

func (m *mockRouterSvc) DiscardStaging(_ context.Context) error {
	return m.discardErr
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

// unmarshalStagingStatus unwraps the {success:true, data:{...}} envelope
// and returns the inner RouterStagingStatusResponse.
func unmarshalStagingStatus(t *testing.T, body []byte) RouterStagingStatusResponse {
	t.Helper()
	var env struct {
		Data RouterStagingStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal staging status: %v (body: %s)", err, body)
	}
	return env.Data
}

func TestGetStaging_NoDraft(t *testing.T) {
	svc := &mockRouterSvc{}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/staging", nil)
	rr := httptest.NewRecorder()
	h.GetStaging(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rr.Code, rr.Body)
	}
	got := unmarshalStagingStatus(t, rr.Body.Bytes())
	if got.HasDraft {
		t.Errorf("HasDraft true on fresh setup")
	}
	if got.DraftedAt != nil {
		t.Errorf("DraftedAt should be nil")
	}
}

func TestGetStaging_WithDraft(t *testing.T) {
	at := time.Date(2026, 5, 11, 16, 32, 0, 0, time.UTC)
	svc := &mockRouterSvc{stagingStatus: router.StagingStatus{HasDraft: true, DraftedAt: at}}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/staging", nil)
	rr := httptest.NewRecorder()
	h.GetStaging(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body)
	}
	got := unmarshalStagingStatus(t, rr.Body.Bytes())
	if !got.HasDraft {
		t.Error("HasDraft false")
	}
	if got.DraftedAt == nil || !got.DraftedAt.Equal(at) {
		t.Errorf("DraftedAt: got %v want %v", got.DraftedAt, at)
	}
}

func TestPostStagingApply_200(t *testing.T) {
	svc := &mockRouterSvc{}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging/apply", nil)
	rr := httptest.NewRecorder()
	h.PostStagingApply(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: %d body=%s", rr.Code, rr.Body)
	}
}

func TestPostStagingApply_409OnNoDraft(t *testing.T) {
	svc := &mockRouterSvc{applyErr: orchestrator.ErrNoDraft}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging/apply", nil)
	rr := httptest.NewRecorder()
	h.PostStagingApply(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status: %d body=%s", rr.Code, rr.Body)
	}
}

func TestPostStagingApply_422OnValidation(t *testing.T) {
	svc := &mockRouterSvc{
		applyRes: orchestrator.ValidationResult{Errors: []orchestrator.ValidationError{
			{Slot: orchestrator.SlotRouter, Kind: "unknown-outbound", Tag: "ghost", InRule: "route.final", Message: "no slot declares this outbound tag"},
		}},
	}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging/apply", nil)
	rr := httptest.NewRecorder()
	h.PostStagingApply(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body)
	}
	var got RouterStagingValidationError
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Validation == nil || len(got.Validation.Errors) != 1 {
		t.Errorf("validation errors missing: %#v", got)
	}
	if got.Validation.Errors[0].Tag != "ghost" {
		t.Errorf("wrong tag: %#v", got.Validation.Errors[0])
	}
}

func TestPostStagingApply_422OnSbCheck(t *testing.T) {
	svc := &mockRouterSvc{applyErr: errors.New("\x1b[31mFATAL\x1b[0m sing-box check failed: bad rule")}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging/apply", nil)
	rr := httptest.NewRecorder()
	h.PostStagingApply(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d", rr.Code)
	}
	var got RouterStagingValidationError
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SbCheck == "" {
		t.Error("SbCheck empty")
	}
	if strings.Contains(got.SbCheck, "\x1b") {
		t.Errorf("ANSI not stripped: %q", got.SbCheck)
	}
}

func TestPostStagingDiscard_200(t *testing.T) {
	svc := &mockRouterSvc{}
	h := newMockRouterHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging/discard", nil)
	rr := httptest.NewRecorder()
	h.PostStagingDiscard(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: %d body=%s", rr.Code, rr.Body)
	}
}

func TestGetStaging_405OnWrongMethod(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/router/staging", nil)
	rr := httptest.NewRecorder()
	h.GetStaging(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rr.Code)
	}
}

func TestPostStagingApply_405OnWrongMethod(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/staging/apply", nil)
	rr := httptest.NewRecorder()
	h.PostStagingApply(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rr.Code)
	}
}

func TestPostStagingDiscard_405OnWrongMethod(t *testing.T) {
	h := newMockRouterHandler(&mockRouterSvc{})
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/router/staging/discard", nil)
	rr := httptest.NewRecorder()
	h.PostStagingDiscard(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rr.Code)
	}
}

// newTestRouterHandlerReal wires a real *router.ServiceImpl over a real
// *orchestrator.Orchestrator rooted at t.TempDir(). This gives the
// regression test a full file-system path to verify staging behaviour.
func newTestRouterHandlerReal(t *testing.T) (*SingboxRouterHandler, string) {
	t.Helper()
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	deps := router.Deps{
		Orch:           orch,
		WANIPCollector: &noopWANIPCollector{},
	}
	svc := router.NewService(deps)
	h := NewSingboxRouterHandler(svc, nil)
	return h, dir
}

// noopWANIPCollector is a test double that returns no WAN IPs. Wired
// into router.Deps so NewService doesn't fall back to the production
// collector (which shells out to /opt/sbin/ip — unavailable in tests).
type noopWANIPCollector struct{}

func (noopWANIPCollector) Collect(_ context.Context) ([]string, error) { return nil, nil }

func TestAddRule_RegressionStagesNotApplies(t *testing.T) {
	h, dir := newTestRouterHandlerReal(t)

	body := `{"action":"route","outbound":"direct","domain_suffix":["example.com"]}`
	req := httptest.NewRequest("POST", "/api/singbox/router/rules",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.AddRule(rr, req)

	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("AddRule status: %d body=%s", rr.Code, rr.Body)
	}
	pendingPath := filepath.Join(dir, "pending", "20-router.json")
	if _, err := os.Stat(pendingPath); err != nil {
		t.Errorf("pending file missing: %v", err)
	}
	activePath := filepath.Join(dir, "20-router.json")
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Errorf("active file should not exist, got err=%v", err)
	}
}
