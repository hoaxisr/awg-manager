package serverwizard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type mockCoordinatorBridge struct {
	lockCount      int
	applyXrayErr   error
	applyTgErr     error
	txParams       serveringress.IngressTransactionParams
	txErr          error
	recoveryNeed   bool
	recoveryReason string
}

func (m *mockCoordinatorBridge) WithIngressLock(txID string, fn func() error) error {
	m.lockCount++
	return fn()
}

func (m *mockCoordinatorBridge) ExecuteIngressTransaction(ctx context.Context, params serveringress.IngressTransactionParams) error {
	m.lockCount++
	m.txParams = params
	if m.txErr != nil {
		return m.txErr
	}
	if params.FingerprintFunc != nil && params.ExpectedFingerprint != "" {
		curFP, err := params.FingerprintFunc(ctx)
		if err != nil {
			return err
		}
		if curFP != params.ExpectedFingerprint {
			return serveringress.ErrPlanStale
		}
	}
	if params.ReadinessProbe != nil {
		if err := params.ReadinessProbe(ctx, serveringress.IngressTopology{}); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockCoordinatorBridge) IsRecoveryRequired() (bool, string) {
	return m.recoveryNeed, m.recoveryReason
}

type mockTestConn struct {
	net.Conn
}

func (m *mockTestConn) Close() error {
	return nil
}

type mockHTTPDoer struct {
	doFn func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	if m.doFn != nil {
		return m.doFn(req)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
	}
	if strings.Contains(req.URL.Path, "xray") || strings.Contains(req.URL.Path, "cdn-bridge") || strings.Contains(req.URL.Path, "xhttp-bridge") {
		resp.Header.Set("X-CDN-Route", "xray")
	} else {
		resp.Header.Set("X-CDN-Route", "tgwebproxy")
	}
	return resp, nil
}

type mockXraySvcFull struct {
	mockXrayReader
	links *vlessserver.ShareLinks
}

func (m *mockXraySvcFull) GenerateLinks(clientID string) (*vlessserver.ShareLinks, error) {
	return m.links, nil
}

type mockTgSvcFull struct {
	mockTgReader
	rev tgwebproxy.RevealData
}

func (m *mockTgSvcFull) RevealSecret() (tgwebproxy.RevealData, error) {
	return m.rev, nil
}

type mockCatalog struct{}

func (m *mockCatalog) ListAll(ctx context.Context) []routing.TunnelEntry {
	return nil
}

func setupTestWizard(t *testing.T) (*WizardService, *mockCoordinatorBridge, *mockXraySvcFull, *mockTgSvcFull, *mockDispReader) {
	xray := &mockXraySvcFull{
		mockXrayReader: mockXrayReader{
			cfg: vlessserver.Config{Enabled: false, ListenPort: 9008},
			st:  vlessserver.Status{Installed: true, Configured: false},
		},
	}
	tg := &mockTgSvcFull{
		mockTgReader: mockTgReader{
			cfg: tgwebproxy.PublicConfig{Enabled: false, ListenPort: 8085},
			st:  tgwebproxy.Status{Installed: true, Running: false},
		},
	}
	disp := &mockDispReader{
		cfg:     cdndispatcher.Config{ListenAddr: ":9009"},
		running: false,
	}
	coord := &mockCoordinatorBridge{}

	egressAdp := egress.NewAdapter(&mockCatalog{})
	recReader := &mockRecReader{required: false}

	preflight := NewPreflightEngine(xray, disp, tg, recReader, egressAdp)
	preflight.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 1000}, nil
	}
	preflight.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	fingerprint := NewFingerprintEngine(xray, disp, tg, recReader, egressAdp)
	procDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(procDir, "net"), 0755)
	_ = os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(""), 0644)
	fingerprint.SetProcDir(procDir)

	planStore := NewPlanStore()
	jobRunner := NewJobRunner()

	svc := NewWizardService(
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
	svc.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return &mockTestConn{}, nil
	})
	svc.SetHTTPDoer(&mockHTTPDoer{})

	return svc, coord, xray, tg, disp
}

func TestWizardService_CapabilitiesAndPreflight(t *testing.T) {
	svc, _, _, _, _ := setupTestWizard(t)
	ctx := context.Background()

	caps := svc.GetCapabilities(ctx, "tgwebproxy")
	if len(caps.Profiles) == 0 || len(caps.Scenarios) == 0 {
		t.Fatalf("expected non-empty profiles and scenarios, got %+v", caps)
	}

	preflight := svc.Preflight(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		DirectHost:   "192.168.1.1",
		DirectPort:   8443,
		ListenPort:   8085,
		TlsDomain:    "gateway.icloud.com",
		CdnProfileID: "cdn_get",
	})
	if !preflight.CanProceed {
		t.Fatalf("expected preflight CanProceed=true, got %+v", preflight.Checks)
	}
	if preflight.Fingerprint == "" {
		t.Errorf("expected non-empty fingerprint in preflight")
	}
}

