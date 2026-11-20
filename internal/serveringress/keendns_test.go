package serveringress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

type fakeKeenDNSState struct {
	mu           sync.Mutex
	status       *ndmsquery.KeenDNSStatus
	statusErr    error
	sslEnabled   bool
	entries      map[string]ndmsquery.HTTPProxyEntry
	runtime      map[string]ndmsquery.HTTPProxyRuntimeEntry
	fetchErr     error
	upsertErr    error
	writeOnErr   bool // simulates RCI timeout where write actually landed on router
	tamperAfter  func(entry *ndmsquery.HTTPProxyEntry)
	deleteErr    error
	keepOnDelete bool // simulates delete returning 200 OK while entry remains in /show/rc/ip/http
	restoreErr   error
	saveErr      error
	upsertCount  int
	deleteCount  int
	restoreCount int
	saveCount    int
}

func newFakeKeenDNSState() *fakeKeenDNSState {
	return &fakeKeenDNSState{
		status: &ndmsquery.KeenDNSStatus{
			Enabled:          true,
			Name:             "demo-router",
			Domain:           "keenetic.pro",
			FQDN:             "example.keenetic.pro",
			DirectAccessible: false,
			Mode:             "cloud",
			AcmeV2:           true,
		},
		sslEnabled: true,
		entries:    make(map[string]ndmsquery.HTTPProxyEntry),
		runtime:    make(map[string]ndmsquery.HTTPProxyRuntimeEntry),
	}
}

func (f *fakeKeenDNSState) GetStatus(ctx context.Context) (*ndmsquery.KeenDNSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.status == nil {
		return nil, nil
	}
	cp := *f.status
	return &cp, nil
}

func (f *fakeKeenDNSState) FetchConfigUncached(ctx context.Context) (*ndmsquery.HTTPProxyConfigSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	out := &ndmsquery.HTTPProxyConfigSnapshot{
		SSLEnabled:    f.sslEnabled,
		PublicEnabled: true,
	}
	for _, e := range f.entries {
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

func (f *fakeKeenDNSState) FetchRuntimeUncached(ctx context.Context) (map[string]ndmsquery.HTTPProxyRuntimeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]ndmsquery.HTTPProxyRuntimeEntry, len(f.runtime))
	for k, v := range f.runtime {
		out[k] = v
	}
	return out, nil
}

func (f *fakeKeenDNSState) UpsertPublication(ctx context.Context, spec ndmscommand.HTTPProxyPublicationSpec) error {
	if err := ndmscommand.ValidatePublicationSpec(spec); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCount++
	if f.upsertErr != nil && !f.writeOnErr {
		return f.upsertErr
	}

	rawMap := map[string]any{
		"upstream": map[string]any{
			"proto":    "http",
			"upstream": spec.UpstreamHost,
			"port":     strconv.Itoa(spec.UpstreamPort),
		},
		"domain":         map[string]any{"ndns": true},
		"ssl":            map[string]any{"redirect": true},
		"security-level": map[string]any{"public": true},
		"preserve-host":  true,
	}
	if spec.BaseRawJSON != "" {
		var base map[string]any
		if json.Unmarshal([]byte(spec.BaseRawJSON), &base) == nil {
			for k, v := range base {
				if _, exists := rawMap[k]; !exists {
					rawMap[k] = v
				}
			}
		}
	}
	rawBytes, _ := json.Marshal(rawMap)

	entry := ndmsquery.HTTPProxyEntry{
		Name:          spec.Label,
		UpstreamProto: "http",
		UpstreamHost:  spec.UpstreamHost,
		UpstreamPort:  strconv.Itoa(spec.UpstreamPort),
		DomainNDNS:    true,
		SSLRedirect:   true,
		Public:        true,
		Auth:          false,
		PreserveHost:  true,
		RawJSON:       string(rawBytes),
	}
	if f.tamperAfter != nil {
		f.tamperAfter(&entry)
	}
	f.entries[spec.Label] = entry
	if f.status != nil && f.status.FQDN != "" {
		f.runtime[spec.Label] = ndmsquery.HTTPProxyRuntimeEntry{
			Name:     spec.Label,
			FQDN:     spec.Label + "." + f.status.FQDN,
			Proto:    "http",
			Upstream: fmt.Sprintf("%s:%d", entry.UpstreamHost, spec.UpstreamPort),
			Host:     entry.UpstreamHost,
			Allow:    "public",
			NDNS:     entry.DomainNDNS,
		}
	}

	if f.upsertErr != nil && f.writeOnErr {
		return f.upsertErr
	}
	return nil
}

func (f *fakeKeenDNSState) DeletePublication(ctx context.Context, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCount++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if !f.keepOnDelete {
		delete(f.entries, label)
		delete(f.runtime, label)
	}
	return nil
}

func (f *fakeKeenDNSState) RestorePublicationRaw(ctx context.Context, label string, rawJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCount++
	if f.restoreErr != nil {
		return f.restoreErr
	}
	e := f.entries[label]
	e.Name = label
	e.RawJSON = rawJSON
	f.entries[label] = e
	return nil
}

func (f *fakeKeenDNSState) SaveConfig(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCount++
	return f.saveErr
}

