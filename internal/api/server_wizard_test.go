package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type mockCoordBridge struct{}

func (m *mockCoordBridge) WithIngressLock(txID string, fn func() error) error {
	return fn()
}
func (m *mockCoordBridge) ApplyXrayConfig(ctx context.Context, cfg vlessserver.Config) error {
	return nil
}
func (m *mockCoordBridge) ApplyTelegramConfig(ctx context.Context, cfg tgwebproxy.Config) error {
	return nil
}
func (m *mockCoordBridge) ExecuteIngressTransaction(ctx context.Context, params serveringress.IngressTransactionParams) error {
	return nil
}
func (m *mockCoordBridge) IsRecoveryRequired() (bool, string) {
	return false, ""
}

type mockXrayComp struct {
	cfg vlessserver.Config
	st  vlessserver.Status
}

func (m *mockXrayComp) GetConfig() vlessserver.Config { return m.cfg }
func (m *mockXrayComp) GetStatus() vlessserver.Status { return m.st }
func (m *mockXrayComp) GenerateLinks(clientID string) (*vlessserver.ShareLinks, error) {
	return &vlessserver.ShareLinks{
		VlessURL: "vless://test@cdn.example.com:443",
		UUID:     clientID,
	}, nil
}

type mockTgComp struct {
	cfg tgwebproxy.PublicConfig
	st  tgwebproxy.Status
}

func (m *mockTgComp) GetConfig() tgwebproxy.PublicConfig { return m.cfg }
func (m *mockTgComp) GetStatus() tgwebproxy.Status       { return m.st }
func (m *mockTgComp) RevealSecret() (tgwebproxy.RevealData, error) {
	return tgwebproxy.RevealData{
		Secret:     "11223344556677889900aabbccddeeff",
		MtproxyURL: "tg://proxy?server=test.com&port=8443",
		TgURL:      "tg://webproxy?server=cdn.com&secret=123",
	}, nil
}

type mockDispComp struct {
	cfg cdndispatcher.Config
}

func (m *mockDispComp) GetConfig() cdndispatcher.Config { return m.cfg }
func (m *mockDispComp) IsRunning() bool                 { return true }

type mockCatalogEmpty struct{}

func (m *mockCatalogEmpty) ListAll(ctx context.Context) []routing.TunnelEntry {
	return nil
}

type mockFileInfoTest struct{}

func (m mockFileInfoTest) Name() string       { return "test" }
func (m mockFileInfoTest) Size() int64        { return 1000 }
func (m mockFileInfoTest) Mode() os.FileMode  { return 0755 }
func (m mockFileInfoTest) ModTime() time.Time { return time.Now() }
func (m mockFileInfoTest) IsDir() bool        { return false }
func (m mockFileInfoTest) Sys() interface{}   { return nil }

func setupTestWizardHandler(t *testing.T) (*ServerWizardHandler, *auth.SessionStore, string) {
	sessions := auth.NewSessionStore(func() time.Duration { return 1 * time.Hour })
	t.Cleanup(sessions.Stop)
	sessionID, err := sessions.Create("root")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	xray := &mockXrayComp{
		cfg: vlessserver.Config{Enabled: false, ListenPort: 9008},
		st:  vlessserver.Status{Installed: true, Configured: false},
	}
	tg := &mockTgComp{
		cfg: tgwebproxy.PublicConfig{Enabled: false, ListenPort: 8085},
		st:  tgwebproxy.Status{Installed: true, Running: false},
	}
	disp := &mockDispComp{
		cfg: cdndispatcher.Config{ListenAddr: ":9009"},
	}
	coord := &mockCoordBridge{}
	egressAdp := egress.NewAdapter(&mockCatalogEmpty{})
	egressAdp.SetFircStateProvider(func(ctx context.Context) (bool, string) {
		return true, ""
	})
	rec := &mockCoordBridge{}

	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "net"), 0755); err != nil {
		t.Fatalf("failed to create net dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to create proc/net/tcp: %v", err)
	}

	preflight := serverwizard.NewPreflightEngine(xray, disp, tg, rec, egressAdp)
	preflight.SetProcDir(procDir)
	preflight.SetStatFn(func(path string) (os.FileInfo, error) {
		return mockFileInfoTest{}, nil
	})
	preflight.SetFindProcFn(func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	})

	fingerprint := serverwizard.NewFingerprintEngine(xray, disp, tg, rec, egressAdp)
	fingerprint.SetProcDir(procDir)

	planStore := serverwizard.NewPlanStore()
	jobRunner := serverwizard.NewJobRunner()

	svc := serverwizard.NewWizardService(
		coord,
		xray,
		tg,
		disp,
		preflight,
		fingerprint,
		planStore,
		jobRunner,
		egressAdp,
	)

	handler := NewServerWizardHandler(svc, sessions)
	return handler, sessions, sessionID
}