func TestWizardService_PlanAndApply_HappyPath(t *testing.T) {
	svc, coord, _, _, _ := setupTestWizard(t)
	ctx := context.Background()
	sessionID := "user-session-abc"

	req := WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		DirectHost:   "192.168.1.1",
		DirectPort:   8443,
		ListenPort:   8085,
		PublicDomain: "tg.example.com",
		TlsDomain:    "gateway.icloud.com",
		CdnProfileID: "cdn_get",
	}

	// 1. Create plan
	rec, err := svc.Plan(ctx, sessionID, req)
	if err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}
	if rec.PlanID == "" || rec.StateFingerprint == "" {
		t.Fatalf("invalid plan record: %+v", rec)
	}

	// 2. Apply plan
	jobID, err := svc.Apply(ctx, sessionID, rec.PlanID)
	if err != nil {
		t.Fatalf("failed to apply plan: %v", err)
	}

	// 3. Wait for async job completion
	var jobResp JobStatusResponse
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		jobResp, err = svc.GetJob(ctx, jobID, sessionID)
		if err != nil {
			t.Fatalf("failed to poll job: %v", err)
		}
		if jobResp.Phase == JobPhaseSucceeded || jobResp.Phase == JobPhaseFailed {
			break
		}
	}

	if jobResp.Phase != JobPhaseSucceeded {
		t.Fatalf("expected job Succeeded, got %s (err: %s)", jobResp.Phase, jobResp.Error)
	}
	if !jobResp.ResultAvailable {
		t.Errorf("expected ResultAvailable=true")
	}

	// 4. Reveal credentials
	creds, err := svc.Reveal(ctx, jobID, sessionID)
	if err != nil {
		t.Fatalf("failed to reveal credentials: %v", err)
	}
	if creds.Kind != "tgwebproxy" || creds.TgSecret == "" || creds.DirectLink == "" {
		t.Fatalf("unexpected reveal credentials: %+v", creds)
	}

	if coord.lockCount < 1 {
		t.Errorf("expected at least 1 ingress lock acquisition, got %d", coord.lockCount)
	}
}

func TestWizardService_StaleFingerprintRejection(t *testing.T) {
	svc, _, xray, _, _ := setupTestWizard(t)
	ctx := context.Background()
	sessionID := "user-session-stale"

	req := WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "vpn.example.com",
		Path:         "/cdn-bridge/",
		ClientRemark: "Test Phone",
	}

	rec, err := svc.Plan(ctx, sessionID, req)
	if err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}

	// Mutate state to cause fingerprint drift before apply
	xray.cfg.ListenPort = 9999

	jobID, err := svc.Apply(ctx, sessionID, rec.PlanID)
	if err != nil {
		t.Fatalf("unexpected apply error: %v", err)
	}

	var jobResp JobStatusResponse
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		jobResp, err = svc.GetJob(ctx, jobID, sessionID)
		if err != nil {
			t.Fatalf("failed to poll job: %v", err)
		}
		if jobResp.Phase == JobPhaseSucceeded || jobResp.Phase == JobPhaseFailed {
			break
		}
	}

	if jobResp.Phase != JobPhaseFailed || jobResp.ErrorCode != ErrCodePlanStale {
		t.Fatalf("expected job failed with PLAN_STALE on drifted state, got %+v", jobResp)
	}
}

func TestWizardService_DuplicateApplyRejection(t *testing.T) {
	svc, _, _, _, _ := setupTestWizard(t)
	ctx := context.Background()
	sessionID := "user-session-dup"

	req := WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "vpn.example.com",
		Path:         "/cdn-bridge/",
	}

	rec, err := svc.Plan(ctx, sessionID, req)
	if err != nil {
		t.Fatalf("failed to create plan: %v", err)
	}

	// First apply succeeds
	_, err = svc.Apply(ctx, sessionID, rec.PlanID)
	if err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	// Second apply should fail with ErrOperationInProgress or ErrPlanAlreadyUsed
	_, err = svc.Apply(ctx, sessionID, rec.PlanID)
	if err == nil || (!errors.Is(err, ErrOperationInProgress) && !errors.Is(err, ErrPlanAlreadyUsed)) {
		t.Fatalf("expected ErrOperationInProgress or ErrPlanAlreadyUsed on duplicate apply, got %v", err)
	}
}

