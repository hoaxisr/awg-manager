package serveringress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

func TestDispatcherMuxTproxyLoopProtection(t *testing.T) {
	muxPort := testutil.FreePort(t)
	dispPort := testutil.FreePort(t)

	// Wire a deliberate runtime loop through Mux -> endpoint -> Dispatcher -> Mux
	mux := tgwebproxy.NewMux()
	eps := []tgwebproxy.WebProxyEndpoint{
		{
			ID:              "loop-ep",
			Hostname:        "loop.example.com",
			ListenPort:      dispPort, // Deliberately points back into dispatcher!
			Enabled:         true,
			ExternalBackend: true,
		},
	}
	if err := mux.Start(muxPort, eps, "000102030405060708090a0b0c0d0e0f"); err != nil {
		t.Fatalf("mux.Start: %v", err)
	}
	t.Cleanup(func() { _ = mux.Stop() })

	disp := cdndispatcher.NewWithDataDir(t.TempDir(), cdndispatcher.Config{
		Enabled:        true,
		ListenAddr:     fmt.Sprintf("127.0.0.1:%d", dispPort),
		XrayTarget:     "http://127.0.0.1:19999",
		TgTarget:       fmt.Sprintf("http://127.0.0.1:%d", muxPort),
		TgPublicHost:   "loop.example.com",
		PublicHostname: "loop.example.com",
		XrayPathPrefix: "/cdn-bridge",
	})
	if err := disp.Start(); err != nil {
		t.Fatalf("disp.Start: %v", err)
	}
	t.Cleanup(func() { _ = disp.Stop() })

	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", dispPort), nil)
	req.Host = "loop.example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected immediate 508 Loop Detected instead of hang/timeout: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("expected HTTP 508 Loop Detected on dispatcher->mux->dispatcher loop, got %d", resp.StatusCode)
	}
}

func TestDispatcherRejectsSelfTargetBeforeApply(t *testing.T) {
	port := testutil.FreePort(t)
	disp := cdndispatcher.NewWithDataDir(t.TempDir(), cdndispatcher.Config{
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	})
	_, err := disp.PrepareCandidate("tx-self", cdndispatcher.Candidate{
		Enabled: true,
		Config: cdndispatcher.Config{
			Enabled:    true,
			ListenAddr: fmt.Sprintf(":%d", port),
			TgTarget:   fmt.Sprintf("http://127.0.0.1:%d", port),
		},
	})
	if err == nil {
		t.Fatalf("expected PrepareCandidate to reject self-referential TgTarget")
	}
}

func TestExecuteIngressTransaction_CoverUpstreamTracksXrayAndIncompleteRollbackSurfacesErrRecovery(t *testing.T) {
	// 1. Telegram-only mode sets CoverUpstream="" (uses built-in static fallback)
	dataDir := t.TempDir()
	xrayMock := &mockXray{cfg: vlessserver.Config{Enabled: false, ListenAddress: "127.0.0.1", ListenPort: 9008}}
	dispMock := &mockDispatcher{}
	tgMock := &mockTgWebProxy{cfg: tgwebproxy.Config{Secret: "000102030405060708090a0b0c0d0e0f", ListenPort: 8085}}
	coord := New(dataDir, xrayMock, dispMock, tgMock)

	muxPort := testutil.FreePort(t)
	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-tg-only",
		ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     muxPort,
			PublicHostname: "tg.example.com",
			Secret:         "000102030405060708090a0b0c0d0e0f",
		},
	})
	if err != nil {
		t.Fatalf("Telegram-only ExecuteIngressTransaction failed: %v", err)
	}
	if tgMock.candidate.CoverUpstream != "" {
		t.Fatalf("expected empty CoverUpstream in Telegram-only mode (no Xray), got %q", tgMock.candidate.CoverUpstream)
	}

	// 2. Both mode sets CoverUpstream to Xray listener
	xrayPort := testutil.FreePort(t)
	err = coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-both",
		ServerKind: "both",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    xrayPort,
			PublicDomain:  "cdn.example.com",
			Path:          "/cdn-bridge/",
		},
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       tgwebproxy.ScenarioDual,
			DirectHost:     "1.2.3.4",
			DirectPort:     testutil.FreePort(t),
			ListenPort:     muxPort,
			PublicHostname: "tg.example.com",
		},
	})
	if err != nil {
		t.Fatalf("Both ExecuteIngressTransaction failed: %v", err)
	}
	wantUpstream := fmt.Sprintf("http://127.0.0.1:%d", xrayPort)
	if tgMock.candidate.CoverUpstream != wantUpstream {
		t.Fatalf("expected CoverUpstream=%q in both mode, got %q", wantUpstream, tgMock.candidate.CoverUpstream)
	}

	// 3. Reject conflicting Xray and Telegram ports before staging
	err = coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-port-conflict",
		ServerKind: "both",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    muxPort,
			PublicDomain:  "cdn.example.com",
			Path:          "/cdn-bridge/",
		},
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     muxPort,
			PublicHostname: "tg.example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected port conflict rejection before commit, got: %v", err)
	}

	// 4. ReadinessProbe failure + rollback failure MUST surface ErrRecoveryRequired (never swallow rbErr!)
	tgMockFailRB := &mockTgWebProxy{
		cfg:           tgMock.cfg,
		rollbackError: errors.New("mux stop failed on rollback"),
	}
	coordRB := New(t.TempDir(), xrayMock, dispMock, tgMockFailRB)
	err = coordRB.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-rb-err",
		ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     muxPort,
			PublicHostname: "tg.example.com",
		},
		ReadinessProbe: func(ctx context.Context, topo IngressTopology) error {
			return errors.New("probe timed out")
		},
	})
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected returned error to wrap ErrRecoveryRequired when rollback fails, got: %v", err)
	}
	if !strings.Contains(err.Error(), "mux stop failed on rollback") {
		t.Fatalf("expected returned error to preserve rollback error detail, got: %v", err)
	}
	_ = httptest.NewRequest
}
