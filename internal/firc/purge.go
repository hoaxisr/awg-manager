package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCannotPurgeActiveEngine is returned when a request attempts to remove the
// currently active routing engine.
var ErrCannotPurgeActiveEngine = errors.New("firc: cannot remove the currently active routing engine")

// EnginePurgeResult describes the outcome of removing an inactive routing engine
// together with its tunnels, rules/subscriptions, and runtime binaries while
// preserving recoverable system snapshots.
type EnginePurgeResult struct {
	Engine                string        `json:"engine"`
	TunnelsRemoved        int           `json:"tunnelsRemoved"`
	GroupsRemoved         int           `json:"groupsRemoved"`
	SubscriptionsRemoved  int           `json:"subscriptionsRemoved,omitempty"`
	RuntimeRemoved        bool          `json:"runtimeRemoved"`
	SnapshotCreated       *SnapshotMeta `json:"snapshotCreated,omitempty"`
	PreservedSnapshots    int           `json:"preservedSnapshots"`
	SavedSingboxPreserved bool          `json:"savedSingboxPreserved"`
}

// EnginePurgeHooks supplies cross-package cleanup and notification callbacks
// required during full engine + tunnel removal.
type EnginePurgeHooks struct {
	// PurgeSingbox stops sing-box, removes owned NDMS ProxyN interfaces and
	// deletes singbox runtime/config/subscription artifacts.
	PurgeSingbox func(ctx context.Context) (tunnelsRemoved, subsRemoved int, err error)
	// OnBeforeRemoveFircRuntime runs right before uninstalling FIRC binaries
	// (e.g. clearing built-in VLESS server FIRC override).
	OnBeforeRemoveFircRuntime func(ctx context.Context)
}

func (o *Operator) verifySnapshotFileOnDisk(snap *SnapshotMeta) error {
	if snap == nil || strings.TrimSpace(snap.ID) == "" {
		return errors.New("pre-purge snapshot metadata is missing")
	}
	path := filepath.Join(o.snapshotsDir(), snap.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("verify pre-purge snapshot %s on disk: %w", snap.ID, err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("pre-purge snapshot %s on disk is empty", snap.ID)
	}
	var decoded SystemSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("pre-purge snapshot %s JSON is corrupt: %w", snap.ID, err)
	}
	if len(decoded.Files) == 0 {
		return fmt.Errorf("pre-purge snapshot %s contains no files", snap.ID)
	}
	return nil
}

func (o *Operator) hasSavedSingboxBundleFile() bool {
	if strings.TrimSpace(o.configDir) == "" {
		return false
	}
	st, err := os.Stat(o.savedSingboxConfigPath())
	return err == nil && !st.IsDir() && st.Size() > 0
}