func TestServerWizardHandler_CSRFAndOrigin(t *testing.T) {
	handler, _, sessionID := setupTestWizardHandler(t)

	// 1. Get CSRF Token
	req := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	req.Host = "router.local"
	req.Header.Set("Origin", "http://router.local")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	w := httptest.NewRecorder()

	handler.GetCSRFToken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store, private" {
		t.Errorf("expected Cache-Control no-store, private")
	}

	var csrfResp struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &csrfResp); err != nil || csrfResp.Data.Token == "" {
		t.Fatalf("failed to decode csrf token: %v", err)
	}

	// 2. Cross-origin request rejected
	reqCross := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCross.Host = "router.local"
	reqCross.Header.Set("Origin", "http://evil.com")
	reqCross.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wCross := httptest.NewRecorder()
	handler.GetCSRFToken(wCross, reqCross)
	if wCross.Code != http.StatusForbidden {
		t.Errorf("expected 403 on cross-origin, got %d", wCross.Code)
	}

	// 3. Apply without CSRF rejected
	applyPayload, _ := json.Marshal(serverwizard.ApplyRequest{PlanID: "plan-123"})
	reqApplyNoCSRF := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyPayload))
	reqApplyNoCSRF.Host = "router.local"
	reqApplyNoCSRF.Header.Set("Origin", "http://router.local")
	reqApplyNoCSRF.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wApplyNoCSRF := httptest.NewRecorder()
	handler.Apply(wApplyNoCSRF, reqApplyNoCSRF, "tgwebproxy")
	if wApplyNoCSRF.Code != http.StatusForbidden {
		t.Errorf("expected 403 on missing CSRF, got %d", wApplyNoCSRF.Code)
	}
}