func TestNDMSKeenDNSOperator_DiscoverStates(t *testing.T) {
	ctx := context.Background()

	// 1. Unsupported (404)
	state := newFakeKeenDNSState()
	state.statusErr = &transport.HTTPError{Status: http.StatusNotFound}
	op := NewNDMSKeenDNSOperator(state, state, state)
	disc, err := op.Discover(ctx, 9009)
	if err != nil || disc.State != "unsupported" {
		t.Fatalf("expected unsupported on 404, got %+v, %v", disc, err)
	}

	// 2. Forbidden (403)
	state.statusErr = &transport.HTTPError{Status: http.StatusForbidden}
	disc, err = op.Discover(ctx, 9009)
	if err != nil || disc.State != "forbidden" {
		t.Fatalf("expected forbidden on 403, got %+v, %v", disc, err)
	}

	// 3. Unconfigured
	state.statusErr = nil
	state.status = &ndmsquery.KeenDNSStatus{Enabled: false}
	disc, err = op.Discover(ctx, 9009)
	if err != nil || disc.State != "unconfigured" {
		t.Fatalf("expected unconfigured, got %+v, %v", disc, err)
	}

	// 4. Direct vs Cloud mode + existing publications
	state.status = &ndmsquery.KeenDNSStatus{
		Enabled:          true,
		Name:             "demo-router",
		Domain:           "keenetic.pro",
		FQDN:             "example.keenetic.pro",
		DirectAccessible: true,
		Mode:             "direct",
		AcmeV2:           true,
	}
	state.entries["tg"] = ndmsquery.HTTPProxyEntry{
		Name:          "tg",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		Public:        true,
		Auth:          false,
		RawJSON:       `{"upstream":{"proto":"http","upstream":"127.0.0.1","port":"9009"}}`,
	}
	state.entries["nas"] = ndmsquery.HTTPProxyEntry{
		Name:          "nas",
		UpstreamProto: "http",
		UpstreamHost:  "192.168.1.50",
		UpstreamPort:  "5000",
		DomainNDNS:    true,
		Public:        true,
		Auth:          true,
		RawJSON:       `{"upstream":{"proto":"http","upstream":"192.168.1.50","port":"5000"},"auth":true}`,
	}
	disc, err = op.Discover(ctx, 9009)
	if err != nil || disc.State != "supported" || disc.AccessMode != "direct" || disc.BaseFQDN != "example.keenetic.pro" {
		t.Fatalf("unexpected discovery: %+v, %v", disc, err)
	}
	if len(disc.Publications) != 2 {
		t.Fatalf("expected 2 publications, got %d", len(disc.Publications))
	}
}

func TestNDMSKeenDNSOperator_ValidationConflictReuseAndIdempotency(t *testing.T) {
	ctx := context.Background()
	state := newFakeKeenDNSState()
	op := NewNDMSKeenDNSOperator(state, state, state)
	op.SetAdminPortProvider(func() int { return 2222 })

	// 1. Invalid label rejected
	if _, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "bad.label", UpstreamPort: 9009}); !errors.Is(err, ndmscommand.ErrInvalidKeenDNSLabel) {
		t.Fatalf("expected ErrInvalidKeenDNSLabel, got: %v", err)
	}

	// 2. Admin port 2222 rejected
	if _, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 2222}); !errors.Is(err, ndmscommand.ErrForbiddenPublicationPort) {
		t.Fatalf("expected ErrForbiddenPublicationPort for 2222, got: %v", err)
	}

	// 3. Conflict with foreign rule on router
	state.entries["nas"] = ndmsquery.HTTPProxyEntry{
		Name:         "nas",
		UpstreamHost: "192.168.1.50",
		UpstreamPort: "8080",
		DomainNDNS:   true,
		Public:       true,
		RawJSON:      `{"upstream":{"upstream":"192.168.1.50","port":"8080"}}`,
	}
	if _, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "nas", UpstreamPort: 9009}); !errors.Is(err, ErrKeenDNSConflict) {
		t.Fatalf("expected ErrKeenDNSConflict for foreign rule, got: %v", err)
	}

	// 4. Create new publication + double apply idempotency + reuse existing compatible publication
	rec, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if rec.ExistedBefore || !rec.CreatedByTx || rec.FQDN != "connect.example.keenetic.pro" {
		t.Fatalf("unexpected prepared record: %+v", rec)
	}
	if err := op.ApplyAndVerifyPublication(ctx, &rec); err != nil {
		t.Fatalf("ApplyAndVerifyPublication: %v", err)
	}

	// Second prepare on already applied publication -> reuses cleanly without conflict!
	rec2, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("second PreparePublication: %v", err)
	}
	if !rec2.ExistedBefore || rec2.CreatedByTx {
		t.Fatalf("expected second prepare to detect existing compatible publication: %+v", rec2)
	}
	if err := op.ApplyAndVerifyPublication(ctx, &rec2); err != nil {
		t.Fatalf("second ApplyAndVerifyPublication: %v", err)
	}
}

