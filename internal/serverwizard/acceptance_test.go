package serverwizard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/cdn"
	"github.com/hoaxisr/awg-manager/internal/serverwizard/egress"
	"github.com/hoaxisr/awg-manager/internal/sys/procnet"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type acceptanceXray struct {
	cfg            vlessserver.Config
	oldCfg         vlessserver.Config
	st             vlessserver.Status
	builtinEnabled bool
	prepareCalls   []string
	commitCalls    []string
	finalizeCalls  []string
	rollbackCalls  []string
	preparedCfg    *vlessserver.Config
	committedCfg   *vlessserver.Config
}

func (m *acceptanceXray) GetConfig() vlessserver.Config { return m.cfg }
func (m *acceptanceXray) GetStatus() vlessserver.Status { return m.st }
func (m *acceptanceXray) BuiltinEnabled() bool          { return m.builtinEnabled }
func (m *acceptanceXray) GenerateLinks(clientID string) (*vlessserver.ShareLinks, error) {
	return &vlessserver.ShareLinks{
		VlessURL: "vless://" + clientID + "@cdn.example.com:443",
		UUID:     clientID,
	}, nil
}
func (m *acceptanceXray) PrepareCandidate(txID string, candidate vlessserver.Config) (string, error) {
	m.prepareCalls = append(m.prepareCalls, txID)
	m.oldCfg = m.cfg
	m.preparedCfg = &candidate
	return "cand-" + txID, nil
}
func (m *acceptanceXray) CommitPrepared(txID string) error {
	m.commitCalls = append(m.commitCalls, txID)
	if m.preparedCfg != nil {
		m.cfg = *m.preparedCfg
		m.committedCfg = m.preparedCfg
	}
	return nil
}
func (m *acceptanceXray) FinalizePrepared(txID string) error {
	m.finalizeCalls = append(m.finalizeCalls, txID)
	m.preparedCfg = nil
	return nil
}
func (m *acceptanceXray) RollbackPrepared(txID string) error {
	m.rollbackCalls = append(m.rollbackCalls, txID)
	m.cfg = m.oldCfg
	m.preparedCfg = nil
	return nil
}
func (m *acceptanceXray) Restart() error { return nil }

type acceptanceDispatcher struct {
	cfg           cdndispatcher.Config
	oldCfg        cdndispatcher.Config
	oldRunning    bool
	candidate     cdndispatcher.Candidate
	running       bool
	commitErr     error
	rollbackCalls []string
}

func (m *acceptanceDispatcher) GetConfig() cdndispatcher.Config { return m.cfg }
func (m *acceptanceDispatcher) IsRunning() bool                 { return m.running }
func (m *acceptanceDispatcher) Start() error {
	m.running = true
	return nil
}
func (m *acceptanceDispatcher) Stop() error {
	m.running = false
	return nil
}
func (m *acceptanceDispatcher) ApplyConfig(cfg cdndispatcher.Config) error {
	m.cfg = cfg
	return nil
}
func (m *acceptanceDispatcher) Reconfigure(cfg cdndispatcher.Config) error {
	m.cfg = cfg
	return nil
}
func (m *acceptanceDispatcher) PrepareCandidate(txID string, candidate cdndispatcher.Candidate) (string, error) {
	m.oldCfg = m.cfg
	m.oldRunning = m.running
	m.candidate = candidate
	return txID, nil
}
func (m *acceptanceDispatcher) CommitPrepared(txID string) error {
	if m.commitErr != nil {
		return m.commitErr
	}
	m.cfg = m.candidate.Config
	m.running = m.candidate.Enabled
	return nil
}
func (m *acceptanceDispatcher) RollbackPrepared(txID string) error {
	m.rollbackCalls = append(m.rollbackCalls, txID)
	m.cfg = m.oldCfg
	m.running = m.oldRunning
	return nil
}
func (m *acceptanceDispatcher) FinalizePrepared(txID string) error {
	return nil
}

type acceptanceTg struct {
	cfg           tgwebproxy.Config
	oldCfg        tgwebproxy.Config
	candidate     tgwebproxy.Config
	st            tgwebproxy.Status
	updateCfg     *tgwebproxy.Config
	rollbackCalls []string
}

func (m *acceptanceTg) GetConfig() tgwebproxy.PublicConfig {
	return tgwebproxy.PublicConfig{
		Enabled:        m.cfg.Enabled,
		Scenario:       m.cfg.Scenario,
		ListenPort:     m.cfg.ListenPort,
		PublicHostname: m.cfg.PublicHostname,
		DirectPort:     m.cfg.DirectPort,
		DirectHost:     m.cfg.DirectHost,
		TlsDomain:      m.cfg.TlsDomain,
		CarrierMode:    m.cfg.CarrierMode,
		UpstreamDevice: m.cfg.UpstreamDevice,
	}
}
func (m *acceptanceTg) GetInternalConfig() tgwebproxy.Config { return m.cfg }
func (m *acceptanceTg) GetStatus() tgwebproxy.Status         { return m.st }
func (m *acceptanceTg) ApplyManagedIngress(cfg tgwebproxy.ManagedIngressConfig) error {
	return nil
}
func (m *acceptanceTg) UpdateConfig(cfg tgwebproxy.Config) error {
	m.cfg = cfg
	m.updateCfg = &cfg
	return nil
}
func (m *acceptanceTg) PrepareCandidate(txID string, candidate tgwebproxy.Config) (string, error) {
	m.oldCfg = m.cfg
	m.candidate = candidate
	return txID, nil
}
func (m *acceptanceTg) CommitPrepared(txID string) error {
	m.cfg = m.candidate
	m.updateCfg = &m.candidate
	return nil
}
func (m *acceptanceTg) RollbackPrepared(txID string) error {
	m.rollbackCalls = append(m.rollbackCalls, txID)
	m.cfg = m.oldCfg
	return nil
}
func (m *acceptanceTg) FinalizePrepared(txID string) error {
	return nil
}
func (m *acceptanceTg) RevealSecret() (tgwebproxy.RevealData, error) {
	return tgwebproxy.RevealData{
		Secret:     "11223344556677889900aabbccddeeff",
		MtproxyURL: "tg://proxy?server=test.com&port=8443",
		TgURL:      "tg://webproxy?server=cdn.com&secret=123",
	}, nil
}

func setupAcceptanceCoordinatorAndWizard(t *testing.T) (
	*WizardService,
	*serveringress.Coordinator,
	*acceptanceXray,
	*acceptanceTg,
	*acceptanceDispatcher,
	*FingerprintEngine,
) {
	dataDir := t.TempDir()
	procDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(procDir, "net"), 0755)
	_ = os.WriteFile(filepath.Join(procDir, "net", "tcp"), []byte(""), 0644)

	xray := &acceptanceXray{
		cfg: vlessserver.Config{Enabled: false, ListenPort: 9008, OutboundMode: "socks"},
		st:  vlessserver.Status{Installed: true, Configured: false},
	}
	tg := &acceptanceTg{
		cfg: tgwebproxy.Config{Enabled: false, ListenPort: 8085, DirectPort: 8443},
		st:  tgwebproxy.Status{Installed: true, Running: false},
	}
	disp := &acceptanceDispatcher{
		cfg: cdndispatcher.Config{ListenAddr: ":9009"},
	}

	coord := serveringress.New(dataDir, xray, disp, tg)
	coord.SetProcDir(procDir)

	egressAdp := egress.NewAdapter(&mockCatalog{})
	egressAdp.SetFircStateProvider(func(ctx context.Context) (bool, string) {
		return true, ""
	})
	recReader := &mockRecReader{required: false}

	preflight := NewPreflightEngine(xray, disp, tg, recReader, egressAdp)
	preflight.statFn = func(path string) (os.FileInfo, error) {
		return mockFileInfo{name: path, size: 1000}, nil
	}
	preflight.findProcFn = func(procDir, addr string, port int) (procnet.ListenerLookup, error) {
		return procnet.ListenerLookup{SocketFound: false}, nil
	}

	fingerprint := NewFingerprintEngine(xray, disp, tg, recReader, egressAdp)
	fingerprint.SetProcDir(procDir)
	fingerprint.SetInitScripts([]string{})

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

	return svc, coord, xray, tg, disp, fingerprint
}

func TestWizardApplyRealCoordinatorDoesNotDeadlock(t *testing.T) {
	svc, coord, xray, _, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	planRec, err := svc.Plan(ctx, "session-deadlock", WizardPlanRequest{
		Kind:         "xray",
		DeviceType:   "phone",
		PublicDomain: "cdn.example.com",
		Path:         "/cdn-bridge/",
		Mode:         "xhttp_get",
		ClientRemark: "iPhone Test",
	})
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}

	jobID, err := svc.Apply(ctx, "session-deadlock", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply initiation failed: %v", err)
	}

	// Poll job status until completion or timeout
	completed := false
	var finalJob JobStatusResponse
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("DEADLOCK DETECTED: job execution did not finish within timeout")
		case <-time.After(50 * time.Millisecond):
			job, err := svc.GetJob(ctx, jobID, "session-deadlock")
			if err != nil {
				t.Fatalf("failed to get job: %v", err)
			}
			if job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed {
				finalJob = job
				completed = true
				break
			}
		}
		if completed {
			break
		}
	}

	if finalJob.Phase != JobPhaseSucceeded {
		t.Fatalf("expected job Succeeded without deadlock, got %s: %s", finalJob.Phase, finalJob.Error)
	}

	// Verify coordinator executed the candidate commits
	if len(xray.commitCalls) != 1 {
		t.Errorf("expected 1 commitCall on xray, got %d", len(xray.commitCalls))
	}
	if len(xray.finalizeCalls) != 1 {
		t.Errorf("expected 1 finalizeCall on xray, got %d", len(xray.finalizeCalls))
	}
	if !disp.running {
		t.Errorf("expected dispatcher to be running after successful apply")
	}

	// Verify no stale lock remains
	locked := false
	err = coord.WithIngressLock("check-lock", func() error {
		locked = true
		return nil
	})
	if err != nil || !locked {
		t.Errorf("failed to acquire lock after apply finished: %v", err)
	}
}