func TestServerWizardHandler_EndToEndFlow(t *testing.T) {
	handler, _, sessionID := setupTestWizardHandler(t)

	// 1. Get Capabilities
	reqCaps := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/capabilities", nil)
	wCaps := httptest.NewRecorder()
	handler.GetCapabilities(wCaps, reqCaps, "tgwebproxy")
	if wCaps.Code != http.StatusOK {
		t.Fatalf("expected 200 on capabilities, got %d: %s", wCaps.Code, wCaps.Body.String())
	}

	// 2. Preflight
	preflightBody, _ := json.Marshal(serverwizard.WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		DirectHost:   "192.168.1.1",
		PublicDomain: "cdn.example.com",
		DirectPort:   8443,
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
		TlsDomain:    "ya.ru",
	})
	reqPreflight := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/preflight", bytes.NewReader(preflightBody))
	wPreflight := httptest.NewRecorder()
	handler.Preflight(wPreflight, reqPreflight, "tgwebproxy")
	if wPreflight.Code != http.StatusOK {
		t.Fatalf("expected 200 on preflight, got %d: %s", wPreflight.Code, wPreflight.Body.String())
	}

	// 3. Plan
	reqPlan := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/plan", bytes.NewReader(preflightBody))
	reqPlan.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wPlan := httptest.NewRecorder()
	handler.Plan(wPlan, reqPlan, "tgwebproxy")
	if wPlan.Code != http.StatusOK {
		t.Fatalf("expected 200 on plan, got %d: %s", wPlan.Code, wPlan.Body.String())
	}
	var planEnvelope struct {
		Success bool                          `json:"success"`
		Data    serverwizard.ServerPlanRecord `json:"data"`
	}
	if err := json.Unmarshal(wPlan.Body.Bytes(), &planEnvelope); err != nil || planEnvelope.Data.PlanID == "" {
		t.Fatalf("failed to decode plan record: %v", err)
	}
	planRec := planEnvelope.Data

	// 4. Get CSRF Token
	reqCSRF := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCSRF.Host = "router.local"
	reqCSRF.Header.Set("Origin", "http://router.local")
	reqCSRF.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wCSRF := httptest.NewRecorder()
	handler.GetCSRFToken(wCSRF, reqCSRF)
	var csrfResp struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wCSRF.Body.Bytes(), &csrfResp)
	csrfToken := csrfResp.Data.Token

	// 5. Apply Plan
	applyBody, _ := json.Marshal(serverwizard.ApplyRequest{PlanID: planRec.PlanID})
	reqApply := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyBody))
	reqApply.Host = "router.local"
	reqApply.Header.Set("Origin", "http://router.local")
	reqApply.Header.Set("X-CSRF-Token", csrfToken)
	reqApply.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wApply := httptest.NewRecorder()
	handler.Apply(wApply, reqApply, "tgwebproxy")
	if wApply.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", wApply.Code, wApply.Body.String())
	}
	var applyResp struct {
		Success bool `json:"success"`
		Data    struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wApply.Body.Bytes(), &applyResp)
	jobID := applyResp.Data.JobID

	// 6. Poll Job Status
	var jobResp serverwizard.JobStatusResponse
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		reqJob := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/jobs/"+jobID, nil)
		reqJob.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
		wJob := httptest.NewRecorder()
		handler.GetJob(wJob, reqJob, "tgwebproxy", jobID)
		var jobEnvelope struct {
			Success bool                           `json:"success"`
			Data    serverwizard.JobStatusResponse `json:"data"`
		}
		_ = json.Unmarshal(wJob.Body.Bytes(), &jobEnvelope)
		jobResp = jobEnvelope.Data
		if jobResp.Phase == serverwizard.JobPhaseSucceeded || jobResp.Phase == serverwizard.JobPhaseFailed {
			break
		}
	}
	if jobResp.Phase != serverwizard.JobPhaseSucceeded {
		t.Fatalf("expected job Succeeded, got %s", jobResp.Phase)
	}

	// 7. Reveal Credentials
	reqReveal := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/jobs/"+jobID+"/reveal", nil)
	reqReveal.Host = "router.local"
	reqReveal.Header.Set("Origin", "http://router.local")
	reqReveal.Header.Set("X-CSRF-Token", csrfToken)
	reqReveal.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wReveal := httptest.NewRecorder()
	handler.Reveal(wReveal, reqReveal, "tgwebproxy", jobID)
	if wReveal.Code != http.StatusOK {
		t.Fatalf("expected 200 on reveal, got %d: %s", wReveal.Code, wReveal.Body.String())
	}
	if wReveal.Header().Get("Cache-Control") != "no-store, private" {
		t.Errorf("expected Cache-Control no-store, private on reveal")
	}
	var credsEnvelope struct {
		Success bool                           `json:"success"`
		Data    serverwizard.RevealCredentials `json:"data"`
	}
	if err := json.Unmarshal(wReveal.Body.Bytes(), &credsEnvelope); err != nil || credsEnvelope.Data.TgSecret == "" {
		t.Fatalf("failed to decode reveal creds: %v", err)
	}
}

