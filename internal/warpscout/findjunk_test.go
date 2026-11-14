package warpscout

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type junkTestRunner struct {
	mu         sync.Mutex
	calls      []ScanParams
	hitsByCall []int
	delay      time.Duration
}

func (r *junkTestRunner) Available() (string, bool) { return "/fake", true }

func (r *junkTestRunner) Run(ctx context.Context, p ScanParams, on func(EngineEvent)) error {
	if r.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.delay):
		}
	}
	r.mu.Lock()
	idx := len(r.calls)
	r.calls = append(r.calls, p)
	hits := 0
	if idx < len(r.hitsByCall) {
		hits = r.hitsByCall[idx]
	}
	r.mu.Unlock()

	total := len(p.Endpoints)
	if total == 0 {
		total = 10
	}
	on(EngineEvent{Type: "start", Total: total})
	for i := 0; i < hits; i++ {
		on(EngineEvent{
			Type:     "hit",
			Endpoint: "162.159.192.1:2408",
			PingMs:   25 + i,
			Colo:     "HEL",
			Loc:      "FI",
			Warp:     "on",
		})
	}
	on(EngineEvent{Type: "done", Scanned: total, Hits: hits})
	return nil
}

func newFindJunkTestService(t *testing.T, runner Runner) *Service {
	t.Helper()
	store, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SetAccount(&Account{
		ID:            "acc1",
		PrivateKey:    "priv",
		PeerPublicKey: WarpPeerPublicKey,
		AddressV4:     "172.16.0.2/32",
	}); err != nil {
		t.Fatalf("SetAccount: %v", err)
	}
	svc := New(Deps{
		Store:   store,
		Runner:  runner,
		Tunnels: &fakeTunnels{refs: map[string]*TunnelRef{}},
		WAN:     func(context.Context) (string, bool) { return "eth3", true },
		Now:     time.Now,
	})
	t.Cleanup(svc.Stop)
	return svc
}

func waitFindJunkDone(t *testing.T, svc *Service) FindJunkStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.State().FindJunk
		if !st.Running {
			return st
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("findJunk did not finish in time")
	return FindJunkStatus{}
}

func TestFindJunk_ReachesThresholdAndApplies(t *testing.T) {
	// PerSubnet=1 => 15 префиксов = 15 кандидатов.
	// Попытка 1: 5/15 (33%), Попытка 2: 14/15 (93%), Попытка 3: 15/15 (100% >= 95%).
	runner := &junkTestRunner{hitsByCall: []int{5, 14, 15, 15}}
	svc := newFindJunkTestService(t, runner)

	err := svc.StartFindJunk(FindJunkRequest{
		I1Profile:    "quic",
		MaxAttempts:  6,
		ThresholdPct: 95,
		PerSubnet:    1,
		AutoApply:    true,
	})
	if err != nil {
		t.Fatalf("StartFindJunk: %v", err)
	}

	st := waitFindJunkDone(t, svc)
	if st.Error != "" {
		t.Fatalf("unexpected error: %s", st.Error)
	}
	if len(st.Attempts) != 3 {
		t.Fatalf("expected 3 attempts (early stop at 100%%), got %d", len(st.Attempts))
	}
	if st.Best == nil || st.Best.Attempt != 3 || !st.Best.GoodEnough {
		t.Fatalf("unexpected best attempt: %+v", st.Best)
	}
	if st.Best.Profile != "quic_initial" || st.Best.AWG.I1 == "" {
		t.Fatalf("expected quic_initial with non-empty I1, got %+v", st.Best)
	}
	if !st.Applied {
		t.Fatalf("expected AutoApply to mark Applied=true")
	}
	saved := svc.d.Store.Config().AWG
	if saved.Jc != st.Best.AWG.Jc || saved.I1 != st.Best.AWG.I1 {
		t.Fatalf("saved AWG %+v != best %+v", saved, st.Best.AWG)
	}
}

