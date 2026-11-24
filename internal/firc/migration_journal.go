package firc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// JournalFileBackup stores the pre-mutation state of a single file for durable crash recovery.
type JournalFileBackup struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Mode    uint32 `json:"mode"`
	DataB64 string `json:"dataB64,omitempty"`
}

// MigrationJournal is the durable on-disk manifest persisted before any multi-file migration
// or snapshot restore operation begins, enabling startup rollback if the process terminates mid-flight.
type MigrationJournal struct {
	Version         int                 `json:"version"`
	JournalID       string              `json:"journalId"`
	Operation       string              `json:"operation"`
	Stage           string              `json:"stage"` // "prepared" | "removing_stale" | "writing_files" | "syncing_runtime" | "committed" | "rolled_back"
	StepIndex       int                 `json:"stepIndex"`
	CurrentFile     string              `json:"currentFile,omitempty"`
	StartedAtUnixMs int64               `json:"startedAtUnixMs"`
	UpdatedAtUnixMs int64               `json:"updatedAtUnixMs"`
	Backups         []JournalFileBackup `json:"backups"`
}

// writeMigrationJournalDetailed marshals and writes j to migration-journal.json.
// It returns renamed=true if and only if the atomic publish (storage.AtomicWritePerm) succeeded, meaning
// the new journal content has been published at migration-journal.json on disk.
// Any error occurring after os.Rename (such as syncParentDir or readback verification)
// returns renamed=true alongside the non-nil error so the caller never mistakes a
// post-publication sync/readback failure for a pre-publication write failure (C1).
func (o *Operator) writeMigrationJournalDetailed(
	j *MigrationJournal,
	syncHookOverride func(dir string) error,
	readbackOverride func(journalPath string, expected []byte) ([]byte, error),
) (renamed bool, err error) {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return false, errors.New("migration journal path is not configured")
	}
	o.mu.Lock()
	dirHook := o.dirSyncHook
	o.mu.Unlock()
	if syncHookOverride != nil {
		dirHook = syncHookOverride
	}

	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal migration journal: %w", err)
	}

	// Ошибка AtomicWritePerm — всегда до публикации или на самом rename: свой
	// fsync каталога он делает best-effort и не возвращает. Строгий fsync
	// каталога (с подменой из тестов) — ниже, уже после публикации.
	if err := storage.AtomicWritePerm(jPath, raw, 0600); err != nil {
		return false, fmt.Errorf("write migration journal: %w", err)
	}
	// Publication point passed: jPath now points to the newly renamed journal file on disk.
	renamed = true

	if err := syncParentDir(jPath, dirHook); err != nil {
		return true, fmt.Errorf("write migration journal: sync parent dir of %s: %w", jPath, err)
	}

	var got []byte
	var rbErr error
	if readbackOverride != nil {
		got, rbErr = readbackOverride(jPath, raw)
	} else {
		got, rbErr = os.ReadFile(jPath)
	}
	if rbErr != nil || !bytes.Equal(got, raw) {
		return true, fmt.Errorf("verify migration journal readback failed: %v", rbErr)
	}
	return true, nil
}

func (o *Operator) writeMigrationJournal(j *MigrationJournal) error {
	_, err := o.writeMigrationJournalDetailed(j, nil, nil)
	return err
}

func (o *Operator) beginMigrationJournal(operation string, snaps []fileSnapshot) (*MigrationJournal, error) {
	sorted := make([]fileSnapshot, len(snaps))
	copy(sorted, snaps)
	sort.Slice(sorted, func(i, k int) bool {
		return filepath.Clean(sorted[i].path) < filepath.Clean(sorted[k].path)
	})

	backups := make([]JournalFileBackup, 0, len(sorted))
	seen := make(map[string]bool, len(sorted))
	for _, s := range sorted {
		clean := filepath.Clean(s.path)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		b := JournalFileBackup{
			Path:   clean,
			Exists: s.exists,
			Mode:   uint32(s.perm),
		}
		if s.exists {
			b.DataB64 = base64.StdEncoding.EncodeToString(s.data)
		}
		backups = append(backups, b)
	}

	nowMs := time.Now().UnixMilli()
	j := &MigrationJournal{
		Version:         1,
		JournalID:       fmt.Sprintf("%s-%d", operation, time.Now().UnixNano()),
		Operation:       operation,
		Stage:           "prepared",
		StepIndex:       -1,
		StartedAtUnixMs: nowMs,
		UpdatedAtUnixMs: nowMs,
		Backups:         backups,
	}
	if err := o.writeMigrationJournal(j); err != nil {
		return nil, err
	}
	return j, nil
}

