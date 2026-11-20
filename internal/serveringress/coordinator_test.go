package serveringress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type mockXray struct {
	mu            sync.Mutex
	cfg           vlessserver.Config
	status        vlessserver.Status
	prepError     error
	commitError   error
	rollbackError error
	finalizeError error
	finalizeCalls []string
	rollbackCalls []string
	commitCalls   []string
}

func (m *mockXray) GetConfig() vlessserver.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mockXray) GetStatus() vlessserver.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockXray) PrepareCandidate(txID string, candidate vlessserver.Config) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prepError != nil {
		return "", m.prepError
	}
	if txID != "" {
		return txID, nil
	}
	return "xray-tx-123", nil
}

func (m *mockXray) CommitPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commitError != nil {
		return m.commitError
	}
	m.commitCalls = append(m.commitCalls, txID)
	return nil
}

func (m *mockXray) FinalizePrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalizeCalls = append(m.finalizeCalls, txID)
	if m.finalizeError != nil {
		return m.finalizeError
	}
	return nil
}

func (m *mockXray) RollbackPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollbackCalls = append(m.rollbackCalls, txID)
	if m.rollbackError != nil {
		return m.rollbackError
	}
	return nil
}

func (m *mockXray) Restart() error {
	return nil
}

func (m *mockXray) UpdatePublicDomain(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.PublicDomain = domain
	return nil
}

type mockDispatcher struct {
	mu            sync.Mutex
	cfg           cdndispatcher.Config
	oldCfg        cdndispatcher.Config
	candidate     cdndispatcher.Candidate
	running       bool
	wasRunning    bool
	reconfError   error
	applyError    error
	applyCalls    []cdndispatcher.Config
	prepError     error
	commitError   error
	rollbackError error
	finalizeError error
	startError    error
	stopError     error
	commitCalls   []string
	rollbackCalls []string
	finalizeCalls []string
}

func (m *mockDispatcher) GetConfig() cdndispatcher.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mockDispatcher) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *mockDispatcher) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startError != nil {
		return m.startError
	}
	m.running = true
	return nil
}

func (m *mockDispatcher) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopError != nil {
		return m.stopError
	}
	m.running = false
	return nil
}

func (m *mockDispatcher) Reconfigure(cfg cdndispatcher.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconfError != nil {
		return m.reconfError
	}
	m.cfg = cfg
	return nil
}

func (m *mockDispatcher) ApplyConfig(cfg cdndispatcher.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyCalls = append(m.applyCalls, cfg)
	if m.applyError != nil {
		return m.applyError
	}
	m.cfg = cfg
	return nil
}

func (m *mockDispatcher) PrepareCandidate(txID string, candidate cdndispatcher.Candidate) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prepError != nil {
		return "", m.prepError
	}
	if candidate.Enabled && m.startError != nil {
		return "", m.startError
	}
	if m.applyError != nil {
		return "", m.applyError
	}
	if m.reconfError != nil {
		return "", m.reconfError
	}
	m.oldCfg = m.cfg
	m.wasRunning = m.running
	m.candidate = candidate
	if txID != "" {
		return txID, nil
	}
	return "disp-tx-123", nil
}

func (m *mockDispatcher) CommitPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commitError != nil {
		return m.commitError
	}
	m.commitCalls = append(m.commitCalls, txID)
	m.cfg = m.candidate.Config
	m.running = m.candidate.Enabled
	return nil
}

func (m *mockDispatcher) RollbackPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollbackCalls = append(m.rollbackCalls, txID)
	if m.rollbackError != nil {
		return m.rollbackError
	}
	m.cfg = m.oldCfg
	m.running = m.wasRunning
	return nil
}

func (m *mockDispatcher) FinalizePrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalizeCalls = append(m.finalizeCalls, txID)
	if m.finalizeError != nil {
		return m.finalizeError
	}
	return nil
}

type mockTgWebProxy struct {
	mu            sync.Mutex
	cfg           tgwebproxy.Config
	oldCfg        tgwebproxy.Config
	candidate     tgwebproxy.Config
	status        tgwebproxy.Status
	updateError   error
	applyError    error
	applyCalls    []tgwebproxy.ManagedIngressConfig
	prepError     error
	commitError   error
	rollbackError error
	finalizeError error
	commitCalls   []string
	rollbackCalls []string
	finalizeCalls []string
}

func (m *mockTgWebProxy) GetConfig() tgwebproxy.PublicConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return tgwebproxy.PublicConfig{
		Enabled:        m.cfg.Enabled,
		ListenPort:     m.cfg.ListenPort,
		PublicHostname: m.cfg.PublicHostname,
	}
}

func (m *mockTgWebProxy) GetInternalConfig() tgwebproxy.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mockTgWebProxy) GetStatus() tgwebproxy.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockTgWebProxy) UpdateConfig(cfg tgwebproxy.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateError != nil {
		return m.updateError
	}
	m.cfg = cfg
	return nil
}

func (m *mockTgWebProxy) ApplyManagedIngress(cfg tgwebproxy.ManagedIngressConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyCalls = append(m.applyCalls, cfg)
	if m.applyError != nil {
		return m.applyError
	}
	m.cfg.Enabled = cfg.Enabled
	m.cfg.ListenPort = cfg.ListenPort
	m.cfg.PublicHostname = cfg.PublicHostname
	return nil
}

func (m *mockTgWebProxy) PrepareCandidate(txID string, candidate tgwebproxy.Config) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prepError != nil {
		return "", m.prepError
	}
	if m.updateError != nil {
		return "", m.updateError
	}
	if m.applyError != nil {
		return "", m.applyError
	}
	m.oldCfg = m.cfg
	m.candidate = candidate
	if txID != "" {
		return txID, nil
	}
	return "tg-tx-123", nil
}

func (m *mockTgWebProxy) CommitPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commitError != nil {
		return m.commitError
	}
	m.commitCalls = append(m.commitCalls, txID)
	m.cfg = m.candidate
	return nil
}