func TestNDMSKeenDNSOperator_FaultInjectionAndRollback(t *testing.T) {
	ctx := context.Background()

	// Case 1: Timeout with unconfirmed write on router (AppliedRaw=="", AppliedFingerprint=="")
	// -> Rollback refuses to blindly delete an entry whose origin was never confirmed by post-write readback
	// and enters safe ErrRecoveryConflict instead.
	state := newFakeKeenDNSState()
	state.upsertErr = errors.New("rci request timeout after 5s")
	state.writeOnErr = true
	op := NewNDMSKeenDNSOperator(state, state, state)

	rec, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if err := op.ApplyAndVerifyPublication(ctx, &rec); err == nil {
		t.Fatalf("expected timeout error from ApplyAndVerifyPublication")
	}
	if !rec.WriteAttempted || rec.AppliedRaw != "" || rec.AppliedFingerprint != "" {
		t.Fatalf("expected WriteAttempted=true and empty AppliedRaw/AppliedFingerprint on timeout, got %+v", rec)
	}
	if _, exists := state.entries["connect"]; !exists {
		t.Fatalf("expected simulated timeout write to have landed on router")
	}
	if err := op.RollbackPublication(ctx, rec); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict on ambiguous timeout without post-write readback, got: %v", err)
	}
	if state.deleteCount != 0 {
		t.Fatalf("expected 0 deletes in ambiguous timeout window, got %d", state.deleteCount)
	}
	if _, exists := state.entries["connect"]; !exists {
		t.Fatalf("expected entry on router to be preserved for manual recovery when origin is unconfirmed")
	}

	// Case 2: Readback mismatch -> fails and rolls back
	state2 := newFakeKeenDNSState()
	state2.tamperAfter = func(entry *ndmsquery.HTTPProxyEntry) {
		entry.UpstreamPort = "8080" // Mismatched port in readback
	}
	op2 := NewNDMSKeenDNSOperator(state2, state2, state2)
	rec2, _ := op2.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err := op2.ApplyAndVerifyPublication(ctx, &rec2); !errors.Is(err, ErrKeenDNSReadbackMismatch) {
		t.Fatalf("expected ErrKeenDNSReadbackMismatch, got: %v", err)
	}

	// Case 3: SaveConfig error -> fails and rolls back
	state3 := newFakeKeenDNSState()
	state3.saveErr = errors.New("flash write busy")
	op3 := NewNDMSKeenDNSOperator(state3, state3, state3)
	rec3, _ := op3.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err := op3.ApplyAndVerifyPublication(ctx, &rec3); err == nil {
		t.Fatalf("expected SaveConfig error")
	}
	state3.saveErr = nil
	if err := op3.RollbackPublication(ctx, rec3); err != nil {
		t.Fatalf("RollbackPublication after save error: %v", err)
	}
	if _, exists := state3.entries["connect"]; exists {
		t.Fatalf("expected connect deleted on rollback")
	}

	// Case 4: Concurrent external edit during transaction -> Rollback refuses to clobber and returns ErrRecoveryConflict
	state4 := newFakeKeenDNSState()
	op4 := NewNDMSKeenDNSOperator(state4, state4, state4)
	rec4, _ := op4.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err := op4.ApplyAndVerifyPublication(ctx, &rec4); err != nil {
		t.Fatalf("ApplyAndVerifyPublication: %v", err)
	}
	// Simulate concurrent external edit changing connect to point to 192.168.1.99:3000
	state4.entries["connect"] = ndmsquery.HTTPProxyEntry{
		Name:         "connect",
		UpstreamHost: "192.168.1.99",
		UpstreamPort: "3000",
		DomainNDNS:   true,
		Public:       true,
		RawJSON:      `{"upstream":{"upstream":"192.168.1.99","port":"3000"}}`,
	}
	if err := op4.RollbackPublication(ctx, rec4); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict on concurrent external edit, got: %v", err)
	}
	if _, exists := state4.entries["connect"]; !exists {
		t.Fatalf("concurrently edited publication must NOT be deleted")
	}

	// Case 5: Shared ownership (ExistedBefore == true) -> Rollback never deletes shared publication
	state5 := newFakeKeenDNSState()
	op5 := NewNDMSKeenDNSOperator(state5, state5, state5)
	firstRec, _ := op5.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "shared", UpstreamPort: 9009, Services: []string{"xray"}})
	_ = op5.ApplyAndVerifyPublication(ctx, &firstRec)

	sharedRec, err := op5.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "shared", UpstreamPort: 9009, Services: []string{"xray", "tgwebproxy"}})
	if err != nil || !sharedRec.ExistedBefore || sharedRec.CreatedByTx {
		t.Fatalf("expected shared publication to have ExistedBefore=true, CreatedByTx=false: %+v, %v", sharedRec, err)
	}
	_ = op5.ApplyAndVerifyPublication(ctx, &sharedRec)
	if err := op5.RollbackPublication(ctx, sharedRec); err != nil {
		t.Fatalf("RollbackPublication on shared publication: %v", err)
	}
	if _, exists := state5.entries["shared"]; !exists {
		t.Fatalf("shared publication must remain intact after rollback of second service")
	}
}