func TestReadinessProbe_HTTPRouteIdentity_SharedHostDualPaths(t *testing.T) {
	ctx := context.Background()

	sharedTopo := serveringress.IngressTopology{
		PublicHostname:     "shared.example.com",
		XrayPublicHostname: "shared.example.com",
		XrayEnabled:        true,
		XrayPort:           9008,
		XrayPathPrefix:     "/cdn-bridge",
		TgPublicHostname:   "shared.example.com",
		TgEnabled:          true,
		TgWebPort:          8085,
		TgScenario:         "dual",
		DispatcherPort:     9009,
	}

	tests := []struct {
		name       string
		xrayHeader string
		tgHeader   string
		tgHttpErr  error
		wantErr    bool
		errSubstr  string
	}{
		{
			name:       "BothSucceed",
			xrayHeader: "xray",
			tgHeader:   "tgwebproxy",
			wantErr:    false,
		},
		{
			name:       "XrayHeaderWrong",
			xrayHeader: "invalid-xray",
			tgHeader:   "tgwebproxy",
			wantErr:    true,
			errSubstr:  "unexpected or missing X-CDN-Route header for Xray",
		},
		{
			name:       "XrayHeaderMissing",
			xrayHeader: "",
			tgHeader:   "tgwebproxy",
			wantErr:    true,
			errSubstr:  "unexpected or missing X-CDN-Route header for Xray",
		},
		{
			name:       "TgHeaderWrong",
			xrayHeader: "xray",
			tgHeader:   "invalid-tg",
			wantErr:    true,
			errSubstr:  "unexpected or missing X-CDN-Route header for Telegram",
		},
		{
			name:       "TgHeaderMissing",
			xrayHeader: "xray",
			tgHeader:   "",
			wantErr:    true,
			errSubstr:  "unexpected or missing X-CDN-Route header for Telegram",
		},
		{
			name:       "TgHTTPError",
			xrayHeader: "xray",
			tgHttpErr:  errors.New("connection reset by peer"),
			wantErr:    true,
			errSubstr:  "http get tg probe",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _, _ := setupTestWizard(t)
			svc.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
				return &mockTestConn{}, nil
			})
			svc.SetHTTPDoer(&mockHTTPDoer{
				doFn: func(req *http.Request) (*http.Response, error) {
					if strings.Contains(req.URL.Path, "cdn-bridge") {
						resp := &http.Response{
							StatusCode: http.StatusOK,
							Header:     make(http.Header),
							Body:       io.NopCloser(strings.NewReader("ok")),
						}
						if tc.xrayHeader != "" {
							resp.Header.Set("X-CDN-Route", tc.xrayHeader)
						}
						return resp, nil
					}
					if tc.tgHttpErr != nil {
						return nil, tc.tgHttpErr
					}
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("ok")),
					}
					if tc.tgHeader != "" {
						resp.Header.Set("X-CDN-Route", tc.tgHeader)
					}
					return resp, nil
				},
			})

			probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()

			err := svc.probeTopologyReadiness(probeCtx, sharedTopo)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tc.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
			}
		})
	}
}

func TestReadinessProbe_SocketVsHTTPIndependence(t *testing.T) {
	svc, _, _, _, _ := setupTestWizard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// Socket dialer fails on dispatcher port 9009
	dialErr := errors.New("connection refused on 9009")
	svc.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
		if strings.Contains(address, "9009") {
			return nil, dialErr
		}
		return &mockTestConn{}, nil
	})

	// HTTP doer is configured to return valid 200 OK with correct header
	httpCalled := false
	svc.SetHTTPDoer(&mockHTTPDoer{
		doFn: func(req *http.Request) (*http.Response, error) {
			httpCalled = true
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
			}
			resp.Header.Set("X-CDN-Route", "xray")
			return resp, nil
		},
	})

	topo := serveringress.IngressTopology{
		PublicHostname:     "cdn.example.com",
		XrayPublicHostname: "cdn.example.com",
		XrayEnabled:        true,
		XrayPort:           9008,
		XrayPathPrefix:     "/cdn-bridge",
		DispatcherPort:     9009,
	}

	err := svc.probeTopologyReadiness(ctx, topo)
	if err == nil {
		t.Fatalf("expected socket probe failure, got nil")
	}
	if !strings.Contains(err.Error(), "cdn dispatcher") || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected dispatcher socket not ready error, got: %v", err)
	}
	if httpCalled {
		t.Errorf("expected HTTP probe NOT to be called when socket readiness failed")
	}
}

type mockKeenDNSOperator struct {
	disc serveringress.KeenDNSDiscovery
}

func (m *mockKeenDNSOperator) Discover(ctx context.Context, dispatcherPort int) (serveringress.KeenDNSDiscovery, error) {
	return m.disc, nil
}

