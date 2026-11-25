package firc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SetServerRoutingTxFaultHooks configures fault-injection hooks for server routing transactions.
func (op *Operator) SetServerRoutingTxFaultHooks(hooks ServerRoutingTxFaultHooks) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.srTxFaultHooks = hooks
}

func (op *Operator) getServerRoutingTxFaultHooks() ServerRoutingTxFaultHooks {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.srTxFaultHooks
}

type srTxManifest struct {
	TxID          string    `json:"tx_id"`
	ServerID      string    `json:"server_id"`
	Enabled       bool      `json:"enabled"`
	PrevExt       ExtConfig `json:"prev_ext"`
	CandidateExt  ExtConfig `json:"candidate_ext"`
	ExtExisted    bool      `json:"ext_existed"`
	GroupsExisted bool      `json:"groups_existed"`
	WasRunning    bool      `json:"was_running"`
	LiveGroups    []Group   `json:"live_groups,omitempty"`
	Committed     bool      `json:"committed"`
	CreatedAt     string    `json:"created_at"`
}

func (op *Operator) srTxDir(txID string) string {
	return filepath.Join(op.configDir, "sr-tx", txID)
}

// IsServerRoutingEnabled returns whether the given serverID (e.g. "vless_xhttp_server")
// is enabled in the persisted FIRC ExtConfig.
func (op *Operator) IsServerRoutingEnabled(serverID string) bool {
	ext := op.LoadExtConfig()
	return ext.ServerRouting.IsServerEnabled(serverID)
}