func TestFindJunk_StopKeepsBestAndApplyWorks(t *testing.T) {
	runner := &junkTestRunner{
		hitsByCall: []int{6, 12, 3, 4},
		delay:      40 * time.Millisecond,
	}
	svc := newFindJunkTestService(t, runner)

	if err := svc.StartFindJunk(FindJunkRequest{
		I1Profile:    "none",
		MaxAttempts:  10,
		ThresholdPct: 95,
		PerSubnet:    1,
	}); err != nil {
		t.Fatalf("StartFindJunk: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.State().FindJunk.Attempts) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	svc.StopFindJunk()
	st := waitFindJunkDone(t, svc)
	if !st.Aborted {
		t.Fatalf("expected Aborted=true after StopFindJunk")
	}
	if st.Best == nil || st.Best.Working != 12 {
		t.Fatalf("expected best working=12, got %+v", st.Best)
	}
	if st.Best.AWG.SignatureProfile != "none" || st.Best.AWG.I1 != "" {
		t.Fatalf("expected none profile with empty I1, got %+v", st.Best.AWG)
	}

	applied, err := svc.ApplyFindJunk()
	if err != nil {
		t.Fatalf("ApplyFindJunk: %v", err)
	}
	if applied.Jc != st.Best.AWG.Jc || svc.d.Store.Config().AWG.Jc != st.Best.AWG.Jc {
		t.Fatalf("ApplyFindJunk did not persist best AWG")
	}
}

type scriptedJunkRunner struct {
	mu    sync.Mutex
	calls []ScanParams
	runFn func(ctx context.Context, callIdx int, p ScanParams, on func(EngineEvent)) error
}

func (r *scriptedJunkRunner) Available() (string, bool) { return "/fake", true }

func (r *scriptedJunkRunner) Run(ctx context.Context, p ScanParams, on func(EngineEvent)) error {
	r.mu.Lock()
	idx := len(r.calls)
	r.calls = append(r.calls, p)
	fn := r.runFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, idx, p, on)
	}
	return nil
}

func emitHitsAndMaybeDone(on func(EngineEvent), total, hits int, sendDone bool, scanned int, aborted bool) {
	on(EngineEvent{Type: "start", Total: total})
	for i := 0; i < hits; i++ {
		on(EngineEvent{
			Type:     "hit",
			Endpoint: "162.159.192.1:2408",
			PingMs:   30 + i,
			Colo:     "HEL",
			Loc:      "FI",
			Warp:     "on",
		})
	}
	if sendDone {
		on(EngineEvent{Type: "done", Scanned: scanned, Hits: hits, Aborted: aborted})
	}
}

func TestFindJunk_ZeroWorking_RejectsAutoAndManualApply(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, plainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SetAccount(&Account{
		ID:            "acc1",
		PrivateKey:    "priv",
		PeerPublicKey: WarpPeerPublicKey,
		AddressV4:     "172.16.0.2/32",
	}); err != nil {
		t.Fatalf("SetAccount: %v", err)
	}
	origAWG := AWGParams{
		SignatureProfile: "quic_initial",
		Jc:               4,
		Jmin:             40,
		Jmax:             70,
		I1:               "<b 0x1122>",
	}
	if err := store.SetAWG(origAWG); err != nil {
		t.Fatalf("SetAWG: %v", err)
	}

	runner := &junkTestRunner{hitsByCall: []int{0, 0, 0}}
	svc := New(Deps{
		Store:   store,
		Runner:  runner,
		Tunnels: &fakeTunnels{refs: map[string]*TunnelRef{}},
		WAN:     func(context.Context) (string, bool) { return "eth3", true },
		Now:     time.Now,
	})
	t.Cleanup(svc.Stop)

	if err := svc.StartFindJunk(FindJunkRequest{
		I1Profile:    "quic",
		MaxAttempts:  3,
		ThresholdPct: 95,
		PerSubnet:    1,
		AutoApply:    true,
	}); err != nil {
		t.Fatalf("StartFindJunk: %v", err)
	}
	st := waitFindJunkDone(t, svc)
	if st.Error != "" {
		t.Fatalf("unexpected error: %s", st.Error)
	}
	if len(st.Attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(st.Attempts))
	}
	if st.Best != nil {
		t.Fatalf("expected Best=nil on 0 working hits, got %+v", st.Best)
	}
	if st.Applied {
		t.Fatalf("expected Applied=false on 0 working hits")
	}

	if got := store.Config().AWG; got.Jc != origAWG.Jc || got.I1 != origAWG.I1 {
		t.Fatalf("AutoApply altered AWG on zero hits: got %+v want %+v", got, origAWG)
	}
	if _, err := svc.ApplyFindJunk(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput from ApplyFindJunk on 0 working, got %v", err)
	}
	reloaded, err := NewStore(dir, plainCipher{})
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if got := reloaded.Config().AWG; got.Jc != origAWG.Jc || got.I1 != origAWG.I1 {
		t.Fatalf("disk AWG altered after rejected apply: got %+v want %+v", got, origAWG)
	}
}