// PurgeSingboxAndTunnels removes the inactive Sing-box runtime, config.d slots,
// tunnels, subscriptions, and owned NDMS ProxyN interfaces after ensuring a
// verified recoverable SystemSnapshot and saved-singbox bundle exist on disk.
func (o *Operator) PurgeSingboxAndTunnels(
	ctx context.Context,
	singboxConfigDir string,
	activeEngine string,
	preCreatedSnap *SnapshotMeta,
	hooks EnginePurgeHooks,
) (*EnginePurgeResult, error) {
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	if err := o.checkMutationAllowed(); err != nil {
		return nil, err
	}
	curEng := o.resolveSnapshotActiveEngine(singboxConfigDir, activeEngine)
	if !strings.EqualFold(curEng, "firc") {
		return nil, fmt.Errorf("%w: active engine is %s", ErrCannotPurgeActiveEngine, curEng)
	}
	if hooks.PurgeSingbox == nil {
		return nil, errors.New("firc: sing-box purge callback is not configured")
	}

	// Best-effort ensure saved-singbox-config.json is present if valid Sing-box
	// configs exist on disk and no bundle was saved previously.
	if strings.TrimSpace(singboxConfigDir) != "" {
		_ = o.SavePreFircSingboxConfig(singboxConfigDir, false)
	}

	// Fail-closed: always guarantee a verified snapshot on disk BEFORE removing
	// any Sing-box binaries, tunnels, or subscriptions.
	snap := preCreatedSnap
	if snap == nil {
		var sErr error
		snap, sErr = o.CreateSystemSnapshot(
			singboxConfigDir,
			"Авто-снапшот перед удалением Sing-box и его туннелей",
			"auto-pre-purge",
			curEng,
		)
		if sErr != nil {
			return nil, fmt.Errorf("create pre-purge system snapshot: %w", sErr)
		}
	}
	if err := o.verifySnapshotFileOnDisk(snap); err != nil {
		return nil, err
	}

	tunnelsRemoved, subsRemoved, err := hooks.PurgeSingbox(ctx)
	if err != nil {
		return nil, fmt.Errorf("purge sing-box runtime and tunnels: %w", err)
	}

	st := o.LoadMigrationWizardState()
	st.LastDirection = "purge-singbox"
	st.LastSnapshotID = snap.ID
	_ = o.SaveMigrationWizardState(st)

	snaps, _ := o.ListSystemSnapshots()
	return &EnginePurgeResult{
		Engine:                "sing-box",
		TunnelsRemoved:        tunnelsRemoved,
		SubscriptionsRemoved:  subsRemoved,
		RuntimeRemoved:        true,
		SnapshotCreated:       snap,
		PreservedSnapshots:    len(snaps),
		SavedSingboxPreserved: o.hasSavedSingboxBundleFile(),
	}, nil
}