func TestCoordinator_KeenDNSTransactionAndRecovery(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	xrayMock := &mockXray{}
	dispMock := &mockDispatcher{}
	tgMock := &mockTgWebProxy{}
	kdState := newFakeKeenDNSState()
	kdOp := NewNDMSKeenDNSOperator(kdState, kdState, kdState)

	coord := New(dataDir, xrayMock, dispMock, tgMock)
	coord.SetKeenDNS(kdOp)

	// 1. Rollback when pre-committing hook fails AFTER KeenDNS publication was written
	err := coord.ExecuteIngressTransaction(ctx, IngressTransactionParams{
		TxID:       "tx-keendns-rb",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
			PublicDomain:  "connect.example.keenetic.pro",
			PublicPort:    443,
			Path:          "/cdn-bridge/",
			OutboundMode:  "direct",
			ClientRemark:  "Phone",
			ClientUUID:    "00000000-0000-4000-8000-000000000001",
		},
		KeenDNS: &KeenDNSPublicationCandidate{
			Label:        "connect",
			UpstreamHost: "127.0.0.1",
			UpstreamPort: 9009,
			Services:     []string{"xray"},
		},
		OnPhaseChange: func(phase JournalPhase) error {
			// Verify KeenDNS publication was applied before PhaseCommitting
			if _, ok := kdState.entries["connect"]; !ok {
				return errors.New("expected connect publication to exist before PhaseCommitting")
			}
			return errors.New("simulated pre-commit failure")
		},
	})
	if err == nil {
		t.Fatalf("expected error from OnPhaseChange")
	}
	if _, exists := kdState.entries["connect"]; exists {
		t.Fatalf("expected KeenDNS publication rolled back after pre-commit failure")
	}

	// 2. Happy path commit
	err = coord.ExecuteIngressTransaction(ctx, IngressTransactionParams{
		TxID:       "tx-keendns-ok",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
			PublicDomain:  "connect.example.keenetic.pro",
			PublicPort:    443,
			Path:          "/cdn-bridge/",
			OutboundMode:  "direct",
			ClientRemark:  "Phone",
			ClientUUID:    "00000000-0000-4000-8000-000000000001",
		},
		KeenDNS: &KeenDNSPublicationCandidate{
			Label:        "connect",
			UpstreamHost: "127.0.0.1",
			UpstreamPort: 9009,
			Services:     []string{"xray"},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteIngressTransaction happy path: %v", err)
	}
	if _, exists := kdState.entries["connect"]; !exists {
		t.Fatalf("expected connect publication committed")
	}

	// 3a. Startup recovery from crashed uncommitted journal in write_pending (WriteAttempted=true, AppliedRaw="", AppliedFingerprint="")
	// with an identical entry on the router: content equality (ExpectedPostFingerprint) does NOT prove origin,
	// so StartupRecovery MUST NOT delete the entry and MUST enter safe recovery_required (ErrRecoveryConflict).
	_ = kdState.UpsertPublication(ctx, ndmscommand.HTTPProxyPublicationSpec{
		Label:        "crashed-ambiguous",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
	})
	expRawAmb, expFPAmb := buildExpectedCreatedEntry("crashed-ambiguous", "127.0.0.1", 9009)
	crashedAmbiguousJournal := &TransactionJournal{
		Version:       2,
		TransactionID: "tx-crash-ambiguous",
		Phase:         PhaseCandidateActive,
		Affected:      []string{"keendns"},
		Desired: IngressTopology{
			KeenDNS: &KeenDNSJournalRecord{
				Label:                   "crashed-ambiguous",
				FQDN:                    "crashed-ambiguous.example.keenetic.pro",
				UpstreamHost:            "127.0.0.1",
				UpstreamPort:            9009,
				ActionMode:              KeenDNSActionCreate,
				State:                   KeenDNSStateWritePending,
				ExistedBefore:           false,
				CreatedByTx:             true,
				WriteIntent:             true,
				WriteAttempted:          true,
				PreConfigFingerprint:    KeenDNSPreStateAbsent,
				ExpectedAppliedRaw:      expRawAmb,
				ExpectedPostFingerprint: expFPAmb,
				AppliedRaw:              "",
				AppliedFingerprint:      "",
			},
		},
	}
	prevDeleteCount := kdState.deleteCount
	if err := writeJournal(coord.journalPath(), crashedAmbiguousJournal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	recovErr := coord.StartupRecovery(ctx)
	if !errors.Is(recovErr, ErrRecoveryRequired) || !errors.Is(recovErr, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryRequired + ErrRecoveryConflict for write_pending crash window with identical entry, got: %v", recovErr)
	}
	if kdState.deleteCount != prevDeleteCount {
		t.Fatalf("expected 0 deletes in write_pending crash window, got %d", kdState.deleteCount-prevDeleteCount)
	}
	if _, exists := kdState.entries["crashed-ambiguous"]; !exists {
		t.Fatalf("expected crashed-ambiguous entry to remain on router without blind auto-delete")
	}

	// 3b. Startup recovery from crashed uncommitted journal AFTER post-write readback was confirmed (AppliedRaw & AppliedFingerprint set)
	coord2 := New(t.TempDir(), xrayMock, dispMock, tgMock)
	coord2.SetKeenDNS(kdOp)
	_ = kdState.UpsertPublication(ctx, ndmscommand.HTTPProxyPublicationSpec{
		Label:        "crashed-confirmed",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
	})
	expRaw, expFP := buildExpectedCreatedEntry("crashed-confirmed", "127.0.0.1", 9009)
	crashedConfirmedJournal := &TransactionJournal{
		Version:       2,
		TransactionID: "tx-crash-confirmed",
		Phase:         PhaseCandidateActive,
		Affected:      []string{"keendns"},
		Desired: IngressTopology{
			KeenDNS: &KeenDNSJournalRecord{
				Label:                   "crashed-confirmed",
				FQDN:                    "crashed-confirmed.example.keenetic.pro",
				UpstreamHost:            "127.0.0.1",
				UpstreamPort:            9009,
				ActionMode:              KeenDNSActionCreate,
				State:                   KeenDNSStateApplied,
				ExistedBefore:           false,
				CreatedByTx:             true,
				WriteIntent:             true,
				WriteAttempted:          true,
				PreConfigFingerprint:    KeenDNSPreStateAbsent,
				ExpectedAppliedRaw:      expRaw,
				ExpectedPostFingerprint: expFP,
				AppliedRaw:              expRaw,
				AppliedFingerprint:      expFP,
			},
		},
	}
	if err := writeJournal(coord2.journalPath(), crashedConfirmedJournal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	if err := coord2.StartupRecovery(ctx); err != nil {
		t.Fatalf("StartupRecovery for confirmed uncommitted write: %v", err)
	}
	if _, exists := kdState.entries["crashed-confirmed"]; exists {
		t.Fatalf("expected StartupRecovery to roll back confirmed uncommitted KeenDNS publication")
	}
}

func TestNDMSKeenDNSOperator_ReuseDoesNotOverwriteExistingCompatibleEntry(t *testing.T) {
	ctx := context.Background()
	state := newFakeKeenDNSState()

	originalRaw := `{"custom-flag":"keep","domain":{"custom-sub":"preserve","ndns":true},"preserve-host":false,"security-level":{"public":true,"rate":50},"ssl":{"hsts":true,"redirect":false},"upstream":{"port":"9009","proto":"http","timeout":30,"upstream":"127.0.0.1"}}`
	state.entries["connect"] = ndmsquery.HTTPProxyEntry{
		Name:          "connect",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		SSLRedirect:   false,
		Public:        true,
		Auth:          false,
		PreserveHost:  false,
		RawJSON:       originalRaw,
	}

	op := NewNDMSKeenDNSOperator(state, state, state)
	rec, err := op.PreparePublication(ctx, KeenDNSPublicationCandidate{
		Label:        "connect",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9009,
		Services:     []string{"xray", "tgwebproxy"},
	})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if !rec.ExistedBefore || rec.CreatedByTx || rec.ActionMode != KeenDNSActionReuse {
		t.Fatalf("expected reuse mode with ExistedBefore=true, got %+v", rec)
	}

	if err := op.ApplyAndVerifyPublication(ctx, &rec); err != nil {
		t.Fatalf("ApplyAndVerifyPublication on reuse: %v", err)
	}

	if state.upsertCount != 0 || state.saveCount != 0 || state.deleteCount != 0 || state.restoreCount != 0 {
		t.Fatalf("expected 0 writes and 0 saves on reuse, got upsert=%d save=%d delete=%d restore=%d",
			state.upsertCount, state.saveCount, state.deleteCount, state.restoreCount)
	}
	if rec.WriteAttempted || rec.State != KeenDNSStateReused {
		t.Fatalf("expected WriteAttempted=false and State=reused, got %+v", rec)
	}

	after := state.entries["connect"]
	if after.RawJSON != originalRaw || after.SSLRedirect != false || after.PreserveHost != false {
		t.Fatalf("expected existing entry bytes and semantics untouched, got %+v", after)
	}
}

func TestNDMSKeenDNSOperator_PreMutationConflictAndCrashWindowSafety(t *testing.T) {
	ctx := context.Background()

	// 1. Name created externally AFTER PreparePublication and BEFORE ApplyAndVerifyPublication
	state1 := newFakeKeenDNSState()
	op1 := NewNDMSKeenDNSOperator(state1, state1, state1)
	rec1, err := op1.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	// External actor creates "connect" before ApplyAndVerifyPublication runs
	externalRaw := `{"upstream":{"proto":"http","upstream":"127.0.0.1","port":"9009"},"domain":{"ndns":true},"ssl":{"redirect":false},"security-level":{"public":true},"preserve-host":false}`
	state1.entries["connect"] = ndmsquery.HTTPProxyEntry{
		Name:          "connect",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		SSLRedirect:   false,
		Public:        true,
		PreserveHost:  false,
		RawJSON:       externalRaw,
	}
	if err := op1.ApplyAndVerifyPublication(ctx, &rec1); !errors.Is(err, ErrKeenDNSConflict) {
		t.Fatalf("expected ErrKeenDNSConflict when name created externally after prepare, got: %v", err)
	}
	if rec1.WriteAttempted || rec1.State != KeenDNSStatePrecheckAborted {
		t.Fatalf("expected WriteAttempted=false and State=precheck_aborted, got %+v", rec1)
	}
	if state1.upsertCount != 0 || state1.saveCount != 0 || state1.deleteCount != 0 {
		t.Fatalf("expected 0 writes/deletes when pre-mutation check detects conflict, got upsert=%d save=%d delete=%d", state1.upsertCount, state1.saveCount, state1.deleteCount)
	}
	if state1.entries["connect"].RawJSON != externalRaw {
		t.Fatalf("external entry must not be modified")
	}

	// 1b. Full Coordinator.ExecuteIngressTransaction integration test:
	// PreparePublication runs when "connect" is absent -> before ApplyAndVerifyPublication precheck (inside ReadinessProbe),
	// an external editor creates "connect" with the EXACT SAME state (ExpectedAppliedRaw / ExpectedPostFingerprint) ->
	// ApplyAndVerifyPublication fails with ErrKeenDNSConflict BEFORE UpsertPublication ->
	// real Coordinator failAndRollback runs -> 0 Upsert, 0 Delete, 0 Save, and external entry remains intact!
	coordState := newFakeKeenDNSState()
	coordOp := NewNDMSKeenDNSOperator(coordState, coordState, coordState)
	coord := New(t.TempDir(), &mockXray{}, &mockDispatcher{}, &mockTgWebProxy{})
	coord.SetKeenDNS(coordOp)

	identicalExternalRaw, _ := buildExpectedCreatedEntry("connect", "127.0.0.1", 9009)
	txErr := coord.ExecuteIngressTransaction(ctx, IngressTransactionParams{
		TxID:       "tx-precheck-identical-external",
		ServerKind: "xray",
		Xray: &IngressXrayCandidate{
			Enabled:       true,
			ListenAddress: "127.0.0.1",
			ListenPort:    9008,
			PublicDomain:  "connect.example.keenetic.pro",
			PublicPort:    443,
			Path:          "/cdn-bridge/",
			OutboundMode:  "direct",
			ClientRemark:  "Phone",
			ClientUUID:    "00000000-0000-4000-8000-000000000001",
		},
		KeenDNS: &KeenDNSPublicationCandidate{
			Label:        "connect",
			UpstreamHost: "127.0.0.1",
			UpstreamPort: 9009,
			Services:     []string{"xray"},
		},
		ReadinessProbe: func(ctx context.Context, topology IngressTopology) error {
			// Inject an external entry identical to the expected created state AFTER PreparePublication
			// and BEFORE ApplyAndVerifyPublication precheck runs.
			coordState.mu.Lock()
			coordState.entries["connect"] = ndmsquery.HTTPProxyEntry{
				Name:          "connect",
				UpstreamProto: "http",
				UpstreamHost:  "127.0.0.1",
				UpstreamPort:  "9009",
				DomainNDNS:    true,
				SSLRedirect:   true,
				Public:        true,
				Auth:          false,
				PreserveHost:  true,
				RawJSON:       identicalExternalRaw,
			}
			coordState.mu.Unlock()
			return nil
		},
	})
	if !errors.Is(txErr, ErrKeenDNSConflict) {
		t.Fatalf("expected ErrKeenDNSConflict from ExecuteIngressTransaction when external entry appears before precheck, got: %v", txErr)
	}
	if coordState.upsertCount != 0 || coordState.deleteCount != 0 || coordState.saveCount != 0 {
		t.Fatalf("expected 0 Upsert, 0 Delete, 0 Save during coordinator rollback after precheck conflict, got upsert=%d delete=%d save=%d",
			coordState.upsertCount, coordState.deleteCount, coordState.saveCount)
	}
	if entry, exists := coordState.entries["connect"]; !exists || entry.RawJSON != identicalExternalRaw {
		t.Fatalf("expected identical external entry to remain intact on router after precheck conflict rollback, got exists=%v entry=%+v", exists, entry)
	}
	if needed, reason := coord.IsRecoveryRequired(); needed {
		t.Fatalf("expected coordinator NOT to enter recovery_required after clean no-write precheck conflict, got reason=%s", reason)
	}

	// 2. Foreign edit on the SAME host/port (127.0.0.1:9009) during crash window (WriteAttempted=true, AppliedRaw="")
	state2 := newFakeKeenDNSState()
	op2 := NewNDMSKeenDNSOperator(state2, state2, state2)
	rec2, err := op2.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "connect", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	rec2.WriteAttempted = true
	rec2.State = KeenDNSStateWritePending
	rec2.AppliedRaw = ""
	// Router has an entry on 127.0.0.1:9009, but with a foreign setting (redirect=false)
	foreignSamePortRaw := `{"domain":{"ndns":true},"preserve-host":true,"security-level":{"public":true},"ssl":{"redirect":false},"upstream":{"port":"9009","proto":"http","upstream":"127.0.0.1"}}`
	state2.entries["connect"] = ndmsquery.HTTPProxyEntry{
		Name:          "connect",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		SSLRedirect:   false,
		Public:        true,
		PreserveHost:  true,
		RawJSON:       foreignSamePortRaw,
	}
	if err := op2.RollbackPublication(ctx, rec2); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict for foreign edit on same host/port 127.0.0.1:9009, got: %v", err)
	}
	if state2.deleteCount != 0 {
		t.Fatalf("must NOT delete entry on 127.0.0.1:9009 when fingerprint does not match expected post-state")
	}
	if _, exists := state2.entries["connect"]; !exists {
		t.Fatalf("foreign entry on 127.0.0.1:9009 must remain intact")
	}

	// 3. External deletion of an existing reused object -> rollback must NOT resurrect it, returns ErrRecoveryConflict
	state3 := newFakeKeenDNSState()
	state3.entries["shared"] = ndmsquery.HTTPProxyEntry{
		Name:          "shared",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		SSLRedirect:   true,
		Public:        true,
		PreserveHost:  true,
		RawJSON:       `{"upstream":{"proto":"http","upstream":"127.0.0.1","port":"9009"},"domain":{"ndns":true},"ssl":{"redirect":true},"security-level":{"public":true},"preserve-host":true}`,
	}
	op3 := NewNDMSKeenDNSOperator(state3, state3, state3)
	rec3, err := op3.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "shared", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	// External admin deletes "shared"
	delete(state3.entries, "shared")
	if err := op3.RollbackPublication(ctx, rec3); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict when existing publication was externally deleted, got: %v", err)
	}
	if state3.restoreCount != 0 || state3.upsertCount != 0 {
		t.Fatalf("must NOT restore externally deleted publication")
	}
	if _, exists := state3.entries["shared"]; exists {
		t.Fatalf("externally deleted publication must remain deleted")
	}

	// 4. External edit of an existing reused object -> rollback must NOT overwrite with PreviousRaw, returns ErrRecoveryConflict
	state4 := newFakeKeenDNSState()
	state4.entries["shared"] = ndmsquery.HTTPProxyEntry{
		Name:          "shared",
		UpstreamProto: "http",
		UpstreamHost:  "127.0.0.1",
		UpstreamPort:  "9009",
		DomainNDNS:    true,
		SSLRedirect:   true,
		Public:        true,
		PreserveHost:  true,
		RawJSON:       `{"upstream":{"proto":"http","upstream":"127.0.0.1","port":"9009"},"domain":{"ndns":true},"ssl":{"redirect":true},"security-level":{"public":true},"preserve-host":true}`,
	}
	op4 := NewNDMSKeenDNSOperator(state4, state4, state4)
	rec4, err := op4.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "shared", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	editedRaw := `{"upstream":{"proto":"http","upstream":"192.168.1.50","port":"8080"},"domain":{"ndns":true},"ssl":{"redirect":false},"security-level":{"public":true},"preserve-host":false}`
	state4.entries["shared"] = ndmsquery.HTTPProxyEntry{
		Name:          "shared",
		UpstreamProto: "http",
		UpstreamHost:  "192.168.1.50",
		UpstreamPort:  "8080",
		DomainNDNS:    true,
		SSLRedirect:   false,
		Public:        true,
		PreserveHost:  false,
		RawJSON:       editedRaw,
	}
	if err := op4.RollbackPublication(ctx, rec4); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("expected ErrRecoveryConflict when existing publication was externally edited, got: %v", err)
	}
	if state4.restoreCount != 0 || state4.upsertCount != 0 {
		t.Fatalf("must NOT overwrite external edit with PreviousRaw")
	}
	if state4.entries["shared"].RawJSON != editedRaw {
		t.Fatalf("external edit must be preserved")
	}
}