func (m *mockTgWebProxy) RollbackPrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollbackCalls = append(m.rollbackCalls, txID)
	if m.rollbackError != nil {
		return m.rollbackError
	}
	m.cfg = m.oldCfg
	return nil
}

func (m *mockTgWebProxy) FinalizePrepared(txID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finalizeCalls = append(m.finalizeCalls, txID)
	if m.finalizeError != nil {
		return m.finalizeError
	}
	return nil
}

func TestCoordinatorHappyPath(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{
		cfg: vlessserver.Config{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
			Path:          "/cdn-bridge",
		},
	}
	disp := &mockDispatcher{
		cfg: cdndispatcher.Config{
			ListenAddr: ":9009",
		},
		running: true,
	}
	tg := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:    true,
			ListenPort: 8085,
		},
	}

	coord := New(dataDir, xray, disp, tg)

	desired := IngressTopology{
		DispatcherEnabled: true,
		DispatcherPort:    9009,
		PublicHostname:    "custom.domain.test",
		XrayEnabled:       true,
		XrayAddress:       "127.0.0.1",
		XrayPort:          9008,
		XrayPathPrefix:    "/cdn-bridge",
		TgEnabled:         true,
		TgPort:            8085,
	}

	err := coord.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(xray.commitCalls) != 1 {
		t.Fatalf("expected 1 commit call on xray, got %d", len(xray.commitCalls))
	}

	// Active journal must be archived, not left active
	jPath := coord.journalPath()
	if _, err := os.Stat(jPath); !os.IsNotExist(err) {
		t.Fatalf("active journal should not exist after successful commit")
	}

	// Check committed archive exists
	files, _ := filepath.Glob(jPath + ".committed.*")
	if len(files) != 1 {
		t.Fatalf("expected 1 committed archive, found %d", len(files))
	}
}

func TestCoordinatorPrepareFailureRollback(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{
		prepError: errors.New("candidate config invalid"),
	}
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}

	coord := New(dataDir, xray, disp, tg)

	desired := IngressTopology{
		XrayEnabled: true,
		XrayPort:    9008,
	}

	err := coord.Apply(context.Background(), desired)
	if err == nil {
		t.Fatalf("expected Apply to fail on prepare error")
	}

	// Active journal should not exist
	jPath := coord.journalPath()
	if _, err := os.Stat(jPath); !os.IsNotExist(err) {
		t.Fatalf("active journal should not exist after failure")
	}

	// Failed archive exists
	files, _ := filepath.Glob(jPath + ".failed.*")
	if len(files) != 1 {
		t.Fatalf("expected 1 failed archive, found %d", len(files))
	}
}

func TestCoordinatorCommitFailureRollback(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{}
	disp := &mockDispatcher{
		startError: errors.New("cannot bind port"),
	}
	tg := &mockTgWebProxy{}

	coord := New(dataDir, xray, disp, tg)

	desired := IngressTopology{
		DispatcherEnabled: true,
		DispatcherPort:    9009,
		XrayEnabled:       true,
		XrayPort:          9008,
	}

	err := coord.Apply(context.Background(), desired)
	if err == nil {
		t.Fatalf("expected Apply to fail when dispatcher fails start")
	}

	// Xray must have been rolled back
	if len(xray.rollbackCalls) != 1 {
		t.Fatalf("expected 1 rollback call on xray, got %d", len(xray.rollbackCalls))
	}
}

func TestCoordinatorStartupRecoveryInterrupted(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{}
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}

	coord := New(dataDir, xray, disp, tg)

	// Simulate a crashed transaction at PhaseCommitting (past point of no return)
	jPath := coord.journalPath()
	j := &TransactionJournal{
		TransactionID: "crashed-tx-1",
		Phase:         PhaseCommitting,
		Affected:      []string{"xray", "dispatcher"},
		ComponentTransactionIDs: map[string]string{
			"xray":       "crashed-tx-1",
			"dispatcher": "crashed-tx-1",
		},
		Previous: IngressTopology{
			DispatcherPort: 9009,
			XrayAddress:    "127.0.0.1",
			XrayPort:       9008,
		},
	}
	if err := writeJournal(jPath, j); err != nil {
		t.Fatal(err)
	}

	err := coord.StartupRecovery(context.Background())
	if err != nil {
		t.Fatalf("StartupRecovery failed: %v", err)
	}

	// At PhaseCommitting, candidate is already active; recovery calls FinalizePrepared (Specification 6)
	if len(xray.finalizeCalls) != 1 || xray.finalizeCalls[0] != "crashed-tx-1" {
		t.Fatalf("expected finalize for crashed-tx-1, got %v", xray.finalizeCalls)
	}

	// Active journal must be archived as committed
	if _, err := os.Stat(jPath); !os.IsNotExist(err) {
		t.Fatalf("active journal should not exist after recovery")
	}
	files, _ := filepath.Glob(jPath + ".committed.*")
	if len(files) != 1 {
		t.Fatalf("expected 1 committed archive, got %d", len(files))
	}
}

