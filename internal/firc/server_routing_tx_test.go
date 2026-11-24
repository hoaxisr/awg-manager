package firc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerRoutingTxCrashRecoveryPreparingAndCommitting(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	cfgDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})

	// Seed baseline awgm-ext.json (disabled) and groups.yaml (single user group, no _server group).
	initialExt := ExtConfig{
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   nil,
			VlessXhttpServer: false,
		},
	}
	if err := op.SaveExtConfig(initialExt); err != nil {
		t.Fatalf("SaveExtConfig baseline: %v", err)
	}
	initialGroups := []Group{
		{
			ID:        "00000001",
			Name:      "main",
			Interface: GroupInterface{"vless0"},
			Enable:    true,
			Devices: GroupDevices{
				Allow: []string{"192.168.1.0/24"},
			},
		},
	}
	if err := op.SaveGroupsFile(initialGroups); err != nil {
		t.Fatalf("SaveGroupsFile baseline: %v", err)
	}
	origExtBytes, err := os.ReadFile(op.extConfigPath())
	if err != nil {
		t.Fatalf("read baseline awgm-ext.json: %v", err)
	}
	origGroupsBytes, err := os.ReadFile(op.GroupsPath())
	if err != nil {
		t.Fatalf("read baseline groups.yaml: %v", err)
	}

	// --- Phase 1: Crash during "preparing" (uncommitted manifest, after SaveExtConfig mutated disk) ---
	txPrep, err := op.PrepareServerRouting(context.Background(), "tx-crash-preparing", "vless_xhttp_server", true)
	if err != nil {
		t.Fatalf("PrepareServerRouting(preparing): %v", err)
	}
	op.SetServerRoutingTxFaultHooks(ServerRoutingTxFaultHooks{
		BeforeReconcile: func(txID string) error {
			return errors.New("simulated crash after SaveExtConfig before commit marker")
		},
	})
	if err := op.CommitServerRouting(context.Background(), txPrep); err == nil {
		t.Fatalf("expected CommitServerRouting to fail on injected BeforeReconcile crash")
	}
	op.SetServerRoutingTxFaultHooks(ServerRoutingTxFaultHooks{})

	// Verify disk was mutated by the interrupted commit before recovery runs.
	if !op.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected interrupted commit to have written candidate awgm-ext.json before crash")
	}

	// Simulate restart with a fresh Operator instance and run RecoverIncompleteServerRoutingTx.
	opRestart1 := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})
	if err := opRestart1.RecoverIncompleteServerRoutingTx(context.Background()); err != nil {
		t.Fatalf("RecoverIncompleteServerRoutingTx(preparing): %v", err)
	}
	if opRestart1.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected uncommitted preparing transaction to be rolled back to false")
	}
	gotExtBytes, err := os.ReadFile(opRestart1.extConfigPath())
	if err != nil || !bytes.Equal(gotExtBytes, origExtBytes) {
		t.Fatalf("awgm-ext.json not restored byte-for-byte after preparing crash recovery")
	}
	gotGroupsBytes, err := os.ReadFile(opRestart1.GroupsPath())
	if err != nil || !bytes.Equal(gotGroupsBytes, origGroupsBytes) {
		t.Fatalf("groups.yaml not restored byte-for-byte after preparing crash recovery")
	}
	if _, err := os.Stat(opRestart1.srTxDir(txPrep)); !os.IsNotExist(err) {
		t.Fatalf("expected sr-tx directory %s to be cleaned up after preparing recovery, err=%v", txPrep, err)
	}

	// --- Phase 2: Crash during "committing" (manifest.Committed == true, before FinalizeServerRouting) ---
	txCommit, err := opRestart1.PrepareServerRouting(context.Background(), "tx-crash-committing", "vless_xhttp_server", true)
	if err != nil {
		t.Fatalf("PrepareServerRouting(committing): %v", err)
	}
	if err := opRestart1.CommitServerRouting(context.Background(), txCommit); err != nil {
		t.Fatalf("CommitServerRouting(committing): %v", err)
	}
	// Do NOT call FinalizeServerRouting — simulate process crash right after CommitServerRouting persisted Committed=true.
	if _, err := os.Stat(filepath.Join(opRestart1.srTxDir(txCommit), "manifest.json")); err != nil {
		t.Fatalf("expected committed manifest.json to exist before finalization: %v", err)
	}

	opRestart2 := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})
	if err := opRestart2.RecoverIncompleteServerRoutingTx(context.Background()); err != nil {
		t.Fatalf("RecoverIncompleteServerRoutingTx(committing): %v", err)
	}
	if !opRestart2.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatalf("expected committed transaction to roll forward and remain enabled after recovery")
	}
	groupsAfterCommit, err := opRestart2.LoadGroupsFile()
	if err != nil {
		t.Fatalf("LoadGroupsFile after committing recovery: %v", err)
	}
	foundServerSubnet := false
	for _, g := range groupsAfterCommit {
		for _, a := range g.Devices.Allow {
			if a == "127.0.0.0/8" {
				foundServerSubnet = true
				break
			}
		}
	}
	if !foundServerSubnet {
		t.Fatalf("expected 127.0.0.0/8 server subnet in groups.yaml after committing recovery, got %+v", groupsAfterCommit)
	}
	if _, err := os.Stat(opRestart2.srTxDir(txCommit)); !os.IsNotExist(err) {
		t.Fatalf("expected sr-tx directory %s to be finalized and removed after committing recovery, err=%v", txCommit, err)
	}
}