func TestServerWizardHandler_StrictJSONDecoding(t *testing.T) {
	handler, _, _ := setupTestWizardHandler(t)

	// 1. Unknown field rejected
	reqUnknown := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/preflight",
		bytes.NewReader([]byte(`{"kind":"tgwebproxy","direct_host":"192.168.1.1","public_domain":"example.com","unknown_field":"value"}`)))
	wUnknown := httptest.NewRecorder()
	handler.Preflight(wUnknown, reqUnknown, "tgwebproxy")
	if wUnknown.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on unknown field, got %d: %s", wUnknown.Code, wUnknown.Body.String())
	}

	// 2. Trailing garbage rejected
	reqTrailing := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/preflight",
		bytes.NewReader([]byte(`{"kind":"tgwebproxy","direct_host":"192.168.1.1","public_domain":"example.com"} trailing`)))
	wTrailing := httptest.NewRecorder()
	handler.Preflight(wTrailing, reqTrailing, "tgwebproxy")
	if wTrailing.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on trailing garbage, got %d: %s", wTrailing.Code, wTrailing.Body.String())
	}

	// 3. Body > 64KB rejected
	largePadding := strings.Repeat(" ", 70*1024)
	largeBody := `{"kind":"tgwebproxy","direct_host":"192.168.1.1","public_domain":"example.com"` + largePadding + `}`
	reqLarge := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/preflight",
		bytes.NewReader([]byte(largeBody)))
	wLarge := httptest.NewRecorder()
	handler.Preflight(wLarge, reqLarge, "tgwebproxy")
	if wLarge.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on body > 64KB, got %d: %s", wLarge.Code, wLarge.Body.String())
	}
}

func TestServerWizardHandler_CompositeBothAndVerify(t *testing.T) {
	handler, _, sessionID := setupTestWizardHandler(t)

	// 1. Capabilities for "both"
	reqCaps := httptest.NewRequest(http.MethodGet, "/api/servers/both/wizard/capabilities", nil)
	wCaps := httptest.NewRecorder()
	handler.GetCapabilities(wCaps, reqCaps, "both")
	if wCaps.Code != http.StatusOK {
		t.Fatalf("expected 200 on capabilities for both, got %d: %s", wCaps.Code, wCaps.Body.String())
	}

	// 2a. Plan for "both" with firc:router egress WITHOUT tg_upstream_device MUST be rejected (no silent direct fallback)
	missingTgBody, _ := json.Marshal(serverwizard.WizardPlanRequest{
		Kind:           "both",
		Scenario:       "dual",
		PublicDomain:   "shared.example.test",
		Path:           "/cdn-bridge/",
		CdnProfileID:   "cdn_get",
		Mode:           "xhttp_get",
		DirectHost:     "direct.example.test",
		DirectPort:     8443,
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "firc:router",
		ClientRemark:   "Unified Device",
	})
	reqMissingTg := httptest.NewRequest(http.MethodPost, "/api/servers/both/wizard/plan", bytes.NewReader(missingTgBody))
	reqMissingTg.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wMissingTg := httptest.NewRecorder()
	handler.Plan(wMissingTg, reqMissingTg, "both")
	if wMissingTg.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when both+firc:router is requested without tg_upstream_device, got %d: %s", wMissingTg.Code, wMissingTg.Body.String())
	}

	// 2b. Plan for "both" with firc:router egress AND explicit tg_upstream_device succeeds
	planBody, _ := json.Marshal(serverwizard.WizardPlanRequest{
		Kind:             "both",
		Scenario:         "dual",
		PublicDomain:     "shared.example.test",
		Path:             "/cdn-bridge/",
		CdnProfileID:     "cdn_get",
		Mode:             "xhttp_get",
		DirectHost:       "direct.example.test",
		DirectPort:       8443,
		TlsDomain:        "gateway.icloud.com",
		UpstreamDevice:   "firc:router",
		TgUpstreamDevice: "direct",
		ClientRemark:     "Unified Device",
	})
	reqPlan := httptest.NewRequest(http.MethodPost, "/api/servers/both/wizard/plan", bytes.NewReader(planBody))
	reqPlan.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wPlan := httptest.NewRecorder()
	handler.Plan(wPlan, reqPlan, "both")
	if wPlan.Code != http.StatusOK {
		t.Fatalf("expected 200 on plan for both, got %d: %s", wPlan.Code, wPlan.Body.String())
	}

	// 3. Verify endpoint for "both"
	reqVerify := httptest.NewRequest(http.MethodPost, "/api/servers/both/wizard/verify", nil)
	reqVerify.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wVerify := httptest.NewRecorder()
	handler.Verify(wVerify, reqVerify, "both")
	if wVerify.Code != http.StatusOK {
		t.Fatalf("expected 200 on verify for both, got %d: %s", wVerify.Code, wVerify.Body.String())
	}
	var verifyEnvelope struct {
		Success bool                            `json:"success"`
		Data    serverwizard.VerificationReport `json:"data"`
	}
	if err := json.Unmarshal(wVerify.Body.Bytes(), &verifyEnvelope); err != nil {
		t.Fatalf("failed to decode verify report: %v", err)
	}
	if verifyEnvelope.Data.Kind != "both" || verifyEnvelope.Data.StateFingerprint == "" {
		t.Errorf("unexpected verify report: %+v", verifyEnvelope.Data)
	}
}

