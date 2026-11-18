package serveringress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

func TestExecuteIngressTransaction_HappyPath(t *testing.T) {
	dataDir := t.TempDir()
	xrayMock := &mockXray{
		cfg: vlessserver.Config{
			Enabled:       false,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
		},
	}
	dispMock := &mockDispatcher{
		running: false,
	}
	tgMock := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:    false,
			ListenPort: 8085,
			Secret:     "oldsecret0123456789abcdef012345",
		},
	}

	coord := New(dataDir, xrayMock, dispMock, tgMock)

	probeCalled := false
	params := IngressTransactionParams{
		TxID:                "test-tx-happy",
		ExpectedFingerprint: "fp-match",
		FingerprintFunc: func(ctx context.Context) (string, error) {
			return "fp-match", nil
		},
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
			PublicDomain:  "cdn.example.com",
			PublicPort:    443,
			Path:          "/cdn-bridge/",
			Transport:     "xhttp",
			Mode:          "packet-up",
			UplinkMethod:  "GET",
			OutboundMode:  "direct",
			ClientRemark:  "TestClient",
			ClientUUID:    "uuid-1234",
		},
		ReadinessProbe: func(ctx context.Context, topo IngressTopology) error {
			probeCalled = true
			return nil
		},
	}

	err := coord.ExecuteIngressTransaction(context.Background(), params)
	if err != nil {
		t.Fatalf("ExecuteIngressTransaction failed: %v", err)
	}

	if !probeCalled {
		t.Fatalf("expected readiness probe to be called")
	}

	// Verify CommitPrepared was called
	if len(xrayMock.commitCalls) != 1 {
		t.Errorf("expected 1 commit call, got %d", len(xrayMock.commitCalls))
	}

	// Verify FinalizePrepared was called
	if len(xrayMock.finalizeCalls) != 1 {
		t.Errorf("expected 1 finalize call, got %d", len(xrayMock.finalizeCalls))
	}

	// Verify Dispatcher was started for CDN ingress
	if !dispMock.running {
		t.Errorf("expected dispatcher to be running")
	}

	// Active journal must be archived as committed
	if fileExists(coord.journalPath()) {
		t.Errorf("expected active journal to be archived")
	}
}

func TestExecuteIngressTransaction_ProbeFailureRollback(t *testing.T) {
	dataDir := t.TempDir()
	xrayMock := &mockXray{
		cfg: vlessserver.Config{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
		},
	}
	dispMock := &mockDispatcher{
		running: true,
	}
	tgMock := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:    false,
			ListenPort: 8085,
			Secret:     "initialsecret0123456789abcdef01",
		},
	}

	coord := New(dataDir, xrayMock, dispMock, tgMock)

	probeErr := errors.New("connection timed out during probe")
	params := IngressTransactionParams{
		TxID: "test-tx-probe-fail",
		Telegram: &IngressTelegramCandidate{
			Enabled:    true,
			DirectHost: "tg.example.com",
			DirectPort: 8443,
			ListenPort: 8085,
			Secret:     "newsecret0123456789abcdef0123456",
		},
		ReadinessProbe: func(ctx context.Context, topo IngressTopology) error {
			return probeErr
		},
	}

	err := coord.ExecuteIngressTransaction(context.Background(), params)
	if err == nil {
		t.Fatalf("expected error from probe failure, got nil")
	}
	if !strings.Contains(err.Error(), "connection timed out") {
		t.Errorf("expected error to mention probe failure, got: %v", err)
	}

	// Verify Telegram config was rolled back to initial unmasked secret
	if tgMock.cfg.Secret != "initialsecret0123456789abcdef01" {
		t.Errorf("expected TG secret to be restored to initial secret, got %s", tgMock.cfg.Secret)
	}

	// Active journal must be archived as failed
	if fileExists(coord.journalPath()) {
		t.Errorf("expected active journal to be archived")
	}

	// Recovery should NOT be needed if rollback succeeded cleanly
	req, reason := coord.IsRecoveryRequired()
	if req {
		t.Errorf("recovery unexpectedly needed: %s", reason)
	}
}

func TestExecuteIngressTransaction_FingerprintMismatch_ErrPlanStale(t *testing.T) {
	dataDir := t.TempDir()
	coord := New(dataDir, &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})

	params := IngressTransactionParams{
		TxID:                "test-stale",
		ExpectedFingerprint: "fp-v1",
		FingerprintFunc: func(ctx context.Context) (string, error) {
			return "fp-v2", nil // Mismatch!
		},
	}

	err := coord.ExecuteIngressTransaction(context.Background(), params)
	if err == nil {
		t.Fatalf("expected error on fingerprint mismatch, got nil")
	}
	if !errors.Is(err, ErrPlanStale) {
		t.Errorf("expected ErrPlanStale, got: %v", err)
	}
}

func TestExecuteIngressTransaction_StateProbeError_FailClosed(t *testing.T) {
	dataDir := t.TempDir()
	coord := New(dataDir, &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})

	probeErr := errors.New("stat /opt/etc/init.d/S99telemt failed: permission denied")
	params := IngressTransactionParams{
		TxID:                "test-probe-err",
		ExpectedFingerprint: "fp-v1",
		FingerprintFunc: func(ctx context.Context) (string, error) {
			return "", probeErr
		},
	}

	err := coord.ExecuteIngressTransaction(context.Background(), params)
	if err == nil {
		t.Fatalf("expected error on state probe failure, got nil")
	}
	// Note #4: MUST NOT be ErrPlanStale
	if errors.Is(err, ErrPlanStale) {
		t.Errorf("state probe error must NOT be masked as ErrPlanStale")
	}
	if !strings.Contains(err.Error(), "state probe failed") {
		t.Errorf("expected 'state probe failed' in error, got: %v", err)
	}
}

func TestExecuteIngressTransaction_RollbackFailureSetsRecovery(t *testing.T) {
	dataDir := t.TempDir()
	xrayMock := &mockXray{
		cfg: vlessserver.Config{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
		},
		rollbackError: errors.New("cannot rollback xray: file locked"),
	}
	coord := New(dataDir, xrayMock, &mockDispatcher{}, &mockTgWebProxy{})

	params := IngressTransactionParams{
		TxID: "test-tx-rb-fail",
		Xray: &IngressXrayCandidate{
			Enabled:    true,
			ListenPort: 9008,
		},
		ReadinessProbe: func(ctx context.Context, topo IngressTopology) error {
			return errors.New("probe simulated failure")
		},
	}

	err := coord.ExecuteIngressTransaction(context.Background(), params)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Coordinator must enter recoveryNeeded
	req, reason := coord.IsRecoveryRequired()
	if !req {
		t.Errorf("expected recoveryNeeded to be true when rollback fails")
	}
	if reason == "" {
		t.Errorf("expected non-empty recovery reason")
	}
}