func TestWizardFingerprintRevalidatedUnderMutationLock(t *testing.T) {
	svc, _, xray, _, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// 1. Create a plan based on initial state
	planRec, err := svc.Plan(ctx, "session-stale", WizardPlanRequest{
		Kind:         "xray",
		DeviceType:   "phone",
		PublicDomain: "cdn.example.com",
		Path:         "/cdn-bridge/",
		Mode:         "xhttp_get",
		ClientRemark: "iPhone Test",
	})
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}

	// 2. Mutate system state externally before apply
	xray.cfg.ListenPort = 9999

	// 3. Apply the plan - coordinator must detect fingerprint mismatch under mutation lock
	jobID, err := svc.Apply(ctx, "session-stale", planRec.PlanID)
	if err != nil {
		// Stale error may be returned synchronously or asynchronously via job runner
		if !errors.Is(err, ErrPlanStale) && !strings.Contains(err.Error(), "stale") {
			t.Fatalf("unexpected synchronous error: %v", err)
		}
		return
	}

	// Poll job runner to ensure asynchronous failure
	var finalJob JobStatusResponse
	completed := false
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		job, err := svc.GetJob(ctx, jobID, "session-stale")
		if err == nil && (job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed) {
			finalJob = job
			completed = true
			break
		}
	}

	if !completed {
		t.Fatalf("job did not complete")
	}

	if finalJob.Phase != JobPhaseFailed {
		t.Fatalf("expected job to fail due to stale fingerprint, got %s", finalJob.Phase)
	}

	if !strings.Contains(strings.ToLower(finalJob.Error), "stale") &&
		!strings.Contains(strings.ToLower(finalJob.Error), "устарел") &&
		!strings.Contains(strings.ToLower(finalJob.Error), "fingerprint") {
		t.Errorf("expected error message to indicate stale plan or fingerprint mismatch, got: %s", finalJob.Error)
	}

	// Ensure no candidate commit occurred
	if len(xray.commitCalls) > 0 {
		t.Errorf("expected 0 commit calls on stale plan, got %d", len(xray.commitCalls))
	}
}

func sha256HexLower(s string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(sum[:])
}

func TestNoPersonalDomainsInDefaults(t *testing.T) {
	// SHA-256 digests of forbidden personal labels, hosts, IPs, and real UUID credentials
	// (hashed so that the guard test itself never embeds personal identifiers in plain text).
	forbiddenTokenHashes := map[string]string{
		"b7dcd0806bc310881394ba5c0102173c79b76a7237c5ed55819b2893237751a0": "personal-domain-label-1",
		"4c9aa45ac2c408594240df0eaf9cb6148208610a61644d9f1373d146c2b0f470": "personal-domain-label-2",
		"2c07558e125f528d8fcfd20788402fff50656c6515a5a61823c482e34eb3f7a8": "personal-domain-label-3",
		"4f22b386fc8405c6dd964b29cd228d8d582af15ff8ee165b24b5f154067441e4": "real-vless-uuid-credential",
		"3d29466ac54cbddf9beb5948ad1004c3b8886b9a13fa5d97e8d1281f3df1a6c5": "real-vmess-uuid-credential",
		"e57d5d34d6570fab56a7c0dbcc6863031d127d4b6cd7f5e00996c2cce153e083": "personal-external-ip-1",
		"ef13930405f7c5d13eabbd8a09fed1eb270b18764cc60e2df7504e6e00b87711": "personal-external-ip-2",
		"bd6cc3fff7ee2996d1380d69cbe4636815c608f100c0baf0ddbace4e471dc3cc": "real-provider-endpoint",
	}

	tokenRe := regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]{2,}`)
	subLabelRe := regexp.MustCompile(`[A-Za-z0-9-]+`)
	type namedSecretPattern struct {
		name string
		re   *regexp.Regexp
	}
	secretPatterns := []namedSecretPattern{
		{name: "pem-private-key", re: regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----\r?\n[A-Za-z0-9+/=]{32,}`)},
		{name: "telegram-bot-token", re: regexp.MustCompile(`\b[0-9]{8,10}:AA[A-Za-z0-9_-]{33}\b`)},
		{name: "github-pat", re: regexp.MustCompile(`\bghp_[A-Za-z0-9]{36}\b`)},
		{name: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
		{name: "openai-api-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}\b`)},
	}
	allowedRedactionTests := map[string]bool{
		"internal/api/system_ai_test.go": true,
	}

	lineNumberAtOffset := func(text string, offset int) int {
		if offset <= 0 {
			return 1
		}
		if offset > len(text) {
			offset = len(text)
		}
		return 1 + strings.Count(text[:offset], "\n")
	}

	checkText := func(sourceName, text string, checkSecretRe bool) {
		for _, loc := range tokenRe.FindAllStringIndex(text, -1) {
			tok := text[loc[0]:loc[1]]
			trimmed := strings.Trim(tok, "._-")
			fp := sha256HexLower(trimmed)
			if kind, bad := forbiddenTokenHashes[fp]; bad {
				lineNo := lineNumberAtOffset(text, loc[0])
				t.Errorf("%s:%d contains forbidden %s token (sha256_prefix=%s)", sourceName, lineNo, kind, fp[:12])
			}
			for _, label := range subLabelRe.FindAllString(trimmed, -1) {
				lfp := sha256HexLower(label)
				if kind, bad := forbiddenTokenHashes[lfp]; bad {
					lineNo := lineNumberAtOffset(text, loc[0])
					t.Errorf("%s:%d contains forbidden %s label (sha256_prefix=%s)", sourceName, lineNo, kind, lfp[:12])
				}
			}
		}
		if checkSecretRe {
			for _, sp := range secretPatterns {
				if loc := sp.re.FindStringIndex(text); loc != nil {
					m := text[loc[0]:loc[1]]
					lineNo := lineNumberAtOffset(text, loc[0])
					mfp := sha256HexLower(m)
					t.Errorf("%s:%d contains secret pattern match rule=%s (sha256_prefix=%s)", sourceName, lineNo, sp.name, mfp[:12])
				}
			}
		}
	}

	// 1. Check tgwebproxy default TLS domain
	if tgwebproxy.DefaultTlsDomain == "ya.ru" {
		t.Errorf("tgwebproxy.DefaultTlsDomain contains ya.ru domain: %s", tgwebproxy.DefaultTlsDomain)
	}
	checkText("tgwebproxy.DefaultTlsDomain", tgwebproxy.DefaultTlsDomain, true)

	// 2. Check cdn default profiles
	for _, p := range cdn.ListProfiles() {
		checkText("cdn.Profile("+p.ID+")", p.Description+"\n"+p.Instructions, true)
	}

	// 3. Scan tracked and release-bound source/test/frontend/script directories
	repoRoot := filepath.Clean("../..")
	scanDirs := []string{
		"cmd",
		"internal",
		"frontend/src",
		"frontend/scripts",
		"scripts",
		"entware",
		"c-core",
	}
	validExts := map[string]bool{
		".go": true, ".svelte": true, ".ts": true, ".js": true, ".mjs": true,
		".json": true, ".yaml": true, ".yml": true, ".sh": true, ".py": true,
		".c": true, ".h": true,
	}

	for _, relDir := range scanDirs {
		absDir := filepath.Join(repoRoot, filepath.FromSlash(relDir))
		_ = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			relPath, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(relPath)
			if d.IsDir() {
				base := d.Name()
				if base == ".git" || base == "node_modules" || base == "vendor" || base == "third_party" ||
					base == "build" || base == "dist" || base == "__pycache__" || base == "out" || base == "paraglide" ||
					strings.HasPrefix(base, "out-") || relSlash == "scripts/diag" || relSlash == "internal/presets/decompiled" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if !validExts[ext] && !strings.HasPrefix(relSlash, "entware/control/") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			checkSecretRe := !allowedRedactionTests[relSlash]
			checkText(relSlash, string(content), checkSecretRe)
			return nil
		})
	}
}

type mockCatalogEntries struct {
	entries []routing.TunnelEntry
}

func (m *mockCatalogEntries) ListAll(ctx context.Context) []routing.TunnelEntry {
	return m.entries
}

func TestSharedIngressTopologyUnion(t *testing.T) {
	svc, _, xray, tg, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// 1. First setup Xray server
	xrayReq := WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "xray.example.org",
		Path:         "/xray-bridge/",
		ListenPort:   9008,
		CdnProfileID: "cdn_get",
		ClientRemark: "Client-Xray",
	}
	planXray, err := svc.Plan(ctx, "session-union", xrayReq)
	if err != nil {
		t.Fatalf("Plan xray failed: %v", err)
	}
	jobIDXray, err := svc.Apply(ctx, "session-union", planXray.PlanID)
	if err != nil {
		t.Fatalf("Apply xray failed: %v", err)
	}

	// Wait for Xray job
	for i := 0; i < 40; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobIDXray, "session-union")
		if job.Phase == JobPhaseSucceeded {
			break
		}
	}
	if xray.cfg.PublicDomain != "xray.example.org" || xray.cfg.ListenPort != 9008 {
		t.Fatalf("xray config not applied properly: %+v", xray.cfg)
	}

	// 2. Now setup Telegram in dual mode
	tgReq := WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "dual",
		DirectHost:   "tg-direct.example.org",
		DirectPort:   8443,
		ListenPort:   8085,
		PublicDomain: "tg-cdn.example.org",
		TlsDomain:    "gateway.icloud.com",
		CdnProfileID: "cdn_get",
	}
	planTG, err := svc.Plan(ctx, "session-union", tgReq)
	if err != nil {
		t.Fatalf("Plan tg failed: %v", err)
	}
	jobIDTG, err := svc.Apply(ctx, "session-union", planTG.PlanID)
	if err != nil {
		t.Fatalf("Apply tg failed: %v", err)
	}

	// Wait for TG job
	for i := 0; i < 40; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobIDTG, "session-union")
		if job.Phase == JobPhaseSucceeded {
			break
		}
	}

	// 3. Verify union: Xray settings must NOT be overwritten!
	if xray.cfg.PublicDomain != "xray.example.org" || xray.cfg.ListenPort != 9008 {
		t.Errorf("Telegram setup corrupted Xray config! Got %+v", xray.cfg)
	}
	// Telegram settings must be applied
	if tg.cfg.PublicHostname != "tg-cdn.example.org" || tg.cfg.DirectPort != 8443 || tg.cfg.ListenPort != 8085 {
		t.Errorf("Telegram config not applied properly: %+v", tg.cfg)
	}
	// Dispatcher config must maintain routes for both
	if disp.cfg.XrayPublicHost != "xray.example.org" && disp.cfg.PublicHostname != "xray.example.org" {
		t.Errorf("expected disp Xray hostname xray.example.org, got %s", disp.cfg.XrayPublicHost)
	}
	if disp.cfg.TgPublicHost != "tg-cdn.example.org" && disp.cfg.PublicHostname != "tg-cdn.example.org" {
		t.Errorf("expected disp TG hostname tg-cdn.example.org, got %s", disp.cfg.TgPublicHost)
	}
}