func TestRecoveryFromPhaseCommittingRollForward(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{}
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}

	coord := New(dataDir, xray, disp, tg)

	// Simulate a crashed transaction at PhaseCommitting (past point of no return)
	jPath := coord.journalPath()
	j := &TransactionJournal{
		Version:       CurrentJournalVersion,
		TransactionID: "crashed-tx-rf",
		Phase:         PhaseCommitting,
		Affected:      []string{"xray", "tgwebproxy", "dispatcher"},
		ComponentTransactionIDs: map[string]string{
			"xray":       "tx-xray-1",
			"tgwebproxy": "tx-tg-1",
			"dispatcher": "tx-disp-1",
		},
		Desired: IngressTopology{
			XrayEnabled:        true,
			XrayPort:           9008,
			XrayPublicHostname: "xray.example.org",
			TgEnabled:          true,
			TgScenario:         "dual",
			TgDirectPort:       8443,
			TgWebPort:          8085,
			TgPublicHostname:   "tg.example.org",
			DispatcherPort:     9009,
		},
	}
	if err := writeJournal(jPath, j); err != nil {
		t.Fatal(err)
	}

	err := coord.StartupRecovery(context.Background())
	if err != nil {
		t.Fatalf("StartupRecovery failed: %v", err)
	}

	// At PhaseCommitting, candidates are already active; recovery calls FinalizePrepared (NOT RollbackPrepared or CommitPrepared)
	if len(xray.rollbackCalls) != 0 {
		t.Errorf("expected 0 rollback calls on xray, got %d", len(xray.rollbackCalls))
	}
	if len(xray.commitCalls) != 0 {
		t.Errorf("expected 0 commit calls on xray (already active), got %d", len(xray.commitCalls))
	}
	if len(xray.finalizeCalls) != 1 || xray.finalizeCalls[0] != "tx-xray-1" {
		t.Errorf("expected finalize on xray for tx-xray-1, got %v", xray.finalizeCalls)
	}
	if len(tg.finalizeCalls) != 1 || tg.finalizeCalls[0] != "tx-tg-1" {
		t.Errorf("expected finalize on tg for tx-tg-1, got %v", tg.finalizeCalls)
	}
	if len(disp.finalizeCalls) != 1 || disp.finalizeCalls[0] != "tx-disp-1" {
		t.Errorf("expected finalize on disp for tx-disp-1, got %v", disp.finalizeCalls)
	}

	// Active journal must be archived as committed
	if _, err := os.Stat(jPath); !os.IsNotExist(err) {
		t.Fatalf("active journal should not exist after recovery")
	}
	files, _ := filepath.Glob(jPath + ".committed.*")
	if len(files) != 1 {
		t.Fatalf("expected 1 committed archive, got %d", len(files))
	}
}

func TestCoordinatorStartupRecovery_PriorToCommittingRollsBack(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{}
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}

	coord := New(dataDir, xray, disp, tg)

	// Simulate a crashed transaction at PhaseCandidateActive (before point of no return)
	jPath := coord.journalPath()
	j := &TransactionJournal{
		TransactionID: "crashed-tx-2",
		Phase:         PhaseCandidateActive,
		Affected:      []string{"xray", "dispatcher"},
		ComponentTransactionIDs: map[string]string{
			"xray":       "crashed-tx-2",
			"dispatcher": "crashed-tx-2",
		},
		Previous: IngressTopology{
			DispatcherPort: 9009,
			XrayAddress:    "127.0.0.1",
			XrayPort:       9008,
		},
	}
	if err := writeJournal(jPath, j); err != nil {
		t.Fatal(err)
	}

	err := coord.StartupRecovery(context.Background())
	if err != nil {
		t.Fatalf("StartupRecovery failed: %v", err)
	}

	// Reverse rollback must be called
	if len(xray.rollbackCalls) != 1 || xray.rollbackCalls[0] != "crashed-tx-2" {
		t.Fatalf("expected rollback for crashed-tx-2, got %v", xray.rollbackCalls)
	}

	// Active journal must be archived as failed
	if _, err := os.Stat(jPath); !os.IsNotExist(err) {
		t.Fatalf("active journal should not exist after recovery")
	}
	files, _ := filepath.Glob(jPath + ".failed.*")
	if len(files) != 1 {
		t.Fatalf("expected 1 failed archive, got %d", len(files))
	}
}

func TestCoordinatorStartupRecoveryConflict(t *testing.T) {
	dataDir := t.TempDir()
	xray := &mockXray{
		cfg: vlessserver.Config{ListenPort: 9008},
	}
	coord := New(dataDir, xray, &mockDispatcher{}, &mockTgWebProxy{})

	// Write committed journal with fake fingerprint that won't match
	jPath := coord.journalPath()
	j := &TransactionJournal{
		TransactionID: "conflict-tx",
		Phase:         PhaseCommitted,
		Affected:      []string{"xray"},
		Fingerprints: map[string]string{
			"xray": "wrong-fingerprint-deadbeef",
		},
	}
	if err := writeJournal(jPath, j); err != nil {
		t.Fatal(err)
	}

	err := coord.StartupRecovery(context.Background())
	if !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict, got %v", err)
	}

	req, reason := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected recoveryRequired to be true")
	}
	if reason == "" {
		t.Fatalf("expected recoveryReason to be non-empty")
	}
}

func TestCoordinatorCorruptJournal(t *testing.T) {
	dataDir := t.TempDir()
	coord := New(dataDir, &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})

	jPath := coord.journalPath()
	_ = os.MkdirAll(filepath.Dir(jPath), 0700)
	_ = os.WriteFile(jPath, []byte("{invalid-json-content"), 0600)

	err := coord.StartupRecovery(context.Background())
	if !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("expected ErrCorruptJournal, got %v", err)
	}

	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected recoveryRequired to be true for corrupt journal")
	}
}

func TestCoordinatorFlockConflict(t *testing.T) {
	dataDir := t.TempDir()
	coord1 := New(dataDir, &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})
	coord2 := New(dataDir, &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})

	// Acquire lock with coord1
	if err := coord1.lock.Lock("test-tx-1"); err != nil {
		t.Fatalf("coord1 lock failed: %v", err)
	}
	defer coord1.lock.Unlock()

	// Try to apply with coord2 - must return lock conflict
	err := coord2.Apply(context.Background(), IngressTopology{
		XrayEnabled: true,
		XrayPort:    9008,
	})
	if !errors.Is(err, ErrLockConflict) {
		t.Fatalf("expected ErrLockConflict, got %v", err)
	}
}