func (m *mockKeenDNSOperator) PreparePublication(ctx context.Context, req serveringress.KeenDNSPublicationCandidate) (serveringress.KeenDNSJournalRecord, error) {
	return serveringress.KeenDNSJournalRecord{
		Label:        req.Label,
		FQDN:         req.Label + "." + m.disc.BaseFQDN,
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
	}, nil
}

func (m *mockKeenDNSOperator) ApplyAndVerifyPublication(ctx context.Context, rec *serveringress.KeenDNSJournalRecord) error {
	return nil
}

func (m *mockKeenDNSOperator) RollbackPublication(ctx context.Context, rec serveringress.KeenDNSJournalRecord) error {
	return nil
}

func generateTestCert(t *testing.T, dnsName string, notBefore, notAfter time.Time) (*x509.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        cert,
	}
	return cert, tlsCert, pool
}

func TestWizardService_KeenDNSCertStatusHonestVerification(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	targetFQDN := "connect.example.keenetic.pro"

	// 1. Unit test VerifyLeafAndChainForFQDN: missing, wrong hostname, expired, untrusted, and valid
	validLeaf, validTLSCert, trustedPool := generateTestCert(t, targetFQDN, now.Add(-time.Hour), now.Add(time.Hour))
	wrongHostLeaf, wrongHostTLSCert, wrongHostPool := generateTestCert(t, "other.example.keenetic.pro", now.Add(-time.Hour), now.Add(time.Hour))
	expiredLeaf, _, expiredPool := generateTestCert(t, targetFQDN, now.Add(-2*time.Hour), now.Add(-time.Minute))

	if err := VerifyLeafAndChainForFQDN(targetFQDN, nil, trustedPool, now); err == nil {
		t.Fatalf("expected error for missing peer certificates")
	}
	if err := VerifyLeafAndChainForFQDN(targetFQDN, []*x509.Certificate{wrongHostLeaf}, wrongHostPool, now); err == nil {
		t.Fatalf("expected error for certificate issued to wrong hostname")
	}
	if err := VerifyLeafAndChainForFQDN(targetFQDN, []*x509.Certificate{expiredLeaf}, expiredPool, now); err == nil {
		t.Fatalf("expected error for expired certificate")
	}
	if err := VerifyLeafAndChainForFQDN(targetFQDN, []*x509.Certificate{validLeaf}, x509.NewCertPool(), now); err == nil {
		t.Fatalf("expected error for untrusted root pool")
	}
	if err := VerifyLeafAndChainForFQDN(targetFQDN, []*x509.Certificate{validLeaf}, trustedPool, now); err != nil {
		t.Fatalf("expected valid certificate to pass, got: %v", err)
	}

	// 2. Real TLS listener check via DialAndVerifyFQDNCertificate: wrong host cert -> "invalid", valid cert -> "ready"
	lnWrong, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{wrongHostTLSCert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer lnWrong.Close()
	go func() {
		for {
			c, err := lnWrong.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	stWrong, errWrong := DialAndVerifyFQDNCertificate(ctx, targetFQDN, lnWrong.Addr().String(), wrongHostPool)
	if stWrong == "ready" || stWrong != "invalid" || errWrong == nil {
		t.Fatalf("expected status='invalid' for wrong hostname certificate over TLS, got status=%q err=%v", stWrong, errWrong)
	}

	lnValid, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{validTLSCert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer lnValid.Close()
	go func() {
		for {
			c, err := lnValid.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	stValid, errValid := DialAndVerifyFQDNCertificate(ctx, targetFQDN, lnValid.Addr().String(), trustedPool)
	if stValid != "ready" || errValid != nil {
		t.Fatalf("expected status='ready' for valid certificate over TLS, got status=%q err=%v", stValid, errValid)
	}

	// 3. Even when SSLEnabled=true and RuntimeActive=true in KeenDNS discovery:
	//    - without TLS probe -> KeenDNSCertStatus MUST be "not_checked" (never "ready"!)
	//    - with failing TLS probe (wrong host / missing cert) -> KeenDNSCertStatus MUST be "invalid" (never "ready"!)
	svc, _, _, _, _ := setupTestWizard(t)
	kdMock := &mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "example.keenetic.pro",
			SSLEnabled: true,
			AcmeV2:     true,
			Publications: []serveringress.KeenDNSPublicationView{
				{
					Label:                 "connect",
					FQDN:                  targetFQDN,
					UpstreamHost:          "127.0.0.1",
					UpstreamPort:          9009,
					DomainNDNS:            true,
					Public:                true,
					CompatibleWithIngress: true,
					RuntimeActive:         true,
				},
			},
		},
	}
	svc.SetKeenDNS(kdMock)

	// Also verify plan item description for reuse vs create
	reusePlan, err := svc.Plan(ctx, "sess-cert-1", WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: targetFQDN,
		KeenDNSLabel: "connect",
		Path:         "/cdn-bridge/",
	})
	if err != nil {
		t.Fatalf("Plan reuse: %v", err)
	}
	foundReuse := false
	for _, it := range reusePlan.Plan.Items {
		if it.Target == "keendns_proxy" && it.Action == "keep" {
			foundReuse = true
		}
	}
	if !foundReuse {
		t.Fatalf("expected 'keep' action for existing compatible KeenDNS publication in plan, got %+v", reusePlan.Plan.Items)
	}

	createPlan, err := svc.Plan(ctx, "sess-cert-1", WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "newsub.example.keenetic.pro",
		KeenDNSLabel: "newsub",
		Path:         "/cdn-bridge/",
	})
	if err != nil {
		t.Fatalf("Plan create: %v", err)
	}
	foundCreateSaveWarning := false
	for _, it := range createPlan.Plan.Items {
		if it.Target == "keendns_proxy" && it.Action == "publish" && strings.Contains(it.Description, "system.configuration.save") {
			foundCreateSaveWarning = true
		}
	}
	if !foundCreateSaveWarning {
		t.Fatalf("expected 'publish' plan item to warn about global system.configuration.save, got %+v", createPlan.Plan.Items)
	}

	// Apply when SSLEnabled=true and RuntimeActive=true, but TLS probe is not run -> "not_checked"
	jobID, err := svc.Apply(ctx, "sess-cert-1", reusePlan.PlanID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for i := 0; i < 20; i++ {
		time.Sleep(30 * time.Millisecond)
		st, _ := svc.GetJob(ctx, jobID, "sess-cert-1")
		if st.Phase == JobPhaseSucceeded || st.Phase == JobPhaseFailed {
			break
		}
	}
	creds, err := svc.Reveal(ctx, jobID, "sess-cert-1")
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if creds.KeenDNSCertStatus == "ready" || creds.KeenDNSCertStatus != "not_checked" {
		t.Fatalf("expected KeenDNSCertStatus='not_checked' when cert not probed despite SSLEnabled=true & RuntimeActive=true, got %q", creds.KeenDNSCertStatus)
	}

	// Apply when SSLEnabled=true and RuntimeActive=true, and TLS probe finds wrong-host certificate -> "invalid"
	svc.SetTLSCertProbe(func(probeCtx context.Context, fqdn string) (string, error) {
		return DialAndVerifyFQDNCertificate(probeCtx, fqdn, lnWrong.Addr().String(), wrongHostPool)
	})
	planInvalid, err := svc.Plan(ctx, "sess-cert-2", WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: targetFQDN,
		KeenDNSLabel: "connect",
		Path:         "/cdn-bridge/",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	jobID2, err := svc.Apply(ctx, "sess-cert-2", planInvalid.PlanID)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for i := 0; i < 20; i++ {
		time.Sleep(30 * time.Millisecond)
		st, _ := svc.GetJob(ctx, jobID2, "sess-cert-2")
		if st.Phase == JobPhaseSucceeded || st.Phase == JobPhaseFailed {
			break
		}
	}
	creds2, err := svc.Reveal(ctx, jobID2, "sess-cert-2")
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if creds2.KeenDNSCertStatus == "ready" || creds2.KeenDNSCertStatus != "invalid" {
		t.Fatalf("expected KeenDNSCertStatus='invalid' when cert is for wrong hostname despite SSLEnabled=true & RuntimeActive=true, got %q", creds2.KeenDNSCertStatus)
	}
}

func TestWizard_MTProtoDirectOnlyForbidsKeenDNSAndPlansWANFirewall(t *testing.T) {
	svc, _, _, tg, _ := setupTestWizard(t)
	ctx := context.Background()

	// 1. BuildDesiredConfig rejects KeenDNS 4th-level creation for tgwebproxy + direct_fake_tls
	for _, badReq := range []WizardPlanRequest{
		{
			Kind:         "tgwebproxy",
			Scenario:     "direct_fake_tls",
			DirectHost:   "203.0.113.10",
			DirectPort:   8443,
			TlsDomain:    "www.google.com",
			KeenDNSLabel: "connect",
		},
		{
			Kind:        "tgwebproxy",
			Scenario:    "direct_fake_tls",
			DirectHost:  "203.0.113.10",
			DirectPort:  8443,
			TlsDomain:   "www.google.com",
			KeenDNSMode: "create",
		},
	} {
		if _, err := BuildDesiredConfig(ctx, nil, badReq); err == nil {
			t.Fatalf("expected BuildDesiredConfig to reject KeenDNS for direct_fake_tls request %+v", badReq)
		}
	}

	// 2. BuildDesiredConfig clears stale HTTP/web fields on tgwebproxy + direct_fake_tls
	cleanDesired, err := BuildDesiredConfig(ctx, nil, WizardPlanRequest{
		Kind:          "tgwebproxy",
		Scenario:      "direct_fake_tls",
		ConnectionWay: "external_https",
		OriginType:    "cloud_address",
		OriginHost:    "stale.example.keenetic.link",
		OriginScheme:  "https",
		OriginPort:    443,
		PublicDomain:  "stale-web.example.com",
		DirectHost:    "203.0.113.10",
		DirectPort:    8443,
		TlsDomain:     "www.google.com",
	})
	if err != nil {
		t.Fatalf("BuildDesiredConfig clean direct_fake_tls failed: %v", err)
	}
	if cleanDesired.KeenDNSLabel != "" || cleanDesired.PublicHostname != "" || cleanDesired.OriginHost != "" ||
		cleanDesired.ConnectionWay != "direct" || cleanDesired.OriginType != "public_address" {
		t.Fatalf("expected web-only fields cleared and direct white-IP normalized, got %+v", cleanDesired)
	}

	// 3. Plan for tgwebproxy + direct_fake_tls includes wan_firewall and omits cdn-dispatcher/keendns_proxy
	directPlan, err := svc.Plan(ctx, "sess-mtproto-only", WizardPlanRequest{
		Kind:       "tgwebproxy",
		Scenario:   "direct_fake_tls",
		DirectHost: "203.0.113.10",
		DirectPort: 8443,
		TlsDomain:  "www.google.com",
	})
	if err != nil {
		t.Fatalf("Plan direct_fake_tls failed: %v", err)
	}
	hasWANFirewall := false
	for _, it := range directPlan.Plan.Items {
		if it.Target == "wan_firewall" && it.Action == "enable" && it.NewValue == "tcp/8443" {
			hasWANFirewall = true
		}
		if it.Target == "keendns_proxy" || it.Target == "cdn-dispatcher" {
			t.Fatalf("did not expect web ingress target %q in direct_fake_tls plan: %+v", it.Target, directPlan.Plan.Items)
		}
	}
	if !hasWANFirewall {
		t.Fatalf("expected wan_firewall tcp/8443 item in direct_fake_tls plan, got %+v", directPlan.Plan.Items)
	}

	// 4. Preflight blocks KeenDNS on direct_fake_tls, even before Plan
	preBlocked := svc.Preflight(ctx, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "direct_fake_tls",
		DirectHost:   "203.0.113.10",
		DirectPort:   8443,
		TlsDomain:    "www.google.com",
		KeenDNSMode:  "create",
		KeenDNSLabel: "connect",
	})
	if preBlocked.CanProceed {
		t.Fatalf("expected Preflight CanProceed=false when KeenDNS is passed for direct_fake_tls")
	}

	// 5. KeenDNS IS allowed on dual, cdn_http, xray, and both (including both + direct_fake_tls!)
	kdMock := &mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{State: "supported", BaseFQDN: "example.keenetic.pro"},
	}
	svc.SetKeenDNS(kdMock)

	dualPlan, err := svc.Plan(ctx, "sess-dual-kd", WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		PublicDomain: "tgsub.example.keenetic.pro",
		DirectHost:   "203.0.113.10",
		DirectPort:   9443,
		TlsDomain:    "www.google.com",
		KeenDNSMode:  "create",
		KeenDNSLabel: "tgsub",
	})
	if err != nil {
		t.Fatalf("Plan dual with KeenDNS failed: %v", err)
	}
	var sawDualFW, sawDualKD bool
	for _, it := range dualPlan.Plan.Items {
		if it.Target == "wan_firewall" && it.NewValue == "tcp/9443" {
			sawDualFW = true
		}
		if it.Target == "keendns_proxy" && it.NewValue == "tgsub" {
			sawDualKD = true
		}
	}
	if !sawDualFW || !sawDualKD {
		t.Fatalf("expected both wan_firewall and keendns_proxy in dual plan, got %+v", dualPlan.Plan.Items)
	}

	// both (VLESS web + Telegram direct_fake_tls) with KeenDNS for VLESS MUST succeed
	bothDesired, err := BuildDesiredConfig(ctx, nil, WizardPlanRequest{
		Kind:         "both",
		Scenario:     "direct_fake_tls",
		PublicDomain: "vless.example.keenetic.pro",
		KeenDNSMode:  "create",
		KeenDNSLabel: "vless",
		DirectHost:   "203.0.113.10",
		DirectPort:   8443,
		TlsDomain:    "www.google.com",
	})
	if err != nil {
		t.Fatalf("expected Kind=both + Scenario=direct_fake_tls with KeenDNS for VLESS to succeed: %v", err)
	}
	if bothDesired.KeenDNSLabel != "vless" {
		t.Fatalf("expected KeenDNSLabel 'vless' preserved on both+direct_fake_tls, got %q", bothDesired.KeenDNSLabel)
	}

	// 6. DirectPort validation rejects privileged, reserved, and out-of-range ports in BuildDesiredConfig & Preflight
	for _, badPort := range []int{-5, 22, 80, 443, 1023, 1099, 9009, 51820, 65536} {
		reqBadPort := WizardPlanRequest{
			Kind:       "tgwebproxy",
			Scenario:   "direct_fake_tls",
			DirectHost: "203.0.113.10",
			DirectPort: badPort,
			TlsDomain:  "www.google.com",
		}
		if _, err := BuildDesiredConfig(ctx, nil, reqBadPort); err == nil {
			t.Fatalf("expected BuildDesiredConfig to reject unsafe DirectPort %d", badPort)
		}
		if preBad := svc.Preflight(ctx, reqBadPort); preBad.CanProceed {
			t.Fatalf("expected Preflight CanProceed=false for unsafe DirectPort %d", badPort)
		}
	}

	// 7. KeenDNSMode="existing" is also rejected on tgwebproxy + direct_fake_tls
	existingKDReq := WizardPlanRequest{
		Kind:        "tgwebproxy",
		Scenario:    "direct_fake_tls",
		DirectHost:  "203.0.113.10",
		DirectPort:  8443,
		TlsDomain:   "www.google.com",
		KeenDNSMode: "existing",
	}
	if _, err := BuildDesiredConfig(ctx, nil, existingKDReq); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject KeenDNSMode='existing' for direct_fake_tls")
	}
	if preKD := svc.Preflight(ctx, existingKDReq); preKD.CanProceed {
		t.Fatalf("expected Preflight to block KeenDNSMode='existing' for direct_fake_tls")
	}

	// 8. Private / CGNAT DirectHost emits CheckStatusWarning (direct_host_private_ip) without blocking Plan
	for _, privIP := range []string{"192.168.1.1", "100.64.1.2"} {
		prePriv := svc.Preflight(ctx, WizardPlanRequest{
			Kind:       "tgwebproxy",
			Scenario:   "direct_fake_tls",
			DirectHost: privIP,
			DirectPort: 8443,
			TlsDomain:  "www.google.com",
		})
		if !prePriv.CanProceed || !prePriv.HasWarnings {
			t.Fatalf("expected CanProceed=true and HasWarnings=true for private IP %s, got %+v", privIP, prePriv)
		}
		sawPrivWarn := false
		for _, c := range prePriv.Checks {
			if c.ID == "direct_host_private_ip" && c.Status == CheckStatusWarning {
				sawPrivWarn = true
			}
		}
		if !sawPrivWarn {
			t.Fatalf("expected direct_host_private_ip warning for %s, got %+v", privIP, prePriv.Checks)
		}
	}

	// 9. Verify uses VerifyCategoryFirewall on tg_mtproto_wan_firewall warning
	tg.cfg = tgwebproxy.PublicConfig{
		Enabled:    true,
		Scenario:   "direct_fake_tls",
		DirectHost: "203.0.113.10",
		DirectPort: 70000, // out-of-range forces IsDirectWANPortOpen=false cross-platform
		TlsDomain:  "www.google.com",
		ListenPort: 8085,
	}
	tg.st = tgwebproxy.Status{
		Installed:       true,
		InstalledTelemt: true,
		DirectOnline:    true,
		DirectPort:      70000,
	}
	svc.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return &mockTestConn{}, nil
	})
	vReport, err := svc.Verify(ctx, "tgwebproxy")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var sawFWCategory bool
	for _, c := range vReport.Checks {
		if c.ID == "tg_mtproto_wan_firewall" && c.Status == CheckStatusWarning {
			if c.Category != VerifyCategoryFirewall {
				t.Fatalf("expected VerifyCategoryFirewall (%q), got %q", VerifyCategoryFirewall, c.Category)
			}
			sawFWCategory = true
		}
	}
	if !sawFWCategory {
		t.Fatalf("expected tg_mtproto_wan_firewall warning in report: %+v", vReport.Checks)
	}
}