func TestStrictCDNProfileModeMatrix(t *testing.T) {
	cat := &mockCatalog{}
	adp := egress.NewAdapter(cat)
	ctx := context.Background()

	// 1. Valid: cdn_get + xhttp_get
	cfg, err := BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_get",
		Mode:         "xhttp_get",
	})
	if err != nil || cfg.Transport != "xhttp" || cfg.UplinkMethod != "GET" {
		t.Fatalf("expected cdn_get + xhttp_get to succeed, got %+v, %v", cfg, err)
	}

	// 2. Valid: cdn_get + "" (default)
	cfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_get",
	})
	if err != nil || cfg.Transport != "xhttp" || cfg.UplinkMethod != "GET" {
		t.Fatalf("expected cdn_get default to succeed, got %+v, %v", cfg, err)
	}

	// 3. Invalid (C-only): cdn_ws + ws -> REJECT with ErrUnsupportedTransport
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_ws",
		Mode:         "ws",
	})
	if !errors.Is(err, vlessserver.ErrUnsupportedTransport) {
		t.Fatalf("expected cdn_ws + ws to be rejected with ErrUnsupportedTransport, got %v", err)
	}

	// 4. Valid: cdn_full + xhttp_get
	cfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_full",
		Mode:         "xhttp_get",
	})
	if err != nil || cfg.Transport != "xhttp" {
		t.Fatalf("expected cdn_full + xhttp_get to succeed, got %+v, %v", cfg, err)
	}

	// 5. Invalid (C-only): cdn_full + ws -> REJECT with ErrUnsupportedTransport
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_full",
		Mode:         "ws",
	})
	if !errors.Is(err, vlessserver.ErrUnsupportedTransport) {
		t.Fatalf("expected cdn_full + ws to be rejected with ErrUnsupportedTransport, got %v", err)
	}

	// 5b. Invalid (C-only): PreserveTransport with RawTransport="ws" -> REJECT with ErrUnsupportedTransport
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:              "xray",
		PublicDomain:      "cdn.example.org",
		CdnProfileID:      "cdn_get",
		PreserveTransport: true,
		RawTransport:      "ws",
	})
	if !errors.Is(err, vlessserver.ErrUnsupportedTransport) {
		t.Fatalf("expected PreserveTransport with RawTransport=ws to be rejected, got %v", err)
	}

	// 6. Invalid: cdn_get + ws -> REJECT
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_get",
		Mode:         "ws",
	})
	if err == nil {
		t.Fatalf("expected cdn_get + ws to be rejected")
	}

	// 7. Invalid: cdn_ws + xhttp_get -> REJECT
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_ws",
		Mode:         "xhttp_get",
	})
	if err == nil {
		t.Fatalf("expected cdn_ws + xhttp_get to be rejected")
	}

	// 8. Invalid: unknown profile -> REJECT
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "xray",
		PublicDomain: "cdn.example.org",
		CdnProfileID: "cdn_post",
	})
	if err == nil {
		t.Fatalf("expected unknown profile cdn_post to be rejected")
	}
}

func TestEgressResolutionAndApplication(t *testing.T) {
	cat := &mockCatalogEntries{
		entries: []routing.TunnelEntry{
			{ID: "Wireguard0", Iface: "nwg1", Available: true, Status: "running"},
			{ID: "Degraded0", Iface: "nwg2", Available: false, Status: "down"},
		},
	}
	adp := egress.NewAdapter(cat)
	adp.SetFircStateProvider(func(ctx context.Context) (bool, string) {
		return true, ""
	})
	adp.SetDialTimeout(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return &mockTestConn{}, nil
	})
	ctx := context.Background()

	// Telegram egress tests
	// 1. Telegram direct -> empty UpstreamDevice and OutboundInterface (NO nwg1!)
	cfg, err := BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "tgwebproxy",
		Scenario:       "dual",
		DirectHost:     "tg.example.org",
		PublicDomain:   "cdn.example.org",
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "direct",
	})
	if err != nil {
		t.Fatalf("BuildDesiredConfig failed for telegram direct: %v", err)
	}
	if cfg.UpstreamDevice != "" || cfg.OutboundInterface != "" || cfg.OutboundMode != "direct" {
		t.Fatalf("expected empty UpstreamDevice and direct mode, got: %+v", cfg)
	}

	// 2. Telegram interface -> Wireguard0 -> nwg1
	cfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "tgwebproxy",
		Scenario:       "dual",
		DirectHost:     "tg.example.org",
		PublicDomain:   "cdn.example.org",
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "Wireguard0",
	})
	if err != nil || cfg.UpstreamDevice != "nwg1" || cfg.OutboundInterface != "nwg1" {
		t.Fatalf("expected nwg1 for Wireguard0, got: %+v, %v", cfg, err)
	}

	// 3. Telegram socks -> REJECT
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "tgwebproxy",
		Scenario:       "dual",
		DirectHost:     "tg.example.org",
		PublicDomain:   "cdn.example.org",
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "mihomo:1099",
	})
	if err == nil {
		t.Fatalf("expected telegram with SOCKS egress to fail")
	}

	// 4. Telegram degraded tunnel -> REJECT
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "tgwebproxy",
		Scenario:       "dual",
		DirectHost:     "tg.example.org",
		PublicDomain:   "cdn.example.org",
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "Degraded0",
	})
	if err == nil {
		t.Fatalf("expected telegram with degraded tunnel to fail")
	}

	// Xray egress tests
	// 5. Xray direct -> OutboundMode: "direct", OutboundInterface: ""
	xCfg, err := BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.org",
		UpstreamDevice: "direct",
	})
	if err != nil || xCfg.OutboundMode != "direct" || xCfg.OutboundInterface != "" {
		t.Fatalf("expected xray direct outbound, got: %+v, %v", xCfg, err)
	}

	// 6. Xray FIRC router outbound -> OutboundMode: "router", UpstreamDevice: "firc:router"
	xCfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.org",
		UpstreamDevice: "firc:router",
	})
	if err != nil || xCfg.OutboundMode != "router" || xCfg.UpstreamDevice != "firc:router" {
		t.Fatalf("expected xray firc:router outbound, got: %+v, %v", xCfg, err)
	}

	// 6b. Xray legacy mihomo:1099 -> REJECT for new configurations (A1/A2)
	_, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.org",
		UpstreamDevice: "mihomo:1099",
	})
	if err == nil {
		t.Fatalf("expected xray with legacy mihomo:1099 to be rejected for new configurations")
	}

	// 7. Xray interface -> OutboundMode: "interface", OutboundInterface: "nwg1"
	xCfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.org",
		UpstreamDevice: "Wireguard0",
	})
	if err != nil || xCfg.OutboundMode != "interface" || xCfg.OutboundInterface != "nwg1" {
		t.Fatalf("expected xray interface outbound, got: %+v, %v", xCfg, err)
	}
}

func TestCancellationBoundarySynchronized(t *testing.T) {
	runner := NewJobRunner()
	planStore := NewPlanStore()

	// 1. Job in Preparing can be cancelled
	_, cancel1 := context.WithCancel(context.Background())
	job1 := runner.CreateJobWithID("job-cancel-1", "xray", "session-1", cancel1)
	planRec := planStore.SavePlan(WizardPlanRequest{Kind: "xray"}, DesiredWizardConfig{ServerKind: "xray"}, "session-1", ChangePlan{}, "fp1", DefaultPlanTTL)
	_, err := planStore.Reserve(planRec.PlanID, "session-1", job1.ID)
	if err != nil {
		t.Fatalf("failed to reserve plan: %v", err)
	}

	runner.UpdateStep(job1.ID, JobPhasePreparing, 20, "Preparing...")
	err = runner.CancelJob(job1.ID, "session-1")
	if err != nil {
		t.Fatalf("expected cancel to succeed in preparing phase: %v", err)
	}
	if !runner.IsCancelRequested(job1.ID) {
		t.Errorf("expected cancelRequested=true")
	}
	runner.MarkCancelled(job1.ID)
	_ = planStore.Release(planRec.PlanID, job1.ID)

	// Verify plan was released and can be re-reserved
	_, err = planStore.Reserve(planRec.PlanID, "session-1", "job-new")
	if err != nil {
		t.Fatalf("expected released plan to be re-reservable: %v", err)
	}

	// 2. Job in Committing (point of no return) CANNOT be cancelled
	_, cancel2 := context.WithCancel(context.Background())
	job2 := runner.CreateJobWithID("job-cancel-2", "xray", "session-1", cancel2)
	runner.SetPhaseNonCancellable(job2.ID, JobPhaseCommitting, 80, "Committing...")

	err = runner.CancelJob(job2.ID, "session-1")
	if !errors.Is(err, ErrCannotCancel) {
		t.Fatalf("expected ErrCannotCancel in Committing phase, got %v", err)
	}
	status, _ := runner.GetJob(job2.ID, "session-1")
	if status.Phase != JobPhaseCommitting {
		t.Errorf("job phase should remain Committing, got %s", status.Phase)
	}
}

func TestTelegramScenariosAppliedCorrectly(t *testing.T) {
	cat := &mockCatalog{}
	adp := egress.NewAdapter(cat)
	ctx := context.Background()

	// 1. direct_fake_tls without TlsDomain must FAIL
	_, err := BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:       "tgwebproxy",
		Scenario:   "direct_fake_tls",
		DirectHost: "tg.example.org",
	})
	if err == nil {
		t.Fatalf("expected direct_fake_tls without TlsDomain to fail")
	}

	// 2. direct_fake_tls with TlsDomain succeeds
	cfg, err := BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:       "tgwebproxy",
		Scenario:   "direct_fake_tls",
		DirectHost: "tg.example.org",
		TlsDomain:  "gateway.icloud.com",
	})
	if err != nil {
		t.Fatalf("expected direct_fake_tls with TlsDomain to succeed: %v", err)
	}
	if cfg.TlsDomain != "gateway.icloud.com" || cfg.DirectPort != 8443 {
		t.Fatalf("unexpected direct_fake_tls config: %+v", cfg)
	}

	// 3. cdn_http without TlsDomain succeeds (TLS domain not needed for CDN HTTP)
	cfg, err = BuildDesiredConfig(ctx, adp, WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		PublicDomain: "cdn.example.org",
	})
	if err != nil {
		t.Fatalf("expected cdn_http to succeed without TlsDomain: %v", err)
	}
	if cfg.TlsDomain != "" {
		t.Fatalf("expected empty TlsDomain for cdn_http, got %s", cfg.TlsDomain)
	}
}