func TestCoordinatorRealXrayIntegration_HappyPath(t *testing.T) {
	dataDir := t.TempDir()
	xraySvc := vlessserver.New(dataDir, nil)
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}
	coord := New(dataDir, xraySvc, disp, tg)

	desiredCfg := vlessserver.Config{
		Enabled:        false,
		ListenAddress:  "127.0.0.1",
		ListenPort:     9008,
		DispatcherPort: 9009,
		PublicDomain:   "cdn.example.org",
		Path:           "/cdn-bridge/",
		Mode:           "packet-up",
		UplinkMethod:   "GET",
		Clients: []vlessserver.Client{
			{ID: "test-client-uuid-1", Remark: "Client 1", Enabled: true},
		},
	}

	err := coord.ApplyXrayConfig(context.Background(), desiredCfg)
	if err != nil {
		t.Fatalf("ApplyXrayConfig failed: %v", err)
	}

	// Verify real Xray service config updated
	curCfg := xraySvc.GetConfig()
	if curCfg.PublicDomain != "cdn.example.org" {
		t.Errorf("expected cdn.example.org, got %s", curCfg.PublicDomain)
	}
	if len(curCfg.Clients) != 1 || curCfg.Clients[0].ID != "test-client-uuid-1" {
		t.Errorf("expected client test-client-uuid-1, got %v", curCfg.Clients)
	}

	// Verify dispatcher was reconfigured
	dispCfg := disp.GetConfig()
	if dispCfg.PublicHostname != "cdn.example.org" {
		t.Errorf("expected dispatcher host cdn.example.org, got %s", dispCfg.PublicHostname)
	}

	// Verify transaction snapshots were finalized and cleaned up
	txDir := filepath.Join(dataDir, "xray", "transactions")
	entries, _ := os.ReadDir(txDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 leftover snapshot dirs after finalization, got %d", len(entries))
	}
}

func TestCoordinatorRealXrayIntegration_DispatcherFailureRollback(t *testing.T) {
	dataDir := t.TempDir()
	xraySvc := vlessserver.New(dataDir, nil)
	disp := &mockDispatcher{
		reconfError: errors.New("dispatcher bind failure"),
		applyError:  errors.New("dispatcher bind failure"),
	}
	tg := &mockTgWebProxy{}
	coord := New(dataDir, xraySvc, disp, tg)

	// Set initial config on real Xray
	initialCfg := vlessserver.Config{
		Enabled:      false,
		ListenPort:   9008,
		PublicDomain: "initial.domain.org",
		Clients: []vlessserver.Client{
			{ID: "initial-client-uuid", Remark: "Initial", Enabled: true},
		},
	}
	if err := xraySvc.UpdateConfig(initialCfg); err != nil {
		t.Fatal(err)
	}

	// Attempt to apply new config with dispatcher failure
	desiredCfg := initialCfg
	desiredCfg.PublicDomain = "broken.domain.org"
	desiredCfg.Clients = []vlessserver.Client{
		{ID: "new-broken-uuid", Remark: "Broken", Enabled: true},
	}

	err := coord.ApplyXrayConfig(context.Background(), desiredCfg)
	if err == nil {
		t.Fatalf("expected ApplyXrayConfig to fail on dispatcher error")
	}

	// Verify real Xray service was rolled back to initial state
	curCfg := xraySvc.GetConfig()
	if curCfg.PublicDomain != "initial.domain.org" {
		t.Errorf("expected rolled back domain initial.domain.org, got %s", curCfg.PublicDomain)
	}
	if len(curCfg.Clients) != 1 || curCfg.Clients[0].ID != "initial-client-uuid" {
		t.Errorf("expected rolled back client initial-client-uuid, got %v", curCfg.Clients)
	}

	// Verify snapshots were cleaned up after rollback
	txDir := filepath.Join(dataDir, "xray", "transactions")
	entries, _ := os.ReadDir(txDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 leftover snapshot dirs after rollback, got %d", len(entries))
	}
}

func TestCoordinatorRollbackComponentErrorKeepsJournal(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg:           vlessserver.Config{ListenPort: 9008},
		rollbackError: errors.New("disk failure during rollback"),
	}
	mockDisp := &mockDispatcher{
		startError: errors.New("dispatcher start failed"),
	}
	coord := New(dataDir, mockX, mockDisp, &mockTgWebProxy{})

	err := coord.Apply(context.Background(), IngressTopology{
		XrayEnabled:       true,
		XrayPort:          9008,
		DispatcherEnabled: true,
	})
	if err == nil {
		t.Fatalf("expected Apply to fail")
	}

	// Because rollback failed, active journal MUST NOT be archived
	jPath := coord.journalPath()
	if _, statErr := os.Stat(jPath); statErr != nil {
		t.Fatalf("active journal should still exist when rollback fails: %v", statErr)
	}

	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected recoveryRequired to be true after rollback error")
	}
}