func (o *Operator) advanceMigrationJournal(j *MigrationJournal, stage string, stepIndex int, currentFile string) error {
	if j == nil {
		return nil
	}
	j.Stage = stage
	j.StepIndex = stepIndex
	j.CurrentFile = currentFile
	j.UpdatedAtUnixMs = time.Now().UnixMilli()
	return o.writeMigrationJournal(j)
}

func (o *Operator) invokeMigrationStepHook(operation, stage string, stepIndex int, targetPath string) error {
	o.mu.Lock()
	hook := o.migrationStepHook
	o.mu.Unlock()
	if hook != nil {
		return hook(operation, stage, stepIndex, targetPath)
	}
	return nil
}

func rollbackMigrationJournalBackups(backups []JournalFileBackup) error {
	var rbErrs []error
	for _, b := range backups {
		if !b.Exists {
			if err := os.Remove(b.Path); err != nil && !os.IsNotExist(err) {
				rbErrs = append(rbErrs, fmt.Errorf("remove created file %s: %w", b.Path, err))
				continue
			}
			if _, stErr := os.Stat(b.Path); stErr == nil || !os.IsNotExist(stErr) {
				rbErrs = append(rbErrs, fmt.Errorf("verify removal of %s failed: %v", b.Path, stErr))
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(b.DataB64)
		if err != nil {
			rbErrs = append(rbErrs, fmt.Errorf("decode journal backup for %s: %w", b.Path, err))
			continue
		}
		mode := os.FileMode(b.Mode) & 0777
		if mode == 0 {
			mode = 0644
		}
		if err := atomicWriteYAMLFile(b.Path, data, mode); err != nil {
			rbErrs = append(rbErrs, fmt.Errorf("restore journal backup for %s: %w", b.Path, err))
			continue
		}
		got, err := os.ReadFile(b.Path)
		if err != nil || !bytes.Equal(got, data) {
			rbErrs = append(rbErrs, fmt.Errorf("verify restored journal backup for %s failed: %v", b.Path, err))
		}
	}
	return errors.Join(rbErrs...)
}

// completeMigrationJournal executes the two-phase commit boundary for a migration journal (R1 / C1):
//   - Pre-publication (before os.Rename of Stage="committed" succeeds): if BeforeCommitMarkerWrite,
//     temp file write/sync/close, or os.Rename fails, Stage="committed" was never published on disk.
//     committed=false is returned and the caller safely rolls back all working files.
//   - Post-publication (once os.Rename of Stage="committed" succeeds): Stage="committed" is published
//     at migration-journal.json on disk, so committed=true is guaranteed and working files MUST NEVER
//     be rolled back. If post-rename syncParentDir, readback verification, journal removal, or post-remove
//     syncParentDir fails, RecoveryRequired is persisted and mutations remain blocked until recovery
//     reconciles/clears the journal state.
func (o *Operator) completeMigrationJournal(j *MigrationJournal) (committed bool, err error) {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return true, nil
	}
	o.mu.Lock()
	hooks := o.journalCommitHooks
	dirHook := o.dirSyncHook
	o.mu.Unlock()

	opName := "migration"
	jID := ""
	if j != nil {
		if strings.TrimSpace(j.Operation) != "" {
			opName = j.Operation
		}
		jID = j.JournalID
	}

	handlePostPublicationErr := func(failedStep string, cErr error) (bool, error) {
		reason := fmt.Sprintf("%s committed working files, but %s failed: %v", opName, failedStep, cErr)
		persistErr := o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:          reason,
			TransitionID:    jID,
			TargetEngine:    "firc",
			PreviousEngine:  "firc",
			FailedStep:      failedStep,
			LastCompleted:   "committed",
			TimestampUnixMs: time.Now().UnixMilli(),
		})
		_ = o.invokeMigrationStepHook(opName, "post_commit_error", 0, jPath)
		return true, errors.Join(fmt.Errorf("%w: %s", ErrRecoveryRequired, reason), cErr, persistErr)
	}

	if j != nil {
		if hooks.BeforeCommitMarkerWrite != nil {
			if hErr := hooks.BeforeCommitMarkerWrite(j); hErr != nil {
				return false, fmt.Errorf("before commit marker write in migration journal: %w", hErr)
			}
		}
		if hErr := o.invokeMigrationStepHook(opName, "before_commit_marker", 0, jPath); hErr != nil {
			return false, hErr
		}
		j.Stage = "committed"
		j.UpdatedAtUnixMs = time.Now().UnixMilli()
		renamed, wErr := o.writeMigrationJournalDetailed(j, hooks.CommitMarkerDirSync, hooks.CommitMarkerReadback)
		if wErr != nil {
			if !renamed {
				// Pre-publication failure: os.Rename did not succeed, so Stage="committed" is not on disk.
				return false, fmt.Errorf("persist committed stage in migration journal: %w", wErr)
			}
			// Post-publication failure: os.Rename already placed Stage="committed" at migration-journal.json,
			// and subsequent syncParentDir or readback failed. Keep new working files intact and block mutations.
			return handlePostPublicationErr("journal_commit_sync_or_readback", fmt.Errorf("post-rename commit marker sync/readback in migration journal: %w", wErr))
		}
	}

	// Commit point passed: Stage="committed" is published, synced, and verified on disk.
	committed = true

	if j != nil {
		if hooks.AfterCommitMarkerWrite != nil {
			if hErr := hooks.AfterCommitMarkerWrite(j); hErr != nil {
				return handlePostPublicationErr("journal_cleanup_after_commit", fmt.Errorf("after commit marker write hook: %w", hErr))
			}
		}
		if hErr := o.invokeMigrationStepHook(opName, "commit_marker_written", 0, jPath); hErr != nil {
			return handlePostPublicationErr("journal_cleanup_after_commit", hErr)
		}
	}

	if hooks.BeforeJournalRemove != nil {
		if hErr := hooks.BeforeJournalRemove(jPath); hErr != nil {
			return handlePostPublicationErr("journal_cleanup_after_commit", fmt.Errorf("before committed migration journal remove: %w", hErr))
		}
	}
	if rmErr := os.Remove(jPath); rmErr != nil && !os.IsNotExist(rmErr) {
		return handlePostPublicationErr("journal_cleanup_after_commit", fmt.Errorf("remove committed migration journal: %w", rmErr))
	}
	if hooks.AfterJournalRemove != nil {
		if hErr := hooks.AfterJournalRemove(jPath); hErr != nil {
			return handlePostPublicationErr("journal_cleanup_after_commit", fmt.Errorf("after committed migration journal remove: %w", hErr))
		}
	}
	postRemoveSyncHook := dirHook
	if hooks.PostRemoveDirSync != nil {
		postRemoveSyncHook = hooks.PostRemoveDirSync
	}
	if syncErr := syncParentDir(jPath, postRemoveSyncHook); syncErr != nil {
		return handlePostPublicationErr("journal_cleanup_after_commit", fmt.Errorf("sync parent dir after committed migration journal remove: %w", syncErr))
	}
	return true, nil
}