func TestWizardService_ConsumeErrorAfterCommitBoundary(t *testing.T) {
	svc, coord, xray, _, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// 1. Create a plan
	planRec, err := svc.Plan(ctx, "session-consume-err", WizardPlanRequest{
		Kind:         "xray",
		DeviceType:   "phone",
		PublicDomain: "cdn.example.com",
		Path:         "/cdn-bridge/",
		Mode:         "xhttp_get",
		ClientRemark: "Test Client",
	})
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}

	// 2. Inject fault: MarkConsumed fails in PlanStore
	forcedConsumeErr := errors.New("simulated disk write failure during consume")
	svc.planStore.SetMarkConsumedSeamForTest(func(planID, jobID string) (*ServerPlanRecord, error) {
		return nil, forcedConsumeErr
	})

	// 3. Apply the plan
	jobID, err := svc.Apply(ctx, "session-consume-err", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply initiation failed: %v", err)
	}

	// 4. Poll job status until it reaches terminal phase
	var finalJob JobStatusResponse
	completed := false
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		job, err := svc.GetJob(ctx, jobID, "session-consume-err")
		if err == nil && (job.Phase == JobPhaseRecoveryRequired || job.Phase == JobPhaseFailed || job.Phase == JobPhaseSucceeded) {
			finalJob = job
			completed = true
			break
		}
	}
	if !completed {
		t.Fatalf("job did not complete within timeout")
	}

	// 5. Verify assertions per closure requirements:
	// a. Coordinator/job transitioned to recovery_required
	if finalJob.Phase != JobPhaseRecoveryRequired {
		t.Fatalf("expected job phase %s, got %s (err: %s)", JobPhaseRecoveryRequired, finalJob.Phase, finalJob.Error)
	}

	// b. Diagnostic reason contains "plan_consume_failed_after_commit_boundary"
	if !strings.Contains(finalJob.Error, "plan_consume_failed_after_commit_boundary") {
		t.Errorf("expected job error to contain 'plan_consume_failed_after_commit_boundary', got error=%q", finalJob.Error)
	}
	if finalJob.ErrorCode != ErrCodeRecoveryRequired {
		t.Errorf("expected job error code %s, got %s", ErrCodeRecoveryRequired, finalJob.ErrorCode)
	}

	// c. Coordinator itself is in recoveryNeeded state
	isRec, reason := coord.IsRecoveryRequired()
	if !isRec {
		t.Errorf("expected coordinator to be in recoveryNeeded state")
	}
	if !strings.Contains(reason, "plan_consume_failed_after_commit_boundary") {
		t.Errorf("expected coordinator recovery reason to contain 'plan_consume_failed_after_commit_boundary', got %q", reason)
	}

	// d. Component rollback was NOT called (we are past point of no return)
	if len(xray.rollbackCalls) > 0 {
		t.Errorf("expected 0 rollback calls on xray after commit boundary, got %d", len(xray.rollbackCalls))
	}

	// e. Plan was NOT released and is NOT re-reservable by another job
	// Reset the seam so we can check real store state
	svc.planStore.SetMarkConsumedSeamForTest(nil)
	storedPlan, err := svc.planStore.GetPlan(planRec.PlanID, "session-consume-err")
	if err != nil {
		t.Fatalf("failed to retrieve stored plan: %v", err)
	}
	if storedPlan.State != PlanStateReserved {
		t.Errorf("expected plan state to remain Reserved (not released to Available), got %s", storedPlan.State)
	}
	// Attempting to reserve with another job must fail
	_, err = svc.planStore.Reserve(planRec.PlanID, "session-consume-err", "another-job")
	if err == nil {
		t.Errorf("expected reservation by another job to fail because plan is not available")
	}
}

func TestWizardService_ConsumeErrorAfterCommitBoundary_WithConcurrentCancelRequest(t *testing.T) {
	svc, coord, xray, _, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()
	sessionID := "session-consume-cancel-race"

	// 1. Create a plan
	planRec, err := svc.Plan(ctx, sessionID, WizardPlanRequest{
		Kind:         "xray",
		DeviceType:   "phone",
		PublicDomain: "cdn.example.com",
		Path:         "/cdn-bridge/",
		Mode:         "xhttp_get",
		ClientRemark: "Test Client",
	})
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}

	// 2. Inject fault: MarkConsumed triggers a cancel request right before returning error
	forcedConsumeErr := errors.New("simulated disk write failure during consume")
	svc.planStore.SetMarkConsumedSeamForTest(func(planID, jobID string) (*ServerPlanRecord, error) {
		// Attempt to request cancellation concurrently right during consume error
		_ = svc.CancelJob(ctx, jobID, sessionID)
		return nil, forcedConsumeErr
	})

	// 3. Apply the plan
	jobID, err := svc.Apply(ctx, sessionID, planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply initiation failed: %v", err)
	}

	// 4. Poll job status tightly across the commit boundary until it reaches terminal phase
	var finalJob JobStatusResponse
	completed := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := svc.GetJob(ctx, jobID, sessionID)
		if err == nil && (job.Phase == JobPhaseRecoveryRequired || job.Phase == JobPhaseFailed || job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseCancelled) {
			finalJob = job
			completed = true
			break
		}
		runtime.Gosched()
		time.Sleep(200 * time.Microsecond)
	}
	if !completed {
		t.Fatalf("job did not complete within timeout")
	}

	// 5. Verify assertions per audit requirements:
	// a. Final job phase must be STRICTLY recovery_required, NEVER cancelled!
	if finalJob.Phase != JobPhaseRecoveryRequired {
		t.Fatalf("expected job phase %s, got %s (err: %s)", JobPhaseRecoveryRequired, finalJob.Phase, finalJob.Error)
	}

	// b. Diagnostic reason contains "plan_consume_failed_after_commit_boundary"
	if !strings.Contains(finalJob.Error, "plan_consume_failed_after_commit_boundary") {
		t.Errorf("expected job error to contain 'plan_consume_failed_after_commit_boundary', got error=%q", finalJob.Error)
	}
	if finalJob.ErrorCode != ErrCodeRecoveryRequired {
		t.Errorf("expected job error code %s, got %s", ErrCodeRecoveryRequired, finalJob.ErrorCode)
	}

	// c. Coordinator itself is in recoveryNeeded state
	isRec, reason := coord.IsRecoveryRequired()
	if !isRec {
		t.Errorf("expected coordinator to be in recoveryNeeded state")
	}
	if !strings.Contains(reason, "plan_consume_failed_after_commit_boundary") {
		t.Errorf("expected coordinator recovery reason to contain 'plan_consume_failed_after_commit_boundary', got %q", reason)
	}

	// d. Component rollback was NOT called (we are past point of no return)
	if len(xray.rollbackCalls) > 0 {
		t.Errorf("expected 0 rollback calls on xray after commit boundary, got %d", len(xray.rollbackCalls))
	}

	// e. Plan remains reserved and is NOT released or re-reservable by another job
	svc.planStore.SetMarkConsumedSeamForTest(nil)
	storedPlan, err := svc.planStore.GetPlan(planRec.PlanID, sessionID)
	if err != nil {
		t.Fatalf("failed to retrieve stored plan: %v", err)
	}
	if storedPlan.State != PlanStateReserved {
		t.Errorf("expected plan state to remain Reserved (not released to Available), got %s", storedPlan.State)
	}

	// f. Explicit Release does NOT change state or make plan available
	_ = svc.planStore.Release(planRec.PlanID, "foreign-job")
	storedPlanAfterRelease, _ := svc.planStore.GetPlan(planRec.PlanID, sessionID)
	if storedPlanAfterRelease.State != PlanStateReserved {
		t.Errorf("expected plan state to remain Reserved after foreign release attempt, got %s", storedPlanAfterRelease.State)
	}

	// Attempting to reserve with another job must fail
	_, err = svc.planStore.Reserve(planRec.PlanID, sessionID, "another-job")
	if err == nil {
		t.Errorf("expected reservation by another job to fail because plan is not available")
	}
}

func TestWizardAcceptance_CustomXrayPath_DispatcherAndReadinessConsistency(t *testing.T) {
	svc, _, _, _, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	var probedURLs []string
	svc.SetHTTPDoer(&mockHTTPDoer{
		doFn: func(req *http.Request) (*http.Response, error) {
			probedURLs = append(probedURLs, req.URL.String())
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
			}
			if strings.Contains(req.URL.Path, "my-custom-vless-prefix") || strings.Contains(req.URL.Path, "xray") || strings.Contains(req.URL.Path, "cdn-bridge") {
				resp.Header.Set("X-CDN-Route", "xray")
			} else {
				resp.Header.Set("X-CDN-Route", "tgwebproxy")
			}
			return resp, nil
		},
	})

	// Input path has leading/trailing slashes and arbitrary prefix
	rawInputPath := "///my-custom-vless-prefix///"
	expectedCanonicalPath := "/my-custom-vless-prefix"

	planRec, err := svc.Plan(ctx, "session-custom-path", WizardPlanRequest{
		Kind:         "xray",
		DeviceType:   "phone",
		PublicDomain: "cdn.example.com",
		Path:         rawInputPath,
		Mode:         "xhttp_get",
		ClientRemark: "Custom Path Client",
	})
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}

	// 1. Verify desired config in plan has normalized path
	if planRec.Desired.Path != expectedCanonicalPath {
		t.Errorf("expected plan desired path %q, got %q", expectedCanonicalPath, planRec.Desired.Path)
	}

	jobID, err := svc.Apply(ctx, "session-custom-path", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply initiation failed: %v", err)
	}

	// Wait for completion
	completed := false
	var lastJob JobStatusResponse
	for i := 0; i < 80; i++ {
		time.Sleep(50 * time.Millisecond)
		job, err := svc.GetJob(ctx, jobID, "session-custom-path")
		if err == nil {
			lastJob = job
			if job.Phase == JobPhaseSucceeded {
				completed = true
				break
			} else if job.Phase == JobPhaseFailed || job.Phase == JobPhaseRecoveryRequired {
				t.Fatalf("job failed with phase %s: %s (code: %s)", job.Phase, job.Error, job.ErrorCode)
			}
		}
	}
	if !completed {
		t.Fatalf("job did not succeed within timeout, last job state: %+v", lastJob)
	}

	// 2. Verify registered dispatcher prefix is canonical without trailing slashes
	if disp.cfg.XrayPathPrefix != expectedCanonicalPath {
		t.Errorf("expected dispatcher XrayPathPrefix %q, got %q", expectedCanonicalPath, disp.cfg.XrayPathPrefix)
	}

	// 3. Verify probed URL matches the registered dispatcher prefix
	expectedProbeURL := fmt.Sprintf("http://127.0.0.1:9009%s", expectedCanonicalPath)
	found := false
	for _, u := range probedURLs {
		if u == expectedProbeURL {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("readiness probe did not request canonical URL %q, probed URLs: %v", expectedProbeURL, probedURLs)
	}
}

func TestWizardAcceptance_BuiltinXrayAndFirc_NoExternalXrayOrMihomo(t *testing.T) {
	svc, _, xray, _, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// C awgm-xhttpd installed; /opt/sbin/xray MUST NEVER be probed or required
	xray.st = vlessserver.Status{Installed: true, Version: "awgm-xhttpd 1.0.0-c"}
	svc.preflight.statFn = func(path string) (os.FileInfo, error) {
		if path == "/opt/sbin/xray" {
			t.Fatalf("preflight must never stat /opt/sbin/xray")
		}
		return mockFileInfo{name: path, size: 1000}, nil
	}

	req := WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "vless.example.test",
		Path:           "/xhttp-bridge/",
		CdnProfileID:   "cdn_get",
		Mode:           "xhttp_get",
		UpstreamDevice: "firc:router",
		ClientRemark:   "Phone FIRC",
	}

	pre := svc.Preflight(ctx, req)
	if !pre.CanProceed {
		t.Fatalf("expected preflight CanProceed=true without /opt/sbin/xray or Mihomo, got: %+v", pre.Checks)
	}

	planRec, err := svc.Plan(ctx, "session-firc-builtin", req)
	if err != nil {
		t.Fatalf("Plan creation failed: %v", err)
	}
	if planRec.Desired.OutboundMode != "router" {
		t.Fatalf("expected OutboundMode='router' for firc:router, got %q", planRec.Desired.OutboundMode)
	}

	jobID, err := svc.Apply(ctx, "session-firc-builtin", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	for i := 0; i < 200; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobID, "session-firc-builtin")
		if job.Phase == JobPhaseSucceeded {
			break
		}
		if job.Phase == JobPhaseFailed {
			t.Fatalf("job failed unexpectedly: %s", job.Error)
		}
	}

	if xray.cfg.OutboundMode != "router" || !disp.running {
		t.Fatalf("expected xray OutboundMode='router' and dispatcher running, got xray=%+v disp.running=%v", xray.cfg, disp.running)
	}
}