func TestResolveMigration_NoDeadlock(t *testing.T) {
	t.Run("keep_legacy_rejected", func(t *testing.T) {
		dataDir := t.TempDir()
		xraySvc := vlessserver.New(dataDir, nil)
		disp := &mockDispatcher{}
		tg := &mockTgWebProxy{}
		coord := New(dataDir, xraySvc, disp, tg)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- coord.ResolveMigration(ctx, "keep_legacy", nil)
		}()

		select {
		case err := <-done:
			if !errors.Is(err, vlessserver.ErrLegacyRuntimeRemoved) {
				t.Fatalf("ResolveMigration keep_legacy error = %v, want ErrLegacyRuntimeRemoved", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ResolveMigration keep_legacy deadlocked!")
		}
	})

	t.Run("import_legacy_draft", func(t *testing.T) {
		dataDir := t.TempDir()
		xraySvc := vlessserver.New(dataDir, nil)
		disp := &mockDispatcher{}
		tg := &mockTgWebProxy{}
		coord := New(dataDir, xraySvc, disp, tg)

		legacyCfg := &vlessserver.Config{
			Enabled:      false,
			ListenPort:   9008,
			Path:         "/imported",
			PublicDomain: "imported.cdn.net",
			Clients: []vlessserver.Client{
				{ID: "legacy-client-1", Remark: "Legacy", Enabled: true},
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- coord.ResolveMigration(ctx, "import_legacy_draft", legacyCfg)
		}()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("ResolveMigration import_legacy_draft failed: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ResolveMigration import_legacy_draft deadlocked!")
		}

		curCfg := xraySvc.GetConfig()
		if curCfg.Path != "/imported" || curCfg.PublicDomain != "imported.cdn.net" {
			t.Errorf("imported config mismatch: %+v", curCfg)
		}
	})
}

func TestCoordinatorParallelApplyXrayConfig_RaceSafe(t *testing.T) {
	dataDir := t.TempDir()
	xraySvc := vlessserver.New(dataDir, nil)
	disp := &mockDispatcher{}
	tg := &mockTgWebProxy{}
	coord := New(dataDir, xraySvc, disp, tg)

	var wg sync.WaitGroup
	wg.Add(2)

	cfg1 := vlessserver.Config{
		Enabled:      false,
		ListenPort:   9008,
		Path:         "/path1",
		PublicDomain: "cdn1.example.org",
	}
	cfg2 := vlessserver.Config{
		Enabled:      false,
		ListenPort:   9008,
		Path:         "/path2",
		PublicDomain: "cdn2.example.org",
	}

	go func() {
		defer wg.Done()
		_ = coord.ApplyXrayConfig(context.Background(), cfg1)
	}()
	go func() {
		defer wg.Done()
		_ = coord.ApplyXrayConfig(context.Background(), cfg2)
	}()

	wg.Wait()

	finalCfg := xraySvc.GetConfig()
	// Must be either cfg1 or cfg2, NOT a mixture or corrupt state
	if (finalCfg.Path == "/path1" && finalCfg.PublicDomain == "cdn1.example.org") ||
		(finalCfg.Path == "/path2" && finalCfg.PublicDomain == "cdn2.example.org") {
		// Valid candidate applied cleanly without substitution or race corruption
	} else {
		t.Fatalf("corrupted config state after parallel apply: %+v", finalCfg)
	}
}

func TestStartupRecovery_CorruptJournalBlocksServiceStart(t *testing.T) {
	dataDir := t.TempDir()
	xrayDir := filepath.Join(dataDir, "xray")
	_ = os.MkdirAll(xrayDir, 0755)
	jPath := filepath.Join(xrayDir, "server-ingress-transaction.json")
	_ = os.WriteFile(jPath, []byte("NOT_A_VALID_JSON{{{"), 0600)

	mockX := &mockXray{cfg: vlessserver.Config{Enabled: true, ListenPort: 9008}}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{cfg: tgwebproxy.Config{Enabled: true}}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	err := coord.StartupRecovery(context.Background())
	if err == nil {
		t.Fatalf("expected StartupRecovery to return error on corrupt journal")
	}
	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected IsRecoveryRequired to be true")
	}

	// Verify fail-closed wiring condition: services must NOT start if recovery failed or is required
	canStart := (err == nil && !req)
	if canStart {
		t.Fatalf("fail-closed violation: canStart must be false")
	}
}

func TestCoordinatorFinalizePreparedErrorPreservesState(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg:           vlessserver.Config{ListenPort: 9008},
		finalizeError: errors.New("cleanup disk error"),
	}
	mockDisp := &mockDispatcher{}
	coord := New(dataDir, mockX, mockDisp, &mockTgWebProxy{})

	err := coord.Apply(context.Background(), IngressTopology{
		XrayEnabled:       true,
		XrayPort:          9008,
		DispatcherEnabled: true,
		DispatcherPort:    9009,
	})
	if err == nil {
		t.Fatalf("expected Apply to fail on FinalizePrepared error")
	}

	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected recoveryRequired to be true after finalize error")
	}

	// Verify active journal is NOT archived, preserved for subsequent cleanup
	jPath := coord.journalPath()
	if _, statErr := os.Stat(jPath); statErr != nil {
		t.Fatalf("active journal should still exist for recovery after finalize error: %v", statErr)
	}
}

func TestCoordinatorRollbackDispatcherStartErrorKeepsJournal(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg: vlessserver.Config{ListenPort: 9008},
	}
	mockDisp := &mockDispatcher{
		running:    true,
		startError: errors.New("dispatcher restart failed during rollback"),
	}
	mockTG := &mockTgWebProxy{
		updateError: errors.New("tgwebproxy update failure triggering rollback"),
		applyError:  errors.New("tgwebproxy update failure triggering rollback"),
	}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	// Step 3 will stop dispatcher (setting running = false), then fail on tgwebproxy.
	// Rollback will attempt to restart dispatcher (calling Start()) which will fail with startError!
	err := coord.Apply(context.Background(), IngressTopology{
		XrayEnabled:       true,
		XrayPort:          9008,
		DispatcherEnabled: false,
		TgEnabled:         true,
	})
	if err == nil {
		t.Fatalf("expected Apply to fail")
	}

	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Fatalf("expected recoveryRequired to be true after rollback dispatcher error")
	}

	jPath := coord.journalPath()
	if _, statErr := os.Stat(jPath); statErr != nil {
		t.Fatalf("active journal should still exist when rollback dispatcher fails: %v", statErr)
	}
}

func TestCoordinatorHostnameClearing(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg: vlessserver.Config{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "initial.domain.com",
		},
	}
	mockDisp := &mockDispatcher{
		cfg: cdndispatcher.Config{
			PublicHostname: "initial.domain.com",
		},
		running: true,
	}
	mockTG := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:        true,
			ListenPort:     8085,
			PublicHostname: "initial.domain.com",
		},
	}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	// Apply Xray config with empty PublicDomain
	clearedCfg := vlessserver.Config{
		Enabled:        true,
		ListenPort:     9008,
		PublicDomain:   "", // Cleared domain!
		DispatcherPort: 9009,
	}

	err := coord.ApplyXrayConfig(context.Background(), clearedCfg)
	if err != nil {
		t.Fatalf("ApplyXrayConfig failed: %v", err)
	}

	// Dispatcher config PublicHostname must be empty
	if mockDisp.cfg.PublicHostname != "" {
		t.Errorf("expected dispatcher hostname to be cleared, got %q", mockDisp.cfg.PublicHostname)
	}

	// TG managed config PublicHostname must be empty
	if mockTG.cfg.PublicHostname != "" {
		t.Errorf("expected tgwebproxy hostname to be cleared, got %q", mockTG.cfg.PublicHostname)
	}
}