// PrepareServerRouting stages a transactional FIRC ServerRouting mutation with durable
// snapshots of awgm-ext.json, groups.yaml, and live fircd groups before committing.
func (op *Operator) PrepareServerRouting(ctx context.Context, txID string, serverID string, enabled bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()

	if !enabled && op.CheckNotForeign() != nil {
		// Снимать нечего: при стороннем firc маршрутизация серверов через FIRC
		// не действует, а сервер должен остаться настраиваемым.
		return "", nil
	}
	if err := op.checkMutationAllowed(); err != nil {
		return "", err
	}
	txID = strings.TrimSpace(txID)
	if txID == "" {
		txID = fmt.Sprintf("firc-sr-%d", time.Now().UnixNano())
	}
	// txID становится именем каталога, а откат делает RemoveAll по нему.
	if strings.ContainsAny(txID, `/\`) || txID == "." || txID == ".." {
		return "", fmt.Errorf("invalid firc sr-tx id %q", txID)
	}
	if op.configDir == "" {
		return "", errors.New("firc config directory is not configured")
	}

	txDir := op.srTxDir(txID)
	if err := os.MkdirAll(txDir, 0700); err != nil {
		return "", fmt.Errorf("create firc sr-tx dir: %w", err)
	}

	extSnap, err := captureFileSnapshot(op.extConfigPath(), 0644)
	if err != nil {
		_ = os.RemoveAll(txDir)
		return "", fmt.Errorf("snapshot awgm-ext.json: %w", err)
	}
	if extSnap.exists {
		if err := atomicWriteYAMLFile(filepath.Join(txDir, "awgm-ext.json.bak"), extSnap.data, 0600); err != nil {
			_ = os.RemoveAll(txDir)
			return "", fmt.Errorf("write awgm-ext.json.bak: %w", err)
		}
	}

	groupsSnap, err := captureFileSnapshot(op.GroupsPath(), 0644)
	if err != nil {
		_ = os.RemoveAll(txDir)
		return "", fmt.Errorf("snapshot groups.yaml: %w", err)
	}
	if groupsSnap.exists {
		if err := atomicWriteYAMLFile(filepath.Join(txDir, "groups.yaml.bak"), groupsSnap.data, 0600); err != nil {
			_ = os.RemoveAll(txDir)
			return "", fmt.Errorf("write groups.yaml.bak: %w", err)
		}
	}

	wasRunning := op.IsRunning(ctx)
	var liveGroups []Group
	if wasRunning {
		live, err := op.Client().GetGroups(ctx, true)
		if err != nil {
			_ = os.RemoveAll(txDir)
			return "", fmt.Errorf("capture live fircd groups before server routing tx: %w", err)
		}
		liveGroups = live
	}

	prevExt := op.LoadExtConfig()
	candExt := prevExt
	candExt.ServerRouting.SetServerEnabled(serverID, enabled)
	candExt = normalizeExtConfig(candExt)

	manifest := srTxManifest{
		TxID:          txID,
		ServerID:      serverID,
		Enabled:       enabled,
		PrevExt:       prevExt,
		CandidateExt:  candExt,
		ExtExisted:    extSnap.exists,
		GroupsExisted: groupsSnap.exists,
		WasRunning:    wasRunning,
		LiveGroups:    liveGroups,
		Committed:     false,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	mBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(txDir)
		return "", fmt.Errorf("marshal firc sr-tx manifest: %w", err)
	}
	if err := atomicWriteYAMLFile(filepath.Join(txDir, "manifest.json"), mBytes, 0600); err != nil {
		_ = os.RemoveAll(txDir)
		return "", fmt.Errorf("write firc sr-tx manifest: %w", err)
	}

	return txID, nil
}

// CommitServerRouting applies the prepared FIRC ServerRouting mutation to awgm-ext.json,
// groups.yaml, live fircd groups, and netfilter hooks with strict error propagation.
func (op *Operator) CommitServerRouting(ctx context.Context, txID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()

	if err := op.checkMutationAllowed(); err != nil {
		return err
	}

	txDir := op.srTxDir(txID)
	manifestPath := filepath.Join(txDir, "manifest.json")
	mBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read firc sr-tx manifest %s: %w", txID, err)
	}
	var manifest srTxManifest
	if err := json.Unmarshal(mBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal firc sr-tx manifest %s: %w", txID, err)
	}
	if manifest.Committed {
		return nil
	}

	// Re-apply the ServerRouting toggle onto the latest on-disk ExtConfig so any concurrent
	// KeenDNS update between PrepareServerRouting and CommitServerRouting is preserved.
	curExt := op.LoadExtConfig()
	curExt.ServerRouting = manifest.CandidateExt.ServerRouting
	if strings.TrimSpace(manifest.ServerID) != "" {
		curExt.ServerRouting.SetServerEnabled(manifest.ServerID, manifest.Enabled)
	}
	manifest.CandidateExt = normalizeExtConfig(curExt)

	hooks := op.getServerRoutingTxFaultHooks()
	if hooks.BeforeSaveExt != nil {
		if err := hooks.BeforeSaveExt(txID); err != nil {
			return fmt.Errorf("save firc ext config: %w", err)
		}
	}

	if err := op.SaveExtConfig(manifest.CandidateExt); err != nil {
		return fmt.Errorf("save firc ext config: %w", err)
	}
	recordAppliedFile(txDir, op.extConfigPath(), "awgm-ext.json.applied")

	if hooks.BeforeReconcile != nil {
		if err := hooks.BeforeReconcile(txID); err != nil {
			return fmt.Errorf("reconcile firc ext runtime: %w", err)
		}
	}

	if err := op.reconcileExtStrict(ctx, manifest.CandidateExt); err != nil {
		return fmt.Errorf("reconcile firc ext runtime: %w", err)
	}
	recordAppliedFile(txDir, op.GroupsPath(), "groups.yaml.applied")

	manifest.Committed = true
	updatedBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal committed firc sr-tx manifest: %w", err)
	}
	if err := atomicWriteYAMLFile(manifestPath, updatedBytes, 0600); err != nil {
		return fmt.Errorf("write committed firc sr-tx manifest: %w", err)
	}
	return nil
}

func (op *Operator) reconcileExtStrict(ctx context.Context, ext ExtConfig) error {
	running := op.IsRunning(ctx)
	var groups []Group
	var err error
	if running {
		groups, err = op.Client().GetGroups(ctx, true)
		if err != nil {
			return fmt.Errorf("get live fircd groups: %w", err)
		}
	} else {
		groups, err = op.LoadGroupsFile()
		if err != nil {
			return fmt.Errorf("load firc groups.yaml: %w", err)
		}
	}

	updated, changed := op.syncGroupsWithExt(ctx, groups, ext)
	if changed {
		if err := op.SaveGroupsFile(updated); err != nil {
			return fmt.Errorf("apply firc groups: save groups.yaml: %w", err)
		}
		groups = updated
		if running {
			res, syncErr := op.SyncGroups(ctx, updated)
			if syncErr != nil {
				return fmt.Errorf("apply firc groups: %w", syncErr)
			}
			if len(res) > 0 {
				if err := op.SaveGroupsFile(res); err != nil {
					return fmt.Errorf("apply firc groups: save canonical groups.yaml: %w", err)
				}
				groups = res
			}
		}
	}
	if err := op.ReconcileExtNetfilterStrict(ctx, ext, groups); err != nil {
		return fmt.Errorf("reconcile firc netfilter: %w", err)
	}
	return nil
}

// RollbackServerRouting restores awgm-ext.json, groups.yaml, live fircd groups, and netfilter
// state from the prepared snapshot. If restoration fails, it marks the Operator as recovery_required.
func (op *Operator) RollbackServerRouting(ctx context.Context, txID string) error {
	// Immediately invalidate any pending asynchronous netfilter pass so a cancelled candidate
	// cannot replay its rules after rollback completes.
	extNetfilterGen.Add(1)
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()

	// Каталог firc чужой: начатую до него транзакцию не трогаем, её подберёт
	// RecoverIncompleteServerRoutingTx на первом запуске без стороннего firc.
	if op.CheckNotForeign() != nil {
		return nil
	}
	txDir := op.srTxDir(txID)
	manifestPath := filepath.Join(txDir, "manifest.json")
	mBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read firc sr-tx manifest for rollback %s: %w", txID, err)
	}
	var manifest srTxManifest
	if err := json.Unmarshal(mBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal firc sr-tx manifest for rollback %s: %w", txID, err)
	}

	hooks := op.getServerRoutingTxFaultHooks()
	var rbErrs []error
	if hooks.BeforeRollback != nil {
		if hErr := hooks.BeforeRollback(txID); hErr != nil {
			rbErrs = append(rbErrs, fmt.Errorf("firc rollback fault: %w", hErr))
		}
	}

	// Между Prepare и откатом mutationMu отпущен: файл, изменённый уже после
	// нашего Commit, — чужая правка, и восстановление копии её бы стёрло.
	skipExt := changedSinceApplied(txDir, op.extConfigPath(), "awgm-ext.json.applied")
	skipGroups := changedSinceApplied(txDir, op.GroupsPath(), "groups.yaml.applied")
	if skipExt || skipGroups {
		op.log.Warn("firc server routing rollback keeps files edited after commit", "tx", txID, "ext", skipExt, "groups", skipGroups)
	}

	if len(rbErrs) == 0 {
		// 1. Restore live fircd runtime groups (with save=false so fircd does not overwrite groups.yaml)
		// and strictly reconcile netfilter hooks back to PrevExt + LiveGroups.
		rbCtx := ctx
		if rbCtx == nil || rbCtx.Err() != nil {
			var cancel context.CancelFunc
			rbCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		if !skipGroups && (manifest.WasRunning || op.IsRunning(rbCtx)) {
			if _, syncErr := op.SyncGroupsRuntimeOnly(rbCtx, manifest.LiveGroups); syncErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore live fircd groups: %w", syncErr))
			} else if nfErr := op.ReconcileExtNetfilterStrict(rbCtx, manifest.PrevExt, manifest.LiveGroups); nfErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore firc netfilter: %w", nfErr))
			}
		}

		// 2. Restore awgm-ext.json and verify byte-for-byte restoration on disk.
		switch {
		case skipExt: // правили после нашего Commit — не трогаем
		case manifest.ExtExisted:
			bakBytes, rErr := os.ReadFile(filepath.Join(txDir, "awgm-ext.json.bak"))
			if rErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("read awgm-ext.json.bak: %w", rErr))
			} else if wErr := atomicWriteYAMLFile(op.extConfigPath(), bakBytes, 0644); wErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore awgm-ext.json: %w", wErr))
			} else if gotBytes, vErr := os.ReadFile(op.extConfigPath()); vErr != nil || !bytes.Equal(gotBytes, bakBytes) {
				rbErrs = append(rbErrs, fmt.Errorf("verify restored awgm-ext.json: %v", vErr))
			}
		default:
			if rmErr := os.Remove(op.extConfigPath()); rmErr != nil && !os.IsNotExist(rmErr) {
				rbErrs = append(rbErrs, fmt.Errorf("remove uncommitted awgm-ext.json: %w", rmErr))
			}
		}

		// 3. Restore groups.yaml last and verify byte-for-byte restoration on disk so pre-apply disk
		// state is preserved even when it differed from live fircd runtime groups.
		switch {
		case skipGroups: // правили после нашего Commit — не трогаем
		case manifest.GroupsExisted:
			bakBytes, rErr := os.ReadFile(filepath.Join(txDir, "groups.yaml.bak"))
			if rErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("read groups.yaml.bak: %w", rErr))
			} else if wErr := atomicWriteYAMLFile(op.GroupsPath(), bakBytes, 0644); wErr != nil {
				rbErrs = append(rbErrs, fmt.Errorf("restore groups.yaml: %w", wErr))
			} else if gotBytes, vErr := os.ReadFile(op.GroupsPath()); vErr != nil || !bytes.Equal(gotBytes, bakBytes) {
				rbErrs = append(rbErrs, fmt.Errorf("verify restored groups.yaml: %v", vErr))
			}
		default:
			if rmErr := os.Remove(op.GroupsPath()); rmErr != nil && !os.IsNotExist(rmErr) {
				rbErrs = append(rbErrs, fmt.Errorf("remove uncommitted groups.yaml: %w", rmErr))
			}
		}
	}

	// Пропущенный файл оставил на диске чужую правку: группы и netfilter
	// сверяем с тем, что лежит на диске, а не с PrevExt/CandidateExt.
	if len(rbErrs) == 0 && (skipExt || skipGroups) {
		rbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := op.reconcileExtStrict(rbCtx, op.LoadExtConfig()); err != nil {
			rbErrs = append(rbErrs, fmt.Errorf("reconcile firc after partial rollback: %w", err))
		}
		cancel()
	}

	if len(rbErrs) > 0 {
		reason := fmt.Sprintf("RollbackServerRouting(%s) failed: %v", txID, errors.Join(rbErrs...))
		persistErr := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:         reason,
			TransitionID:   txID,
			TargetEngine:   "firc",
			PreviousEngine: "firc",
			FailedStep:     "rollback_server_routing",
			LastCompleted:  "prepare_server_routing",
		})
		return errors.Join(append(rbErrs, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), persistErr)...)
	}

	_ = os.RemoveAll(txDir)
	return nil
}

// FinalizeServerRouting cleans up the transaction snapshot directory after commit boundary.
func (op *Operator) FinalizeServerRouting(txID string) error {
	if strings.TrimSpace(txID) == "" || op.configDir == "" || op.CheckNotForeign() != nil {
		return nil
	}
	return os.RemoveAll(op.srTxDir(txID))
}

// RecoverIncompleteServerRoutingTx inspects any pending sr-tx journals after a crash/restart:
//   - Uncommitted ("preparing") transactions are deterministically rolled back to pre-tx state.
//   - Committed ("committing") transactions past the commit point are reconciled and finalized.
func (op *Operator) RecoverIncompleteServerRoutingTx(ctx context.Context) error {
	if op.configDir == "" || op.CheckNotForeign() != nil {
		return nil
	}
	srRoot := filepath.Join(op.configDir, "sr-tx")
	entries, err := os.ReadDir(srRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read firc sr-tx root: %w", err)
	}

	var recErrs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		txID := entry.Name()
		manifestPath := filepath.Join(srRoot, txID, "manifest.json")
		mBytes, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				_ = os.RemoveAll(filepath.Join(srRoot, txID))
				continue
			}
			recErrs = append(recErrs, fmt.Errorf("read sr-tx manifest %s: %w", txID, err))
			continue
		}
		var manifest srTxManifest
		if err := json.Unmarshal(mBytes, &manifest); err != nil {
			reason := fmt.Sprintf("corrupt firc sr-tx manifest %s: %v", txID, err)
			_ = op.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:         reason,
				TransitionID:   txID,
				TargetEngine:   "firc",
				PreviousEngine: "firc",
				FailedStep:     "recover_server_routing_tx",
				LastCompleted:  "prepare_server_routing",
			})
			recErrs = append(recErrs, fmt.Errorf("%w: %s", ErrRecoveryRequired, reason))
			continue
		}

		if !manifest.Committed {
			if rbErr := op.RollbackServerRouting(ctx, txID); rbErr != nil {
				recErrs = append(recErrs, rbErr)
			}
			continue
		}

		// Committed == true ("committing" phase before FinalizeServerRouting):
		// Ensure candidate state is persisted and reconciled, then finalize.
		// Если ext правили уже после Commit (каталог не удалился из-за сбоя
		// finalize), устаревший CandidateExt поверх новой правки не пишем.
		if changedSinceApplied(filepath.Join(srRoot, txID), op.extConfigPath(), "awgm-ext.json.applied") {
			if err := op.FinalizeServerRouting(txID); err != nil {
				recErrs = append(recErrs, fmt.Errorf("recover committed sr-tx %s finalize: %w", txID, err))
			}
			continue
		}
		op.mutationMu.Lock()
		if err := op.SaveExtConfig(manifest.CandidateExt); err != nil {
			op.mutationMu.Unlock()
			recErrs = append(recErrs, fmt.Errorf("recover committed sr-tx %s save ext: %w", txID, err))
			continue
		}
		if err := op.reconcileExtStrict(ctx, manifest.CandidateExt); err != nil {
			op.mutationMu.Unlock()
			recErrs = append(recErrs, fmt.Errorf("recover committed sr-tx %s reconcile: %w", txID, err))
			continue
		}
		op.mutationMu.Unlock()
		if err := op.FinalizeServerRouting(txID); err != nil {
			recErrs = append(recErrs, fmt.Errorf("recover committed sr-tx %s finalize: %w", txID, err))
		}
	}
	return errors.Join(recErrs...)
}

// recordAppliedFile кладёт в каталог транзакции копию того, что Commit оставил
// на диске. Ошибка не фатальна: без отметки откат ведёт себя как раньше.
func recordAppliedFile(txDir, path, marker string) {
	if data, err := os.ReadFile(path); err == nil {
		_ = atomicWriteYAMLFile(filepath.Join(txDir, marker), data, 0600)
	}
}

// changedSinceApplied — файл после нашего Commit кто-то переписал. Без отметки
// (Commit файл не тронул или упал раньше) — false, откат по-старому.
func changedSinceApplied(txDir, path, marker string) bool {
	applied, err := os.ReadFile(filepath.Join(txDir, marker))
	if err != nil {
		return false
	}
	cur, err := os.ReadFile(path)
	return err == nil && !bytes.Equal(cur, applied)
}