// cleanupRolledBackMigrationJournal marks an aborted journal as "rolled_back" and removes it
// after in-process file rollback has succeeded.
func (o *Operator) cleanupRolledBackMigrationJournal(j *MigrationJournal) error {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return nil
	}
	if j != nil {
		j.Stage = "rolled_back"
		j.UpdatedAtUnixMs = time.Now().UnixMilli()
		if err := o.writeMigrationJournal(j); err != nil {
			return fmt.Errorf("persist rolled_back stage in migration journal: %w", err)
		}
	}
	if err := os.Remove(jPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rolled_back migration journal: %w", err)
	}
	o.mu.Lock()
	dirHook := o.dirSyncHook
	o.mu.Unlock()
	return syncParentDir(jPath, dirHook)
}

func (o *Operator) recoverMigrationJournalOnStartup() {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return
	}
	raw, err := os.ReadFile(jPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:          fmt.Sprintf("unreadable migration-journal.json on startup: %v", err),
			TransitionID:    fmt.Sprintf("journal-unreadable-%d", time.Now().UnixNano()),
			TargetEngine:    "firc",
			PreviousEngine:  "firc",
			FailedStep:      "startup_journal_read",
			TimestampUnixMs: time.Now().UnixMilli(),
		})
		return
	}

	var j MigrationJournal
	if err := json.Unmarshal(raw, &j); err != nil || j.Version != 1 || strings.TrimSpace(j.Operation) == "" {
		_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:          fmt.Sprintf("corrupt migration-journal.json on startup: %v", err),
			TransitionID:    fmt.Sprintf("journal-corrupt-%d", time.Now().UnixNano()),
			TargetEngine:    "firc",
			PreviousEngine:  "firc",
			FailedStep:      "startup_journal_parse",
			TimestampUnixMs: time.Now().UnixMilli(),
		})
		return
	}

	if j.Stage == "committed" {
		// An unremoved migration-journal.json with Stage="committed" on startup indicates that
		// the previous process terminated after os.Rename of Stage="committed" (e.g. during
		// syncParentDir of the committed marker, readback, or post-commit journal removal).
		// All working files already hold the complete committed state and MUST NOT be rolled back;
		// RecoveryRequired is ensured so mutations stay blocked until explicit recovery resolution.
		if !o.IsRecoveryRequired() {
			_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:          fmt.Sprintf("committed %s migration journal was not finalized before shutdown; verify state and clear recovery", j.Operation),
				TransitionID:    j.JournalID,
				TargetEngine:    "firc",
				PreviousEngine:  "firc",
				FailedStep:      "startup_unfinalized_committed_journal",
				LastCompleted:   "committed",
				TimestampUnixMs: time.Now().UnixMilli(),
			})
		}
		rmErr := os.Remove(jPath)
		if rmErr != nil && !os.IsNotExist(rmErr) {
			_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:          fmt.Sprintf("committed %s journal cleanup failed on startup: %v", j.Operation, rmErr),
				TransitionID:    j.JournalID,
				TargetEngine:    "firc",
				PreviousEngine:  "firc",
				FailedStep:      "startup_committed_journal_cleanup",
				LastCompleted:   "committed",
				TimestampUnixMs: time.Now().UnixMilli(),
			})
			return
		}
		o.mu.Lock()
		dirHook := o.dirSyncHook
		o.mu.Unlock()
		if syncErr := syncParentDir(jPath, dirHook); syncErr != nil {
			_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:          fmt.Sprintf("committed %s journal dir sync failed on startup: %v", j.Operation, syncErr),
				TransitionID:    j.JournalID,
				TargetEngine:    "firc",
				PreviousEngine:  "firc",
				FailedStep:      "startup_committed_journal_cleanup",
				LastCompleted:   "committed",
				TimestampUnixMs: time.Now().UnixMilli(),
			})
			return
		}
		return
	}

	if j.Stage == "rolled_back" {
		if !o.IsRecoveryRequired() {
			_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
				Reason:          fmt.Sprintf("unfinished %s was rolled back on a prior startup; resolve recovery before mutations", j.Operation),
				TransitionID:    j.JournalID,
				TargetEngine:    "firc",
				PreviousEngine:  "firc",
				FailedStep:      "startup_journal_pending_resolution",
				LastCompleted:   "startup_journal_rollback",
				TimestampUnixMs: time.Now().UnixMilli(),
			})
		}
		return
	}

	prevStage := j.Stage
	prevStep := j.StepIndex
	rbErr := rollbackMigrationJournalBackups(j.Backups)
	if rbErr != nil {
		_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
			Reason:          fmt.Sprintf("unfinished %s interrupted at stage %s (step %d) and startup file rollback failed: %v", j.Operation, prevStage, prevStep, rbErr),
			TransitionID:    j.JournalID,
			TargetEngine:    "firc",
			PreviousEngine:  "firc",
			FailedStep:      "startup_journal_rollback_failed",
			LastCompleted:   prevStage,
			TimestampUnixMs: time.Now().UnixMilli(),
		})
		return
	}

	j.Stage = "rolled_back"
	j.UpdatedAtUnixMs = time.Now().UnixMilli()
	_ = o.writeMigrationJournal(&j)
	_ = o.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:          fmt.Sprintf("unfinished %s interrupted at stage %s (step %d); files rolled back from durable journal on startup", j.Operation, prevStage, prevStep),
		TransitionID:    j.JournalID,
		TargetEngine:    "firc",
		PreviousEngine:  "firc",
		FailedStep:      "interrupted_" + prevStage,
		LastCompleted:   "startup_journal_rollback",
		TimestampUnixMs: time.Now().UnixMilli(),
	})
}