func TestResolveMigration_KeepNewAndKeepLegacyRejection(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg: vlessserver.Config{
			Enabled:    true,
			ListenPort: 9008,
		},
	}
	mockDisp := &mockDispatcher{running: true}
	mockTG := &mockTgWebProxy{cfg: tgwebproxy.Config{Enabled: false}}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	if err := coord.ResolveMigration(context.Background(), "keep_legacy", nil); !errors.Is(err, vlessserver.ErrLegacyRuntimeRemoved) {
		t.Fatalf("ResolveMigration(keep_legacy) error = %v, want ErrLegacyRuntimeRemoved", err)
	}

	if err := coord.ResolveMigration(context.Background(), "keep_new", nil); err != nil {
		t.Fatalf("ResolveMigration(keep_new) failed: %v", err)
	}
	dec, err := vlessserver.GetRuntimeDecision(dataDir)
	if err != nil {
		t.Fatalf("GetRuntimeDecision failed: %v", err)
	}
	if dec.ActiveGeneration != "new" || dec.MigrationStatus != "migrated" {
		t.Errorf("unexpected runtime decision: %+v", dec)
	}
}

type mockLocker struct {
	lockErr   error
	unlockErr error
}

func (m *mockLocker) Lock(txID string) error { return m.lockErr }
func (m *mockLocker) Unlock() error          { return m.unlockErr }

func TestCoordinatorWithIngressLock_UnlockFailureSetsRecovery(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	customLock := &mockLocker{
		unlockErr: errors.New("simulated unlock disk sync failure"),
	}
	coord.SetLock(customLock)

	err := coord.Apply(context.Background(), IngressTopology{
		XrayEnabled: true,
		XrayPort:    9008,
	})
	if err == nil {
		t.Fatalf("expected Apply to return error on unlock failure")
	}

	req, reason := coord.IsRecoveryRequired()
	if !req {
		t.Errorf("expected recoveryRequired to be true when unlock fails")
	}
	if reason == "" {
		t.Errorf("expected non-empty recovery reason")
	}
}

func setupTestProcDir(t *testing.T, coord *Coordinator) {
	t.Helper()
	procWithNet := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procWithNet, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procWithNet, "net", "tcp"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	coord.SetProcDir(procWithNet)
}

func TestCoordinator_JournalWriteFailureAfterPrepare_Xray(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseStaged && j.ComponentTransactionIDs["xray"] != "" {
			return errors.New("simulated disk full after xray prepare")
		}
		return writeJournal(path, j)
	}, nil)

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-fault-xray",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
	})
	if err == nil {
		t.Fatalf("expected transaction to fail on journal write error")
	}

	if len(mockX.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on xray, got %d", len(mockX.rollbackCalls))
	}
	if len(mockX.commitCalls) != 0 {
		t.Errorf("expected 0 commit calls on xray, got %d", len(mockX.commitCalls))
	}
}

func TestCoordinator_JournalWriteFailureAfterPrepare_Telegram(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseStaged && j.ComponentTransactionIDs["tgwebproxy"] != "" {
			return errors.New("simulated disk full after tg prepare")
		}
		return writeJournal(path, j)
	}, nil)

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-fault-tg",
		ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       "cdn_http",
			ListenPort:     8085,
			PublicHostname: "tg.example.org",
		},
	})
	if err == nil {
		t.Fatalf("expected transaction to fail on tg journal write error")
	}

	if len(mockTG.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on tg, got %d", len(mockTG.rollbackCalls))
	}
	if len(mockTG.commitCalls) != 0 {
		t.Errorf("expected 0 commit calls on tg, got %d", len(mockTG.commitCalls))
	}
}

func TestCoordinator_JournalWriteFailureAfterPrepare_Dispatcher(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseStaged && j.ComponentTransactionIDs["dispatcher"] != "" {
			return errors.New("simulated disk full after dispatcher prepare")
		}
		return writeJournal(path, j)
	}, nil)

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-fault-disp",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
	})
	if err == nil {
		t.Fatalf("expected transaction to fail on dispatcher journal write error")
	}

	if len(mockDisp.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on dispatcher, got %d", len(mockDisp.rollbackCalls))
	}
	if len(mockX.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on xray, got %d", len(mockX.rollbackCalls))
	}
}

func TestCoordinator_PrePointOfNoReturn_PhaseCommittingWriteFailure_RollsBack(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	callbackCalled := false
	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseCommitting {
			return errors.New("simulated failure writing PhaseCommitting journal")
		}
		return writeJournal(path, j)
	}, nil)

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-pre-ponr-fail",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
		OnPointOfNoReturn: func(txID string) error {
			callbackCalled = true
			return nil
		},
	})
	if err == nil {
		t.Fatalf("expected error on PhaseCommitting write failure")
	}

	// Post-boundary callback must NOT have been called
	if callbackCalled {
		t.Errorf("expected OnPointOfNoReturn NOT to be called when PhaseCommitting write fails")
	}

	// Candidates were activated during PhaseCandidateActive, so they must be rolled back!
	if len(mockX.commitCalls) != 1 {
		t.Errorf("expected 1 commit (activation) call on xray, got %d", len(mockX.commitCalls))
	}
	if len(mockX.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on xray after committing write failure, got %d", len(mockX.rollbackCalls))
	}
	if len(mockDisp.rollbackCalls) != 1 {
		t.Errorf("expected 1 rollback call on dispatcher, got %d", len(mockDisp.rollbackCalls))
	}

	// Because rollback and archive succeeded cleanly, no ErrRecoveryRequired is returned
	if errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected clean pre-commit rollback error, got ErrRecoveryRequired: %v", err)
	}
}

