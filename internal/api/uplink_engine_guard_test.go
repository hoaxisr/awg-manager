package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestFircEngineSwitchRejectsProtectedUplinkWithConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(uplink.Policy{ID: "vpn-fallback", Target: uplink.Target{Engine: "sing-box", Ref: "vpn"}, Providers: []string{"a", "b"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	controller := uplink.New(store, nil, nil)
	op := firc.NewOperator(firc.Options{ConfigDir: dir})
	op.SetDefaultEngineHooks(firc.EngineSwitchHooks{AcquireTransition: func(ctx context.Context) (firc.EngineTransitionGuard, error) {
		return controller.AcquireEngineTransition(ctx)
	}})
	handler := NewFircHandler(op)
	response := httptest.NewRecorder()
	handler.handleSwitchEngine(response, httptest.NewRequest(http.MethodPost, "/api/firc/engine/switch", strings.NewReader(`{"engine":"firc"}`)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "vpn-fallback") || !strings.Contains(response.Body.String(), "UPLINK_POLICY_CONFLICT") {
		t.Fatal(response.Code, response.Body.String())
	}
}