func TestFindJunk_IncompleteAttempts_NotRankedOrApplied(t *testing.T) {
	t.Run("preserves_prior_valid_best_when_subsequent_runs_incomplete", func(t *testing.T) {
		runner := &scriptedJunkRunner{
			runFn: func(_ context.Context, idx int, p ScanParams, on func(EngineEvent)) error {
				total := len(p.Endpoints)
				switch idx {
				case 0:
					// Валидный полный прогон: 10/15
					emitHitsAndMaybeDone(on, total, 10, true, total, false)
				case 1:
					// Дедлайн / неполный прогон: проверено 12 из 15, все 12 ответили
					emitHitsAndMaybeDone(on, total, 12, true, 12, false)
				case 2:
					// done.Aborted=true при живом контексте
					emitHitsAndMaybeDone(on, total, total, true, total, true)
				case 3:
					// Пропавшее событие done
					emitHitsAndMaybeDone(on, total, total, false, 0, false)
				}
				return nil
			},
		}
		svc := newFindJunkTestService(t, runner)
		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "quic",
			MaxAttempts:  4,
			ThresholdPct: 95,
			PerSubnet:    1,
		}); err != nil {
			t.Fatalf("StartFindJunk: %v", err)
		}
		st := waitFindJunkDone(t, svc)
		if len(st.Attempts) != 4 {
			t.Fatalf("expected 4 attempts (no false GoodEnough stop), got %d", len(st.Attempts))
		}
		if st.Attempts[0].Incomplete || st.Attempts[0].Working != 10 {
			t.Fatalf("attempt 1 should be complete 10/15: %+v", st.Attempts[0])
		}
		// Попытка 2: Scanned=12 < Total=15 -> не 100%, а 80%, и Incomplete=true.
		if !st.Attempts[1].Incomplete || st.Attempts[1].Scanned != 12 || st.Attempts[1].Pct != 80 || st.Attempts[1].GoodEnough {
			t.Fatalf("attempt 2 should be incomplete 12/15 (80%%): %+v", st.Attempts[1])
		}
		if !st.Attempts[2].Incomplete || st.Attempts[2].GoodEnough {
			t.Fatalf("attempt 3 (done.Aborted=true) should be incomplete: %+v", st.Attempts[2])
		}
		if !st.Attempts[3].Incomplete || st.Attempts[3].GoodEnough {
			t.Fatalf("attempt 4 (missing done) should be incomplete: %+v", st.Attempts[3])
		}
		if st.Best == nil || st.Best.Attempt != 1 || st.Best.Working != 10 {
			t.Fatalf("expected attempt 1 preserved as Best, got %+v", st.Best)
		}
	})

	t.Run("all_incomplete_blocks_auto_and_manual_apply", func(t *testing.T) {
		runner := &scriptedJunkRunner{
			runFn: func(_ context.Context, _ int, p ScanParams, on func(EngineEvent)) error {
				total := len(p.Endpoints)
				emitHitsAndMaybeDone(on, total, total, true, total-1, false)
				return nil
			},
		}
		svc := newFindJunkTestService(t, runner)
		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "dns",
			MaxAttempts:  2,
			ThresholdPct: 80,
			PerSubnet:    1,
			AutoApply:    true,
		}); err != nil {
			t.Fatalf("StartFindJunk: %v", err)
		}
		st := waitFindJunkDone(t, svc)
		if st.Best != nil || st.Applied {
			t.Fatalf("incomplete runs must not populate Best or AutoApply: %+v", st)
		}
		if _, err := svc.ApplyFindJunk(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput when all attempts incomplete, got %v", err)
		}
	})
}