func TestCoordinator_PrePointOfNoReturn_PhaseCommittingWriteFailure_WithRollbackPersistFailure_TriggersRecovery(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseCommitting {
			return errors.New("simulated failure writing PhaseCommitting")
		}
		if j.Phase == PhaseRollingBack {
			return errors.New("simulated failure writing PhaseRollingBack")
		}
		return writeJournal(path, j)
	}, func(srcPath, reason, txID string) error {
		return errors.New("simulated failure archiving failed journal")
	})

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-rollback-persist-fail",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
	})
	if err == nil {
		t.Fatalf("expected error")
	}

	// When rollback persistence fails, recovery is required
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on rollback persistence failure, got: %v", err)
	}
	req, _ := coord.IsRecoveryRequired()
	if !req {
		t.Errorf("expected coord.IsRecoveryRequired() to be true")
	}
}

func TestCoordinator_PostPointOfNoReturn_FinalizeFailure_PreservesCommittedAndTriggersRecovery(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{finalizeError: errors.New("simulated finalize disk failure")}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	callbackCalled := false
	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-post-ponr-fin-fail",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
		OnPointOfNoReturn: func(txID string) error {
			callbackCalled = true
			return nil
		},
	})
	if err == nil {
		t.Fatalf("expected error on finalize failure")
	}

	if !callbackCalled {
		t.Errorf("expected OnPointOfNoReturn to be called before finalize")
	}
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired, got %v", err)
	}
	// Past PONR, rollback must NEVER be called
	if len(mockX.rollbackCalls) != 0 {
		t.Errorf("expected 0 rollback calls on xray post-PONR, got %d", len(mockX.rollbackCalls))
	}
}

func TestCoordinator_PostPointOfNoReturn_PhaseCommittedWriteFailure_TriggersRecovery(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(func(path string, j *TransactionJournal) error {
		if j.Phase == PhaseCommitted {
			return errors.New("simulated failure writing PhaseCommitted")
		}
		return writeJournal(path, j)
	}, nil)

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-post-ponr-commit-write-fail",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired, got %v", err)
	}
}

func TestCoordinator_PostPointOfNoReturn_ArchiveFailure_TriggersRecovery(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	coord.SetJournalSeamsForTest(nil, func(srcPath, reason, txID string) error {
		if reason == "committed" {
			return errors.New("simulated failure archiving committed journal")
		}
		return archiveJournal(srcPath, reason, txID)
	})

	err := coord.ExecuteIngressTransaction(context.Background(), IngressTransactionParams{
		TxID:       "tx-post-ponr-arch-fail",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:      true,
			ListenPort:   9008,
			PublicDomain: "xray.example.org",
			PublicPort:   443,
			Path:         "/cdn-bridge",
			OutboundMode: "direct",
			ClientRemark: "Test",
			ClientUUID:   "00000000-0000-0000-0000-000000000001",
		},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired, got %v", err)
	}
}

func TestCoordinator_NoDeadlockOnIsRecoveryRequiredUnderLock(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockDisp := &mockDispatcher{}
	mockTG := &mockTgWebProxy{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	done := make(chan struct{})
	go func() {
		defer close(done)
		err := coord.WithIngressLock("tx-deadlock-test", func() error {
			// This must not deadlock!
			needed, _ := coord.IsRecoveryRequired()
			if needed {
				t.Errorf("unexpected recovery needed")
			}
			can, _ := coord.CanAutoStart()
			if !can {
				t.Errorf("unexpected canAutoStart false")
			}
			return nil
		})
		if err != nil {
			t.Errorf("WithIngressLock failed: %v", err)
		}
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatalf("deadlock detected: WithIngressLock hung when calling IsRecoveryRequired/CanAutoStart")
	}
}

func TestCoordinator_DispatcherHydrationAndApply(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{
		cfg: vlessserver.Config{
			Enabled:      true,
			PublicDomain: "cdn1.example.com, cdn2.example.com",
		},
	}
	mockTG := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:        true,
			PublicHostname: "cdn2.example.com, cdn3.example.com",
		},
	}
	mockDisp := &mockDispatcher{
		cfg: cdndispatcher.Config{
			ListenAddr: ":9009",
		},
	}

	coord := New(dataDir, mockX, mockDisp, mockTG)

	// 1. Test HydrateDispatcherRoutes
	ctx := context.Background()
	if err := coord.HydrateDispatcherRoutes(ctx); err != nil {
		t.Fatalf("failed to hydrate routes: %v", err)
	}

	dispCfg := coord.GetDispatcherConfig()
	if len(dispCfg.Routes) != 3 {
		t.Fatalf("expected 3 deduplicated routes, got %d", len(dispCfg.Routes))
	}

	// Verify route rules: cdn1 has xray, cdn2 has both, cdn3 has tg
	var r1, r2, r3 *cdndispatcher.CDNRouteRule
	for i := range dispCfg.Routes {
		r := &dispCfg.Routes[i]
		switch r.Domain {
		case "cdn1.example.com":
			r1 = r
		case "cdn2.example.com":
			r2 = r
		case "cdn3.example.com":
			r3 = r
		}
	}

	if r1 == nil || !r1.RouteXray || r1.RouteTelegram {
		t.Errorf("unexpected r1 flags: %+v", r1)
	}
	if r2 == nil || !r2.RouteXray || !r2.RouteTelegram {
		t.Errorf("unexpected r2 flags (should have both): %+v", r2)
	}
	if r3 == nil || r3.RouteXray || !r3.RouteTelegram {
		t.Errorf("unexpected r3 flags: %+v", r3)
	}

	// 2. Test ApplyDispatcherConfig with new routes and verify 1-way projection
	newRoutes := []cdndispatcher.CDNRouteRule{
		{
			ID:            "r-new",
			Domain:        "custom.newcdn.org",
			Description:   "New CDN",
			RouteXray:     true,
			RouteTelegram: true,
			Enabled:       true,
		},
	}
	newCfg := cdndispatcher.Config{
		Enabled:       true,
		ListenAddr:    ":9009",
		DefaultAction: "reject",
		Routes:        newRoutes,
	}

	if err := coord.ApplyDispatcherConfig(ctx, newCfg); err != nil {
		t.Fatalf("failed to apply dispatcher config: %v", err)
	}

	// Verify projection into Xray
	xCfg := mockX.GetConfig()
	if xCfg.PublicDomain != "custom.newcdn.org" {
		t.Errorf("expected projected public domain 'custom.newcdn.org', got %q", xCfg.PublicDomain)
	}

	// Verify projection into Telegram
	tgCfg := mockTG.GetInternalConfig()
	if tgCfg.PublicHostname != "custom.newcdn.org" {
		t.Errorf("expected projected tg public hostname 'custom.newcdn.org', got %q", tgCfg.PublicHostname)
	}
}

