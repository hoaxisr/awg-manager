package firc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func seedPurgeWorkspace(t *testing.T) (sbDir string, awgmDir string, fircDir string) {
	t.Helper()
	root := t.TempDir()
	awgmDir = filepath.Join(root, "opt", "etc", "awg-manager")
	sbDir = filepath.Join(awgmDir, "singbox", "config.d")
	fircDir = filepath.Join(root, "opt", "etc", "firc")
	for _, dir := range []string{sbDir, fircDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	files := map[string]string{
		filepath.Join(awgmDir, "settings.json"):            `{"singboxRouter":{"routingEngine":"sing-box"}}`,
		filepath.Join(awgmDir, "subscriptions.json"):       `[{"id":"sub1","name":"Sub 1","url":"https://example.com/sub"}]`,
		filepath.Join(awgmDir, "subscription-groups.json"): `[{"id":"sg1","name":"SG 1"}]`,
		filepath.Join(awgmDir, "awg3.json"):                `[{"tag":"awg3-1"}]`,
		filepath.Join(awgmDir, "client-routes.json"):       `{"routes":[],"tables":{}}`,
		filepath.Join(sbDir, "00-base.json"):               `{"log":{"level":"info"}}`,
		filepath.Join(sbDir, "10-tunnels.json"):            `{"outbounds":[{"type":"vless","tag":"tun-1","server":"example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`,
		filepath.Join(sbDir, "20-router.json"):             `{"route":{"rules":[]}}`,
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return sbDir, awgmDir, fircDir
}

func TestPurgeFircAndTunnels_RejectsActiveEngine(t *testing.T) {
	sbDir, _, fircDir := seedPurgeWorkspace(t)
	op := NewOperator(Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	_, err := op.PurgeFircAndTunnels(context.Background(), sbDir, "firc", nil, nil, EnginePurgeHooks{})
	if !errors.Is(err, ErrCannotPurgeActiveEngine) {
		t.Fatalf("expected ErrCannotPurgeActiveEngine, got: %v", err)
	}
}

func TestPurgeFircAndTunnels_FullPurgeAndSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	inst, pm, _ := runtimeFixture(t)
	if err := inst.Install(ctx); err != nil {
		t.Fatalf("install runtime fixture: %v", err)
	}
	if st := inst.GetInstallStatus(ctx, nil, false); st.InstalledVersion == "" {
		t.Fatalf("expected installed runtime version, got %+v", st)
	}

	root := inst.layout.RootDir
	awgmDir := filepath.Join(root, "opt", "etc", "awg-manager")
	sbDir := filepath.Join(awgmDir, "singbox", "config.d")
	fircDir := filepath.Join(root, "opt", "etc", "firc")
	varLibDir := filepath.Join(root, "opt", "var", "lib", "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(varLibDir, "nested"), 0755); err != nil {
		t.Fatalf("mkdir varLibDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(fircDir, "sr-tx"), 0755); err != nil {
		t.Fatalf("mkdir sr-tx: %v", err)
	}

	seedFiles := map[string]string{
		filepath.Join(awgmDir, "settings.json"):            `{"singboxRouter":{"routingEngine":"sing-box"}}`,
		filepath.Join(awgmDir, "subscriptions.json"):       `[{"id":"sub1","name":"Sub 1","url":"https://example.com/sub"}]`,
		filepath.Join(awgmDir, "subscription-groups.json"): `[{"id":"sg1","name":"SG 1"}]`,
		filepath.Join(awgmDir, "awg3.json"):                `[{"tag":"awg3-1"}]`,
		filepath.Join(sbDir, "00-base.json"):               `{"log":{"level":"info"}}`,
		filepath.Join(sbDir, "10-tunnels.json"):            `{"outbounds":[{"type":"vless","tag":"sb-tun","server":"example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`,
		filepath.Join(fircDir, "tunnels.yaml"):             "tunnels:\n  - id: f1\n    device: tunvless0\n    enable: true\n  - id: f2\n    device: tunvless1\n    enable: true\n",
		filepath.Join(fircDir, "groups.yaml"):              "groups:\n  - id: g1\n    name: Group 1\n    interface:\n      - tunvless0\n  - id: g2\n    name: Group 2\n    interface:\n      - tunvless1\n",
		filepath.Join(fircDir, "firc.conf"):                "app:\n  autolist:\n    enabled: true\n",
		filepath.Join(fircDir, "config.yaml"):              "listen: 127.0.0.1:1099\n",
		filepath.Join(fircDir, "awgm-ext.json"):            `{"proxyGroups":[]}`,
		filepath.Join(fircDir, "v6-pool-prefix"):           "fd00:f1rc::/64\n",
		filepath.Join(fircDir, ".awgm-disabled"):           "disabled\n",
		filepath.Join(fircDir, "SHA256SUMS"):               "deadbeef  firc.conf\n",
		filepath.Join(fircDir, "sr-tx", "tx1.json"):        `{"pending":true}`,
		filepath.Join(varLibDir, "nested", "cache.db"):     "cache",
	}
	for p, content := range seedFiles {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	op := NewOperator(Options{
		ConfigDir:   fircDir,
		FircdBinary: inst.layout.FircdBin,
		TunvlessBin: inst.layout.TunvlessBin,
		InitScript:  inst.layout.InitScript,
		InboundPort: 1099,
	})
	op.SetSingboxValidator(func(context.Context, string) error { return nil })
	op.SetVarLibDir(varLibDir)
	stopCalls := 0
	op.SetLifecycleHooks(
		func(context.Context) error { return nil },
		func(context.Context) error {
			stopCalls++
			return nil
		},
	)

	if err := op.SavePreFircSingboxConfig(sbDir, true); err != nil {
		t.Fatalf("SavePreFircSingboxConfig: %v", err)
	}
	if err := op.SaveMigrationWizardState(MigrationWizardState{DismissedAutoPopup: true, LastDirection: "singbox->firc"}); err != nil {
		t.Fatalf("SaveMigrationWizardState: %v", err)
	}

	onBeforeCalls := 0
	res, err := op.PurgeFircAndTunnels(ctx, sbDir, "sing-box", inst, nil, EnginePurgeHooks{
		OnBeforeRemoveFircRuntime: func(context.Context) {
			onBeforeCalls++
		},
	})
	if err != nil {
		t.Fatalf("PurgeFircAndTunnels: %v", err)
	}
	if res.Engine != "firc" || res.TunnelsRemoved != 2 || res.GroupsRemoved != 2 || !res.RuntimeRemoved {
		t.Fatalf("unexpected purge result: %+v", res)
	}
	if res.SnapshotCreated == nil || res.SnapshotCreated.Trigger != "auto-pre-purge" {
		t.Fatalf("expected auto-pre-purge snapshot, got %+v", res.SnapshotCreated)
	}
	if !res.SavedSingboxPreserved || res.PreservedSnapshots < 1 {
		t.Fatalf("expected recovery bundles preserved, got %+v", res)
	}
	if stopCalls == 0 || onBeforeCalls != 1 {
		t.Fatalf("expected stopCalls>0 and onBeforeCalls=1, got stop=%d onBefore=%d", stopCalls, onBeforeCalls)
	}
	if pm.removes != 1 {
		t.Fatalf("expected runtime package uninstall count=1, got %d", pm.removes)
	}

	removedPaths := []string{
		filepath.Join(fircDir, "tunnels.yaml"),
		filepath.Join(fircDir, "groups.yaml"),
		filepath.Join(fircDir, "firc.conf"),
		filepath.Join(fircDir, "config.yaml"),
		filepath.Join(fircDir, "awgm-ext.json"),
		filepath.Join(fircDir, "v6-pool-prefix"),
		filepath.Join(fircDir, ".awgm-disabled"),
		filepath.Join(fircDir, "SHA256SUMS"),
		filepath.Join(fircDir, "sr-tx"),
		varLibDir,
	}
	for _, p := range removedPaths {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be deleted, stat err=%v", p, statErr)
		}
	}

	preservedPaths := []string{
		filepath.Join(fircDir, snapshotsSubdirName, res.SnapshotCreated.ID+".json"),
		filepath.Join(fircDir, savedSingboxBundleName),
		filepath.Join(fircDir, wizardStateFileName),
		filepath.Join(sbDir, "10-tunnels.json"),
		filepath.Join(awgmDir, "subscriptions.json"),
		filepath.Join(awgmDir, "subscription-groups.json"),
		filepath.Join(awgmDir, "awg3.json"),
	}
	for _, p := range preservedPaths {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Fatalf("expected recovery/singbox path %s preserved: %v", p, statErr)
		}
	}

	// Reinstall FIRC runtime and restore the auto-pre-purge snapshot -> full recovery.
	if err := inst.Install(ctx); err != nil {
		t.Fatalf("reinstall runtime fixture: %v", err)
	}
	restored, err := op.RestoreSystemSnapshot(ctx, res.SnapshotCreated.ID, sbDir)
	if err != nil {
		t.Fatalf("RestoreSystemSnapshot after FIRC purge: %v", err)
	}
	if restored.FircTunnelsCount != 2 || restored.FircGroupsCount != 2 {
		t.Fatalf("unexpected restored counts: %+v", restored)
	}
	for _, p := range []string{
		filepath.Join(fircDir, "tunnels.yaml"),
		filepath.Join(fircDir, "groups.yaml"),
		filepath.Join(fircDir, "firc.conf"),
		filepath.Join(fircDir, "awgm-ext.json"),
	} {
		raw, rErr := os.ReadFile(p)
		if rErr != nil || len(raw) == 0 {
			t.Fatalf("expected restored file %s non-empty: err=%v len=%d", p, rErr, len(raw))
		}
	}
}

func TestPurgeSingboxAndTunnels_FailClosedAndSuccess(t *testing.T) {
	ctx := context.Background()
	sbDir, awgmDir, fircDir := seedPurgeWorkspace(t)
	if err := os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte("tunnels: []\n"), 0644); err != nil {
		t.Fatalf("write firc tunnels: %v", err)
	}
	op := NewOperator(Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	op.SetSingboxValidator(func(context.Context, string) error { return nil })

	purgeCalls := 0
	hooks := EnginePurgeHooks{
		PurgeSingbox: func(context.Context) (int, int, error) {
			purgeCalls++
			return 1, 1, nil
		},
	}

	// 1. Active sing-box cannot be purged.
	if _, err := op.PurgeSingboxAndTunnels(ctx, sbDir, "sing-box", nil, hooks); !errors.Is(err, ErrCannotPurgeActiveEngine) {
		t.Fatalf("expected ErrCannotPurgeActiveEngine, got %v", err)
	}
	if purgeCalls != 0 {
		t.Fatalf("PurgeSingbox callback called on active sing-box")
	}

	// 2. Fail closed if snapshot verification on disk fails.
	missingSnap := &SnapshotMeta{ID: "snap-missing-on-disk"}
	if _, err := op.PurgeSingboxAndTunnels(ctx, sbDir, "firc", missingSnap, hooks); err == nil {
		t.Fatal("expected error when preCreatedSnap file does not exist on disk")
	}
	if purgeCalls != 0 {
		t.Fatalf("PurgeSingbox callback called despite missing snapshot file")
	}
	if _, err := os.Stat(filepath.Join(sbDir, "10-tunnels.json")); err != nil {
		t.Fatalf("sing-box file removed despite failed snapshot verification: %v", err)
	}

	// 3. Successful purge under active FIRC creates auto-pre-purge snapshot & saved-singbox bundle.
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"firc"}}`), 0644); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	res, err := op.PurgeSingboxAndTunnels(ctx, sbDir, "firc", nil, hooks)
	if err != nil {
		t.Fatalf("PurgeSingboxAndTunnels: %v", err)
	}
	if purgeCalls != 1 {
		t.Fatalf("expected purgeCalls=1, got %d", purgeCalls)
	}
	if res.Engine != "sing-box" || res.TunnelsRemoved != 1 || res.SubscriptionsRemoved != 1 || !res.RuntimeRemoved {
		t.Fatalf("unexpected PurgeSingboxAndTunnels result: %+v", res)
	}
	if res.SnapshotCreated == nil || res.SnapshotCreated.Trigger != "auto-pre-purge" {
		t.Fatalf("expected auto-pre-purge snapshot, got %+v", res.SnapshotCreated)
	}
	if !res.SavedSingboxPreserved || res.PreservedSnapshots < 1 {
		t.Fatalf("expected saved-singbox & snapshots preserved, got %+v", res)
	}

	rawSnap, err := os.ReadFile(filepath.Join(fircDir, snapshotsSubdirName, res.SnapshotCreated.ID+".json"))
	if err != nil {
		t.Fatalf("read created snapshot: %v", err)
	}
	var snap SystemSnapshot
	if err := json.Unmarshal(rawSnap, &snap); err != nil {
		t.Fatalf("unmarshal created snapshot: %v", err)
	}
	foundFiles := map[string]bool{}
	for _, f := range snap.Files {
		foundFiles[f.Scope+":"+f.RelPath] = true
	}
	for _, mustContain := range []string{
		"singbox:10-tunnels.json",
		"awgm:subscriptions.json",
		"awgm:subscription-groups.json",
		"awgm:awg3.json",
	} {
		if !foundFiles[mustContain] {
			t.Fatalf("expected %s captured in pre-purge snapshot, got keys: %+v", mustContain, foundFiles)
		}
	}
	if _, err := os.Stat(filepath.Join(fircDir, savedSingboxBundleName)); err != nil {
		t.Fatalf("expected saved-singbox-config.json preserved: %v", err)
	}
}

func TestPruneOldSnapshots_ProtectsAutoPrePurgeAndMigrate(t *testing.T) {
	_, _, fircDir := seedPurgeWorkspace(t)
	op := NewOperator(Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	if err := os.MkdirAll(op.snapshotsDir(), 0755); err != nil {
		t.Fatalf("mkdir snapshots: %v", err)
	}

	writeSynthetic := func(id, trigger string, createdAt int64) {
		snap := SystemSnapshot{
			SnapshotMeta: SnapshotMeta{
				ID:           id,
				CreatedAt:    createdAt,
				Label:        id,
				Trigger:      trigger,
				ActiveEngine: "firc",
				FilesCount:   1,
			},
			Files: []SnapshotFileEntry{
				{Scope: "firc", RelPath: "tunnels.yaml", Mode: 0644, DataB64: "dHVubmVsczogW10K"},
			},
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(op.snapshotsDir(), id+".json"), raw, 0600); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
	}

	// Seed oldest auto-pre-migrate and auto-pre-purge plus maxStoredSnapshots+4 newer manual snapshots.
	writeSynthetic("snap-old-migrate", "auto-pre-migrate", 100)
	writeSynthetic("snap-old-purge", "auto-pre-purge", 200)
	for i := 0; i < maxStoredSnapshots+4; i++ {
		id := "snap-manual-" + string(rune('a'+i))
		writeSynthetic(id, "manual", int64(300+i))
	}

	op.pruneOldSnapshots()

	remaining, err := op.ListSystemSnapshots()
	if err != nil {
		t.Fatalf("ListSystemSnapshots: %v", err)
	}
	if len(remaining) != maxStoredSnapshots {
		t.Fatalf("expected %d snapshots after prune, got %d", maxStoredSnapshots, len(remaining))
	}
	byID := map[string]bool{}
	for _, m := range remaining {
		byID[m.ID] = true
	}
	if !byID["snap-old-migrate"] {
		t.Fatal("expected newest auto-pre-migrate snapshot to be protected from pruning")
	}
	if !byID["snap-old-purge"] {
		t.Fatal("expected newest auto-pre-purge snapshot to be protected from pruning")
	}
}