func TestNDMSKeenDNSOperator_IncompleteDeletionAndSaveFailureRequireRecovery(t *testing.T) {
	ctx := context.Background()

	// 1. Entry absent from running-config, startup-config still has it (WriteAttempted=true), SaveConfig fails
	// -> Rollback and StartupRecovery MUST return ErrRecoveryRequired (NOT nil) and keep the journal active!
	dataDir := t.TempDir()
	xrayMock := &mockXray{}
	dispMock := &mockDispatcher{}
	tgMock := &mockTgWebProxy{}
	kdState := newFakeKeenDNSState()
	kdState.saveErr = errors.New("nvram flash commit failed")
	kdOp := NewNDMSKeenDNSOperator(kdState, kdState, kdState)

	coord := New(dataDir, xrayMock, dispMock, tgMock)
	coord.SetKeenDNS(kdOp)

	expRaw, expFP := buildExpectedCreatedEntry("ghost", "127.0.0.1", 9009)
	rec := KeenDNSJournalRecord{
		Label:                   "ghost",
		FQDN:                    "ghost.example.keenetic.pro",
		UpstreamHost:            "127.0.0.1",
		UpstreamPort:            9009,
		ActionMode:              KeenDNSActionCreate,
		State:                   KeenDNSStateWritePending,
		ExistedBefore:           false,
		CreatedByTx:             true,
		WriteAttempted:          true,
		PreConfigFingerprint:    KeenDNSPreStateAbsent,
		ExpectedAppliedRaw:      expRaw,
		ExpectedPostFingerprint: expFP,
	}

	if err := kdOp.RollbackPublication(ctx, rec); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired when current==nil, WriteAttempted==true, and SaveConfig fails, got: %v", err)
	}

	crashedJournal := &TransactionJournal{
		Version:       2,
		TransactionID: "tx-unpersisted-del",
		Phase:         PhaseCandidateActive,
		Affected:      []string{"keendns"},
		Desired: IngressTopology{
			KeenDNS: &rec,
		},
	}
	if err := writeJournal(coord.journalPath(), crashedJournal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	if err := coord.StartupRecovery(ctx); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected StartupRecovery to return ErrRecoveryRequired when SaveConfig fails on absent entry, got: %v", err)
	}
	if needed, _ := coord.IsRecoveryRequired(); !needed {
		t.Fatalf("expected coordinator IsRecoveryRequired=true")
	}
	if j, _ := readJournal(coord.journalPath()); j == nil {
		t.Fatalf("expected active journal to remain on disk (not archived) when SaveConfig fails")
	}

	// 2. DeletePublication returns nil, but entry remains in /show/rc/ip/http readback
	kdState2 := newFakeKeenDNSState()
	kdOp2 := NewNDMSKeenDNSOperator(kdState2, kdState2, kdState2)
	rec2, err := kdOp2.PreparePublication(ctx, KeenDNSPublicationCandidate{Label: "stuck", UpstreamPort: 9009})
	if err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if err := kdOp2.ApplyAndVerifyPublication(ctx, &rec2); err != nil {
		t.Fatalf("ApplyAndVerifyPublication: %v", err)
	}
	kdState2.keepOnDelete = true
	if err := kdOp2.RollbackPublication(ctx, rec2); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired when deleted entry remains in /show/rc/ip/http readback, got: %v", err)
	}
}

