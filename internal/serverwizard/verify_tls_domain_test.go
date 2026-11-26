package serverwizard

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

func TestVerifyTelegram_EmptyTlsDomainUsesDefaultYaRu(t *testing.T) {
	port := testutil.FreePort(t)

	tgSvc := tgwebproxy.New(t.TempDir(), nil)
	defer tgSvc.Close()
	if err := tgSvc.UpdateConfig(tgwebproxy.Config{
		Scenario:   tgwebproxy.ScenarioDirectFakeTLS,
		DirectHost: "203.0.113.10",
		DirectPort: port,
		TlsDomain:  "",
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	wiz := &WizardService{
		tg: tgSvc,
		lookupHost: func(_ context.Context, _ string) ([]string, error) {
			return []string{"203.0.113.10"}, nil
		},
	}

	if err := tgSvc.Start(); err != nil {
		t.Fatalf("tgSvc.Start: %v", err)
	}
	defer func() { _ = tgSvc.Stop() }()

	var checks []VerificationCheck
	wiz.verifyTelegram(context.Background(), &checks)

	foundListenerOK := false
	for _, ch := range checks {
		if ch.ID == "tg_mtproto_listener" {
			if ch.Status != CheckStatusOK {
				t.Fatalf("tg_mtproto_listener status = %s (%s), want ok", ch.Status, ch.Message)
			}
			foundListenerOK = true
		}
	}
	if !foundListenerOK {
		t.Fatalf("tg_mtproto_listener check not found in %+v", checks)
	}
}