func (o *Operator) checkNoUncommittedMigrationJournal() error {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return nil
	}
	raw, err := os.ReadFile(jPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: unreadable migration-journal.json: %v", ErrRecoveryRequired, err)
	}
	var j MigrationJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return fmt.Errorf("%w: corrupt migration-journal.json: %v", ErrRecoveryRequired, err)
	}
	if j.Stage != "committed" {
		return fmt.Errorf("%w: unfinished operation %q in migration-journal.json (stage=%s, step=%d)", ErrRecoveryRequired, j.Operation, j.Stage, j.StepIndex)
	}
	return nil
}

func (o *Operator) cleanupMigrationJournalOnClear() error {
	jPath := o.MigrationJournalPath()
	if jPath == "" {
		return nil
	}
	raw, err := os.ReadFile(jPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var j MigrationJournal
	if err := json.Unmarshal(raw, &j); err == nil && len(j.Backups) > 0 && j.Stage != "committed" && j.Stage != "rolled_back" {
		if rbErr := rollbackMigrationJournalBackups(j.Backups); rbErr != nil {
			return fmt.Errorf("rollback unfinished migration journal before clear: %w", rbErr)
		}
	}
	if err := os.Remove(jPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	o.mu.Lock()
	dirHook := o.dirSyncHook
	o.mu.Unlock()
	return syncParentDir(jPath, dirHook)
}