func TestFindJunk_OuterWarp_SamplesOnePerAllSubnetsAndExpandsDeadline(t *testing.T) {
	store, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	acct := &Account{ID: "outer-acc", PrivateKey: "outerPriv", PublicKey: "outerPub", PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.2/32"}
	inner := &Account{ID: "inner-acc", PrivateKey: "innerPriv", PublicKey: "innerPub", PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.9/32"}
	if err := store.SetAccount(acct); err != nil {
		t.Fatal(err)
	}
	if err := store.SetInnerAccount(inner); err != nil {
		t.Fatal(err)
	}
	tunnels := &fakeTunnels{refs: map[string]*TunnelRef{
		key(TargetNative, "awg3"): {
			Mode: TargetNative, ID: "awg3", Name: "WARP HEL", Iface: "nwg3",
			Endpoint: "162.159.192.1:2408", AddressV4: "172.16.0.2/32",
			LocalPublicKey: "outerPub", IsWarp: true, Running: true,
		},
	}}
	runner := &junkTestRunner{hitsByCall: []int{15}}
	svc := New(Deps{
		Store:   store,
		Runner:  runner,
		Tunnels: tunnels,
		WAN:     func(context.Context) (string, bool) { return "eth3", true },
		Routes: func(context.Context) []RouteRef {
			return []RouteRef{{Name: "nwg3", Label: "WARP HEL", Kind: "warp", Up: true, TunnelMode: TargetNative, TunnelID: "awg3"}}
		},
		Now: time.Now,
	})
	t.Cleanup(svc.Stop)

	if err := svc.StartFindJunk(FindJunkRequest{
		I1Profile:    "stun",
		MaxAttempts:  1,
		ThresholdPct: 95,
		PerSubnet:    3,
		Ifname:       "nwg3",
	}); err != nil {
		t.Fatalf("StartFindJunk: %v", err)
	}
	st := waitFindJunkDone(t, svc)
	if st.Error != "" {
		t.Fatalf("unexpected error: %s", st.Error)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Concurrency != 1 {
		t.Fatalf("expected Concurrency=1 for outer WARP, got %d", call.Concurrency)
	}
	if call.DeadlineMs < 70000 {
		t.Fatalf("expected expanded sequential DeadlineMs >= 70000, got %d", call.DeadlineMs)
	}
	if len(call.Endpoints) != len(DefaultPrefixes) {
		t.Fatalf("expected 1 endpoint per prefix (%d), got %d", len(DefaultPrefixes), len(call.Endpoints))
	}
	subnets := map[string]bool{}
	for _, ep := range call.Endpoints {
		host, _, _ := net.SplitHostPort(ep)
		parts := strings.Split(host, ".")
		subnets[strings.Join(parts[:3], ".")] = true
	}
	if len(subnets) != len(DefaultPrefixes) {
		t.Fatalf("expected all %d distinct /24 subnets sampled, got %d", len(DefaultPrefixes), len(subnets))
	}
}

func TestFindJunk_StoreTransactionalAndNarrowSetAWG(t *testing.T) {
	t.Run("autoapply_save_error_rolls_back_memory_and_reports_status_error", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewStore(dir, plainCipher{})
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if err := store.SetAccount(&Account{ID: "a1", PrivateKey: "p1", PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.2/32"}); err != nil {
			t.Fatal(err)
		}
		origAWG := AWGParams{SignatureProfile: "stun", Jc: 3, Jmin: 25, Jmax: 55, I1: "<b 0xabcd>"}
		if err := store.SetAWG(origAWG); err != nil {
			t.Fatal(err)
		}

		runner := &scriptedJunkRunner{
			runFn: func(_ context.Context, _ int, p ScanParams, on func(EngineEvent)) error {
				emitHitsAndMaybeDone(on, len(p.Endpoints), len(p.Endpoints), true, len(p.Endpoints), false)
				return nil
			},
		}
		svc := New(Deps{
			Store:   store,
			Runner:  runner,
			Tunnels: &fakeTunnels{refs: map[string]*TunnelRef{}},
			WAN:     func(context.Context) (string, bool) { return "eth3", true },
			Now:     time.Now,
		})
		t.Cleanup(svc.Stop)

		store.setTestSaveHook(func() error { return errors.New("disk full") })
		defer store.setTestSaveHook(nil)

		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "quic",
			MaxAttempts:  1,
			ThresholdPct: 95,
			PerSubnet:    1,
			AutoApply:    true,
		}); err != nil {
			t.Fatalf("StartFindJunk: %v", err)
		}
		st := waitFindJunkDone(t, svc)
		if st.Applied {
			t.Fatalf("expected Applied=false on save error")
		}
		if !strings.Contains(st.Error, "disk full") {
			t.Fatalf("expected error in status, got %q", st.Error)
		}
		if got := store.Config().AWG; got.Jc != origAWG.Jc || got.I1 != origAWG.I1 {
			t.Fatalf("in-memory AWG mutated despite save failure: %+v vs %+v", got, origAWG)
		}
		reloaded, err := NewStore(dir, plainCipher{})
		if err != nil {
			t.Fatalf("reload store: %v", err)
		}
		if got := reloaded.Config().AWG; got.Jc != origAWG.Jc || got.I1 != origAWG.I1 {
			t.Fatalf("disk AWG mutated despite save failure: %+v vs %+v", got, origAWG)
		}
	})

	t.Run("setconfig_rollback_on_save_error", func(t *testing.T) {
		store, err := NewStore(t.TempDir(), plainCipher{})
		if err != nil {
			t.Fatal(err)
		}
		orig := store.Config()
		orig.ScanInterface = "eth3"
		if err := store.SetConfig(orig); err != nil {
			t.Fatal(err)
		}
		store.setTestSaveHook(func() error { return errors.New("ro fs") })
		defer store.setTestSaveHook(nil)

		next := orig
		next.ScanInterface = "eth9"
		if err := store.SetConfig(next); err == nil {
			t.Fatalf("expected error from SetConfig when save hook fails")
		}
		if got := store.Config().ScanInterface; got != "eth3" {
			t.Fatalf("SetConfig did not rollback ScanInterface: got %q", got)
		}
	})

	t.Run("narrow_setawg_preserves_concurrent_unrelated_config_fields", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		runner := &scriptedJunkRunner{
			runFn: func(_ context.Context, _ int, p ScanParams, on func(EngineEvent)) error {
				close(started)
				<-release
				emitHitsAndMaybeDone(on, len(p.Endpoints), len(p.Endpoints), true, len(p.Endpoints), false)
				return nil
			},
		}
		svc := newFindJunkTestService(t, runner)
		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "quic",
			MaxAttempts:  1,
			ThresholdPct: 95,
			PerSubnet:    1,
			AutoApply:    true,
		}); err != nil {
			t.Fatalf("StartFindJunk: %v", err)
		}
		<-started
		// Параллельно меняем несвязанное поле ScanInterface, пока find-junk работает.
		cfg := svc.d.Store.Config()
		cfg.ScanInterface = "nwg9"
		if err := svc.d.Store.SetConfig(cfg); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		close(release)

		st := waitFindJunkDone(t, svc)
		if !st.Applied || st.Error != "" {
			t.Fatalf("expected Applied=true and no error, got %+v", st)
		}
		finalCfg := svc.d.Store.Config()
		if finalCfg.ScanInterface != "nwg9" {
			t.Fatalf("AutoApply clobbered ScanInterface: got %q, want nwg9", finalCfg.ScanInterface)
		}
		if finalCfg.AWG.Jc != st.Best.AWG.Jc || finalCfg.AWG.I1 != st.Best.AWG.I1 {
			t.Fatalf("AutoApply did not update AWG: got %+v vs %+v", finalCfg.AWG, st.Best.AWG)
		}
	})
}