// PurgeFircAndTunnels stops FIRC, removes the runtime package (when installed),
// and cleans all FIRC tunnels, routing groups, configs, and runtime caches while
// strictly preserving snapshots/, saved-singbox-config.json, and wizard state.
func (o *Operator) PurgeFircAndTunnels(
	ctx context.Context,
	singboxConfigDir string,
	activeEngine string,
	inst *OnDemandInstaller,
	preCreatedSnap *SnapshotMeta,
	hooks EnginePurgeHooks,
) (*EnginePurgeResult, error) {
	var (
		tunnelsRemoved int
		groupsRemoved  int
		snap           = preCreatedSnap
		extBackupPath  = o.extConfigPath()
		extBackupBytes []byte
		hadExtFile     bool
		extPerm        os.FileMode = 0644
	)

	// Phase 1 (mutationMu): validate preconditions, count removed items, take a
	// fail-closed snapshot, and strip awgm-ext.json dependency markers after backup.
	if err := func() error {
		o.mutationMu.Lock()
		defer o.mutationMu.Unlock()

		if err := o.checkMutationAllowed(); err != nil {
			return err
		}
		curEng := o.resolveSnapshotActiveEngine(singboxConfigDir, activeEngine)
		if strings.EqualFold(curEng, "firc") {
			return fmt.Errorf("%w: active engine is %s", ErrCannotPurgeActiveEngine, curEng)
		}

		if tuns, err := o.LoadTunnelsFile(); err == nil {
			tunnelsRemoved = len(tuns)
		}
		if grps, err := o.LoadGroupsFile(); err == nil {
			groupsRemoved = len(grps)
		}

		if snap == nil {
			var sErr error
			snap, sErr = o.CreateSystemSnapshot(
				singboxConfigDir,
				"Авто-снапшот перед удалением FIRC и его туннелей",
				"auto-pre-purge",
				curEng,
			)
			if sErr != nil {
				return fmt.Errorf("create pre-purge system snapshot: %w", sErr)
			}
		}
		if err := o.verifySnapshotFileOnDisk(snap); err != nil {
			return err
		}

		if st, statErr := os.Stat(extBackupPath); statErr == nil && !st.IsDir() {
			data, rErr := os.ReadFile(extBackupPath)
			if rErr != nil {
				return fmt.Errorf("backup awgm-ext.json before purge: %w", rErr)
			}
			extBackupBytes = data
			extPerm = st.Mode().Perm()
			if extPerm == 0 {
				extPerm = 0644
			}
			hadExtFile = true
			if rmErr := os.Remove(extBackupPath); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("clear awgm-ext.json dependencies before purge: %w", rmErr)
			}
		}
		return nil
	}(); err != nil {
		return nil, err
	}

	restoreExtIfNeeded := func() {
		if hadExtFile {
			_ = atomicWriteYAMLFile(extBackupPath, extBackupBytes, extPerm)
		}
	}

	// Phase 2 (unlocked wrt mutationMu/lifecycleMu): stop daemon, clean barriers,
	// and uninstall the standalone awg-firc-runtime package if present.
	if err := o.Stop(ctx); err != nil {
		restoreExtIfNeeded()
		return nil, fmt.Errorf("stop firc runtime: %w", err)
	}
	o.CleanupPoolRejectBarrier(ctx)
	if hooks.OnBeforeRemoveFircRuntime != nil {
		hooks.OnBeforeRemoveFircRuntime(ctx)
	}

	runtimeRemoved := false
	if inst != nil {
		st := inst.GetInstallStatus(ctx, o, false)
		if st.InstalledVersion != "" {
			if err := inst.Uninstall(ctx, o, false, false); err != nil {
				restoreExtIfNeeded()
				return nil, fmt.Errorf("uninstall firc runtime package: %w", err)
			}
			runtimeRemoved = true
		}
	}

	// Phase 3 (mutationMu): delete FIRC active configs, tunnels/groups, runtime caches
	// while preserving snapshots/, saved-singbox-config.json, and awgm-migration-state.json.
	if err := func() error {
		o.mutationMu.Lock()
		defer o.mutationMu.Unlock()

		var errs []error
		if strings.TrimSpace(o.configDir) != "" {
			for _, name := range []string{
				"tunnels.yaml",
				"tunnels.yaml.bak",
				"groups.yaml",
				"groups.yaml.bak",
				"firc.conf",
				"firc.conf.bak",
				"config.yaml",
				"awgm-ext.json",
				"v6-pool-prefix",
				".awgm-disabled",
				"SHA256SUMS",
			} {
				p := filepath.Join(o.configDir, name)
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("remove %s: %w", p, err))
				}
			}
			srTxDir := filepath.Join(o.configDir, "sr-tx")
			if err := os.RemoveAll(srTxDir); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", srTxDir, err))
			}
		}

		if libDir := strings.TrimSpace(o.effectiveVarLibDir()); libDir != "" {
			if err := os.RemoveAll(libDir); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", libDir, err))
			}
		}

		if o.isSystemConfigDir() {
			cleanupExtNetfilter(ctx, "")
			for _, p := range []string{"/tmp/firc", "/tmp/firc-init"} {
				_ = os.RemoveAll(p)
			}
		} else {
			cleanupExtNetfilterRules(ctx)
		}

		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		o.extConfigMu.Lock()
		o.extConfigSaved.Store(false)
		o.extConfigGen.Add(1)
		o.extConfigMu.Unlock()

		st := o.LoadMigrationWizardState()
		st.LastDirection = "purge-firc"
		st.LastSnapshotID = snap.ID
		_ = o.SaveMigrationWizardState(st)
		return nil
	}(); err != nil {
		return nil, err
	}

	snaps, _ := o.ListSystemSnapshots()
	return &EnginePurgeResult{
		Engine:                "firc",
		TunnelsRemoved:        tunnelsRemoved,
		GroupsRemoved:         groupsRemoved,
		RuntimeRemoved:        runtimeRemoved,
		SnapshotCreated:       snap,
		PreservedSnapshots:    len(snaps),
		SavedSingboxPreserved: o.hasSavedSingboxBundleFile(),
	}, nil
}
