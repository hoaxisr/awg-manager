package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/aiassistant"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestSafeDiagnosticErrorRedactsURLAndLimitsLength(t *testing.T) {
	value := safeDiagnosticError("fetch https://user:secret@example.com/path?token=secret Authorization: Bearer abc123 api_key=xyz " + strings.Repeat("x", 700))
	if strings.Contains(value, "secret") || strings.Contains(value, "example.com") || len(value) > 503 {
		t.Fatalf("unsafe diagnostic error: %q", value)
	}
	if strings.Contains(value, "abc123") || strings.Contains(value, "xyz") {
		t.Fatalf("credentials leaked: %q", value)
	}
}

func TestAILogAllowlist(t *testing.T) {
	if bucket, err := allowedLogBucket("app"); err != nil || bucket != logging.BucketApp {
		t.Fatalf("app bucket=%q err=%v", bucket, err)
	}
	if _, err := allowedLogBucket("/var/log/messages"); err == nil {
		t.Fatal("arbitrary log source must be rejected")
	}
	if !allowedLogGroup(logging.BucketApp, "routing") || allowedLogGroup(logging.BucketApp, "singbox") {
		t.Fatal("app group allowlist is incorrect")
	}
	if !allowedLogGroup(logging.BucketSingbox, "singbox") || allowedLogGroup(logging.BucketSingbox, "system") {
		t.Fatal("singbox group allowlist is incorrect")
	}
	if allowedLogLevel("trace") || !allowedLogLevel("warn") {
		t.Fatal("log level allowlist is incorrect")
	}
}

func TestSafeInspectDestination(t *testing.T) {
	for _, value := range []string{"youtube.com", "1.1.1.1", "2001:db8::1"} {
		if !safeInspectDestination(value) {
			t.Fatalf("valid destination rejected: %q", value)
		}
	}
	for _, value := range []string{"https://youtube.com", "a b", "../secret", "host?x=1"} {
		if safeInspectDestination(value) {
			t.Fatalf("unsafe destination accepted: %q", value)
		}
	}
}

func TestSelectedAIEngineRejectsUnknownValue(t *testing.T) {
	s := &Server{}
	if engine, err := s.selectedAIEngine("sing-box"); err != nil || engine != "singbox" {
		t.Fatalf("sing-box engine=%q err=%v", engine, err)
	}
	if engine, err := s.selectedAIEngine("mihomo"); err != nil || engine != "mihomo" {
		t.Fatalf("mihomo engine=%q err=%v", engine, err)
	}
	if _, err := s.selectedAIEngine("arbitrary"); err == nil {
		t.Fatal("unknown engine must be rejected")
	}
}