func TestWizardAcceptance_CompositeBoth_AtomicApplyAndRollback(t *testing.T) {
	svc, _, xray, tg, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// Seed pre-existing state so rollback can be verified against non-empty state
	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "old-xray.example.test",
		Path:         "/old-path",
		OutboundMode: "direct",
	}
	tg.cfg = tgwebproxy.Config{
		Enabled:        true,
		Scenario:       "direct_fake_tls",
		DirectHost:     "old-tg.example.test",
		DirectPort:     8443,
		TlsDomain:      "gateway.icloud.com",
		ListenPort:     8085,
		PublicHostname: "old-tg.example.test",
	}
	disp.cfg = cdndispatcher.Config{
		ListenAddr:     ":9009",
		PublicHostname: "old-xray.example.test",
		XrayPublicHost: "old-xray.example.test",
		XrayPathPrefix: "/old-path",
	}
	disp.running = true

	bothReq := WizardPlanRequest{
		Kind:             "both",
		Scenario:         "dual",
		PublicDomain:     "shared.example.test",
		Path:             "/vless-bridge/",
		CdnProfileID:     "cdn_get",
		Mode:             "xhttp_get",
		DirectHost:       "direct.example.test",
		DirectPort:       8443,
		TlsDomain:        "gateway.icloud.com",
		UpstreamDevice:   "firc:router",
		TgUpstreamDevice: "direct",
		ClientRemark:     "Unified Phone",
	}

	// Part 1: Fault-injected failure on 3rd service (after xray and tgwebproxy were already committed)
	// MUST atomically roll back all 3 services to their exact pre-apply state.
	disp.commitErr = errors.New("simulated dispatcher commit failure")
	planFail, err := svc.Plan(ctx, "session-both-rollback", bothReq)
	if err != nil {
		t.Fatalf("Plan creation for both failed: %v", err)
	}
	jobIDFail, err := svc.Apply(ctx, "session-both-rollback", planFail.PlanID)
	if err != nil {
		t.Fatalf("Apply initiation failed: %v", err)
	}

	var failedJob JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		failedJob, _ = svc.GetJob(ctx, jobIDFail, "session-both-rollback")
		if failedJob.Phase == JobPhaseCancelled || failedJob.Phase == JobPhaseFailed {
			break
		}
	}
	if failedJob.Phase != JobPhaseFailed {
		t.Fatalf("expected composite job to fail on injected fault, got %s", failedJob.Phase)
	}

	// Verify all 3 components rolled back to their exact pre-apply state
	if len(xray.rollbackCalls) == 0 || xray.cfg.PublicDomain != "old-xray.example.test" || xray.cfg.OutboundMode != "direct" {
		t.Fatalf("xray did not roll back to old state: rollbackCalls=%d cfg=%+v", len(xray.rollbackCalls), xray.cfg)
	}
	if len(tg.rollbackCalls) == 0 || tg.cfg.DirectHost != "old-tg.example.test" || tg.cfg.Scenario != "direct_fake_tls" {
		t.Fatalf("tgwebproxy did not roll back to old state: rollbackCalls=%d cfg=%+v", len(tg.rollbackCalls), tg.cfg)
	}
	if len(disp.rollbackCalls) == 0 || disp.cfg.XrayPublicHost != "old-xray.example.test" {
		t.Fatalf("dispatcher did not roll back to old state: rollbackCalls=%d cfg=%+v", len(disp.rollbackCalls), disp.cfg)
	}

	// Part 2: Happy path composite apply succeeds and reveals both VLESS and Telegram credentials
	disp.commitErr = nil
	svc.SetHTTPDoer(&mockHTTPDoer{
		doFn: func(req *http.Request) (*http.Response, error) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
			}
			if strings.Contains(req.URL.Path, "vless-bridge") {
				resp.Header.Set("X-CDN-Route", "xray")
			} else {
				resp.Header.Set("X-CDN-Route", "tgwebproxy")
			}
			return resp, nil
		},
	})

	planOK, err := svc.Plan(ctx, "session-both-ok", bothReq)
	if err != nil {
		t.Fatalf("Plan creation for both (happy path) failed: %v", err)
	}
	jobIDOK, err := svc.Apply(ctx, "session-both-ok", planOK.PlanID)
	if err != nil {
		t.Fatalf("Apply for both (happy path) failed: %v", err)
	}

	var finalJob JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		finalJob, _ = svc.GetJob(ctx, jobIDOK, "session-both-ok")
		if finalJob.Phase == JobPhaseSucceeded || finalJob.Phase == JobPhaseFailed {
			break
		}
	}
	if finalJob.Phase != JobPhaseSucceeded {
		t.Fatalf("expected composite both job to succeed, got %s: %s", finalJob.Phase, finalJob.Error)
	}

	creds, err := svc.Reveal(ctx, jobIDOK, "session-both-ok")
	if err != nil {
		t.Fatalf("Reveal for composite both failed: %v", err)
	}
	if creds.Kind != "both" || creds.VlessURL == "" || creds.DirectLink == "" || creds.WebLink == "" {
		t.Fatalf("expected composite credentials to contain VlessURL, DirectLink, and WebLink, got: %+v", creds)
	}
}

func TestWizardAcceptance_Verify_ErrorClassificationAndNoSecretLeak(t *testing.T) {
	svc, _, xray, tg, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	secretUUID := "11111111-2222-4333-8444-555555555555"
	secretTg := "aabbccddeeff00112233445566778899"

	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "vless.example.test",
		Path:         "/cdn-bridge",
		OutboundMode: "socks", // legacy egress -> should be classified as VerifyCategoryEgress blocked
		Clients: []vlessserver.Client{
			{ID: secretUUID, Remark: "Test", Enabled: true},
		},
	}
	xray.st = vlessserver.Status{Installed: true, Configured: true, Running: true}

	tg.cfg = tgwebproxy.Config{
		Enabled:        true,
		Scenario:       "dual",
		DirectHost:     "direct.example.test",
		DirectPort:     8443,
		TlsDomain:      "gateway.icloud.com",
		ListenPort:     8085,
		PublicHostname: "tg.example.test",
		Secret:         secretTg,
	}
	// Simulate MTProto running, but Web proxy stopped -> independent statuses!
	tg.st = tgwebproxy.Status{
		Installed:      true,
		Running:        true,
		DirectOnline:   true,
		WebProxyOnline: false,
	}
	disp.running = true

	// Simulate DNS failure for vless.example.test and success for others
	svc.SetLookupHost(func(ctx context.Context, host string) ([]string, error) {
		if host == "vless.example.test" {
			return nil, errors.New("no such host")
		}
		return []string{"198.51.100.10"}, nil
	})

	report, err := svc.Verify(ctx, "both")
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if report.StateFingerprint == "" || report.CheckedAt == "" || report.Scope != VerificationScopeRouterAndDNS {
		t.Fatalf("unexpected report metadata: %+v", report)
	}

	categoriesFound := map[VerificationErrorCategory]bool{}
	servicesStatus := map[string]CheckStatus{}
	for _, c := range report.Checks {
		if c.Category != "" {
			categoriesFound[c.Category] = true
		}
		servicesStatus[c.ID] = c.Status
		if strings.Contains(c.Message, secretUUID) || strings.Contains(c.Message, secretTg) {
			t.Errorf("secret leaked in verification check %s: %s", c.ID, c.Message)
		}
	}

	if !categoriesFound[VerifyCategoryDNS] {
		t.Errorf("expected DNS category in verification report, checks: %+v", report.Checks)
	}
	if !categoriesFound[VerifyCategoryEgress] {
		t.Errorf("expected Egress category for legacy socks outbound, checks: %+v", report.Checks)
	}
	if servicesStatus["tg_mtproto_listener"] != CheckStatusOK {
		t.Errorf("expected tg_mtproto_listener to be OK, got %s", servicesStatus["tg_mtproto_listener"])
	}
	if servicesStatus["tg_web_listener"] != CheckStatusBlocked {
		t.Errorf("expected tg_web_listener to be Blocked when RunningWeb=false, got %s", servicesStatus["tg_web_listener"])
	}
}

func TestWizardAcceptance_BothFircRequiresExplicitTelegramEgress(t *testing.T) {
	svc, _, _, tg, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// 1. both + firc:router WITHOUT TgUpstreamDevice must NOT silently fallback to direct
	reqMissingTg := WizardPlanRequest{
		Kind:           "both",
		Scenario:       "dual",
		PublicDomain:   "shared.example.test",
		Path:           "/vless-bridge/",
		CdnProfileID:   "cdn_get",
		Mode:           "xhttp_get",
		DirectHost:     "direct.example.test",
		DirectPort:     8443,
		TlsDomain:      "gateway.icloud.com",
		UpstreamDevice: "firc:router",
		ClientRemark:   "Unified Device",
	}

	if _, err := BuildDesiredConfig(ctx, svc.egress, reqMissingTg); err == nil {
		t.Fatalf("expected BuildDesiredConfig to reject both+firc:router without TgUpstreamDevice")
	}

	pre := svc.Preflight(ctx, reqMissingTg)
	if pre.CanProceed {
		t.Fatalf("expected Preflight to block both+firc:router without TgUpstreamDevice, got checks: %+v", pre.Checks)
	}

	if _, err := svc.Plan(ctx, "session-both-no-tg-egress", reqMissingTg); err == nil {
		t.Fatalf("expected Plan to reject both+firc:router without TgUpstreamDevice")
	}

	// Verify Telegram config remained untouched (no silent direct config created)
	if tg.cfg.Enabled {
		t.Fatalf("expected Telegram config to remain disabled, got: %+v", tg.cfg)
	}

	// 2. With explicit TgUpstreamDevice="direct", Plan and Apply succeed and expose both egress routes
	reqExplicit := reqMissingTg
	reqExplicit.TgUpstreamDevice = "direct"

	planRec, err := svc.Plan(ctx, "session-both-explicit-tg", reqExplicit)
	if err != nil {
		t.Fatalf("expected Plan with explicit TgUpstreamDevice to succeed, got: %v", err)
	}

	foundVlessEgress := false
	foundTgEgress := false
	for _, item := range planRec.Plan.Items {
		if item.Target == "outbound_routing" && item.NewValue == "firc:router" {
			foundVlessEgress = true
		}
		if item.Target == "upstream_egress" && item.NewValue == "direct" {
			foundTgEgress = true
		}
	}
	if !foundVlessEgress || !foundTgEgress {
		t.Fatalf("expected ChangePlan to explicitly list both outbound_routing=firc:router and upstream_egress=direct, got items: %+v", planRec.Plan.Items)
	}
}