func TestFindJunk_ApplyRejectsWhileRunningAndBindsToRunID(t *testing.T) {
	t.Run("rejects_apply_while_running", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		runner := &scriptedJunkRunner{
			runFn: func(_ context.Context, _ int, p ScanParams, on func(EngineEvent)) error {
				close(started)
				<-release
				emitHitsAndMaybeDone(on, len(p.Endpoints), len(p.Endpoints), true, len(p.Endpoints), false)
				return nil
			},
		}
		svc := newFindJunkTestService(t, runner)
		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "quic",
			MaxAttempts:  1,
			ThresholdPct: 95,
			PerSubnet:    1,
		}); err != nil {
			t.Fatalf("StartFindJunk: %v", err)
		}
		<-started
		if _, err := svc.ApplyFindJunk(); !errors.Is(err, ErrScanRunning) {
			t.Fatalf("expected ErrScanRunning while find-junk is running, got %v", err)
		}
		close(release)
		_ = waitFindJunkDone(t, svc)
	})

	t.Run("barrier_concurrent_apply_does_not_mark_new_run_applied", func(t *testing.T) {
		runner := &junkTestRunner{hitsByCall: []int{12, 12}}
		svc := newFindJunkTestService(t, runner)

		if err := svc.StartFindJunk(FindJunkRequest{
			I1Profile:    "dns",
			MaxAttempts:  1,
			ThresholdPct: 95,
			PerSubnet:    1,
		}); err != nil {
			t.Fatalf("run 1 StartFindJunk: %v", err)
		}
		st1 := waitFindJunkDone(t, svc)
		if st1.RunID != 1 || st1.Best == nil || st1.Best.Working != 12 {
			t.Fatalf("unexpected run 1 state: %+v", st1)
		}

		enteredApply := make(chan struct{})
		releaseApply := make(chan struct{})
		svc.testBeforeApplySave = func() {
			close(enteredApply)
			<-releaseApply
		}

		applyErrCh := make(chan error, 1)
		go func() {
			_, err := svc.ApplyFindJunk()
			applyErrCh <- err
		}()
		<-enteredApply

		// Пытаемся запустить новый подбор, пока Apply#1 ещё не закончил сохранение.
		startErrCh := make(chan error, 1)
		go func() {
			startErrCh <- svc.StartFindJunk(FindJunkRequest{
				I1Profile:    "stun",
				MaxAttempts:  1,
				ThresholdPct: 95,
				PerSubnet:    1,
			})
		}()

		// Убеждаемся, что StartFindJunk сериализован за ApplyFindJunk.
		select {
		case err := <-startErrCh:
			t.Fatalf("StartFindJunk should wait for ApplyFindJunk barrier, returned early: %v", err)
		case <-time.After(35 * time.Millisecond):
		}

		close(releaseApply)
		if err := <-applyErrCh; err != nil {
			t.Fatalf("ApplyFindJunk: %v", err)
		}
		if err := <-startErrCh; err != nil {
			t.Fatalf("run 2 StartFindJunk: %v", err)
		}
		st2 := waitFindJunkDone(t, svc)
		if st2.RunID != 2 {
			t.Fatalf("expected RunID=2, got %d", st2.RunID)
		}
		if st2.Applied {
			t.Fatalf("run 1's ApplyFindJunk must not mark run 2 Applied=true: %+v", st2)
		}
		if got := svc.d.Store.Config().AWG; got.Jc != st1.Best.AWG.Jc || got.I1 != st1.Best.AWG.I1 {
			t.Fatalf("run 1 AWG was not saved before run 2 started: got %+v want %+v", got, st1.Best.AWG)
		}
	})

	t.Run("concurrent_start_stop_apply_state_stress", func(t *testing.T) {
		runner := &junkTestRunner{
			hitsByCall: []int{10, 12, 15, 10, 14, 15, 10, 12, 15},
			delay:      3 * time.Millisecond,
		}
		svc := newFindJunkTestService(t, runner)
		var wg sync.WaitGroup
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for i := 0; i < 15; i++ {
					switch (id + i) % 4 {
					case 0:
						_ = svc.StartFindJunk(FindJunkRequest{I1Profile: "random", MaxAttempts: 1, PerSubnet: 1})
					case 1:
						svc.StopFindJunk()
					case 2:
						_, _ = svc.ApplyFindJunk()
					case 3:
						_ = svc.State()
					}
					time.Sleep(time.Millisecond)
				}
			}(g)
		}
		wg.Wait()
		svc.StopFindJunk()
		_ = waitFindJunkDone(t, svc)
	})
}