func TestSelectedAIEngine_MihomoDynamic(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	s := &Server{settings: store}
	// Default engine without explicit settings should be singbox
	if eng, err := s.selectedAIEngine("auto"); err != nil || eng != "singbox" {
		t.Fatalf("expected default singbox, got %q (err=%v)", eng, err)
	}

	// Switch to mihomo in storage
	err := store.Update(func(cfg *storage.Settings) error {
		cfg.SingboxRouter.RoutingEngine = "mihomo"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// "auto" and "" should now resolve to mihomo
	if eng, err := s.selectedAIEngine("auto"); err != nil || eng != "mihomo" {
		t.Fatalf("expected mihomo for auto, got %q (err=%v)", eng, err)
	}
	if eng, err := s.selectedAIEngine(""); err != nil || eng != "mihomo" {
		t.Fatalf("expected mihomo for empty, got %q (err=%v)", eng, err)
	}
	// Explicit singbox request still returns singbox
	if eng, err := s.selectedAIEngine("singbox"); err != nil || eng != "singbox" {
		t.Fatalf("expected singbox for explicit singbox, got %q (err=%v)", eng, err)
	}
}

func TestAIClashClient_Mihomo(t *testing.T) {
	s := &Server{}
	s.SetMihomoClashAddr("127.0.0.1:9095")
	engine, client, err := s.aiClashClient("mihomo")
	if err != nil {
		t.Fatalf("aiClashClient mihomo failed: %v", err)
	}
	if engine != "mihomo" {
		t.Fatalf("expected engine mihomo, got %q", engine)
	}
	if client == nil || client.Address() != "127.0.0.1:9095" {
		t.Fatalf("expected client with address 127.0.0.1:9095, got %+v", client)
	}
}

func TestAIToolSources_EngineStatusAndExplainClient_Mihomo(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	err := store.Update(func(cfg *storage.Settings) error {
		cfg.SingboxRouter.RoutingEngine = "mihomo"
		cfg.SingboxRouter.Enabled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	mockOrch := &mockMihomoOrch{}
	s := &Server{
		settings:   store,
		mihomoOrch: mockOrch,
	}

	sources := s.aiToolSources(nil, nil, nil)

	// EngineStatus verification
	statusVal, err := sources.EngineStatus(context.Background())
	if err != nil {
		t.Fatalf("EngineStatus failed: %v", err)
	}
	statusMap, ok := statusVal.(map[string]any)
	if !ok {
		t.Fatalf("unexpected EngineStatus type: %T", statusVal)
	}
	routingMap, ok := statusMap["routing"].(map[string]any)
	if !ok || routingMap["selectedEngine"] != "mihomo" {
		t.Fatalf("expected routing.selectedEngine == mihomo, got: %+v", routingMap)
	}
	mihomoMap, ok := statusMap["mihomo"].(map[string]any)
	if !ok || mihomoMap["running"] != true || !mockOrch.verified {
		t.Fatalf("expected mihomo.running == true, got: %+v", mihomoMap)
	}

	// ExplainClient verification
	clientVal, err := sources.ExplainClient(context.Background(), "192.168.1.100")
	if err != nil {
		t.Fatalf("ExplainClient failed: %v", err)
	}
	clientMap, ok := clientVal.(map[string]any)
	if !ok || clientMap["routingEngine"] != "mihomo" {
		t.Fatalf("expected ExplainClient routingEngine == mihomo, got: %+v", clientMap)
	}
}

func TestAIToolSources_ExplainDNS_Mihomo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns/query" && r.URL.Query().Get("name") == "example.com" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Status": 0,
				"Answer": []map[string]any{
					{"name": "example.com.", "type": 1, "data": "93.184.216.34"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	_ = store.Update(func(cfg *storage.Settings) error {
		cfg.SingboxRouter.RoutingEngine = "mihomo"
		return nil
	})

	s := &Server{
		settings: store,
	}
	s.SetMihomoClashAddr(strings.TrimPrefix(ts.URL, "http://"))

	sources := s.aiToolSources(nil, nil, nil)
	val, err := sources.ExplainDNS(context.Background(), "example.com", "", "A")
	if err != nil {
		t.Fatalf("ExplainDNS failed: %v", err)
	}
	resMap, ok := val.(map[string]any)
	if !ok || resMap["engine"] != "mihomo" {
		t.Fatalf("expected engine mihomo in ExplainDNS, got: %+v", resMap)
	}
	if resMap["result"] == nil {
		t.Fatal("expected non-nil result from ExplainDNS")
	}
}

func TestAIToolSources_ConnectionsAndExplainConn_NilConnectionSource(t *testing.T) {
	s := &Server{}
	sources := s.aiToolSources(nil, nil, nil)

	// Connections must return an error without panicking
	if _, err := sources.Connections(context.Background(), "127.0.0.1", 10); err == nil {
		t.Fatal("expected error from Connections with nil connectionSource, got nil")
	}

	// ExplainConn must return an error without panicking
	if _, err := sources.ExplainConn(context.Background(), "127.0.0.1", 1234, "1.1.1.1", 53, "udp"); err == nil {
		t.Fatal("expected error from ExplainConn with nil connectionSource, got nil")
	}
}

type mockMihomoOrchNilVerify struct{}

func (m *mockMihomoOrchNilVerify) Restart(ctx context.Context) error { return nil }
func (m *mockMihomoOrchNilVerify) Reload(ctx context.Context) error  { return nil }
func (m *mockMihomoOrchNilVerify) Verify(ctx context.Context) (*aiassistant.ActionVerification, error) {
	return nil, nil
}

func TestAIToolSources_EngineStatus_MihomoOrchVerifyNil(t *testing.T) {
	s := &Server{
		mihomoOrch: &mockMihomoOrchNilVerify{},
	}
	sources := s.aiToolSources(nil, nil, nil)
	val, err := sources.EngineStatus(context.Background())
	if err != nil {
		t.Fatalf("EngineStatus failed: %v", err)
	}
	statusMap, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("unexpected EngineStatus type: %T", val)
	}
	mihomoMap, ok := statusMap["mihomo"].(map[string]any)
	if !ok {
		t.Fatalf("expected mihomo map in EngineStatus, got: %+v", statusMap)
	}
	if mihomoMap["running"] != false || mihomoMap["status"] != "unknown" {
		t.Fatalf("expected running=false status=unknown, got: %+v", mihomoMap)
	}
}

func TestAIToolSources_ExplainDNS_MihomoErrorBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"DNS resolver is disabled"}`))
	}))
	defer ts.Close()

	s := &Server{}
	s.SetMihomoClashAddr(strings.TrimPrefix(ts.URL, "http://"))

	sources := s.aiToolSources(nil, nil, nil)
	// Force mihomo engine via requested context
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	_ = store.Update(func(cfg *storage.Settings) error {
		cfg.SingboxRouter.RoutingEngine = "mihomo"
		return nil
	})
	s.settings = store

	_, err := sources.ExplainDNS(context.Background(), "example.com", "", "A")
	if err == nil {
		t.Fatal("expected ExplainDNS error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "DNS resolver is disabled") {
		t.Fatalf("expected error body in ExplainDNS message, got: %v", err)
	}
}

func TestAIClashClient_NilServerSingbox(t *testing.T) {
	var s *Server
	if _, _, err := s.aiClashClient("singbox"); err == nil {
		t.Fatal("expected error from nil Server for singbox clash client")
	}
}