type fakeCSRFAuthChecker struct {
	enabled bool
}

func (f *fakeCSRFAuthChecker) IsAuthEnabled() bool { return f.enabled }
func (f *fakeCSRFAuthChecker) GetApiKey() string   { return "" }

func TestServerWizardHandler_AuthDisabled_EndToEndAndCSRF(t *testing.T) {
	handler, _, sessionID := setupTestWizardHandler(t)
	checker := &fakeCSRFAuthChecker{enabled: false}
	handler.SetAuthChecker(checker)

	planBody, _ := json.Marshal(serverwizard.WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		DirectHost:   "192.168.1.1",
		PublicDomain: "cdn.example.com",
		DirectPort:   8443,
		ListenPort:   8085,
		CdnProfileID: "cdn_get",
		TlsDomain:    "ya.ru",
	})

	// 1. Plan without awg_session cookie succeeds.
	reqPlan := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/plan", bytes.NewReader(planBody))
	wPlan := httptest.NewRecorder()
	handler.Plan(wPlan, reqPlan, "tgwebproxy")
	if wPlan.Code != http.StatusOK {
		t.Fatalf("expected 200 on plan without cookie, got %d: %s", wPlan.Code, wPlan.Body.String())
	}
	var planEnvelope struct {
		Success bool                          `json:"success"`
		Data    serverwizard.ServerPlanRecord `json:"data"`
	}
	if err := json.Unmarshal(wPlan.Body.Bytes(), &planEnvelope); err != nil || planEnvelope.Data.PlanID == "" {
		t.Fatalf("failed to decode plan record: %v", err)
	}
	planID := planEnvelope.Data.PlanID

	// 2. GetCSRFToken with valid Origin and NO cookie returns 200 and non-empty token.
	reqCSRF := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCSRF.Host = "router.local"
	reqCSRF.Header.Set("Origin", "http://router.local")
	wCSRF := httptest.NewRecorder()
	handler.GetCSRFToken(wCSRF, reqCSRF)
	if wCSRF.Code != http.StatusOK {
		t.Fatalf("expected 200 on csrf without cookie when auth disabled, got %d: %s", wCSRF.Code, wCSRF.Body.String())
	}
	var csrfResp struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wCSRF.Body.Bytes(), &csrfResp); err != nil || csrfResp.Data.Token == "" {
		t.Fatalf("failed to decode csrf token: %v", err)
	}
	authDisabledToken := csrfResp.Data.Token

	// 3. Apply without cookie using auth-disabled CSRF token succeeds (202).
	applyBody, _ := json.Marshal(serverwizard.ApplyRequest{PlanID: planID})
	reqApply := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyBody))
	reqApply.Host = "router.local"
	reqApply.Header.Set("Origin", "http://router.local")
	reqApply.Header.Set("X-CSRF-Token", authDisabledToken)
	wApply := httptest.NewRecorder()
	handler.Apply(wApply, reqApply, "tgwebproxy")
	if wApply.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on apply without cookie, got %d: %s", wApply.Code, wApply.Body.String())
	}
	var applyResp struct {
		Success bool `json:"success"`
		Data    struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wApply.Body.Bytes(), &applyResp); err != nil || applyResp.Data.JobID == "" {
		t.Fatalf("failed to decode job id: %v", err)
	}
	jobID := applyResp.Data.JobID

	// 4. Poll GetJob without cookie to completion.
	var jobResp serverwizard.JobStatusResponse
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		reqJob := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/jobs/"+jobID, nil)
		wJob := httptest.NewRecorder()
		handler.GetJob(wJob, reqJob, "tgwebproxy", jobID)
		if wJob.Code != http.StatusOK {
			t.Fatalf("expected 200 on get job without cookie, got %d: %s", wJob.Code, wJob.Body.String())
		}
		var jobEnvelope struct {
			Success bool                           `json:"success"`
			Data    serverwizard.JobStatusResponse `json:"data"`
		}
		_ = json.Unmarshal(wJob.Body.Bytes(), &jobEnvelope)
		jobResp = jobEnvelope.Data
		if jobResp.Phase == serverwizard.JobPhaseSucceeded || jobResp.Phase == serverwizard.JobPhaseFailed {
			break
		}
	}
	if jobResp.Phase != serverwizard.JobPhaseSucceeded {
		t.Fatalf("expected job Succeeded, got %s (%s)", jobResp.Phase, jobResp.Error)
	}

	// 5. Reveal credentials without cookie succeeds (200).
	reqReveal := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/jobs/"+jobID+"/reveal", nil)
	reqReveal.Host = "router.local"
	reqReveal.Header.Set("Origin", "http://router.local")
	reqReveal.Header.Set("X-CSRF-Token", authDisabledToken)
	wReveal := httptest.NewRecorder()
	handler.Reveal(wReveal, reqReveal, "tgwebproxy", jobID)
	if wReveal.Code != http.StatusOK {
		t.Fatalf("expected 200 on reveal without cookie, got %d: %s", wReveal.Code, wReveal.Body.String())
	}
	var credsEnvelope struct {
		Success bool                           `json:"success"`
		Data    serverwizard.RevealCredentials `json:"data"`
	}
	if err := json.Unmarshal(wReveal.Body.Bytes(), &credsEnvelope); err != nil || credsEnvelope.Data.TgSecret == "" {
		t.Fatalf("expected non-empty TgSecret on reveal, got err=%v body=%s", err, wReveal.Body.String())
	}

	// 6. Plan + GetCSRFToken + Apply also succeed with a stale/invalid awg_session cookie when auth is disabled.
	staleCookie := &http.Cookie{Name: auth.SessionCookie, Value: "stale-expired-session"}
	reqPlanStale := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/plan", bytes.NewReader(planBody))
	reqPlanStale.AddCookie(staleCookie)
	wPlanStale := httptest.NewRecorder()
	handler.Plan(wPlanStale, reqPlanStale, "tgwebproxy")
	if wPlanStale.Code != http.StatusOK {
		t.Fatalf("expected 200 on plan with stale cookie, got %d: %s", wPlanStale.Code, wPlanStale.Body.String())
	}
	var planStaleEnv struct {
		Success bool                          `json:"success"`
		Data    serverwizard.ServerPlanRecord `json:"data"`
	}
	if err := json.Unmarshal(wPlanStale.Body.Bytes(), &planStaleEnv); err != nil || planStaleEnv.Data.PlanID == "" {
		t.Fatalf("failed to decode stale-cookie plan record: %v", err)
	}

	reqCSRFStale := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCSRFStale.Host = "router.local"
	reqCSRFStale.Header.Set("Origin", "http://router.local")
	reqCSRFStale.AddCookie(staleCookie)
	wCSRFStale := httptest.NewRecorder()
	handler.GetCSRFToken(wCSRFStale, reqCSRFStale)
	if wCSRFStale.Code != http.StatusOK {
		t.Fatalf("expected 200 on csrf with stale cookie when auth disabled, got %d: %s", wCSRFStale.Code, wCSRFStale.Body.String())
	}

	applyStaleBody, _ := json.Marshal(serverwizard.ApplyRequest{PlanID: planStaleEnv.Data.PlanID})
	reqApplyStale := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyStaleBody))
	reqApplyStale.Host = "router.local"
	reqApplyStale.Header.Set("Origin", "http://router.local")
	reqApplyStale.Header.Set("X-CSRF-Token", authDisabledToken)
	reqApplyStale.AddCookie(staleCookie)
	wApplyStale := httptest.NewRecorder()
	handler.Apply(wApplyStale, reqApplyStale, "tgwebproxy")
	if wApplyStale.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on apply with stale cookie when auth disabled, got %d: %s", wApplyStale.Code, wApplyStale.Body.String())
	}

	// 7. Strict CSRF checks still apply when auth is disabled.
	reqCross := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCross.Host = "router.local"
	reqCross.Header.Set("Origin", "http://evil.example")
	wCross := httptest.NewRecorder()
	handler.GetCSRFToken(wCross, reqCross)
	if wCross.Code != http.StatusForbidden {
		t.Errorf("expected 403 on cross-origin GetCSRFToken when auth disabled, got %d", wCross.Code)
	}

	applyAnyBody, _ := json.Marshal(serverwizard.ApplyRequest{PlanID: "plan-check"})

	reqMissingToken := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyAnyBody))
	reqMissingToken.Host = "router.local"
	reqMissingToken.Header.Set("Origin", "http://router.local")
	wMissingToken := httptest.NewRecorder()
	handler.Apply(wMissingToken, reqMissingToken, "tgwebproxy")
	if wMissingToken.Code != http.StatusForbidden {
		t.Errorf("expected 403 on missing X-CSRF-Token when auth disabled, got %d", wMissingToken.Code)
	}

	reqWrongToken := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyAnyBody))
	reqWrongToken.Host = "router.local"
	reqWrongToken.Header.Set("Origin", "http://router.local")
	reqWrongToken.Header.Set("X-CSRF-Token", "deadbeef")
	wWrongToken := httptest.NewRecorder()
	handler.Apply(wWrongToken, reqWrongToken, "tgwebproxy")
	if wWrongToken.Code != http.StatusForbidden {
		t.Errorf("expected 403 on wrong X-CSRF-Token when auth disabled, got %d", wWrongToken.Code)
	}

	reqNoOrigin := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyAnyBody))
	reqNoOrigin.Host = "router.local"
	reqNoOrigin.Header.Set("X-CSRF-Token", authDisabledToken)
	wNoOrigin := httptest.NewRecorder()
	handler.Apply(wNoOrigin, reqNoOrigin, "tgwebproxy")
	if wNoOrigin.Code != http.StatusForbidden {
		t.Errorf("expected 403 on missing Origin and Referer when auth disabled, got %d", wNoOrigin.Code)
	}

	// 8. Re-enabling auth immediately requires session cookie and rejects the old auth-disabled token.
	checker.enabled = true

	reqCSRFNoCookie := httptest.NewRequest(http.MethodGet, "/api/servers/tgwebproxy/wizard/csrf", nil)
	reqCSRFNoCookie.Host = "router.local"
	reqCSRFNoCookie.Header.Set("Origin", "http://router.local")
	wCSRFNoCookie := httptest.NewRecorder()
	handler.GetCSRFToken(wCSRFNoCookie, reqCSRFNoCookie)
	if wCSRFNoCookie.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on GetCSRFToken without cookie after auth re-enabled, got %d", wCSRFNoCookie.Code)
	}

	reqApplyOldToken := httptest.NewRequest(http.MethodPost, "/api/servers/tgwebproxy/wizard/apply", bytes.NewReader(applyAnyBody))
	reqApplyOldToken.Host = "router.local"
	reqApplyOldToken.Header.Set("Origin", "http://router.local")
	reqApplyOldToken.Header.Set("X-CSRF-Token", authDisabledToken)
	reqApplyOldToken.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sessionID})
	wApplyOldToken := httptest.NewRecorder()
	handler.Apply(wApplyOldToken, reqApplyOldToken, "tgwebproxy")
	if wApplyOldToken.Code != http.StatusForbidden {
		t.Errorf("expected 403 when auth-disabled token used after auth re-enabled, got %d", wApplyOldToken.Code)
	}
}