func TestWizardAcceptance_FircRoutingTransactionRollbackAndRecovery(t *testing.T) {
	svc, coord, xray, _, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	fircDir := t.TempDir()
	fircOp := firc.NewOperator(firc.Options{
		ConfigDir:  fircDir,
		SocketPath: filepath.Join(fircDir, "nonexistent.sock"),
	})
	// Seed initial awgm-ext.json (xray_enabled=false) and groups.yaml with a restricted group
	initialExt := firc.ExtConfig{}
	initialExt.ServerRouting.SetServerEnabled("awg_server", true)
	initialExt.ServerRouting.SetServerEnabled("vless_xhttp_server", false)
	if err := fircOp.SaveExtConfig(initialExt); err != nil {
		t.Fatalf("SaveExtConfig initial failed: %v", err)
	}
	initialGroups := []firc.Group{
		{
			ID:        "grp1",
			Name:      "Main Group",
			Enable:    true,
			Interface: firc.GroupInterface{"nwg1"},
			Devices: firc.GroupDevices{
				Allow: []string{"192.168.1.50/32", "10.10.0.0/24"},
				Deny:  []string{"policy4"},
			},
		},
	}
	if err := fircOp.SaveGroupsFile(initialGroups); err != nil {
		t.Fatalf("SaveGroupsFile initial failed: %v", err)
	}

	coord.SetFircRouting(fircOp)

	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "old.example.test",
		Path:         "/cdn-bridge",
		OutboundMode: "direct",
	}
	disp.cfg = cdndispatcher.Config{
		ListenAddr:     ":9009",
		PublicHostname: "old.example.test",
		XrayPublicHost: "old.example.test",
		XrayPathPrefix: "/cdn-bridge",
	}
	disp.running = true

	reqFirc := WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "new.example.test",
		Path:           "/cdn-bridge/",
		CdnProfileID:   "cdn_get",
		Mode:           "xhttp_get",
		UpstreamDevice: "firc:router",
		ClientRemark:   "Router FIRC Client",
	}

	// Case 1: Fault during FIRC SaveExtConfig (after xray and dispatcher have already committed!)
	// Must roll back xray, dispatcher, awgm-ext.json, and groups.yaml.
	fircOp.SetServerRoutingTxFaultHooks(firc.ServerRoutingTxFaultHooks{
		BeforeSaveExt: func(txID string) error {
			return errors.New("simulated disk full saving awgm-ext.json")
		},
	})

	plan1, err := svc.Plan(ctx, "session-firc-tx-1", reqFirc)
	if err != nil {
		t.Fatalf("Plan 1 failed: %v", err)
	}
	jobID1, err := svc.Apply(ctx, "session-firc-tx-1", plan1.PlanID)
	if err != nil {
		t.Fatalf("Apply 1 failed: %v", err)
	}
	var job1 JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job1, _ = svc.GetJob(ctx, jobID1, "session-firc-tx-1")
		if job1.Phase == JobPhaseFailed || job1.Phase == JobPhaseSucceeded || job1.Phase == JobPhaseRecoveryRequired {
			break
		}
	}
	if job1.Phase != JobPhaseFailed {
		t.Fatalf("expected job1 to fail and roll back cleanly, got phase=%s err=%s", job1.Phase, job1.Error)
	}
	if xray.cfg.PublicDomain != "old.example.test" || xray.cfg.OutboundMode != "direct" {
		t.Fatalf("xray did not roll back after FIRC save failure: %+v", xray.cfg)
	}
	if fircOp.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected vless_xhttp_server to remain false in awgm-ext.json after rollback")
	}

	// Case 2: Fault during FIRC Reconcile (after awgm-ext.json was already mutated on disk!)
	// Must restore awgm-ext.json and groups.yaml from snapshot and roll back xray & dispatcher.
	fircOp.SetServerRoutingTxFaultHooks(firc.ServerRoutingTxFaultHooks{
		BeforeReconcile: func(txID string) error {
			return errors.New("simulated firc reconcile failure after ext save")
		},
	})

	plan2, err := svc.Plan(ctx, "session-firc-tx-2", reqFirc)
	if err != nil {
		t.Fatalf("Plan 2 failed: %v", err)
	}
	jobID2, err := svc.Apply(ctx, "session-firc-tx-2", plan2.PlanID)
	if err != nil {
		t.Fatalf("Apply 2 failed: %v", err)
	}
	var job2 JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job2, _ = svc.GetJob(ctx, jobID2, "session-firc-tx-2")
		if job2.Phase == JobPhaseFailed || job2.Phase == JobPhaseSucceeded || job2.Phase == JobPhaseRecoveryRequired {
			break
		}
	}
	if job2.Phase != JobPhaseFailed {
		t.Fatalf("expected job2 to fail and roll back cleanly, got phase=%s err=%s", job2.Phase, job2.Error)
	}
	if fircOp.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected awgm-ext.json to be rolled back to vless_xhttp_server=false after reconcile failure")
	}
	if !fircOp.IsServerRoutingEnabled("awg_server") {
		t.Fatalf("expected awg_server=true to be preserved in restored awgm-ext.json")
	}
	if xray.cfg.PublicDomain != "old.example.test" || xray.cfg.OutboundMode != "direct" {
		t.Fatalf("xray did not roll back after FIRC reconcile failure: %+v", xray.cfg)
	}

	// Case 3: Live fircd runtime + SetExtIptablesRunner fault injection + distinct disk vs live groups + 1.2s async replay guard
	var iptMu sync.Mutex
	iptNat := map[string][]string{
		"PREROUTING": nil,
		"OUTPUT":     nil,
		"FIRC_REDIR": {"-p tcp -m set --match-set firc_g1 dst -j REDIRECT --to-ports 1080"},
		"FIRC_DNAT":  {"-d 198.18.0.10/32 -p tcp -j DNAT --to-destination 142.250.74.10:443"},
	}
	iptMangle := map[string][]string{
		"FORWARD":         nil,
		"OUTPUT":          nil,
		"FIRC_PREROUTING": nil,
		"FIRC_grp1":       {"-d 8.8.8.8/32 -j FIRC_grp1D"},
		"FIRC_grp1D":      {"-j MARK --set-xmark 0x40010000/0x40ff0000"},
	}
	iptFilter := map[string][]string{
		"FORWARD": nil,
		"OUTPUT":  nil,
	}
	failCmdSub := ""
	firc.SetExtIptablesRunner(func(_ context.Context, args ...string) ([]byte, error) {
		iptMu.Lock()
		defer iptMu.Unlock()
		if len(args) > 0 && args[0] == "-w" {
			args = args[1:]
		}
		cmdLine := strings.Join(args, " ")
		if failCmdSub != "" && strings.Contains(cmdLine, failCmdSub) {
			return []byte("xtables simulated failure"), errors.New("exit status 4")
		}
		table := "filter"
		idx := 0
		if len(args) >= 2 && args[0] == "-t" {
			table = args[1]
			idx = 2
		}
		if idx >= len(args) {
			return nil, nil
		}
		chains := iptFilter
		if table == "nat" {
			chains = iptNat
		} else if table == "mangle" {
			chains = iptMangle
		}
		op := args[idx]
		switch op {
		case "-N":
			ch := args[idx+1]
			if _, ok := chains[ch]; !ok {
				chains[ch] = nil
			}
			return nil, nil
		case "-F":
			chains[args[idx+1]] = nil
			return nil, nil
		case "-S":
			ch := args[idx+1]
			rules, ok := chains[ch]
			if !ok {
				return []byte("No chain/target/match by that name"), errors.New("exit status 1")
			}
			var sb strings.Builder
			sb.WriteString("-N " + ch + "\n")
			for _, r := range rules {
				sb.WriteString("-A " + ch + " " + r + "\n")
			}
			return []byte(sb.String()), nil
		case "-C":
			ch := args[idx+1]
			spec := strings.Join(args[idx+2:], " ")
			for _, r := range chains[ch] {
				if r == spec {
					return nil, nil
				}
			}
			return nil, errors.New("exit status 1")
		case "-A":
			ch := args[idx+1]
			spec := strings.Join(args[idx+2:], " ")
			chains[ch] = append(chains[ch], spec)
			return nil, nil
		case "-I":
			ch := args[idx+1]
			rest := args[idx+2:]
			if len(rest) > 0 && rest[0] == "1" {
				rest = rest[1:]
			}
			spec := strings.Join(rest, " ")
			chains[ch] = append([]string{spec}, chains[ch]...)
			return nil, nil
		case "-D":
			ch := args[idx+1]
			spec := strings.Join(args[idx+2:], " ")
			rules := chains[ch]
			for i, r := range rules {
				if r == spec {
					chains[ch] = append(rules[:i], rules[i+1:]...)
					return nil, nil
				}
			}
			return nil, errors.New("exit status 1")
		default:
			return nil, nil
		}
	})
	defer firc.SetExtIptablesRunner(nil)
	firc.InvalidateOutputRoutingCache()

	// Set up a mock fircd server where live in-memory groups differ from on-disk groups.yaml,
	// and where PUT /api/v1/groups?save=true overwrites groups.yaml on disk (matching real fircd).
	var liveMu sync.Mutex
	liveGroups := []firc.Group{
		{
			ID:        "grp1",
			Name:      "Live Runtime Only Group",
			Enable:    true,
			Interface: firc.GroupInterface{"nwg1"},
			Devices: firc.GroupDevices{
				Allow: []string{"192.168.1.99/32"}, // no 10.10.0.0/24 yet
				Deny:  []string{"policy4"},
			},
		},
	}
	diskOnlyGroups := []firc.Group{
		{
			ID:        "grp1",
			Name:      "On-Disk Pre-Apply Group",
			Enable:    true,
			Interface: firc.GroupInterface{"nwg1"},
			Devices: firc.GroupDevices{
				Allow: []string{"192.168.1.50/32"},
				Deny:  []string{"policy4"},
			},
		},
	}
	if err := fircOp.SaveGroupsFile(diskOnlyGroups); err != nil {
		t.Fatalf("SaveGroupsFile diskOnlyGroups failed: %v", err)
	}
	preApplyDiskBytes, err := os.ReadFile(fircOp.GroupsPath())
	if err != nil {
		t.Fatalf("read pre-apply groups.yaml failed: %v", err)
	}

	fircMux := http.NewServeMux()
	fircMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(firc.NetfilterHealth{Ok: true})
	})
	fircMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		liveMu.Lock()
		defer liveMu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": liveGroups})
		case http.MethodPut:
			var req struct {
				Groups []firc.Group `json:"groups"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			liveGroups = req.Groups
			if r.URL.Query().Get("save") == "true" {
				_ = fircOp.SaveGroupsFile(req.Groups)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": liveGroups})
		}
	})
	fircSrv := httptest.NewServer(fircMux)
	defer fircSrv.Close()
	fircOp.SetClient(firc.NewHTTPClient(fircSrv.URL, fircSrv.Client()))
	fircOp.SetServerRoutingTxFaultHooks(firc.ServerRoutingTxFaultHooks{})

	// Inject iptables failure on adding mangle OUTPUT -> FEXT_OUT hook during CommitServerRouting
	// AND trigger a concurrent ReconcileExtNetfilter pass right before rollback to test the 1.2s replay guard.
	iptMu.Lock()
	failCmdSub = "-t mangle -A OUTPUT -j FEXT_OUT"
	iptMu.Unlock()

	// Spawn a background ReconcileExtNetfilter with candidate vless_xhttp_server=true right before Apply
	candReplayExt := initialExt
	candReplayExt.ServerRouting.SetServerEnabled("vless_xhttp_server", true)
	fircOp.ReconcileExtNetfilter(ctx, candReplayExt, liveGroups)

	planIptFail, err := svc.Plan(ctx, "session-firc-ipt-fail", reqFirc)
	if err != nil {
		t.Fatalf("Plan iptFail failed: %v", err)
	}
	jobIDIptFail, err := svc.Apply(ctx, "session-firc-ipt-fail", planIptFail.PlanID)
	if err != nil {
		t.Fatalf("Apply iptFail failed: %v", err)
	}
	var jobIptFail JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		jobIptFail, _ = svc.GetJob(ctx, jobIDIptFail, "session-firc-ipt-fail")
		if jobIptFail.Phase == JobPhaseFailed || jobIptFail.Phase == JobPhaseSucceeded || jobIptFail.Phase == JobPhaseRecoveryRequired {
			break
		}
	}
	if jobIptFail.Phase != JobPhaseFailed {
		t.Fatalf("expected job to fail and roll back when iptables -A OUTPUT -j FEXT_OUT fails, got phase=%s err=%s", jobIptFail.Phase, jobIptFail.Error)
	}
	if xray.cfg.PublicDomain != "old.example.test" || xray.cfg.OutboundMode != "direct" {
		t.Fatalf("xray did not roll back after iptables failure: %+v", xray.cfg)
	}
	if fircOp.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected vless_xhttp_server=false after iptables failure rollback")
	}

	// Verify BOTH live fircd runtime groups AND on-disk groups.yaml match their distinct pre-apply states!
	liveMu.Lock()
	gotLiveName := ""
	var gotLiveAllow []string
	if len(liveGroups) == 1 {
		gotLiveName = liveGroups[0].Name
		gotLiveAllow = append([]string(nil), liveGroups[0].Devices.Allow...)
	}
	liveMu.Unlock()
	if gotLiveName != "Live Runtime Only Group" || len(gotLiveAllow) != 1 || gotLiveAllow[0] != "192.168.1.99/32" {
		t.Fatalf("expected live fircd groups to be restored to pre-apply runtime state, got name=%q allow=%v", gotLiveName, gotLiveAllow)
	}
	afterRollbackDiskBytes, err := os.ReadFile(fircOp.GroupsPath())
	if err != nil {
		t.Fatalf("read restored groups.yaml failed: %v", err)
	}
	if !bytes.Equal(afterRollbackDiskBytes, preApplyDiskBytes) {
		t.Fatalf("expected on-disk groups.yaml bytes to be preserved independently of live fircd groups:\ngot:\n%s\nwant:\n%s", string(afterRollbackDiskBytes), string(preApplyDiskBytes))
	}

	// Clear injected iptables failure and wait > 1.2s to prove no stale async goroutine replays candidate FEXT_OUT hook
	iptMu.Lock()
	failCmdSub = ""
	iptMu.Unlock()
	time.Sleep(1350 * time.Millisecond)
	iptMu.Lock()
	mangleOutRules := append([]string(nil), iptMangle["OUTPUT"]...)
	iptMu.Unlock()
	for _, r := range mangleOutRules {
		if strings.Contains(r, "FEXT_OUT") {
			t.Fatalf("stale async netfilter pass re-applied candidate FEXT_OUT hook after rollback: %v", mangleOutRules)
		}
	}

	// Case 4: Happy path commit updates xray, awgm-ext.json, live fircd groups, and netfilter hooks
	plan3, err := svc.Plan(ctx, "session-firc-tx-3", reqFirc)
	if err != nil {
		t.Fatalf("Plan 3 failed: %v", err)
	}
	jobID3, err := svc.Apply(ctx, "session-firc-tx-3", plan3.PlanID)
	if err != nil {
		t.Fatalf("Apply 3 failed: %v", err)
	}
	var job3 JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job3, _ = svc.GetJob(ctx, jobID3, "session-firc-tx-3")
		if job3.Phase == JobPhaseFailed || job3.Phase == JobPhaseSucceeded || job3.Phase == JobPhaseRecoveryRequired {
			break
		}
	}
	if job3.Phase != JobPhaseSucceeded {
		t.Fatalf("expected job3 to succeed, got phase=%s err=%s", job3.Phase, job3.Error)
	}
	if !fircOp.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected vless_xhttp_server=true in awgm-ext.json after successful commit")
	}
	iptMu.Lock()
	hasFextOutHook := false
	for _, r := range iptMangle["OUTPUT"] {
		if r == "-j FEXT_OUT" {
			hasFextOutHook = true
		}
	}
	iptMu.Unlock()
	if !hasFextOutHook {
		t.Fatalf("expected mangle OUTPUT -j FEXT_OUT hook to be installed after strict commit")
	}

	// Case 5: Fault during FIRC Rollback transitions both firc.Operator and Coordinator/Job to recovery_required
	reqDirect := reqFirc
	reqDirect.UpstreamDevice = "direct"
	fircOp.SetServerRoutingTxFaultHooks(firc.ServerRoutingTxFaultHooks{
		BeforeReconcile: func(txID string) error {
			return errors.New("simulated reconcile failure triggering rollback")
		},
		BeforeRollback: func(txID string) error {
			return errors.New("simulated unrecoverable disk error during FIRC rollback")
		},
	})
	plan4, err := svc.Plan(ctx, "session-firc-tx-4", reqDirect)
	if err != nil {
		t.Fatalf("Plan 4 failed: %v", err)
	}
	jobID4, err := svc.Apply(ctx, "session-firc-tx-4", plan4.PlanID)
	if err != nil {
		t.Fatalf("Apply 4 failed: %v", err)
	}
	var job4 JobStatusResponse
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job4, _ = svc.GetJob(ctx, jobID4, "session-firc-tx-4")
		if job4.Phase == JobPhaseFailed || job4.Phase == JobPhaseSucceeded || job4.Phase == JobPhaseRecoveryRequired {
			break
		}
	}
	if job4.Phase != JobPhaseRecoveryRequired {
		t.Fatalf("expected job4 phase=%s on FIRC rollback failure, got %s (err=%s)", JobPhaseRecoveryRequired, job4.Phase, job4.Error)
	}
	if !fircOp.IsRecoveryRequired() {
		t.Fatalf("expected fircOp.IsRecoveryRequired()=true after failed FIRC rollback")
	}
	if recReq, _ := coord.IsRecoveryRequired(); !recReq {
		t.Fatalf("expected coord.IsRecoveryRequired()=true after failed FIRC rollback")
	}
}

func TestWizardAcceptance_PreserveExistingCredentialsAndCustomTransport(t *testing.T) {
	svc, _, xray, tg, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	existingUUID := "22222222-3333-4444-8555-666666666666"
	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "custom.example.test",
		Path:         "/custom-secret-path",
		Transport:    "xhttp",
		Mode:         "stream-up",
		UplinkMethod: "POST",
		OutboundMode: "direct",
		Clients: []vlessserver.Client{
			{ID: existingUUID, Remark: "Основное устройство", Enabled: true},
		},
		XHTTPTuning: &vlessserver.XHTTPTuning{
			Preset:            "custom",
			UplinkMethod:      "POST",
			StreamPathSuffix:  "stream.ts",
			SessionPlacement:  "query",
			SessionKey:        "custom_sid",
			SeqPlacement:      "query",
			SeqKey:            "custom_seq",
			XPaddingBytes:     "200-400",
			XPaddingHeader:    "Referer",
			XPaddingPlacement: "queryInHeader",
			XPaddingKey:       "_dc",
			XPaddingMethod:    "tokenish",
			XPaddingObfsMode:  true,
		},
	}
	tg.cfg = tgwebproxy.Config{
		Enabled:        true,
		Scenario:       "dual",
		DirectHost:     "direct.example.test",
		DirectPort:     8443,
		TlsDomain:      "custom-sni.example.org",
		ListenPort:     8085,
		PublicHostname: "custom.example.test",
		CarrierMode:    "https-lanes",
	}

	svc.SetHTTPDoer(&mockHTTPDoer{
		doFn: func(req *http.Request) (*http.Response, error) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
			}
			if strings.Contains(req.URL.Path, "custom-secret-path") {
				resp.Header.Set("X-CDN-Route", "xray")
			} else {
				resp.Header.Set("X-CDN-Route", "tgwebproxy")
			}
			return resp, nil
		},
	})

	editReq := WizardPlanRequest{
		Kind:                        "both",
		Scenario:                    "dual",
		PublicDomain:                "custom.example.test",
		Path:                        "/custom-secret-path",
		CdnProfileID:                "cdn_get",
		ConnectionWay:               "external_https",
		OriginType:                  "cloud_address",
		OriginHost:                  "example.keenetic.pro",
		OriginScheme:                "https",
		OriginPort:                  9443,
		DirectHost:                  "direct.example.test",
		DirectPort:                  8443,
		TlsDomain:                   "custom-sni.example.org",
		CarrierMode:                 "https-lanes",
		UpstreamDevice:              "direct",
		TgUpstreamDevice:            "direct",
		ClientRemark:                "Основное устройство",
		PreserveTransport:           true,
		RawTransport:                "xhttp",
		RawMode:                     "stream-up",
		RawUplinkMethod:             "POST",
		PreserveExistingCredentials: true,
		ExistingClientUUID:          existingUUID,
	}

	planRec, err := svc.Plan(ctx, "session-edit-preserve", editReq)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if planRec.Desired.Mode != "stream-up" || planRec.Desired.UplinkMethod != "POST" {
		t.Fatalf("expected custom transport mode/uplink to be preserved, got: %+v", planRec.Desired)
	}

	jobID, err := svc.Apply(ctx, "session-edit-preserve", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobID, "session-edit-preserve")
		if job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed {
			break
		}
	}

	creds, err := svc.Reveal(ctx, jobID, "session-edit-preserve")
	if err != nil {
		t.Fatalf("Reveal failed: %v", err)
	}
	if creds.UUID != existingUUID {
		t.Fatalf("expected existing client UUID %s to be preserved, got %s", existingUUID, creds.UUID)
	}
	if creds.TgSecret != "11223344556677889900aabbccddeeff" {
		t.Fatalf("expected existing Telegram secret to be preserved, got %s", creds.TgSecret)
	}

	// Verify durable origin metadata persisted in committed xray.cfg
	if xray.cfg.OriginHost != "example.keenetic.pro" || xray.cfg.OriginScheme != "https" || xray.cfg.OriginPort != 9443 || xray.cfg.ConnectionWay != "external_https" || xray.cfg.OriginType != "cloud_address" {
		t.Fatalf("expected durable origin metadata in committed xray.cfg, got: %+v", xray.cfg)
	}

	// Verify revealed VLESS links match the committed server config (including custom XHTTPTuning!)
	expectedLinks, err := vlessserver.BuildShareLinks(xray.cfg, xray.cfg.Clients[0])
	if err != nil {
		t.Fatalf("BuildShareLinks on committed xray.cfg failed: %v", err)
	}
	if creds.VlessURL != expectedLinks.VlessURL || creds.HappJSON != expectedLinks.HappJSON || creds.SingboxJSON != expectedLinks.SingboxJSON {
		t.Fatalf("revealed links diverged from committed server config!\nrevealed VlessURL:  %s\ncommitted VlessURL: %s", creds.VlessURL, expectedLinks.VlessURL)
	}
	parsedVless, err := url.Parse(creds.VlessURL)
	if err != nil {
		t.Fatalf("parse revealed VlessURL failed: %v", err)
	}
	if gotPath := parsedVless.Query().Get("path"); gotPath != "/custom-secret-path/stream.ts" {
		t.Fatalf("expected revealed VlessURL path '/custom-secret-path/stream.ts', got %q", gotPath)
	}
	extraJSON := parsedVless.Query().Get("extra")
	if !strings.Contains(extraJSON, `"custom_sid"`) || !strings.Contains(extraJSON, `"custom_seq"`) || !strings.Contains(extraJSON, `"200-400"`) {
		t.Fatalf("expected revealed VlessURL extra to include custom_sid, custom_seq, and 200-400 padding, got: %s", extraJSON)
	}

	// Part 2: Non-preserve switch to GET (mode="xhttp_get", PreserveTransport=false) must update XHTTPTuning
	// so stale POST in XHTTPTuning does NOT override GET on either the server or the revealed share links!
	switchGetReq := editReq
	switchGetReq.PreserveTransport = false
	switchGetReq.Mode = "xhttp_get"
	planGet, err := svc.Plan(ctx, "session-switch-get", switchGetReq)
	if err != nil {
		t.Fatalf("Plan switchGet failed: %v", err)
	}
	jobIDGet, err := svc.Apply(ctx, "session-switch-get", planGet.PlanID)
	if err != nil {
		t.Fatalf("Apply switchGet failed: %v", err)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobIDGet, "session-switch-get")
		if job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed {
			break
		}
	}
	if gotTuning := xray.cfg.GetTuning(); gotTuning.UplinkMethod != "GET" {
		t.Fatalf("expected committed xray.cfg.GetTuning().UplinkMethod='GET' after switching to xhttp_get, got %q", gotTuning.UplinkMethod)
	}
	credsGet, err := svc.Reveal(ctx, jobIDGet, "session-switch-get")
	if err != nil {
		t.Fatalf("Reveal switchGet failed: %v", err)
	}
	expectedGetLinks, err := vlessserver.BuildShareLinks(xray.cfg, xray.cfg.Clients[0])
	if err != nil {
		t.Fatalf("BuildShareLinks after switchGet failed: %v", err)
	}
	if credsGet.VlessURL != expectedGetLinks.VlessURL || !strings.Contains(credsGet.HappJSON, `"uplinkHTTPMethod": "GET"`) {
		t.Fatalf("expected revealed links after switchGet to use GET and match committed server config, got VlessURL=%s", credsGet.VlessURL)
	}
}

func TestWizardAcceptance_FircFailClosedReadiness(t *testing.T) {
	svc, _, xray, _, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// 1. When fircStateProvider is nil -> fail-closed
	svc.egress.SetFircStateProvider(nil)
	preNil := svc.Preflight(ctx, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.test",
		Path:           "/cdn-bridge/",
		UpstreamDevice: "firc:router",
	})
	if preNil.CanProceed {
		t.Fatalf("expected Preflight to block firc:router when provider is nil")
	}

	// 2. When fircStateProvider returns false (e.g. fircd stopped or settings error) -> fail-closed
	svc.egress.SetFircStateProvider(func(ctx context.Context) (bool, string) {
		return false, "Служба FIRC (fircd) остановлена"
	})
	preStopped := svc.Preflight(ctx, WizardPlanRequest{
		Kind:           "xray",
		PublicDomain:   "cdn.example.test",
		Path:           "/cdn-bridge/",
		UpstreamDevice: "firc:router",
	})
	if preStopped.CanProceed {
		t.Fatalf("expected Preflight to block firc:router when fircd is stopped")
	}

	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "cdn.example.test",
		Path:         "/cdn-bridge",
		OutboundMode: "router",
		Clients:      []vlessserver.Client{{ID: "11111111-2222-4333-8444-555555555555", Enabled: true}},
	}
	xray.st = vlessserver.Status{Installed: true, Configured: true, Running: true}

	report, err := svc.Verify(ctx, "xray")
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if report.OverallStatus != CheckStatusBlocked {
		t.Fatalf("expected Verify OverallStatus=blocked when FIRC is stopped, got %s", report.OverallStatus)
	}
}

func TestWizardAcceptance_MultiDomainVerifyAndSingleServiceBothVerify(t *testing.T) {
	svc, _, xray, tg, disp, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	xray.cfg = vlessserver.Config{
		Enabled:      true,
		ListenPort:   9008,
		PublicDomain: "edge1.example.test, edge2.example.test",
		Path:         "/cdn-bridge",
		OutboundMode: "direct",
		Clients:      []vlessserver.Client{{ID: "11111111-2222-4333-8444-555555555555", Enabled: true}},
	}
	xray.st = vlessserver.Status{Installed: true, Configured: true, Running: true}
	tg.cfg = tgwebproxy.Config{Enabled: false}
	tg.st = tgwebproxy.Status{Installed: true, Running: false}
	disp.running = true

	var probedHosts []string
	svc.SetHTTPDoer(&mockHTTPDoer{
		doFn: func(req *http.Request) (*http.Response, error) {
			probedHosts = append(probedHosts, req.Host)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
			}
			resp.Header.Set("X-CDN-Route", "xray")
			return resp, nil
		},
	})
	svc.SetLookupHost(func(ctx context.Context, host string) ([]string, error) {
		return []string{"198.51.100.25"}, nil
	})

	// Verify with kind="both" when only Xray is enabled and multi-domain is configured
	report, err := svc.Verify(ctx, "both")
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if report.OverallStatus != CheckStatusOK {
		t.Fatalf("expected OverallStatus=ok when Xray is healthy and Telegram is not enabled, got %s (checks: %+v)", report.OverallStatus, report.Checks)
	}

	checkIDs := map[string]bool{}
	for _, c := range report.Checks {
		checkIDs[c.ID] = true
	}
	if !checkIDs["xray_dispatcher_1"] || !checkIDs["xray_dispatcher_2"] || !checkIDs["xray_dns_1"] || !checkIDs["xray_dns_2"] {
		t.Fatalf("expected per-domain dispatcher and DNS checks for comma-separated domains, got: %v", checkIDs)
	}
	if !checkIDs["tg_service_disabled"] {
		t.Fatalf("expected tg_service_disabled informational check when only Xray is enabled, got: %v", checkIDs)
	}
	if len(probedHosts) != 2 || probedHosts[0] != "edge1.example.test" || probedHosts[1] != "edge2.example.test" {
		t.Fatalf("expected both comma-separated domains to be probed on dispatcher, got: %v", probedHosts)
	}
}

func TestWizardAcceptance_DirectKeenDNSUsesWebSocketAndStreamOne(t *testing.T) {
	svc, _, xray, tg, _, _ := setupAcceptanceCoordinatorAndWizard(t)
	ctx := context.Background()

	// Pre-populate stale CDN / packet-up config as an existing router would have
	xray.cfg = vlessserver.Config{
		Enabled:       true,
		ListenAddress: "127.0.0.1",
		ListenPort:    9008,
		PublicDomain:  "connect.example.netcraze.club",
		Mode:          "packet-up",
		UplinkMethod:  "POST",
		ConnectionWay: "direct",
	}
	tg.cfg = tgwebproxy.Config{
		Enabled:        true,
		Scenario:       tgwebproxy.ScenarioDual,
		NetworkPreset:  tgwebproxy.PresetCDNKeenDNS,
		CarrierMode:    "https-lanes",
		PublicHostname: "connect.example.netcraze.club",
		DirectHost:     "connect.example.netcraze.club",
		DirectPort:     8443,
		TlsDomain:      "www.google.com",
		Secret:         "11223344556677889900aabbccddeeff",
		Endpoints: []tgwebproxy.WebProxyEndpoint{{
			ID: "ep-primary", Hostname: "connect.example.netcraze.club",
			Preset: tgwebproxy.PresetCDNKeenDNS, CarrierMode: "https-lanes", IsCDN: true, Enabled: true,
		}},
	}

	req := WizardPlanRequest{
		Kind:             "both",
		Scenario:         "dual",
		PublicDomain:     "connect.example.netcraze.club",
		Path:             "/xhttp-bridge",
		ConnectionWay:    "direct",
		OriginType:       "cloud_address",
		CdnProfileID:     "direct",
		Mode:             "xhttp_get",
		DirectHost:       "connect.example.netcraze.club",
		DirectPort:       8443,
		TlsDomain:        "www.google.com",
		CarrierMode:      "https-lanes", // even if caller sends stale default, direct KeenDNS must select websocket
		UpstreamDevice:   "direct",
		TgUpstreamDevice: "direct",
	}
	planRec, err := svc.Plan(ctx, "session-keendns-direct", req)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if planRec.Desired.Mode != "stream-one" || planRec.Desired.UplinkMethod != "POST" {
		t.Fatalf("expected direct KeenDNS VLESS to select stream-one POST, got mode=%s method=%s", planRec.Desired.Mode, planRec.Desired.UplinkMethod)
	}
	if planRec.Desired.CarrierMode != "websocket" {
		t.Fatalf("expected direct KeenDNS Telegram to select websocket carrier, got %s", planRec.Desired.CarrierMode)
	}

	jobID, err := svc.Apply(ctx, "session-keendns-direct", planRec.PlanID)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(25 * time.Millisecond)
		job, _ := svc.GetJob(ctx, jobID, "session-keendns-direct")
		if job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed {
			break
		}
	}

	if xray.cfg.Mode != "stream-one" || xray.cfg.GetTuning().Preset != "balanced" {
		t.Fatalf("expected committed Xray stream-one + balanced preset, got mode=%s tuning=%+v", xray.cfg.Mode, xray.cfg.GetTuning())
	}
	if tg.cfg.CarrierMode != "websocket" || tg.cfg.NetworkPreset != tgwebproxy.PresetKeenDNS {
		t.Fatalf("expected committed Tg websocket + keendns preset, got carrier=%s preset=%s", tg.cfg.CarrierMode, tg.cfg.NetworkPreset)
	}
	if len(tg.cfg.Endpoints) != 1 || tg.cfg.Endpoints[0].IsCDN || tg.cfg.Endpoints[0].CarrierMode != "websocket" {
		t.Fatalf("expected committed Tg endpoint[0] non-CDN websocket, got %+v", tg.cfg.Endpoints)
	}
}