func TestCoordinator_FreshTopologyPopulatesDispatcherRoutesAndDefaultAction(t *testing.T) {
	dataDir := t.TempDir()
	mockX := &mockXray{}
	mockTG := &mockTgWebProxy{}
	mockDisp := &mockDispatcher{}
	coord := New(dataDir, mockX, mockDisp, mockTG)

	ctx := context.Background()
	topo := IngressTopology{
		XrayEnabled:        true,
		TgEnabled:          true,
		TgScenario:         "dual",
		TgDirectAddress:    "connect.example.netcraze.club",
		TgDirectPort:       8443,
		DispatcherEnabled:  true,
		DispatcherPort:     9009,
		PublicHostname:     "connect.example.netcraze.club",
		XrayPublicHostname: "connect.example.netcraze.club",
		TgPublicHostname:   "connect.example.netcraze.club",
		XrayPathPrefix:     "/xhttp-bridge",
	}

	if err := coord.Apply(ctx, topo); err != nil {
		t.Fatalf("Apply failed on clean state: %v", err)
	}

	dispCfg := coord.GetDispatcherConfig()
	if dispCfg.DefaultAction != "404" {
		t.Errorf("expected default_action='404' on fresh topology apply, got %q", dispCfg.DefaultAction)
	}
	if len(dispCfg.Routes) != 1 {
		t.Fatalf("expected 1 populated dispatcher route immediately after apply, got %d (%+v)", len(dispCfg.Routes), dispCfg.Routes)
	}
	r := dispCfg.Routes[0]
	if r.Domain != "connect.example.netcraze.club" || !r.RouteXray || !r.RouteTelegram || !r.Enabled {
		t.Errorf("unexpected hydrated route rule: %+v", r)
	}

	// Disabling Xray via topology updates route flags cleanly instead of leaving stale VLESS route active
	topo.XrayEnabled = false
	topo.XrayPublicHostname = ""
	if err := coord.Apply(ctx, topo); err != nil {
		t.Fatalf("Apply disabling xray failed: %v", err)
	}
	dispCfg2 := coord.GetDispatcherConfig()
	if len(dispCfg2.Routes) != 1 || dispCfg2.Routes[0].RouteXray || !dispCfg2.Routes[0].RouteTelegram || !dispCfg2.Routes[0].Enabled {
		t.Errorf("expected RouteXray cleared and RouteTelegram preserved, got %+v", dispCfg2.Routes)
	}
}

func TestCoordinator_HydrateDispatcherPreservesXrayPathPrefixWhenRoutesEmpty(t *testing.T) {
	dataDir := t.TempDir()
	dispDir := filepath.Join(dataDir, "dispatcher")
	if err := os.MkdirAll(dispDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Simulate persisted config without routes (e.g. upgraded from build before routes hydration)
	legacySaved := `{
  "enabled": true,
  "listen_addr": ":9009",
  "xray_target": "http://127.0.0.1:9008",
  "tg_target": "http://127.0.0.1:8085",
  "public_hostname": "connect.example.netcraze.club",
  "xray_public_host": "connect.example.netcraze.club",
  "tg_public_host": "connect.example.netcraze.club",
  "xray_path_prefix": "/xhttp-bridge"
}`
	if err := os.WriteFile(filepath.Join(dispDir, "config.json"), []byte(legacySaved), 0600); err != nil {
		t.Fatal(err)
	}

	mockX := &mockXray{
		cfg: vlessserver.Config{
			Enabled:      true,
			PublicDomain: "connect.example.netcraze.club",
			Path:         "/xhttp-bridge",
		},
	}
	mockTG := &mockTgWebProxy{
		cfg: tgwebproxy.Config{
			Enabled:        true,
			PublicHostname: "connect.example.netcraze.club",
		},
	}
	// Simulate boot default /cdn-bridge in memory before HydrateDispatcherRoutes runs
	mockDisp := &mockDispatcher{
		cfg: cdndispatcher.Config{
			ListenAddr:     ":9009",
			XrayPathPrefix: "/cdn-bridge",
		},
	}

	coord := New(dataDir, mockX, mockDisp, mockTG)
	if err := coord.HydrateDispatcherRoutes(context.Background()); err != nil {
		t.Fatalf("HydrateDispatcherRoutes failed: %v", err)
	}

	got := coord.GetDispatcherConfig()
	if got.XrayPathPrefix != "/xhttp-bridge" {
		t.Fatalf("expected XrayPathPrefix '/xhttp-bridge' to be preserved across hydration, got %q", got.XrayPathPrefix)
	}
	if len(got.Routes) != 1 || got.Routes[0].Domain != "connect.example.netcraze.club" || !got.Routes[0].RouteXray || !got.Routes[0].RouteTelegram {
		t.Fatalf("expected hydrated route for connect.example.netcraze.club, got %+v", got.Routes)
	}
	if got.DefaultAction != "404" {
		t.Fatalf("expected default_action '404', got %q", got.DefaultAction)
	}
}