func TestServerRoutingTxRollbackFailureSetsRecoveryRequired(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	cfgDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})

	if err := op.SaveExtConfig(ExtConfig{}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}

	txID, err := op.PrepareServerRouting(context.Background(), "tx-rb-fail", "vless_xhttp_server", true)
	if err != nil {
		t.Fatalf("PrepareServerRouting: %v", err)
	}

	// Inject deterministic rollback fault.
	op.SetServerRoutingTxFaultHooks(ServerRoutingTxFaultHooks{
		BeforeRollback: func(id string) error {
			return fmt.Errorf("simulated disk I/O error restoring snapshot for %s", id)
		},
	})
	rbErr := op.RollbackServerRouting(context.Background(), txID)
	if rbErr == nil || !errors.Is(rbErr, ErrRecoveryRequired) {
		t.Fatalf("expected RollbackServerRouting failure to return ErrRecoveryRequired, got: %v", rbErr)
	}
	if !op.IsRecoveryRequired() {
		t.Fatalf("expected Operator.IsRecoveryRequired() == true after rollback failure")
	}

	// Verify durable recovery-state.json marker on disk and across Operator restart.
	opRestart := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})
	if !opRestart.IsRecoveryRequired() {
		t.Fatalf("expected restarted Operator to load durable recovery_required marker from disk")
	}
	marker := opRestart.RecoveryMarkerInfo()
	if marker == nil || marker.FailedStep != "rollback_server_routing" || marker.TransitionID != txID {
		t.Fatalf("unexpected recovery marker metadata: %+v", marker)
	}

	// Verify further mutations are strictly blocked until recovery is resolved.
	if _, err := opRestart.PrepareServerRouting(context.Background(), "tx-blocked", "vless_xhttp_server", false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected PrepareServerRouting to be blocked with ErrRecoveryRequired, got: %v", err)
	}
}

func TestServerRoutingTxConcurrentMutationSerialization(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	cfgDir := t.TempDir()
	op := NewOperator(Options{
		ConfigDir:  cfgDir,
		SocketPath: filepath.Join(cfgDir, "nonexistent.sock"),
	})
	if err := op.SaveExtConfig(ExtConfig{}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	if err := op.SaveGroupsFile([]Group{{ID: "00000001", Name: "main", Interface: GroupInterface{"vless0"}, Enable: true}}); err != nil {
		t.Fatalf("SaveGroupsFile: %v", err)
	}

	var activeInCommit atomic.Int32
	var maxConcurrentInCommit atomic.Int32

	op.SetServerRoutingTxFaultHooks(ServerRoutingTxFaultHooks{
		BeforeSaveExt: func(txID string) error {
			cur := activeInCommit.Add(1)
			for {
				prevMax := maxConcurrentInCommit.Load()
				if cur <= prevMax || maxConcurrentInCommit.CompareAndSwap(prevMax, cur) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			activeInCommit.Add(-1)
			return nil
		},
	})
	defer op.SetServerRoutingTxFaultHooks(ServerRoutingTxFaultHooks{})

	const workers = 6
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			txID := fmt.Sprintf("tx-conc-%d", idx)
			enable := idx%2 == 0
			id, err := op.PrepareServerRouting(context.Background(), txID, "vless_xhttp_server", enable)
			if err != nil {
				errCh <- fmt.Errorf("worker %d PrepareServerRouting: %w", idx, err)
				return
			}
			if err := op.CommitServerRouting(context.Background(), id); err != nil {
				errCh <- fmt.Errorf("worker %d CommitServerRouting: %w", idx, err)
				return
			}
			if err := op.FinalizeServerRouting(id); err != nil {
				errCh <- fmt.Errorf("worker %d FinalizeServerRouting: %w", idx, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	if gotMax := maxConcurrentInCommit.Load(); gotMax != 1 {
		t.Fatalf("expected strict mutationMu serialization (maxConcurrentInCommit == 1), got %d", gotMax)
	}
}

// F833: правка awgm-ext.json, сделанная после Commit (mutationMu между
// Prepare/Commit и откатом отпущен), откатом не затирается. Без правки откат
// по-прежнему возвращает файл к копии.
func TestServerRoutingTxRollbackKeepsEditAfterCommit(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	for _, edit := range []bool{false, true} {
		cfgDir := t.TempDir()
		op := NewOperator(Options{ConfigDir: cfgDir, SocketPath: filepath.Join(cfgDir, "nonexistent.sock")})
		if err := op.SaveExtConfig(ExtConfig{}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(op.extConfigPath())
		txID, err := op.PrepareServerRouting(context.Background(), "tx-edit", "vless_xhttp_server", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := op.CommitServerRouting(context.Background(), txID); err != nil {
			t.Fatal(err)
		}
		var edited []byte
		if edit {
			edited = []byte(`{"userEdit":true}` + "\n")
			if err := os.WriteFile(op.extConfigPath(), edited, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := op.RollbackServerRouting(context.Background(), txID); err != nil {
			t.Fatalf("rollback (edit=%v): %v", edit, err)
		}
		got, _ := os.ReadFile(op.extConfigPath())
		want := before
		if edit {
			want = edited
		}
		if string(got) != string(want) {
			t.Fatalf("edit=%v: ext after rollback = %q, want %q", edit, got, want)
		}
	}
}