func TestResolveWANInfo_AndCGNATClassification(t *testing.T) {
	ctx := context.Background()

	// 1. IsPrivateOrCGNATIP classification (RFC 1918 + RFC 6598 CGNAT 100.64.0.0/10 + public IPv4)
	cgnatAndPrivateCases := []struct {
		ip   string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.72.15.9", true},
		{"100.127.255.254", true},
		{"100.63.255.254", false},
		{"100.128.0.1", false},
		{"10.0.0.1", true},
		{"172.16.5.4", true},
		{"192.168.1.50", true},
		{"127.0.0.1", true},
		{"169.254.1.1", true},
		{"85.140.2.10", false},
		{"93.184.216.34", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, tc := range cgnatAndPrivateCases {
		if got := IsPrivateOrCGNATIP(tc.ip); got != tc.want {
			t.Fatalf("IsPrivateOrCGNATIP(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}

	// 2. Nil queries or nil stores
	if ip, priv := ResolveWANInfo(ctx, nil); ip != "" || priv {
		t.Fatalf("expected empty result for nil Queries, got (%q, %v)", ip, priv)
	}
	if ip, priv := ResolveWANInfo(ctx, &ndmsquery.Queries{}); ip != "" || priv {
		t.Fatalf("expected empty result for empty Queries, got (%q, %v)", ip, priv)
	}

	// 3. RCI error on /show/interface/ and /show/ip/route -> ("", false)
	fgErr := ndmsquery.NewFakeGetter()
	fgErr.SetDefaultError(errors.New("rci unavailable"))
	qErr := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fgErr})
	if ip, priv := ResolveWANInfo(ctx, qErr); ip != "" || priv {
		t.Fatalf("expected empty result on RCI error, got (%q, %v)", ip, priv)
	}

	// 4. Default route points to a VPN tunnel (Wireguard0 with 10.8.0.2), while ISP WAN has CGNAT 100.72.15.9
	// ResolveWANInfo MUST NOT pick the VPN tunnel address; it must select the ISP WAN and detect CGNAT!
	fgVPN := ndmsquery.NewFakeGetter()
	fgVPN.SetJSON("/show/ip/route", `[
		{"destination":"0.0.0.0/0","gateway":"10.8.0.1","interface":"Wireguard0"}
	]`)
	fgVPN.SetJSON("/show/interface/", `{
		"Home": {
			"id": "Bridge0",
			"interface-name": "br0",
			"type": "Bridge",
			"security-level": "private",
			"state": "up",
			"address": "192.168.1.1",
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		},
		"Wireguard0": {
			"id": "Wireguard0",
			"interface-name": "nwg0",
			"type": "Wireguard",
			"security-level": "public",
			"state": "up",
			"address": "10.8.0.2",
			"priority": 900,
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		},
		"ISP": {
			"id": "ISP",
			"interface-name": "eth2",
			"type": "GigabitEthernet",
			"security-level": "public",
			"state": "up",
			"address": "100.72.15.9",
			"priority": 700,
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		}
	}`)
	qVPN := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fgVPN})
	if ip, priv := ResolveWANInfo(ctx, qVPN); ip != "100.72.15.9" || !priv {
		t.Fatalf("expected CGNAT ISP WAN (100.72.15.9, true) ignoring Wireguard0 VPN, got (%q, %v)", ip, priv)
	}

	// 5. Multiple WAN interfaces: selects highest-priority active WAN when default route is absent,
	// or default-gateway WAN when default route points to an active ISP WAN.
	fgMulti := ndmsquery.NewFakeGetter()
	fgMulti.SetJSON("/show/ip/route", `[]`)
	fgMulti.SetJSON("/show/interface/", `{
		"UsbLte0": {
			"id": "UsbLte0",
			"interface-name": "usb0",
			"type": "UsbLte",
			"security-level": "public",
			"state": "up",
			"address": "192.168.8.100",
			"priority": 300,
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		},
		"PPPoE0": {
			"id": "PPPoE0",
			"interface-name": "ppp0",
			"type": "PPPoE",
			"security-level": "public",
			"state": "up",
			"address": "85.140.2.10",
			"priority": 1000,
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		}
	}`)
	qMulti := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fgMulti})
	if ip, priv := ResolveWANInfo(ctx, qMulti); ip != "85.140.2.10" || priv {
		t.Fatalf("expected primary public WAN (85.140.2.10, false), got (%q, %v)", ip, priv)
	}

	// 6. Primary WAN down / no address -> selects active backup WAN
	fgFailover := ndmsquery.NewFakeGetter()
	fgFailover.SetJSON("/show/ip/route", `[
		{"destination":"0.0.0.0/0","gateway":"0.0.0.0","interface":"PPPoE0"}
	]`)
	fgFailover.SetJSON("/show/interface/", `{
		"UsbLte0": {
			"id": "UsbLte0",
			"interface-name": "usb0",
			"type": "UsbLte",
			"security-level": "public",
			"state": "up",
			"address": "192.168.8.100",
			"priority": 300,
			"summary": {"layer": {"ipv4": "running", "conf": "running"}}
		},
		"PPPoE0": {
			"id": "PPPoE0",
			"interface-name": "ppp0",
			"type": "PPPoE",
			"security-level": "public",
			"state": "down",
			"address": "",
			"priority": 1000,
			"summary": {"layer": {"ipv4": "disabled", "conf": "running"}}
		}
	}`)
	qFailover := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fgFailover})
	if ip, priv := ResolveWANInfo(ctx, qFailover); ip != "192.168.8.100" || !priv {
		t.Fatalf("expected active backup WAN (192.168.8.100, true), got (%q, %v)", ip, priv)
	}

	// 7. WAN interface present but has no IPv4 address -> ("", false)
	fgNoAddr := ndmsquery.NewFakeGetter()
	fgNoAddr.SetJSON("/show/ip/route", `[]`)
	fgNoAddr.SetJSON("/show/interface/", `{
		"ISP": {
			"id": "ISP",
			"interface-name": "eth2",
			"type": "GigabitEthernet",
			"security-level": "public",
			"state": "up",
			"address": "",
			"priority": 700,
			"summary": {"layer": {"ipv4": "pending", "conf": "running"}}
		}
	}`)
	qNoAddr := ndmsquery.NewQueries(ndmsquery.Deps{Getter: fgNoAddr})
	if ip, priv := ResolveWANInfo(ctx, qNoAddr); ip != "" || priv {
		t.Fatalf("expected empty result when WAN has no IPv4 address, got (%q, %v)", ip, priv)
	}
}