func TestWizardService_VerifyTelegramGrayIPAndCloudKeenDNS(t *testing.T) {
	svc, _, _, tg, _ := setupTestWizard(t)
	ctx := context.Background()

	svc.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return &mockTestConn{}, nil
	})
	svc.SetLookupHost(func(ctx context.Context, host string) ([]string, error) {
		return []string{"192.0.2.42"}, nil
	})
	tg.st = tgwebproxy.Status{
		Installed:       true,
		InstalledTelemt: true,
		DirectOnline:    true,
		DirectPort:      8443,
	}

	findCheck := func(checks []VerificationCheck, id string) (VerificationCheck, bool) {
		for _, c := range checks {
			if c.ID == id {
				return c, true
			}
		}
		return VerificationCheck{}, false
	}

	// 1. direct_fake_tls + cloud KeenDNS DirectHost + private WAN -> blocked overall
	svc.SetKeenDNS(&mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "dacha.example.keenetic.pro",
			AccessMode: "cloud",
			WANAddress: "10.20.30.40",
			WANPrivate: true,
		},
	})
	tg.cfg = tgwebproxy.PublicConfig{
		Enabled:    true,
		Scenario:   "direct_fake_tls",
		DirectHost: "dacha.example.keenetic.pro",
		DirectPort: 8443,
		TlsDomain:  "www.google.com",
	}
	rep1, err := svc.Verify(ctx, "tgwebproxy")
	if err != nil {
		t.Fatalf("Verify 1 failed: %v", err)
	}
	if rep1.OverallStatus != CheckStatusBlocked {
		t.Fatalf("expected OverallStatus=blocked for cloud KeenDNS DirectHost, got %q (%+v)", rep1.OverallStatus, rep1.Checks)
	}
	dns1, okDNS1 := findCheck(rep1.Checks, "tg_mtproto_dns")
	wan1, okWAN1 := findCheck(rep1.Checks, "tg_mtproto_wan_ip")
	if !okDNS1 || dns1.Status != CheckStatusBlocked || !strings.Contains(dns1.Message, "192.0.2.42") {
		t.Fatalf("expected tg_mtproto_dns blocked with resolved proxy IP, got ok=%v %+v", okDNS1, dns1)
	}
	if !okWAN1 || wan1.Status != CheckStatusWarning {
		t.Fatalf("expected tg_mtproto_wan_ip warning, got ok=%v %+v", okWAN1, wan1)
	}

	// 2. direct_fake_tls + external direct host + private WAN -> warning overall (no false OK)
	tg.cfg.DirectHost = "router.example.com"
	rep2, err := svc.Verify(ctx, "tgwebproxy")
	if err != nil {
		t.Fatalf("Verify 2 failed: %v", err)
	}
	if rep2.OverallStatus != CheckStatusWarning {
		t.Fatalf("expected OverallStatus=warning for private WAN with external host, got %q (%+v)", rep2.OverallStatus, rep2.Checks)
	}
	dns2, okDNS2 := findCheck(rep2.Checks, "tg_mtproto_dns")
	wan2, okWAN2 := findCheck(rep2.Checks, "tg_mtproto_wan_ip")
	if !okDNS2 || dns2.Status != CheckStatusOK {
		t.Fatalf("expected tg_mtproto_dns ok for external host, got ok=%v %+v", okDNS2, dns2)
	}
	if !okWAN2 || wan2.Status != CheckStatusWarning {
		t.Fatalf("expected tg_mtproto_wan_ip warning, got ok=%v %+v", okWAN2, wan2)
	}

	// 3. direct_fake_tls + private IP DirectHost -> tg_mtproto_host warning
	tg.cfg.DirectHost = "10.20.30.40"
	rep3, err := svc.Verify(ctx, "tgwebproxy")
	if err != nil {
		t.Fatalf("Verify 3 failed: %v", err)
	}
	host3, okHost3 := findCheck(rep3.Checks, "tg_mtproto_host")
	if !okHost3 || host3.Status != CheckStatusWarning {
		t.Fatalf("expected tg_mtproto_host warning, got ok=%v %+v", okHost3, host3)
	}

	// 4. direct_fake_tls + direct-mode KeenDNS + public WAN -> tg_mtproto_dns OK, no tg_mtproto_wan_ip
	svc.SetKeenDNS(&mockKeenDNSOperator{
		disc: serveringress.KeenDNSDiscovery{
			State:      "supported",
			BaseFQDN:   "home.example.keenetic.pro",
			AccessMode: "direct",
			WANAddress: "198.51.100.10",
			WANPrivate: false,
		},
	})
	tg.cfg.DirectHost = "home.example.keenetic.pro"
	rep4, err := svc.Verify(ctx, "tgwebproxy")
	if err != nil {
		t.Fatalf("Verify 4 failed: %v", err)
	}
	dns4, okDNS4 := findCheck(rep4.Checks, "tg_mtproto_dns")
	if !okDNS4 || dns4.Status != CheckStatusOK {
		t.Fatalf("expected tg_mtproto_dns OK for direct KeenDNS, got ok=%v %+v", okDNS4, dns4)
	}
	if wan4, hasWAN4 := findCheck(rep4.Checks, "tg_mtproto_wan_ip"); hasWAN4 {
		t.Fatalf("did not expect tg_mtproto_wan_ip on public WAN: %+v", wan4)
	}
}
