package mihomo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

var (
	ErrRecoveryRequired = errors.New("mihomo coordinator: engine is in degraded mode, administrative recovery required")
	ErrTxInProgress     = errors.New("mihomo coordinator: transaction already in progress")
	ErrSimulatedCrash   = errors.New("failpoint: simulated crash")
)

// ApplyCoordinatorHooks allows injecting failpoints during coordinator operations in tests.
type ApplyCoordinatorHooks struct {
	FailManifestPersistAtState             ManifestState
	FailCommitVerifiedActive               bool
	FailAdvanceLKGPointer                  bool
	FailRollForwardCandidateCommit         bool
	FailCleanupRecoveryMarker              bool
	FailBundlePublish                      bool
	FailAtomicConfigWrite                  bool
	FailManifestPersistBridgeOpState       BridgeOpState
	FailCleanupUnlink                      bool
	PostCommitHook                         func()
	FailAfterPreSnapshot                   bool
	FailAfterCandidateWrite                bool
	FailAfterPostSnapshot                  bool
	FailActiveConfigUnlink                 bool
	FailAuthoritativeWriteAfterPromote     bool
	FailMigrationRollForwardActiveWrite    bool
	FailMigrationRollForwardPointerWrite   bool
	FailMigrationRollForwardActiveVerify   bool
	FailMigrationRollForwardPointerVerify  bool
	FailMigrationRollForwardManifestCAS    bool
	FailMigrationRollForwardManifestUnlink bool
	FailMigrationRollForwardBackupUnlink   bool
	FailHotReload                          bool
	FailHotRollback                        bool
	OnAuthoritativeWrite                   func(op string, path string)
	CrashAtHook                            func(point string)
}

func (c *ApplyCoordinator) crashAt(point string) {
	if c.hooks.CrashAtHook != nil {
		c.hooks.CrashAtHook(point)
	}
}

// AuthoritativeWriter abstracts atomic writes and staged renames of authoritative metadata files
// (verified-active.json and lkg.pointer.json) to ensure strict observable invariants.
type AuthoritativeWriter interface {
	WriteAuthoritative(path string, data []byte, perm os.FileMode) error
	RenameAuthoritative(oldPath, newPath string) error
}

type defaultAuthoritativeWriter struct{}

func (defaultAuthoritativeWriter) WriteAuthoritative(path string, data []byte, perm os.FileMode) error {
	return strictfs.StrictWriteAtomic(path, data, perm)
}

func (defaultAuthoritativeWriter) RenameAuthoritative(oldPath, newPath string) error {
	return strictfs.StrictRename(oldPath, newPath)
}

// CoordinatorConfig holds the paths and dependencies required by ApplyCoordinator.
type CoordinatorConfig struct {
	ConfigDir           string
	Operator            MihomoOperator
	ConfigReloader      ConfigReloader
	Validator           ConfigFileValidator
	BridgeRuntime       BridgeRuntime
	StoreTx             NativeStoreTx
	LogFn               func(level, action, message string)
	Verifier            ProcessVerifier
	Compiler            func(ctx context.Context) (*CompileResult, error)
	AuthoritativeWriter AuthoritativeWriter
	Stat                func(name string) (os.FileInfo, error)
}

// TransactionTiming instruments the individual phases of an apply transaction.
type TransactionTiming struct {
	TxID           string        `json:"txid"`
	Path           ApplyPath     `json:"path"`
	LockWait       time.Duration `json:"lock_wait"`
	PreSnapshot    time.Duration `json:"pre_snapshot"`
	Mutate         time.Duration `json:"mutate"`
	PostSnapshot   time.Duration `json:"post_snapshot"`
	Compile        time.Duration `json:"compile"`
	CandidateWrite time.Duration `json:"candidate_write"`
	Validate       time.Duration `json:"validate"`
	RuntimeApply   time.Duration `json:"runtime_apply"`
	StopWait       time.Duration `json:"stop_wait"`
	StartWait      time.Duration `json:"start_wait"`
	ReloadWait     time.Duration `json:"reload_wait"`
	Verify         time.Duration `json:"verify"`
	Commit         time.Duration `json:"commit"`
	Rollback       time.Duration `json:"rollback"`
	TotalDuration  time.Duration `json:"total_duration"`
}

// ServerTimingHeader formats the transaction timing as a W3C Server-Timing header value.
func (t *TransactionTiming) ServerTimingHeader() string {
	if t == nil {
		return ""
	}
	var parts []string
	if t.LockWait > 0 {
		parts = append(parts, fmt.Sprintf("lock;dur=%.1f", float64(t.LockWait)/float64(time.Millisecond)))
	}
	if t.PreSnapshot > 0 {
		parts = append(parts, fmt.Sprintf("pre_snap;dur=%.1f", float64(t.PreSnapshot)/float64(time.Millisecond)))
	}
	if t.Mutate > 0 {
		parts = append(parts, fmt.Sprintf("mutate;dur=%.1f", float64(t.Mutate)/float64(time.Millisecond)))
	}
	if t.PostSnapshot > 0 {
		parts = append(parts, fmt.Sprintf("post_snap;dur=%.1f", float64(t.PostSnapshot)/float64(time.Millisecond)))
	}
	if t.Compile > 0 {
		parts = append(parts, fmt.Sprintf("compile;dur=%.1f", float64(t.Compile)/float64(time.Millisecond)))
	}
	if t.CandidateWrite > 0 {
		parts = append(parts, fmt.Sprintf("cand_write;dur=%.1f", float64(t.CandidateWrite)/float64(time.Millisecond)))
	}
	if t.Validate > 0 {
		parts = append(parts, fmt.Sprintf("validate;dur=%.1f", float64(t.Validate)/float64(time.Millisecond)))
	}
	if t.RuntimeApply > 0 {
		parts = append(parts, fmt.Sprintf("runtime;dur=%.1f;desc=%s", float64(t.RuntimeApply)/float64(time.Millisecond), t.Path))
	}
	if t.Verify > 0 {
		parts = append(parts, fmt.Sprintf("verify;dur=%.1f", float64(t.Verify)/float64(time.Millisecond)))
	}
	if t.Commit > 0 {
		parts = append(parts, fmt.Sprintf("commit;dur=%.1f", float64(t.Commit)/float64(time.Millisecond)))
	}
	if t.Rollback > 0 {
		parts = append(parts, fmt.Sprintf("rollback;dur=%.1f", float64(t.Rollback)/float64(time.Millisecond)))
	}
	return strings.Join(parts, ", ")
}

// CoordinatorCounters tracks transaction path frequencies.
type CoordinatorCounters struct {
	HotReloadCount   uint64 `json:"hot_reload_count"`
	FullRestartCount uint64 `json:"full_restart_count"`
	DraftOnlyCount   uint64 `json:"draft_only_count"`
	RollbackCount    uint64 `json:"rollback_count"`
}

// MutationOutcome returns the result details of an executed mutation.
type MutationOutcome struct {
	TxID         string
	ApplyPath    ApplyPath
	Generation   uint64
	Timing       *TransactionTiming
	ServerTiming string
}

// ApplyCoordinator is the single authoritative coordinator for configuration compiling,
// staging, validating, hot swapping, runtime identity verification, bridge synchronization,
// and durable rollback.
type ApplyCoordinator struct {
	mu             sync.RWMutex
	applyMu        sync.Mutex
	cfg            CoordinatorConfig
	genStore       *GenerationStore
	authWriter     AuthoritativeWriter
	daemonEpoch    string
	state          ManifestState
	activeTxID     string
	appliedRecord  *AppliedGenerationRecord
	hooks          ApplyCoordinatorHooks
	cleanupPending []string
	verifier       ProcessVerifier
	stat           func(name string) (os.FileInfo, error)
	processReceipt *ProcessReceipt
	counters       CoordinatorCounters
	lastTiming     *TransactionTiming

	// File paths
	activeConfigFile     string
	manifestFile         string
	lkgConfigFile        string
	verifiedActiveFile   string
	recoveryMarkerFile   string
	draftJournalFile     string
	pendingInputFile     string
	quarantineDir        string
	cleanupJournalFile   string
	migrationJournalFile string
}

// NewApplyCoordinator instantiates the coordinator with paths resolved relative to ConfigDir.
func NewApplyCoordinator(cfg CoordinatorConfig) *ApplyCoordinator {
	var rnd [4]byte
	_, err := rand.Read(rnd[:])
	if err != nil {
		panic(err)
	}
	epoch := fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(rnd[:]))

	verifier := cfg.Verifier
	if verifier == nil {
		verifier = DefaultProcessVerifier
	}

	authWriter := cfg.AuthoritativeWriter
	if authWriter == nil {
		authWriter = defaultAuthoritativeWriter{}
	}

	stat := cfg.Stat
	if stat == nil {
		stat = os.Stat
	}

	dir := cfg.ConfigDir
	return &ApplyCoordinator{
		cfg:                  cfg,
		genStore:             NewGenerationStore(dir),
		authWriter:           authWriter,
		daemonEpoch:          epoch,
		state:                StateIdle,
		verifier:             verifier,
		stat:                 stat,
		activeConfigFile:     filepath.Join(dir, "config.yaml"),
		manifestFile:         filepath.Join(dir, "config.yaml.txn.json"),
		lkgConfigFile:        filepath.Join(dir, "config.yaml.lkg"),
		verifiedActiveFile:   filepath.Join(dir, "verified-active.json"),
		recoveryMarkerFile:   filepath.Join(dir, "recovery.marker"),
		draftJournalFile:     filepath.Join(dir, "store.draft.json"),
		pendingInputFile:     filepath.Join(dir, "input.pending.json"),
		quarantineDir:        filepath.Join(dir, "quarantine"),
		cleanupJournalFile:   filepath.Join(dir, "cleanup.journal.json"),
		migrationJournalFile: filepath.Join(dir, "migration.journal.json"),
	}
}

// SetAuthoritativeWriter overrides the writer for authoritative metadata files.
func (c *ApplyCoordinator) SetAuthoritativeWriter(w AuthoritativeWriter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w == nil {
		c.authWriter = defaultAuthoritativeWriter{}
	} else {
		c.authWriter = w
	}
}

// SetVerifier sets the process verifier used for runtime process proofs.
func (c *ApplyCoordinator) SetVerifier(v ProcessVerifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v == nil {
		v = DefaultProcessVerifier
	}
	c.verifier = v
}

// Counters returns snapshot of coordinator transaction counters.
func (c *ApplyCoordinator) Counters() CoordinatorCounters {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.counters
}

// LastTiming returns the timing details of the most recent apply transaction.
func (c *ApplyCoordinator) LastTiming() *TransactionTiming {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastTiming == nil {
		return nil
	}
	cp := *c.lastTiming
	return &cp
}

func (c *ApplyCoordinator) statActiveConfigAbsenceLocked() error {
	if _, statErr := c.stat(c.activeConfigFile); statErr == nil {
		return fmt.Errorf("active config still exists after unlink in RuntimeOff")
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("cannot prove active config absence in RuntimeOff: %w", statErr)
	}
	return nil
}

// enrichLegacyBridgeRefLocked enriches a legacy BridgeRef that lacks complete identity fields
// by uniquely mapping it against a provided bridge snapshot.
// Rules:
// - complete refs pass unchanged;
// - only legacy refs may be enriched;
// - match by every non-empty legacy field;
// - require exactly one candidate;
// - candidate must itself be complete;
// - ambiguity or absence returns ErrForeignBridgeOwnership;
// - never invent an index, owner, interface or port.
func (c *ApplyCoordinator) enrichLegacyBridgeRefLocked(ref BridgeRef, snapshot []BridgeRef) (BridgeRef, error) {
	if !ref.IsLegacy() {
		return ref, nil
	}
	if len(snapshot) == 0 {
		return BridgeRef{}, fmt.Errorf("%w: cannot enrich legacy bridge %s: snapshot is empty", ErrForeignBridgeOwnership, ref.SlotKey())
	}

	var matched []BridgeRef
	for _, nb := range snapshot {
		if ref.ProxyIndex > 0 && nb.ProxyIndex != ref.ProxyIndex {
			continue
		}
		if ref.ProxyInterface != "" && nb.ProxyInterface != ref.ProxyInterface {
			continue
		}
		if ref.KernelInterface != "" && nb.KernelInterface != ref.KernelInterface {
			continue
		}
		if ref.ListenPort > 0 && nb.ListenPort != ref.ListenPort {
			continue
		}
		if ref.OwnerUUID != "" && nb.OwnerUUID != ref.OwnerUUID {
			continue
		}
		if ref.LegacyOwner != "" && nb.LegacyOwner != ref.LegacyOwner {
			continue
		}
		matched = append(matched, nb)
	}

	if len(matched) == 0 {
		return BridgeRef{}, fmt.Errorf("%w: legacy bridge %s has no snapshot mapping for ownership enrichment", ErrForeignBridgeOwnership, ref.SlotKey())
	}
	if len(matched) > 1 {
		return BridgeRef{}, fmt.Errorf("%w: legacy bridge %s has ambiguous (%d) snapshot mappings for ownership enrichment", ErrForeignBridgeOwnership, ref.SlotKey(), len(matched))
	}

	cand := matched[0]
	if err := cand.ValidateComplete(); err != nil {
		return BridgeRef{}, fmt.Errorf("%w: snapshot candidate for legacy bridge %s is incomplete: %v", ErrForeignBridgeOwnership, ref.SlotKey(), err)
	}

	if ref.Generation > 0 {
		cand.Generation = ref.Generation
	}
	return cand, nil
}

func (c *ApplyCoordinator) enrichBridgeRefLocked(ref BridgeRef) (BridgeRef, error) {
	var snap []BridgeRef
	if c.cfg.StoreTx != nil {
		snap = c.cfg.StoreTx.ListBridges()
	}
	return c.enrichLegacyBridgeRefLocked(ref, snap)
}

func (c *ApplyCoordinator) validateMigrationBackupPathLocked(actualPath, expectedPath string) error {
	cleanActual := filepath.Clean(actualPath)
	cleanExpected := filepath.Clean(expectedPath)
	if cleanActual != cleanExpected {
		return fmt.Errorf("path %q does not match expected canonical path %q", actualPath, cleanExpected)
	}
	cleanConfigDir := filepath.Clean(c.cfg.ConfigDir)
	if filepath.Dir(cleanActual) != cleanConfigDir {
		return fmt.Errorf("path %q is not direct child of config dir %q", actualPath, cleanConfigDir)
	}
	fi, err := os.Lstat(cleanActual)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symlink", actualPath)
		}
		evalConfigDir, err := filepath.EvalSymlinks(cleanConfigDir)
		if err == nil {
			evalActual, err := filepath.EvalSymlinks(cleanActual)
			if err == nil && filepath.Dir(evalActual) != evalConfigDir {
				return fmt.Errorf("path %q escaped config dir via symlink", actualPath)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat %q: %w", actualPath, err)
	}
	return nil
}

func (c *ApplyCoordinator) validateMigrationManifestLocked(m *TransactionManifest) error {
	if m == nil {
		return errors.New("nil migration manifest")
	}
	if m.OperationKind != OperationMigration {
		return fmt.Errorf("manifest operation_kind %q is not %s", m.OperationKind, OperationMigration)
	}
	if err := m.ValidateSchemaForPhase(); err != nil {
		return fmt.Errorf("manifest schema for phase %s invalid: %w", m.State, err)
	}
	if err := ValidateBasename(m.CandidateGenerationID); err != nil {
		return fmt.Errorf("invalid candidate generation ID %q: %w", m.CandidateGenerationID, err)
	}
	if c.genStore != nil {
		candidateDir := filepath.Join(c.genStore.GenerationsDir(), m.CandidateGenerationID)
		if filepath.Clean(candidateDir) != candidateDir {
			return fmt.Errorf("candidate dir path %q is not clean", candidateDir)
		}
		if filepath.Dir(candidateDir) != filepath.Clean(c.genStore.GenerationsDir()) {
			return fmt.Errorf("candidate dir %q is not direct child of generations dir %q", candidateDir, c.genStore.GenerationsDir())
		}
		if fi, err := os.Lstat(candidateDir); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("candidate dir %q is a symlink", candidateDir)
			}
			if evalGensDir, err := filepath.EvalSymlinks(c.genStore.GenerationsDir()); err == nil {
				if evalCandDir, err := filepath.EvalSymlinks(candidateDir); err == nil {
					if filepath.Dir(evalCandDir) != evalGensDir {
						return fmt.Errorf("candidate dir %q escaped generations dir via symlink", candidateDir)
					}
				}
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("lstat candidate dir %q: %w", candidateDir, err)
		}
	}
	if m.PreviousRecordFile != "" {
		expectedVA := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("migration_backup_active_%s.json", m.TxID))
		if err := c.validateMigrationBackupPathLocked(m.PreviousRecordFile, expectedVA); err != nil {
			return fmt.Errorf("validate backup active path: %w", err)
		}
	}
	if m.PreviousLKGPointerFile != "" {
		expectedPtr := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("migration_backup_pointer_%s.json", m.TxID))
		if err := c.validateMigrationBackupPathLocked(m.PreviousLKGPointerFile, expectedPtr); err != nil {
			return fmt.Errorf("validate backup pointer path: %w", err)
		}
	}
	return nil
}

// rollbackMigrationBeforeCommitLocked restores original state if failure occurred before the commit boundary.
func (c *ApplyCoordinator) rollbackMigrationBeforeCommitLocked(m *TransactionManifest) error {
	if err := c.validateMigrationManifestLocked(m); err != nil {
		return fmt.Errorf("validate migration manifest before rollback: %w", err)
	}

	if m.PreviousRecordFile != "" {
		b, err := os.ReadFile(m.PreviousRecordFile)
		if err != nil {
			return fmt.Errorf("read backup active file %s: %w", m.PreviousRecordFile, err)
		}
		if actualDigest := strictfs.ComputeBytesDigest(b); actualDigest != m.PreviousRecordDigest {
			return fmt.Errorf("backup active digest mismatch: got %s, want %s", actualDigest, m.PreviousRecordDigest)
		}
		if err := strictfs.StrictWriteAtomic(c.verifiedActiveFile, b, 0600); err != nil {
			return fmt.Errorf("restore verified-active: %w", err)
		}
		restoredDigest, err := strictfs.ComputeFileDigest(c.verifiedActiveFile)
		if err != nil {
			return fmt.Errorf("compute restored verified-active digest: %w", err)
		}
		if restoredDigest != m.PreviousRecordDigest {
			return fmt.Errorf("restored verified-active digest mismatch: got %s, want %s", restoredDigest, m.PreviousRecordDigest)
		}
	}

	if m.PreviousLKGPointerFile != "" {
		b, err := os.ReadFile(m.PreviousLKGPointerFile)
		if err != nil {
			return fmt.Errorf("read backup pointer file %s: %w", m.PreviousLKGPointerFile, err)
		}
		if actualDigest := strictfs.ComputeBytesDigest(b); actualDigest != m.PreviousLKGPointerDigest {
			return fmt.Errorf("backup pointer digest mismatch: got %s, want %s", actualDigest, m.PreviousLKGPointerDigest)
		}
		if c.genStore == nil {
			return errors.New("genStore is nil during pointer restoration")
		}
		if err := strictfs.StrictWriteAtomic(c.genStore.LKGPointerFile(), b, 0600); err != nil {
			return fmt.Errorf("restore lkg pointer: %w", err)
		}
		restoredPtrDigest, err := strictfs.ComputeFileDigest(c.genStore.LKGPointerFile())
		if err != nil {
			return fmt.Errorf("compute restored lkg pointer digest: %w", err)
		}
		if restoredPtrDigest != m.PreviousLKGPointerDigest {
			return fmt.Errorf("restored lkg pointer digest mismatch: got %s, want %s", restoredPtrDigest, m.PreviousLKGPointerDigest)
		}
	}

	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		return fmt.Errorf("fsync config dir after restore: %w", err)
	}

	if m.CandidateGenerationID != "" && c.genStore != nil {
		if err := c.genStore.RemoveCandidateGeneration(m.CandidateGenerationID, m.PreviousGenerationID, m.PreviousLKGGenerationID); err != nil {
			return fmt.Errorf("remove uncommitted candidate generation: %w", err)
		}
	}

	if m.PreviousRecordFile != "" {
		if err := strictfs.StrictUnlink(m.PreviousRecordFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("unlink backup active file: %w", err)
		}
	}
	if m.PreviousLKGPointerFile != "" {
		if err := strictfs.StrictUnlink(m.PreviousLKGPointerFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("unlink backup pointer file: %w", err)
		}
	}
	if err := strictfs.StrictUnlink(c.manifestFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unlink manifest file: %w", err)
	}
	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		return fmt.Errorf("fsync config dir after cleanup: %w", err)
	}
	if err := c.syncActiveTransactionRolesLocked(nil); err != nil {
		return fmt.Errorf("clear active transaction roles: %w", err)
	}
	return nil
}

// rollForwardMigrationLocked deterministically converges an in-flight startup migration after the commit boundary.
func (c *ApplyCoordinator) rollForwardMigrationLocked(m *TransactionManifest) error {
	if err := c.validateMigrationManifestLocked(m); err != nil {
		return fmt.Errorf("validate migration manifest before roll-forward: %w", err)
	}

	if c.genStore == nil {
		return errors.New("genStore is nil during migration roll-forward")
	}

	gm, cfgPath, snapPath, err := c.genStore.ReadGenerationBundle(m.CandidateGenerationID)
	if err != nil {
		return fmt.Errorf("read candidate generation bundle %s: %w", m.CandidateGenerationID, err)
	}
	if gm == nil {
		return fmt.Errorf("candidate bundle %s manifest is nil", m.CandidateGenerationID)
	}

	// Validate bundle metadata
	if gm.GenerationID != m.CandidateGenerationID {
		return fmt.Errorf("bundle generation ID mismatch: got %s, want %s", gm.GenerationID, m.CandidateGenerationID)
	}
	if gm.GenerationNumber != m.VerifiedActiveGeneration+1 {
		return fmt.Errorf("bundle generation number mismatch: got %d, want %d", gm.GenerationNumber, m.VerifiedActiveGeneration+1)
	}
	if gm.AppliedConfigDigest != m.CandidateConfigDigest {
		return fmt.Errorf("bundle config digest mismatch: got %s, want %s", gm.AppliedConfigDigest, m.CandidateConfigDigest)
	}
	if gm.RuntimeMode != RuntimeOff {
		if cfgPath == "" {
			return fmt.Errorf("candidate bundle %s config.yaml missing", m.CandidateGenerationID)
		}
		cb, err := os.ReadFile(cfgPath)
		if err != nil {
			return fmt.Errorf("read bundle config: %w", err)
		}
		if actual := strictfs.ComputeBytesDigest(cb); actual != gm.AppliedConfigDigest {
			return fmt.Errorf("bundle config digest mismatch: got %s, want %s", actual, gm.AppliedConfigDigest)
		}
	}
	if gm.AppliedStoreDigest != "" {
		if snapPath == "" {
			return fmt.Errorf("candidate bundle %s store.snapshot.json missing", m.CandidateGenerationID)
		}
		actual, err := strictfs.ComputeFileDigest(snapPath)
		if err != nil {
			return fmt.Errorf("compute bundle store snapshot digest: %w", err)
		}
		if actual != gm.AppliedStoreDigest {
			return fmt.Errorf("bundle store snapshot digest mismatch: got %s, want %s", actual, gm.AppliedStoreDigest)
		}
	}

	// Reconstruct candidate record from fully validated bundle
	reconstructedRec := AppliedGenerationRecord{
		Version:                   1,
		BridgeIdentityVersion:     CurrentBridgeIdentityVersion,
		Generation:                gm.GenerationNumber,
		GenerationID:              gm.GenerationID,
		AppliedAt:                 gm.ArchivedAt,
		AppliedStoreDigest:        gm.AppliedStoreDigest,
		AppliedDesiredStoreDigest: gm.AppliedDesiredStoreDigest,
		AppliedConfigDigest:       gm.AppliedConfigDigest,
		AppliedInputDigest:        gm.AppliedInputDigest,
		AppliedListeners:          gm.AppliedListeners,
		AppliedBridges:            gm.AppliedBridges,
		AppliedBridgesDigest:      gm.AppliedBridgesDigest,
		RuntimeMode:               gm.RuntimeMode,
	}
	if err := reconstructedRec.ValidateSchema(); err != nil {
		return fmt.Errorf("validate reconstructed candidate record schema: %w", err)
	}

	recBytes, err := json.MarshalIndent(reconstructedRec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal reconstructed candidate record: %w", err)
	}
	reconstructedDigest := strictfs.ComputeBytesDigest(recBytes)
	if reconstructedDigest != m.CandidateRecordDigest {
		return fmt.Errorf("reconstructed candidate record digest mismatch: got %s, want %s", reconstructedDigest, m.CandidateRecordDigest)
	}

	if c.hooks.FailMigrationRollForwardActiveWrite {
		return errors.New("failpoint: roll-forward active write failed")
	}

	vaBytes, err := os.ReadFile(c.verifiedActiveFile)
	if err != nil || strictfs.ComputeBytesDigest(vaBytes) != m.CandidateRecordDigest {
		if err := strictfs.StrictWriteAtomic(c.verifiedActiveFile, recBytes, 0600); err != nil {
			return fmt.Errorf("write candidate verified-active: %w", err)
		}
		if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
			return fmt.Errorf("fsync config dir after verified-active write: %w", err)
		}
	}

	if c.hooks.FailMigrationRollForwardPointerWrite {
		return errors.New("failpoint: roll-forward pointer write failed")
	}

	nextGen := m.VerifiedActiveGeneration + 1
	if err := c.genStore.AdvanceLKGPointer(m.CandidateGenerationID, nextGen, reconstructedRec, c.DaemonEpoch()); err != nil {
		return fmt.Errorf("advance candidate lkg pointer: %w", err)
	}

	// 3. Re-read and validate both records and assert exact file agreement (P0-3)
	if c.hooks.FailMigrationRollForwardActiveVerify {
		return errors.New("failpoint: roll-forward active verify failed")
	}
	reReadVABytes, err := os.ReadFile(c.verifiedActiveFile)
	if err != nil {
		return fmt.Errorf("re-read verified-active: %w", err)
	}
	var vaRec AppliedGenerationRecord
	if err := DecodeJSONStrict(reReadVABytes, &vaRec); err != nil {
		return fmt.Errorf("decode re-read verified-active: %w", err)
	}
	if err := vaRec.ValidateSchema(); err != nil {
		return fmt.Errorf("validate re-read verified-active schema: %w", err)
	}

	if c.hooks.FailMigrationRollForwardPointerVerify {
		return errors.New("failpoint: roll-forward pointer verify failed")
	}
	reReadPtrBytes, err := os.ReadFile(c.genStore.LKGPointerFile())
	if err != nil {
		return fmt.Errorf("re-read lkg pointer: %w", err)
	}
	var ptrRec LKGPointer
	if err := DecodeJSONStrict(reReadPtrBytes, &ptrRec); err != nil {
		return fmt.Errorf("decode re-read lkg pointer: %w", err)
	}
	if err := ptrRec.ValidateSchema(); err != nil {
		return fmt.Errorf("validate re-read lkg pointer schema: %w", err)
	}

	if vaRec.GenerationID != m.CandidateGenerationID || ptrRec.GenerationID != m.CandidateGenerationID {
		return fmt.Errorf("generation ID disagreement: va=%s, ptr=%s, want=%s", vaRec.GenerationID, ptrRec.GenerationID, m.CandidateGenerationID)
	}
	if vaRec.Generation != ptrRec.GenerationNumber || vaRec.Generation != nextGen {
		return fmt.Errorf("generation number disagreement: va=%d, ptr=%d, want=%d", vaRec.Generation, ptrRec.GenerationNumber, nextGen)
	}
	if vaRec.AppliedConfigDigest != ptrRec.AppliedConfigDigest || vaRec.AppliedConfigDigest != m.CandidateConfigDigest {
		return fmt.Errorf("config digest disagreement: va=%s, ptr=%s, want=%s", vaRec.AppliedConfigDigest, ptrRec.AppliedConfigDigest, m.CandidateConfigDigest)
	}
	if vaRec.AppliedStoreDigest != ptrRec.AppliedStoreDigest {
		return fmt.Errorf("store digest disagreement: va=%s, ptr=%s", vaRec.AppliedStoreDigest, ptrRec.AppliedStoreDigest)
	}
	if vaRec.BridgeIdentityVersion != CurrentBridgeIdentityVersion {
		return fmt.Errorf("bridge identity version disagreement: va=%d, want=%d", vaRec.BridgeIdentityVersion, CurrentBridgeIdentityVersion)
	}
	if vaRec.AppliedBridgesDigest != m.TargetBridgesDigest {
		return fmt.Errorf("applied bridges digest disagreement: va=%s, want=%s", vaRec.AppliedBridgesDigest, m.TargetBridgesDigest)
	}
	if BridgesDigest(vaRec.AppliedBridges) != m.TargetBridgesDigest {
		return fmt.Errorf("computed bridges digest disagreement: va=%s, want=%s", BridgesDigest(vaRec.AppliedBridges), m.TargetBridgesDigest)
	}

	// 4. Checkpoint committed state (P0-3)
	if m.State != StateMigrationCommitted {
		if c.hooks.FailMigrationRollForwardManifestCAS {
			return errors.New("failpoint: roll-forward manifest CAS failed")
		}
		if err := c.transitionManifestLocked(m, StateMigrationCommitted); err != nil {
			return fmt.Errorf("transition to migration_committed: %w", err)
		}
	}

	// 5. Cleanup failures must not be swallowed (P0-3)
	if c.hooks.FailMigrationRollForwardBackupUnlink {
		return errors.New("failpoint: roll-forward backup unlink failed")
	}
	if m.PreviousRecordFile != "" {
		if err := strictfs.StrictUnlink(m.PreviousRecordFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("unlink backup active file: %w", err)
		}
	}
	if m.PreviousLKGPointerFile != "" {
		if err := strictfs.StrictUnlink(m.PreviousLKGPointerFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("unlink backup pointer file: %w", err)
		}
	}

	if c.hooks.FailMigrationRollForwardManifestUnlink {
		return errors.New("failpoint: roll-forward manifest unlink failed")
	}
	if err := strictfs.StrictUnlink(c.manifestFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unlink manifest file: %w", err)
	}
	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		return fmt.Errorf("fsync config dir after cleanup: %w", err)
	}
	if err := c.syncActiveTransactionRolesLocked(nil); err != nil {
		return fmt.Errorf("clear active transaction roles: %w", err)
	}
	return nil
}

// recoverMigrationTransactionIfPresentLocked deterministically converges an in-flight startup migration.
func (c *ApplyCoordinator) recoverMigrationTransactionIfPresentLocked(ctx context.Context) (bool, error) {
	mData, err := os.ReadFile(c.manifestFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read manifest for migration recovery: %w", err)
	}
	var m TransactionManifest
	if err := DecodeJSONStrict(mData, &m); err != nil {
		return false, nil // unparseable manifest handled by standard recoverManifestLocked
	}
	if m.OperationKind != OperationMigration {
		return false, nil // handled by standard recoverManifestLocked
	}

	if err := c.validateMigrationManifestLocked(&m); err != nil {
		return false, fmt.Errorf("validate in-flight migration manifest: %w", err)
	}

	switch m.State {
	case StateMigrationIntent, StateMigrationBundlePublished, StateMigrationActiveWriteIntent:
		// BEFORE COMMIT BOUNDARY: Deterministically rollback and restore complete clean old legacy record
		if err := c.rollbackMigrationBeforeCommitLocked(&m); err != nil {
			return false, fmt.Errorf("rollback migration before commit: %w", err)
		}
		return true, nil

	case StateMigrationActiveWritten, StateMigrationPointerWriteIntent, StateMigrationPointerWritten, StateMigrationCommitted:
		// AFTER COMMIT BOUNDARY: Deterministically converge to complete NEW pair
		if err := c.rollForwardMigrationLocked(&m); err != nil {
			return false, fmt.Errorf("roll-forward migration: %w", err)
		}
		return true, nil

	default:
		return false, fmt.Errorf("unexpected migration state %s", m.State)
	}
}

// upgradeLegacyAppliedBridgeIdentityLocked performs atomic startup migration of legacy bridge identity.
func (c *ApplyCoordinator) upgradeLegacyAppliedBridgeIdentityLocked(ctx context.Context, rec AppliedGenerationRecord) (*AppliedGenerationRecord, error) {
	if !rec.IsLegacyBridgeIdentity() {
		return &rec, nil
	}

	if c.cfg.StoreTx == nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: storeTx is nil")
		return nil, errors.New("storeTx is nil during legacy bridge startup migration")
	}

	storeSnap := c.cfg.StoreTx.ListBridges()
	enrichedBridges := make([]BridgeRef, len(rec.AppliedBridges))
	seenSlots := make(map[string]struct{}, len(rec.AppliedBridges))
	for i, b := range rec.AppliedBridges {
		eb, err := c.enrichLegacyBridgeRefLocked(b, storeSnap)
		if err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: " + err.Error())
			return nil, fmt.Errorf("enrich legacy bridge %s: %w", b.SlotKey(), err)
		}
		if err := eb.ValidateComplete(); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: " + err.Error())
			return nil, fmt.Errorf("enriched bridge %s incomplete: %w", eb.SlotKey(), err)
		}
		slot := eb.SlotKey()
		if _, exists := seenSlots[slot]; exists {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: duplicate slot " + slot)
			return nil, fmt.Errorf("duplicate slot key %q during legacy migration", slot)
		}
		seenSlots[slot] = struct{}{}
		enrichedBridges[i] = eb
	}

	// Strict bundle reads (P1-3)
	var configBytes []byte
	var storeSnapshotPath string
	if rec.GenerationID != "" {
		if c.genStore == nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: genStore is nil")
			return nil, errors.New("genStore is nil during legacy migration of referenced bundle")
		}
		gm, cfgPath, snapPath, err := c.genStore.ReadGenerationBundle(rec.GenerationID)
		if err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration read bundle failed: " + err.Error())
			return nil, fmt.Errorf("read referenced generation bundle %s: %w", rec.GenerationID, err)
		}
		if rec.RuntimeMode != RuntimeOff {
			if cfgPath == "" {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: referenced bundle config missing")
				return nil, fmt.Errorf("referenced bundle %s config.yaml missing", rec.GenerationID)
			}
			cb, err := os.ReadFile(cfgPath)
			if err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration read config failed: " + err.Error())
				return nil, fmt.Errorf("read bundle config %s: %w", cfgPath, err)
			}
			if actualDigest := strictfs.ComputeBytesDigest(cb); actualDigest != rec.AppliedConfigDigest {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("legacy bridge migration config digest mismatch: got %s, want %s", actualDigest, rec.AppliedConfigDigest))
				return nil, fmt.Errorf("referenced bundle config digest mismatch: got %s, want %s", actualDigest, rec.AppliedConfigDigest)
			}
			configBytes = cb
		}
		if rec.AppliedStoreDigest != "" {
			if snapPath == "" {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: referenced bundle store snapshot missing")
				return nil, fmt.Errorf("referenced bundle %s store.snapshot.json missing", rec.GenerationID)
			}
			actualDigest, err := strictfs.ComputeFileDigest(snapPath)
			if err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration read snapshot failed: " + err.Error())
				return nil, fmt.Errorf("read bundle store snapshot %s: %w", snapPath, err)
			}
			if actualDigest != rec.AppliedStoreDigest {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("legacy bridge migration store snapshot digest mismatch: got %s, want %s", actualDigest, rec.AppliedStoreDigest))
				return nil, fmt.Errorf("referenced bundle store snapshot digest mismatch: got %s, want %s", actualDigest, rec.AppliedStoreDigest)
			}
			storeSnapshotPath = snapPath
		}
		_ = gm
	} else {
		// Dedicated pre-generation legacy branch (GenerationID == "")
		if rec.RuntimeMode != RuntimeOff {
			cb, err := os.ReadFile(c.activeConfigFile)
			if err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read active config failed: " + err.Error())
				return nil, fmt.Errorf("read active config: %w", err)
			}
			if rec.AppliedConfigDigest != "" {
				if actualDigest := strictfs.ComputeBytesDigest(cb); actualDigest != rec.AppliedConfigDigest {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("active config digest mismatch")
					return nil, fmt.Errorf("active config digest mismatch: got %s, want %s", actualDigest, rec.AppliedConfigDigest)
				}
			}
			configBytes = cb
		}
	}

	// Unique generation ID (P2-1)
	txid := GenerateTxID()
	nextGen := rec.Generation + 1
	newGenID := fmt.Sprintf("gen-%06d-%s", nextGen, txid)

	newRec := rec
	newRec.Version = 1
	newRec.BridgeIdentityVersion = CurrentBridgeIdentityVersion
	newRec.Generation = nextGen
	newRec.GenerationID = newGenID
	newRec.AppliedBridges = enrichedBridges
	newRec.AppliedBridgesDigest = BridgesDigest(enrichedBridges)
	newRec.AppliedAt = time.Now()

	if err := newRec.ValidateSchema(); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("legacy bridge startup migration failed: " + err.Error())
		return nil, fmt.Errorf("validate upgraded record schema: %w", err)
	}

	oldVABytes, err := os.ReadFile(c.verifiedActiveFile)
	if err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("read verified-active before migration: " + err.Error())
		return nil, fmt.Errorf("read verified-active before migration: %w", err)
	}
	oldVADigest := strictfs.ComputeBytesDigest(oldVABytes)
	backupVAPath := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("migration_backup_active_%s.json", txid))
	if err := strictfs.StrictWriteAtomic(backupVAPath, oldVABytes, 0600); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("write backup active file: " + err.Error())
		return nil, fmt.Errorf("write backup active file: %w", err)
	}
	actualVABackupDigest, err := strictfs.ComputeFileDigest(backupVAPath)
	if err != nil || actualVABackupDigest != oldVADigest {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("backup active digest verification failed")
		return nil, fmt.Errorf("backup active digest verification failed")
	}

	var oldLKGGenID string
	var oldPointerDigest string
	var backupPtrPath string
	if rec.GenerationID != "" {
		if c.genStore == nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("genStore is nil during generation-backed legacy migration")
			return nil, errors.New("genStore is nil during generation-backed legacy migration")
		}
		ptrBytes, pErr := os.ReadFile(c.genStore.LKGPointerFile())
		if pErr != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("read lkg pointer before migration: " + pErr.Error())
			return nil, fmt.Errorf("read lkg pointer before migration: %w", pErr)
		}
		var oldPtr LKGPointer
		if err := DecodeJSONStrict(ptrBytes, &oldPtr); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("strict decode lkg pointer before migration: " + err.Error())
			return nil, fmt.Errorf("strict decode lkg pointer before migration: %w", err)
		}
		if err := oldPtr.ValidateSchema(); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("validate lkg pointer schema before migration: " + err.Error())
			return nil, fmt.Errorf("validate lkg pointer schema before migration: %w", err)
		}
		if oldPtr.GenerationID != rec.GenerationID || oldPtr.GenerationNumber != rec.Generation {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("lkg pointer mismatch with applied record: ptr=(%s, %d) rec=(%s, %d)",
				oldPtr.GenerationID, oldPtr.GenerationNumber, rec.GenerationID, rec.Generation))
			return nil, fmt.Errorf("lkg pointer does not agree with legacy applied record")
		}
		oldPointerDigest = strictfs.ComputeBytesDigest(ptrBytes)
		oldLKGGenID = oldPtr.GenerationID
		backupPtrPath = filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("migration_backup_pointer_%s.json", txid))
		if err := strictfs.StrictWriteAtomic(backupPtrPath, ptrBytes, 0600); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("write backup pointer file: " + err.Error())
			return nil, fmt.Errorf("write backup pointer file: %w", err)
		}
		actualPtrBackupDigest, err := strictfs.ComputeFileDigest(backupPtrPath)
		if err != nil || actualPtrBackupDigest != oldPointerDigest {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("backup pointer digest verification failed")
			return nil, fmt.Errorf("backup pointer digest verification failed")
		}
	}
	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("fsync config dir after backups: " + err.Error())
		return nil, fmt.Errorf("fsync config dir after backups: %w", err)
	}

	newRecBytes, _ := json.MarshalIndent(newRec, "", "  ")
	candidateRecDigest := strictfs.ComputeBytesDigest(newRecBytes)

	m := TransactionManifest{
		Version:                  1,
		TxID:                     txid,
		OperationKind:            OperationMigration,
		State:                    StateMigrationIntent,
		Sequence:                 1,
		PreviousGenerationID:     rec.GenerationID,
		VerifiedActiveGeneration: rec.Generation,
		CandidateGenerationID:    newGenID,
		PreviousRecordDigest:     oldVADigest,
		PreviousRecordFile:       backupVAPath,
		PreviousLKGGenerationID:  oldLKGGenID,
		PreviousLKGPointerDigest: oldPointerDigest,
		PreviousLKGPointerFile:   backupPtrPath,
		PreviousConfigDigest:     rec.AppliedConfigDigest,
		CandidateConfigDigest:    newRec.AppliedConfigDigest,
		CandidateRecordDigest:    candidateRecDigest,
		PreviousBridges:          rec.AppliedBridges,
		PreviousBridgesDigest:    BridgesDigest(rec.AppliedBridges),
		TargetBridges:            enrichedBridges,
		TargetBridgesDigest:      BridgesDigest(enrichedBridges),
		DesiredMode:              newRec.RuntimeMode,
		CreatedAt:                time.Now(),
		UpdatedAt:                time.Now(),
	}

	if err := c.casManifest(nil, &m); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("persist migration intent failed: " + err.Error())
		return nil, fmt.Errorf("persist migration intent: %w", err)
	}
	if err := c.syncActiveTransactionRolesLocked(&m); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration sync active transaction roles failed: " + err.Error() + ": " + m.TxID)
		return nil, fmt.Errorf("sync migration transaction roles: %w", err)
	}

	// Boundary 1: before candidate bundle publication
	c.crashAt("BeforeMigrationBundlePublish")

	if c.genStore != nil {
		if c.hooks.FailBundlePublish {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("migration failpoint: bundle publish failed: " + m.TxID)
			return nil, errors.New("failpoint: bundle publish failed")
		}
		if err := c.genStore.PublishStagedBundle(newGenID, nextGen, configBytes, storeSnapshotPath, newRec, c.DaemonEpoch()); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("migration publish upgraded generation bundle failed: " + err.Error() + ": " + m.TxID)
			return nil, fmt.Errorf("publish upgraded generation bundle: %w", err)
		}
	}

	if err := c.transitionManifestLocked(&m, StateMigrationBundlePublished); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration transition to bundle_published failed: " + err.Error() + ": " + m.TxID)
		return nil, err
	}

	// Boundary 2: after candidate bundle publication
	c.crashAt("AfterMigrationBundlePublish")

	if err := c.transitionManifestLocked(&m, StateMigrationActiveWriteIntent); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration transition to active_write_intent failed: " + err.Error() + ": " + m.TxID)
		return nil, err
	}

	// Boundary 3: before verified-active replacement
	c.crashAt("BeforeMigrationActiveWrite")

	if c.hooks.FailCommitVerifiedActive {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration failpoint: commit verified active failed: " + m.TxID)
		return nil, errors.New("failpoint: commit verified active failed")
	}

	if err := strictfs.StrictWriteAtomic(c.verifiedActiveFile, newRecBytes, 0600); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration write upgraded verified-active failed: " + err.Error() + ": " + m.TxID)
		return nil, fmt.Errorf("write upgraded verified-active: %w", err)
	}
	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration fsync config dir after verified-active: " + err.Error() + ": " + m.TxID)
		return nil, fmt.Errorf("fsync config dir: %w", err)
	}

	// COMMIT BOUNDARY CROSSED
	if err := c.transitionManifestLocked(&m, StateMigrationActiveWritten); err != nil {
		return nil, err
	}

	// Boundary 4: after verified-active replacement
	c.crashAt("AfterMigrationActiveWrite")

	if err := c.transitionManifestLocked(&m, StateMigrationPointerWriteIntent); err != nil {
		return nil, err
	}

	// Boundary 5: before pointer replacement
	c.crashAt("BeforeMigrationPointerWrite")

	if c.hooks.FailAdvanceLKGPointer || (c.genStore != nil && c.genStore.hooks.FailPointerWrite) {
		_ = strictfs.StrictWriteAtomic(c.verifiedActiveFile, oldVABytes, 0600)
		_ = strictfs.FsyncDirectory(c.cfg.ConfigDir)
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("migration failpoint: advance LKG pointer failed: " + m.TxID)
		return nil, errors.New("failpoint: advance LKG pointer failed")
	}

	if c.genStore != nil {
		if err := c.genStore.AdvanceLKGPointer(newGenID, nextGen, newRec, c.DaemonEpoch()); err != nil {
			_ = strictfs.StrictWriteAtomic(c.verifiedActiveFile, oldVABytes, 0600)
			_ = strictfs.FsyncDirectory(c.cfg.ConfigDir)
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("migration advance upgraded LKG pointer failed: " + err.Error() + ": " + m.TxID)
			return nil, fmt.Errorf("advance upgraded LKG pointer: %w", err)
		}
	}

	if err := c.transitionManifestLocked(&m, StateMigrationPointerWritten); err != nil {
		return nil, err
	}

	// Boundary 6: after pointer replacement
	c.crashAt("AfterMigrationPointerWrite")

	// Boundary 7: before committed checkpoint
	c.crashAt("BeforeMigrationCommit")

	if err := c.transitionManifestLocked(&m, StateMigrationCommitted); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("transition to migration_committed failed: " + err.Error() + ": " + m.TxID)
		return nil, fmt.Errorf("migration committed transition: %w", err)
	}

	// Boundary 8: after committed checkpoint
	c.crashAt("AfterMigrationCommit")

	if err := strictfs.StrictUnlink(backupVAPath); err != nil && !os.IsNotExist(err) {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("unlink backup active failed: " + err.Error())
		return nil, fmt.Errorf("unlink backup active: %w", err)
	}
	if backupPtrPath != "" {
		if err := strictfs.StrictUnlink(backupPtrPath); err != nil && !os.IsNotExist(err) {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("unlink backup pointer failed: " + err.Error())
			return nil, fmt.Errorf("unlink backup pointer: %w", err)
		}
	}
	if err := strictfs.StrictUnlink(c.manifestFile); err != nil && !os.IsNotExist(err) {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("unlink manifest failed: " + err.Error())
		return nil, fmt.Errorf("unlink manifest: %w", err)
	}
	if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("fsync config dir after migration cleanup: " + err.Error())
		return nil, fmt.Errorf("fsync config dir after migration cleanup: %w", err)
	}
	if err := c.syncActiveTransactionRolesLocked(nil); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("clear active roles after migration: " + err.Error())
		return nil, fmt.Errorf("clear active roles after migration: %w", err)
	}

	return &newRec, nil
}

// SetCompiler configures the compilation function used by the coordinator.
func (c *ApplyCoordinator) SetCompiler(fn func(ctx context.Context) (*CompileResult, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Compiler = fn
}

// CheckMutationAllowed returns ErrRecoveryRequired if the coordinator is in degraded recovery state.
func (c *ApplyCoordinator) CheckMutationAllowed() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state == StateRecoveryRequired {
		return ErrRecoveryRequired
	}
	if _, err := os.Stat(c.recoveryMarkerFile); err == nil {
		return ErrRecoveryRequired
	}
	return nil
}

// IsDegraded returns true if the coordinator is in recovery required state or recovery marker exists.
func (c *ApplyCoordinator) IsDegraded() bool {
	return c.CheckMutationAllowed() != nil
}

// GenStore returns the generation store instance.
func (c *ApplyCoordinator) GenStore() *GenerationStore {
	return c.genStore
}

// DaemonEpoch returns the epoch identifier of this coordinator instance.
func (c *ApplyCoordinator) DaemonEpoch() string {
	return c.daemonEpoch
}

// State returns the current high-level state of the coordinator.
func (c *ApplyCoordinator) State() ManifestState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// AppliedRecord returns the currently verified and active generation record.
func (c *ApplyCoordinator) AppliedRecord() *AppliedGenerationRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.appliedRecord == nil {
		return nil
	}
	cp := *c.appliedRecord
	return &cp
}

// VerifyActiveProcessProof verifies whether the currently active process proof matches reality.
// If no applied record exists or if RuntimeMode is RuntimeOff, no process is expected and nil is returned.
// For RuntimeEnforced, an authoritative ProcessReceipt must exist and strictly match reality
// by PID + ProcStartTicks + ExecutablePath + Cmdline + Sockets.
func (c *ApplyCoordinator) VerifyActiveProcessProof(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.appliedRecord == nil {
		return nil
	}
	if c.appliedRecord.RuntimeMode == RuntimeOff {
		return nil
	}
	if c.appliedRecord.ProcessReceipt == nil {
		return fmt.Errorf("%w: enforced runtime requires authoritative process receipt", ErrProcessProofFailed)
	}
	receipt := c.appliedRecord.ProcessReceipt

	if receipt.AppliedGeneration != c.appliedRecord.Generation {
		return fmt.Errorf("%w: receipt applied generation %d != record generation %d", ErrProcessProofFailed, receipt.AppliedGeneration, c.appliedRecord.Generation)
	}
	if receipt.RuntimeProcessIdentity.Generation != c.appliedRecord.Generation {
		return fmt.Errorf("%w: identity generation %d != record generation %d", ErrProcessProofFailed, receipt.RuntimeProcessIdentity.Generation, c.appliedRecord.Generation)
	}

	if c.cfg.Operator != nil {
		running, pid := c.cfg.Operator.IsRunning()
		if !running || pid != receipt.PID {
			return fmt.Errorf("%w: operator process not running or PID mismatch: running=%v, pid=%d, want=%d", ErrProcessProofFailed, running, pid, receipt.PID)
		}
	}

	// 2. Across daemon restarts: strictly verify by PID + ticks + executable + cmdline + sockets
	binPath := c.cfg.ConfigDir
	if opBin, ok := c.cfg.Operator.(interface{ Binary() string }); ok {
		binPath = opBin.Binary()
	}
	if err := c.verifier.VerifyIdentity("", receipt.PID, receipt.ProcStartTicks, binPath, c.cfg.ConfigDir); err != nil {
		return fmt.Errorf("%w: active process identity mismatch: %v", ErrProcessProofFailed, err)
	}

	for _, l := range c.appliedRecord.AppliedListeners {
		if err := c.verifier.VerifySocketOwnership("", l.Address, int(l.Port), l.Network, receipt.PID); err != nil {
			return fmt.Errorf("%w: listener %s:%d check failed: %v", ErrProcessProofFailed, l.Address, l.Port, err)
		}
	}

	return nil
}

// SetHooks sets failpoint hooks for testing.
func (c *ApplyCoordinator) SetHooks(hooks ApplyCoordinatorHooks) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.hooks = hooks
}

// SyncBridgesForTest exports syncBridgesLocked for cross-package integration testing.
func (c *ApplyCoordinator) SyncBridgesForTest(ctx context.Context, m *TransactionManifest, before, target []BridgeRef) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return c.syncBridgesLocked(ctx, m, before, target)
}

// CleanupPending returns any pending post-commit cleanup items.
func (c *ApplyCoordinator) CleanupPending() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make([]string, len(c.cleanupPending))
	copy(cp, c.cleanupPending)
	return cp
}

func (c *ApplyCoordinator) addCleanupPendingLocked(item string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupPending = append(c.cleanupPending, item)
}

func (c *ApplyCoordinator) log(level, action, message string) {
	if c.cfg.LogFn != nil {
		c.cfg.LogFn(level, action, message)
	}
}

// GenerateTxID creates a timestamp-prefixed, cryptographically random transaction identifier
// conforming strictly to ^[0-9]{14,24}$.
func GenerateTxID() string {
	ts := time.Now().Format("20060102150405")
	var rnd [3]byte
	_, err := rand.Read(rnd[:])
	if err != nil {
		panic(err)
	}
	// 14 digits timestamp + 6 digits random number
	num := (int(rnd[0])<<16 | int(rnd[1])<<8 | int(rnd[2])) % 1000000
	return fmt.Sprintf("%s%06d", ts, num)
}

func (c *ApplyCoordinator) validateManifestOwnershipLocked(m *TransactionManifest) error {
	if m.OperationKind != OperationRollback && m.CandidateConfigFile != "" {
		expected := filepath.Clean(filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("config.yaml.candidate.%s", m.TxID)))
		if filepath.Clean(m.CandidateConfigFile) != expected {
			return fmt.Errorf("candidate config path %q does not match expected canonical path %q", m.CandidateConfigFile, expected)
		}
		evalConfigDir, err := filepath.EvalSymlinks(c.cfg.ConfigDir)
		if err != nil {
			return fmt.Errorf("eval symlinks config dir: %w", err)
		}
		evalCandidateDir, err := filepath.EvalSymlinks(filepath.Dir(m.CandidateConfigFile))
		if err != nil {
			return fmt.Errorf("eval symlinks candidate dir: %w", err)
		}
		if evalCandidateDir != evalConfigDir {
			return fmt.Errorf("candidate config path %q escaped ConfigDir %q (resolved %q vs %q)", m.CandidateConfigFile, c.cfg.ConfigDir, evalCandidateDir, evalConfigDir)
		}
	}

	if m.PreMutationStoreSnapshotFile != "" {
		expected, err := c.cfg.StoreTx.SnapshotFilePath(m.TxID)
		if err != nil {
			return fmt.Errorf("resolve expected pre-mutation snapshot path: %w", err)
		}
		if filepath.Clean(m.PreMutationStoreSnapshotFile) != expected {
			return fmt.Errorf("pre-mutation snapshot path %q does not match expected canonical path %q", m.PreMutationStoreSnapshotFile, expected)
		}
	}

	if m.CandidatePostMutationStoreSnapshotFile != "" {
		expected, err := c.cfg.StoreTx.SnapshotFilePath(m.TxID + "-post")
		if err != nil {
			return fmt.Errorf("resolve expected post-mutation snapshot path: %w", err)
		}
		if filepath.Clean(m.CandidatePostMutationStoreSnapshotFile) != expected {
			return fmt.Errorf("post-mutation snapshot path %q does not match expected canonical path %q", m.CandidatePostMutationStoreSnapshotFile, expected)
		}
	}

	if m.CandidateGenerationID != "" {
		expectedGenDir := filepath.Join(c.genStore.GenerationsDir(), m.CandidateGenerationID)
		if filepath.Clean(expectedGenDir) != expectedGenDir {
			return fmt.Errorf("candidate generation dir is not clean: %s", expectedGenDir)
		}
		evalGensDir, err := filepath.EvalSymlinks(c.genStore.GenerationsDir())
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("eval symlinks generations dir: %w", err)
			}
		} else {
			evalParent, err := filepath.EvalSymlinks(filepath.Dir(expectedGenDir))
			if err != nil {
				if !os.IsNotExist(err) {
					return fmt.Errorf("eval symlinks generation parent dir: %w", err)
				}
			} else if evalParent != evalGensDir {
				return fmt.Errorf("candidate generation path %q escaped generations dir %q", expectedGenDir, c.genStore.GenerationsDir())
			}
		}
	}
	return nil
}

func (c *ApplyCoordinator) RecoverOnStartup(ctx context.Context) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	ipcLock, err := strictfs.LockTransaction(c.cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("acquire transaction lock: %w", err)
	}
	defer ipcLock.Close()

	var isMigrationTx bool
	var migrationTxID string
	if mData, err := os.ReadFile(c.manifestFile); err == nil {
		var peekM TransactionManifest
		if DecodeJSONStrict(mData, &peekM) == nil && peekM.OperationKind == OperationMigration {
			if c.validateMigrationManifestLocked(&peekM) == nil {
				isMigrationTx = true
				migrationTxID = peekM.TxID
			}
		}
	}

	var recoveredMigration bool
	if isMigrationTx {
		var recErr error
		recoveredMigration, recErr = c.recoverMigrationTransactionIfPresentLocked(ctx)
		if recErr != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerIfAbsentLocked("migration recovery failed: " + recErr.Error() + ": " + migrationTxID)
			return fmt.Errorf("migration recovery: %w", recErr)
		}
		if recoveredMigration {
			if markerBytes, mErr := os.ReadFile(c.recoveryMarkerFile); mErr == nil {
				markerContent := string(markerBytes)
				if migrationTxID != "" && strings.HasSuffix(strings.TrimSpace(markerContent), ": "+migrationTxID) {
					if err := strictfs.StrictUnlink(c.recoveryMarkerFile); err != nil && !os.IsNotExist(err) {
						c.setState(StateRecoveryRequired)
						return fmt.Errorf("clear transaction-owned migration recovery marker: %w", err)
					}
				}
			}
		}
	}

	if _, err := os.Stat(c.recoveryMarkerFile); err == nil {
		c.setState(StateRecoveryRequired)
		c.log("error", "startup-recovery", "recovery.marker present; system enters StateRecoveryRequired")
		return ErrRecoveryRequired
	}

	if !isMigrationTx {
		var mErr error
		recoveredMigration, mErr = c.recoverMigrationTransactionIfPresentLocked(ctx)
		if mErr != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerIfAbsentLocked("migration recovery failed: " + mErr.Error())
			return fmt.Errorf("migration recovery: %w", mErr)
		}
	}

	if err := c.recoverCleanupJournalLocked(); err != nil {
		c.setState(StateRecoveryRequired)
		c.log("error", "startup-recovery", "recover cleanup journal failed: "+err.Error())
		return fmt.Errorf("recover cleanup journal: %w", err)
	}

	if err := c.recoverDraftJournalLocked(ctx); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("corrupted or unrecoverable draft journal: " + err.Error())
		return ErrRecoveryRequired
	}

	if !recoveredMigration {
		if err := c.recoverManifestLocked(ctx); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("transaction recovery failed: " + err.Error())
			return fmt.Errorf("%w: transaction recovery failed: %v", ErrRecoveryRequired, err)
		}
	}

	if err := c.recoverMigrationJournalLocked(); err != nil {
		c.setState(StateRecoveryRequired)
		c.log("error", "startup-recovery", "recover migration journal failed: "+err.Error())
		return fmt.Errorf("recover migration journal: %w", err)
	}

	if c.state == StateRecoveryRequired {
		return ErrRecoveryRequired
	}

	if b, err := os.ReadFile(c.verifiedActiveFile); err == nil {
		var rec AppliedGenerationRecord
		if err := DecodeJSONStrict(b, &rec); err == nil && rec.ValidateSchema() == nil {
			if rec.IsLegacyBridgeIdentity() {
				upgradedRec, upErr := c.upgradeLegacyAppliedBridgeIdentityLocked(ctx, rec)
				if upErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerIfAbsentLocked("legacy bridge startup migration failed: " + upErr.Error())
					return fmt.Errorf("legacy bridge startup migration: %w", upErr)
				}
				rec = *upgradedRec
			}
			c.mu.Lock()
			c.appliedRecord = &rec
			c.mu.Unlock()
			if c.cfg.BridgeRuntime != nil {
				if reg, ok := c.cfg.BridgeRuntime.(DurableBridgeRegistry); ok {
					if err := reg.ReplaceDurableBridges(rec.AppliedBridges); err != nil {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked("startup bridge registry replacement failed: " + err.Error())
						return fmt.Errorf("startup bridge registry replacement: %w", err)
					}
				}
			}
		} else {
			_, qErr := strictfs.StrictQuarantine(c.verifiedActiveFile, c.quarantineDir, "corrupt_verified_active")
			if qErr != nil {
				c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
			}
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("corrupt verified-active.json")
			return ErrRecoveryRequired
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("read verified-active.json failed: " + err.Error())
		return fmt.Errorf("read verified-active.json: %w", err)
	}

	c.reconcilePendingInputLocked()
	if c.state != StateRecoveryRequired {
		c.setState(StateIdle)
	}
	return nil
}

func (c *ApplyCoordinator) reconcilePendingInputLocked() {
	b, err := os.ReadFile(c.pendingInputFile)
	if err != nil {
		return
	}
	var rec PendingInputRecord
	if err := DecodeJSONStrict(b, &rec); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.pendingInputFile, c.quarantineDir, "corrupt_pending_input")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return
	}
	if c.appliedRecord != nil {
		matched := false
		if rec.TargetInputDigest != "" && c.appliedRecord.AppliedInputDigest == rec.TargetInputDigest {
			matched = true
		} else if rec.MonotonicGeneration > 0 && c.appliedRecord.Generation >= rec.MonotonicGeneration {
			matched = true
		}
		if matched {
			if uErr := strictfs.StrictUnlink(c.pendingInputFile); uErr != nil && !os.IsNotExist(uErr) {
				c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
			}
		}
	}
}

// QueuePendingInput queues a convergence request from an external mutation producer
// (e.g. settings, router rules, subscription refresh, tunnels, native resources).
// It coalesces concurrent requests, merges sources, increments MonotonicGeneration,
// and writes the result durably to input.pending.json.
func (c *ApplyCoordinator) QueuePendingInput(ctx context.Context, source string, reason string) (uint64, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	var rec PendingInputRecord
	b, err := os.ReadFile(c.pendingInputFile)
	if err == nil && len(b) > 0 {
		if unmarshalErr := DecodeJSONStrict(b, &rec); unmarshalErr != nil {
			_, _ = strictfs.StrictQuarantine(c.pendingInputFile, c.quarantineDir, "corrupt_pending_input")
			rec = PendingInputRecord{}
		}
	}

	rec.Version = 1
	rec.MonotonicGeneration++
	if rec.TxID == "" {
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		rec.TxID = fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(rnd[:]))
	}
	var opRnd [6]byte
	_, _ = rand.Read(opRnd[:])
	rec.OperationID = fmt.Sprintf("op-%d-%s", rec.MonotonicGeneration, hex.EncodeToString(opRnd[:]))

	if source != "" {
		found := false
		for _, s := range rec.Sources {
			if s == source {
				found = true
				break
			}
		}
		if !found {
			rec.Sources = append(rec.Sources, source)
			sort.Strings(rec.Sources)
		}
		if rec.Source == "" {
			rec.Source = source
		}
	}

	if reason != "" {
		if rec.Reason == "" {
			rec.Reason = reason
		} else if !strings.Contains(rec.Reason, reason) {
			rec.Reason += "; " + reason
		}
	}

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	rec.MergedAt = time.Now()
	rec.State = "pending"

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal pending input: %w", err)
	}

	if err := strictfs.StrictWriteAtomic(c.pendingInputFile, data, 0600); err != nil {
		return 0, fmt.Errorf("write pending input: %w", err)
	}

	return rec.MonotonicGeneration, nil
}

// ClearPendingInputIfGenerationMatches attempts to clear input.pending.json via
// compare-and-delete. If newer mutations have arrived (MonotonicGeneration > targetGen),
// the file is preserved to trigger subsequent convergence, and false is returned.
func (c *ApplyCoordinator) ClearPendingInputIfGenerationMatches(targetGen uint64) (bool, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	b, err := os.ReadFile(c.pendingInputFile)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	var rec PendingInputRecord
	if err := DecodeJSONStrict(b, &rec); err != nil {
		_, _ = strictfs.StrictQuarantine(c.pendingInputFile, c.quarantineDir, "corrupt_pending_input")
		return true, nil
	}

	if rec.MonotonicGeneration <= targetGen {
		if err := strictfs.StrictUnlink(c.pendingInputFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("unlink pending input: %w", err)
		}
		return true, nil
	}

	// Newer intent exists; do not delete
	return false, nil
}

// ReadPendingInput reads and validates the current pending input record if present.
func (c *ApplyCoordinator) ReadPendingInput() (*PendingInputRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	b, err := os.ReadFile(c.pendingInputFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rec PendingInputRecord
	if err := DecodeJSONStrict(b, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal pending input: %w", err)
	}
	if err := rec.ValidateSchema(); err != nil {
		return nil, err
	}
	return &rec, nil
}

// StartMigrationJournalLocked initializes a durable two-phase migration journal.
func (c *ApplyCoordinator) StartMigrationJournalLocked(files []string) (*MigrationJournal, error) {
	mj := &MigrationJournal{
		Version:   1,
		State:     "in_progress",
		StartedAt: time.Now(),
		Imported:  files,
	}
	data, err := json.MarshalIndent(mj, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal migration journal: %w", err)
	}
	if err := strictfs.StrictWriteAtomic(c.migrationJournalFile, data, 0600); err != nil {
		return nil, fmt.Errorf("write migration journal: %w", err)
	}
	return mj, nil
}

// CompleteMigrationJournalLocked marks the migration as completed.
func (c *ApplyCoordinator) CompleteMigrationJournalLocked(mj *MigrationJournal) error {
	mj.State = "completed"
	mj.CompletedAt = time.Now()
	data, err := json.MarshalIndent(mj, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal completed migration journal: %w", err)
	}
	if err := strictfs.StrictWriteAtomic(c.migrationJournalFile, data, 0600); err != nil {
		return fmt.Errorf("write completed migration journal: %w", err)
	}
	return nil
}

func (c *ApplyCoordinator) recoverMigrationJournalLocked() error {
	data, err := os.ReadFile(c.migrationJournalFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read migration journal: %w", err)
	}

	var mj MigrationJournal
	if err := DecodeJSONStrict(data, &mj); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.migrationJournalFile, c.quarantineDir, "corrupt_migration_journal")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("corrupt migration journal: %w", err)
	}
	if err := mj.ValidateSchema(); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.migrationJournalFile, c.quarantineDir, "invalid_migration_journal_schema")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("invalid migration journal schema: %w", err)
	}

	// If migration was interrupted (in_progress), quarantine it so fresh migration can be re-evaluated
	if mj.State == "in_progress" {
		_, qErr := strictfs.StrictQuarantine(c.migrationJournalFile, c.quarantineDir, "interrupted_migration_journal")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
			return fmt.Errorf("quarantine interrupted migration journal: %w", qErr)
		}
		c.log("warn", "startup-recovery", "interrupted legacy migration journal quarantined")
	}

	return nil
}

func (c *ApplyCoordinator) recoverDraftJournalLocked(ctx context.Context) error {
	data, err := os.ReadFile(c.draftJournalFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read draft journal: %w", err)
	}

	var dj DraftJournal
	if err := DecodeJSONStrict(data, &dj); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.draftJournalFile, c.quarantineDir, "corrupt_draft_journal")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("corrupt draft journal: %w", err)
	}
	if err := dj.ValidateSchema(); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.draftJournalFile, c.quarantineDir, "invalid_draft_journal_schema")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("invalid draft journal schema: %w", err)
	}

	switch dj.State {
	case DraftSnapshotSecured:
		if dj.DraftSnapshotFile != "" {
			if restErr := c.cfg.StoreTx.RestoreSnapshotFile(dj.DraftSnapshotFile); restErr != nil {
				c.writeRecoveryMarkerLocked("restore snapshot failed: " + restErr.Error())
			}
			if uErr := strictfs.StrictUnlink(dj.DraftSnapshotFile); uErr != nil && !os.IsNotExist(uErr) {
				c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
			}
		}
		if uErr := strictfs.StrictUnlink(c.draftJournalFile); uErr != nil && !os.IsNotExist(uErr) {
			c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
		}
	case DraftStoreMutated:
		if dj.DraftSnapshotFile != "" {
			if restErr := c.cfg.StoreTx.RestoreSnapshotFile(dj.DraftSnapshotFile); restErr != nil {
				c.writeRecoveryMarkerLocked("restore snapshot failed: " + restErr.Error())
			}
			if uErr := strictfs.StrictUnlink(dj.DraftSnapshotFile); uErr != nil && !os.IsNotExist(uErr) {
				c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
			}
		}
		if uErr := strictfs.StrictUnlink(c.draftJournalFile); uErr != nil && !os.IsNotExist(uErr) {
			c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
		}
	case DraftPending:
		c.log("info", "startup-recovery", "retained valid pending draft journal")
	case DraftConsuming:
		manifestData, mErr := os.ReadFile(c.manifestFile)
		if mErr == nil {
			var m TransactionManifest
			if DecodeJSONStrict(manifestData, &m) == nil && m.State == StateCommitted {
				if dj.DraftSnapshotFile != "" {
					if uErr := strictfs.StrictUnlink(dj.DraftSnapshotFile); uErr != nil && !os.IsNotExist(uErr) {
						c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
					}
				}
				if uErr := strictfs.StrictUnlink(c.draftJournalFile); uErr != nil && !os.IsNotExist(uErr) {
					c.writeRecoveryMarkerLocked("unlink failed: " + uErr.Error())
				}
				return nil
			}
		}
		dj.State = DraftPending
		dj.AssociatedApplyTxID = ""
		dj.UpdatedAt = time.Now()
		if b, err := json.MarshalIndent(dj, "", "  "); err == nil {
			if wErr := strictfs.StrictWriteAtomic(c.draftJournalFile, b, 0600); wErr != nil {
				c.writeRecoveryMarkerLocked("atomic write failed: " + wErr.Error())
			}
		}
	default:
		_, qErr := strictfs.StrictQuarantine(c.draftJournalFile, c.quarantineDir, "unknown_draft_state")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("unknown draft journal state: %s", dj.State)
	}
	return nil
}

func (c *ApplyCoordinator) recoverManifestLocked(ctx context.Context) error {
	data, err := os.ReadFile(c.manifestFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var m TransactionManifest
	if err := DecodeJSONStrict(data, &m); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.manifestFile, c.quarantineDir, "corrupt_manifest")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("corrupt manifest: %w", err)
	}
	if err := m.ValidateSchema(); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.manifestFile, c.quarantineDir, "invalid_manifest_schema")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("invalid manifest schema: %w", err)
	}
	if err := m.ValidateSchemaForPhase(); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.manifestFile, c.quarantineDir, "invalid_phase_schema")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("invalid manifest phase schema: %w", err)
	}
	if err := c.validateManifestOwnershipLocked(&m); err != nil {
		_, qErr := strictfs.StrictQuarantine(c.manifestFile, c.quarantineDir, "manifest_ownership_violation")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("manifest ownership violation: %w", err)
	}

	c.log("warn", "startup-recovery", fmt.Sprintf("recovering interrupted transaction %s in state %s", m.TxID, m.State))

	switch m.State {
	case StateRollbackIntent, StateRollbackStoreRestored, StateRollbackConfigPromoted, StateRollbackRuntimeVerified, StateRollbackBridgesVerified:
		c.log("info", "startup-recovery", fmt.Sprintf("resuming interrupted rollback in state %s", m.State))
		return c.resumeRollbackTransactionLocked(ctx, &m)

	case StateIdle, StateCommitted, StateRolledBack, StateTerminalCleanup, StateRecoveryCommitted, StateRollbackCommitted:
		if err := c.cleanupTxArtifactsLocked(&m); err != nil {
			return fmt.Errorf("terminal cleanup failed during recovery: %w", err)
		}
		c.setState(StateIdle)
		return nil

	case StatePreSnapshotWriteIntent, StateSnapshotSecured, StateCandidateWriteIntent, StateCandidateBuilt, StateCandidatePublished, StateRecoveryIntent:
		c.log("warn", "startup-recovery", fmt.Sprintf("aborting pre-mutation transaction %s in state %s", m.TxID, m.State))
		activeConfigModified := false
		if m.CandidateConfigDigest != "" {
			d, err := strictfs.ComputeFileDigest(c.activeConfigFile)
			if err == nil && d == m.CandidateConfigDigest {
				activeConfigModified = true
			}
		}
		if activeConfigModified {
			targetLKG := m.PreviousLKGGenerationID
			if targetLKG == "" {
				targetLKG = m.LKGGenerationID
			}
			if targetLKG == "" {
				ptr, pErr := c.genStore.ReadLKGPointer()
				if pErr != nil {
					if !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked("read LKG pointer during abort failed: " + pErr.Error())
						return fmt.Errorf("read LKG pointer: %w", pErr)
					}
				} else if ptr != nil {
					targetLKG = ptr.GenerationID
				}
			}
			if targetLKG != "" {
				return c.rollbackToGenerationLocked(ctx, targetLKG, m.StartedFromRecoveryRequired)
			}
			if _, err := os.Stat(c.lkgConfigFile); err == nil {
				data, rErr := os.ReadFile(c.lkgConfigFile)
				if rErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("read legacy LKG config failed: " + rErr.Error())
					return fmt.Errorf("read legacy LKG config: %w", rErr)
				}
				if wErr := strictfs.StrictWriteAtomic(c.activeConfigFile, data, 0644); wErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("restore legacy LKG config failed: " + wErr.Error())
					return fmt.Errorf("restore legacy LKG config: %w", wErr)
				}
			}
		}
		if m.PreMutationStoreSnapshotFile != "" && c.cfg.StoreTx != nil {
			if rErr := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); rErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("restore pre-mutation snapshot failed: " + rErr.Error())
				return fmt.Errorf("restore pre-mutation snapshot: %w", rErr)
			}
		}
		if clErr := c.cleanupTxArtifactsLocked(&m); clErr != nil {
			return fmt.Errorf("cleanup failed during recovery: %w", clErr)
		}
		if m.StartedFromRecoveryRequired {
			c.setState(StateRecoveryRequired)
			if _, err := os.Stat(c.recoveryMarkerFile); os.IsNotExist(err) {
				_ = c.writeRecoveryMarkerLocked("transaction aborted; preserved recovery required state")
			}
			return ErrRecoveryRequired
		}
		c.setState(StateIdle)
		return nil

	case StateSwapIntent:
		activeConfigModified := false
		if m.CandidateConfigDigest != "" {
			d, err := strictfs.ComputeFileDigest(c.activeConfigFile)
			if err == nil && d == m.CandidateConfigDigest {
				activeConfigModified = true
			}
		}
		if activeConfigModified {
			targetLKG := m.PreviousLKGGenerationID
			if targetLKG == "" {
				targetLKG = m.LKGGenerationID
			}
			if targetLKG == "" {
				ptr, pErr := c.genStore.ReadLKGPointer()
				if pErr != nil {
					if !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked("read LKG pointer during swap abort failed: " + pErr.Error())
						return fmt.Errorf("read LKG pointer: %w", pErr)
					}
				} else if ptr != nil {
					targetLKG = ptr.GenerationID
				}
			}
			if targetLKG != "" {
				return c.rollbackToGenerationLocked(ctx, targetLKG, m.StartedFromRecoveryRequired)
			}
			if _, err := os.Stat(c.lkgConfigFile); err == nil {
				data, rErr := os.ReadFile(c.lkgConfigFile)
				if rErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("read legacy LKG config failed: " + rErr.Error())
					return fmt.Errorf("read legacy LKG config: %w", rErr)
				}
				if wErr := strictfs.StrictWriteAtomic(c.activeConfigFile, data, 0644); wErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("restore legacy LKG config failed: " + wErr.Error())
					return fmt.Errorf("restore legacy LKG config: %w", wErr)
				}
			}
		}
		if m.PreMutationStoreSnapshotFile != "" && c.cfg.StoreTx != nil {
			if rErr := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); rErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("restore store snapshot failed: " + rErr.Error())
				return fmt.Errorf("restore store snapshot: %w", rErr)
			}
		}
		if clErr := c.cleanupTxArtifactsLocked(&m); clErr != nil {
			return fmt.Errorf("cleanup failed: %w", clErr)
		}
		if m.StartedFromRecoveryRequired {
			c.setState(StateRecoveryRequired)
			return ErrRecoveryRequired
		}
		c.setState(StateIdle)
		return nil

	case StateConfigPromoted, StateRuntimeVerified, StateBridgesReconciling, StateSwapApplied, StateSwapVerified, StateRuntimeIntent, StateRuntimeApplied, StateAbortInProgress:
		c.log("warn", "startup-recovery", fmt.Sprintf("rolling back interrupted transaction %s in state %s to LKG", m.TxID, m.State))
		targetLKG := m.PreviousLKGGenerationID
		if targetLKG == "" {
			targetLKG = m.LKGGenerationID
		}
		if targetLKG == "" {
			ptr, pErr := c.genStore.ReadLKGPointer()
			if pErr != nil {
				if !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("read LKG pointer for rollback failed: " + pErr.Error())
					return fmt.Errorf("read LKG pointer for rollback: %w", pErr)
				}
			} else if ptr != nil {
				targetLKG = ptr.GenerationID
			}
		}
		if targetLKG != "" {
			return c.rollbackToGenerationLocked(ctx, targetLKG, m.StartedFromRecoveryRequired)
		}
		// Check legacy config.yaml.lkg fallback
		if _, statErr := os.Stat(c.lkgConfigFile); statErr == nil {
			lkgBytes, readErr := os.ReadFile(c.lkgConfigFile)
			if readErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read legacy LKG config failed: " + readErr.Error())
				return fmt.Errorf("read legacy LKG config: %w", readErr)
			}
			if wErr := strictfs.StrictWriteAtomic(c.activeConfigFile, lkgBytes, 0644); wErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("restore legacy LKG config failed: " + wErr.Error())
				return fmt.Errorf("restore legacy LKG config: %w", wErr)
			}
			if m.PreMutationStoreSnapshotFile != "" && c.cfg.StoreTx != nil {
				if rErr := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); rErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("restore store snapshot failed: " + rErr.Error())
					return fmt.Errorf("restore store snapshot: %w", rErr)
				}
			}
			if clErr := c.cleanupTxArtifactsLocked(&m); clErr != nil {
				return fmt.Errorf("cleanup failed during recovery: %w", clErr)
			}
			c.setState(StateIdle)
			return nil
		}
		// First generation case: ONLY when we positively know pointer file does NOT exist on disk
		if _, pStatErr := os.Stat(c.genStore.LKGPointerFile()); pStatErr == nil || !os.IsNotExist(pStatErr) {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("LKG pointer file exists but could not be resolved; refusing to unlink active config")
			return errors.New("LKG pointer file exists but could not be resolved; refusing to unlink active config")
		}
		if err := c.stopControlledLocked(ctx, m.TxID, "first gen rollback"); err != nil {
			return err
		}
		if _, statErr := os.Stat(c.activeConfigFile); statErr == nil {
			if uErr := strictfs.StrictUnlink(c.activeConfigFile); uErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("unlink active config during first gen rollback failed: " + uErr.Error())
				return fmt.Errorf("unlink active config during first gen rollback: %w", uErr)
			}
		}
		if clErr := c.cleanupTxArtifactsLocked(&m); clErr != nil {
			return fmt.Errorf("first gen cleanup failed: %w", clErr)
		}
		if m.StartedFromRecoveryRequired {
			c.setState(StateRecoveryRequired)
			return ErrRecoveryRequired
		}
		c.setState(StateIdle)
		return nil

	case StateCommitIntent:
		c.log("info", "startup-recovery", "rolling forward commit intent")
		var rec AppliedGenerationRecord
		if m.CandidateGenerationID != "" {
			genMan, _, _, err := c.genStore.ReadGenerationBundle(m.CandidateGenerationID)
			if err != nil {
				targetLKG := m.PreviousLKGGenerationID
				if targetLKG != "" && targetLKG != m.CandidateGenerationID {
					return c.rollbackToGenerationLocked(ctx, targetLKG, m.StartedFromRecoveryRequired)
				}
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read bundle failed: " + err.Error())
				return ErrRecoveryRequired
			}
			rec = AppliedGenerationRecord{
				Version:                   1,
				BridgeIdentityVersion:     genMan.BridgeIdentityVersion,
				Generation:                genMan.GenerationNumber,
				GenerationID:              genMan.GenerationID,
				AppliedAt:                 time.Now(),
				AppliedStoreDigest:        genMan.AppliedStoreDigest,
				AppliedDesiredStoreDigest: genMan.AppliedDesiredStoreDigest,
				AppliedConfigDigest:       genMan.AppliedConfigDigest,
				AppliedInputDigest:        genMan.AppliedInputDigest,
				AppliedListeners:          genMan.AppliedListeners,
				AppliedBridges:            genMan.AppliedBridges,
				AppliedBridgesDigest:      genMan.AppliedBridgesDigest,
				RuntimeMode:               genMan.RuntimeMode,
			}
		} else {
			rec = AppliedGenerationRecord{
				Version:                   1,
				BridgeIdentityVersion:     CurrentBridgeIdentityVersion,
				Generation:                m.VerifiedActiveGeneration + 1,
				GenerationID:              m.CandidateGenerationID,
				AppliedAt:                 time.Now(),
				AppliedStoreDigest:        m.CandidatePostMutationStoreDigest,
				AppliedDesiredStoreDigest: m.TargetDesiredStoreDigest,
				AppliedConfigDigest:       m.CandidateConfigDigest,
				AppliedInputDigest:        m.TargetInputDigest,
				AppliedBridges:            m.TargetBridges,
				AppliedBridgesDigest:      m.TargetBridgesDigest,
				RuntimeMode:               m.DesiredMode,
			}
		}

		if err := c.executeFinalCommitLocked(ctx, &m, rec); err != nil {
			targetLKG := m.PreviousLKGGenerationID
			if targetLKG != "" && targetLKG != m.CandidateGenerationID {
				c.log("warn", "startup-recovery", fmt.Sprintf("commit roll-forward failed: %v; rolling back to PreviousLKG %s", err, targetLKG))
				return c.rollbackToGenerationLocked(ctx, targetLKG, m.StartedFromRecoveryRequired)
			}
			return err
		}
		if clErr := c.cleanupTxArtifactsLocked(&m); clErr != nil {
			return fmt.Errorf("commit roll-forward succeeded but cleanup failed: %w", clErr)
		}
		c.setState(StateIdle)
		return nil

	case StateRecoveryRequired:
		return ErrRecoveryRequired
	default:
		_, qErr := strictfs.StrictQuarantine(c.manifestFile, c.quarantineDir, "unknown_manifest_state")
		if qErr != nil {
			c.writeRecoveryMarkerLocked("quarantine failed: " + qErr.Error())
		}
		return fmt.Errorf("unknown manifest state: %s", m.State)
	}
}

func (c *ApplyCoordinator) processCleanupJournalFilesLocked(cj *CleanupJournal) error {
	finishCleanup := func() error {
		var finalErr error
		if c.state != StateRecoveryRequired {
			if c.hooks.FailCleanupRecoveryMarker {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("failpoint: cleanup recovery marker failed")
				return fmt.Errorf("failpoint: cleanup recovery marker failed")
			}
			if err := strictfs.StrictUnlink(c.recoveryMarkerFile); err != nil && !os.IsNotExist(err) {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("cleanup recovery marker failed: " + err.Error())
				return fmt.Errorf("cleanup recovery marker: %w", err)
			}
		}

		if err := strictfs.StrictUnlink(c.manifestFile); err != nil && !os.IsNotExist(err) {
			finalErr = fmt.Errorf("manifest unlink failed: %w", err)
			_ = c.writeRecoveryMarkerLocked(finalErr.Error())
			return finalErr
		}
		c.syncActiveTransactionRolesLocked(nil)

		c.crashAt("terminal_cleanup_interrupted")

		if err := strictfs.StrictUnlink(c.cleanupJournalFile); err != nil && !os.IsNotExist(err) {
			finalErr = fmt.Errorf("cleanup journal unlink failed: %w", err)
			_ = c.writeRecoveryMarkerLocked(finalErr.Error())
			return finalErr
		}
		return nil
	}

	if cj == nil || len(cj.Files) == 0 {
		return finishCleanup()
	}

	var remaining []string
	changed := false
	var unlinkErrs []error
	for _, f := range cj.Files {
		var err error
		if strings.HasPrefix(f, "gen-") {
			var activeGenID, lkgGenID string
			c.mu.RLock()
			if c.appliedRecord != nil {
				activeGenID = c.appliedRecord.GenerationID
			}
			c.mu.RUnlock()
			if lkgPtr, pErr := c.genStore.ReadLKGPointer(); pErr == nil && lkgPtr != nil {
				lkgGenID = lkgPtr.GenerationID
			} else if pErr != nil && !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
				return fmt.Errorf("read LKG pointer during cleanup: %w", pErr)
			}
			if f == activeGenID || (lkgGenID != "" && f == lkgGenID) {
				err = fmt.Errorf("refusing to remove active or LKG generation %s", f)
			} else {
				err = c.genStore.RemoveCandidateGeneration(f, activeGenID, lkgGenID)
			}
		} else {
			targetPath := filepath.Join(c.cfg.ConfigDir, f)
			err = strictfs.StrictUnlink(targetPath)
		}
		if c.hooks.FailCleanupUnlink {
			err = fmt.Errorf("failpoint: unlink failed")
		}
		if err != nil && !os.IsNotExist(err) {
			remaining = append(remaining, f)
			unlinkErrs = append(unlinkErrs, fmt.Errorf("unlink %s: %w", f, err))
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("cleanup unlink failed %s: %v", f, err))
		} else {
			var stillExists bool
			if strings.HasPrefix(f, "gen-") {
				targetDir := filepath.Join(c.genStore.GenerationsDir(), f)
				if _, statErr := os.Stat(targetDir); statErr == nil || !os.IsNotExist(statErr) {
					stillExists = true
				}
			} else {
				targetPath := filepath.Join(c.cfg.ConfigDir, f)
				if _, statErr := os.Stat(targetPath); statErr == nil || !os.IsNotExist(statErr) {
					stillExists = true
				}
			}
			if stillExists {
				remaining = append(remaining, f)
				unlinkErrs = append(unlinkErrs, fmt.Errorf("verify unlink %s: still exists", f))
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("cleanup unlink verify failed %s: still exists", f))
			} else {
				changed = true
			}
		}
	}

	var finalErr error
	if len(unlinkErrs) > 0 {
		finalErr = unlinkErrs[0]
	}

	if len(remaining) == 0 {
		if err := finishCleanup(); err != nil {
			return err
		}
	} else if changed {
		cj.Sequence++
		cj.Files = remaining
		b, err := json.MarshalIndent(cj, "", "  ")
		if err != nil {
			err = fmt.Errorf("cleanup journal marshal failed: %w", err)
			_ = c.writeRecoveryMarkerLocked(err.Error())
			if finalErr == nil {
				finalErr = err
			}
		} else if err := strictfs.StrictWriteAtomic(c.cleanupJournalFile, b, 0600); err != nil {
			err = fmt.Errorf("cleanup journal rewrite failed: %w", err)
			_ = c.writeRecoveryMarkerLocked(err.Error())
			if finalErr == nil {
				finalErr = err
			}
		}
	}
	return finalErr
}

func (c *ApplyCoordinator) abortPreMutationIntentLocked(m *TransactionManifest, causeErr error) error {
	if err := c.cleanupTxArtifactsLocked(m); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("abort pre-mutation cleanup failed: %v", err))
		return errors.Join(causeErr, ErrRecoveryRequired, err)
	}
	if m.StartedFromRecoveryRequired {
		c.setState(StateRecoveryRequired)
		if _, err := os.Stat(c.recoveryMarkerFile); os.IsNotExist(err) {
			_ = c.writeRecoveryMarkerLocked("transaction aborted; preserved initial recovery required state")
		}
		return errors.Join(causeErr, ErrRecoveryRequired)
	}
	c.setState(StateIdle)
	return causeErr
}

func (c *ApplyCoordinator) abortEarlyLocked(m *TransactionManifest, causeErr error) error {
	var recoveryErrs []error

	if m.PreMutationStoreSnapshotFile != "" {
		if err := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("restore pre-mutation store: %w", err))
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("early abort restore snapshot failed: %v", err))
		} else {
			digest, err := c.cfg.StoreTx.CurrentSnapshotDigest()
			if err != nil {
				recoveryErrs = append(recoveryErrs, fmt.Errorf("read store digest after restore: %w", err))
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("early abort read store digest failed: %v", err))
			} else if digest != m.PreMutationStoreDigest {
				recoveryErrs = append(recoveryErrs, fmt.Errorf("restored snapshot digest mismatch: got %s, want %s", digest, m.PreMutationStoreDigest))
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("early abort restored snapshot digest mismatch: got %s, want %s", digest, m.PreMutationStoreDigest))
			}
		}
	}

	if len(recoveryErrs) != 0 {
		c.setState(StateRecoveryRequired)
		return errors.Join(append([]error{causeErr}, recoveryErrs...)...)
	}

	if clErr := c.cleanupTxArtifactsLocked(m); clErr != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("early abort cleanup failed: %v", clErr))
		return errors.Join(causeErr, fmt.Errorf("cleanup failed: %w", clErr))
	}

	if m.StartedFromRecoveryRequired {
		c.setState(StateRecoveryRequired)
		if _, err := os.Stat(c.recoveryMarkerFile); os.IsNotExist(err) {
			_ = c.writeRecoveryMarkerLocked("transaction aborted; preserved initial recovery required state")
		}
		return errors.Join(causeErr, ErrRecoveryRequired)
	}

	c.setState(StateIdle)
	return causeErr
}

func (c *ApplyCoordinator) cleanupTxArtifactsLocked(m *TransactionManifest) error {
	defer c.syncActiveTransactionRolesLocked(nil)
	var files []string
	if m.PreMutationStoreSnapshotFile != "" {
		files = append(files, filepath.Base(m.PreMutationStoreSnapshotFile))
	}
	if m.CandidateConfigFile != "" {
		files = append(files, filepath.Base(m.CandidateConfigFile))
	}
	if m.CandidatePostMutationStoreSnapshotFile != "" {
		files = append(files, filepath.Base(m.CandidatePostMutationStoreSnapshotFile))
	}
	candidateVA := fmt.Sprintf("verified-active.json.staged.%s", m.TxID)
	if _, err := os.Stat(filepath.Join(c.cfg.ConfigDir, candidateVA)); err == nil {
		files = append(files, candidateVA)
	}
	candidatePtr := fmt.Sprintf("lkg.pointer.json.staged.%s", m.TxID)
	if _, err := os.Stat(filepath.Join(c.cfg.ConfigDir, candidatePtr)); err == nil {
		files = append(files, candidatePtr)
	}
	if m.ArchivedManifestFile != "" {
		if _, err := os.Stat(filepath.Join(c.cfg.ConfigDir, m.ArchivedManifestFile)); err == nil {
			files = append(files, m.ArchivedManifestFile)
		}
	}
	if _, err := os.Stat(c.draftJournalFile); err == nil {
		files = append(files, filepath.Base(c.draftJournalFile))
	}
	if _, err := os.Stat(c.pendingInputFile); err == nil {
		files = append(files, filepath.Base(c.pendingInputFile))
	}
	if m.CandidateGenerationID != "" {
		var activeGenID, lkgGenID string
		c.mu.RLock()
		if c.appliedRecord != nil {
			activeGenID = c.appliedRecord.GenerationID
		}
		c.mu.RUnlock()
		if lkgPtr, pErr := c.genStore.ReadLKGPointer(); pErr == nil && lkgPtr != nil {
			lkgGenID = lkgPtr.GenerationID
		} else if pErr != nil && !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
			return fmt.Errorf("read LKG pointer for cleanup journal: %w", pErr)
		}
		if m.CandidateGenerationID != activeGenID && (lkgGenID == "" || m.CandidateGenerationID != lkgGenID) {
			files = append(files, m.CandidateGenerationID)
		}
	}

	var cj *CleanupJournal
	if len(files) > 0 {
		cj = &CleanupJournal{
			Version:  1,
			Sequence: 1,
			TxID:     m.TxID,
			Files:    files,
		}
		if m.Sequence > 0 && m.State != StateTerminalCleanup && m.State != StateCandidateWriteIntent && m.State != StatePreSnapshotWriteIntent {
			if b, err := json.MarshalIndent(cj, "", "  "); err == nil {
				if wErr := strictfs.StrictWriteAtomic(c.cleanupJournalFile, b, 0600); wErr != nil {
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("cleanup journal write failed: %v", wErr))
					return fmt.Errorf("cleanup journal write failed: %w", wErr)
				}
			}
			if err := c.transitionManifestLocked(m, StateTerminalCleanup); err != nil {
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("terminal cleanup transition failed: %v", err))
				return fmt.Errorf("terminal cleanup transition failed: %w", err)
			}
		}
	} else if m.Sequence > 0 && m.State != StateTerminalCleanup && m.State != StateCandidateWriteIntent && m.State != StatePreSnapshotWriteIntent {
		if err := c.transitionManifestLocked(m, StateTerminalCleanup); err != nil {
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("terminal cleanup transition failed: %v", err))
			return fmt.Errorf("terminal cleanup transition failed: %w", err)
		}
	}

	return c.processCleanupJournalFilesLocked(cj)
}

func (c *ApplyCoordinator) recoverCleanupJournalLocked() error {
	data, err := os.ReadFile(c.cleanupJournalFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		_ = c.writeRecoveryMarkerLocked("read cleanup journal failed: " + err.Error())
		return fmt.Errorf("%w: read cleanup journal failed: %v", ErrRecoveryRequired, err)
	}

	var cj CleanupJournal
	if err := DecodeJSONStrict(data, &cj); err != nil {
		_ = c.writeRecoveryMarkerLocked("corrupt cleanup journal json: " + err.Error())
		return fmt.Errorf("%w: corrupt cleanup journal json: %v", ErrRecoveryRequired, err)
	}
	if err := cj.ValidateSchema(); err != nil {
		_ = c.writeRecoveryMarkerLocked("invalid cleanup journal schema: " + err.Error())
		return fmt.Errorf("%w: invalid cleanup journal schema: %v", ErrRecoveryRequired, err)
	}

	if err := c.processCleanupJournalFilesLocked(&cj); err != nil {
		_ = c.writeRecoveryMarkerLocked("process cleanup journal files failed: " + err.Error())
		return fmt.Errorf("%w: process cleanup journal files failed: %v", ErrRecoveryRequired, err)
	}
	return nil
}

func (c *ApplyCoordinator) GarbageCollectGenerations(keepRecent int) error {
	ipcLock, err := strictfs.LockTransaction(c.cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("acquire transaction lock: %w", err)
	}
	defer ipcLock.Close()

	c.mu.RLock()
	var activeGenID string
	if c.appliedRecord != nil {
		activeGenID = c.appliedRecord.GenerationID
	}
	c.mu.RUnlock()

	lkgPtr, pErr := c.genStore.ReadLKGPointer()
	if pErr != nil && !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
		return fmt.Errorf("read LKG pointer for GC: %w", pErr)
	}
	var lkgGenID string
	if lkgPtr != nil {
		lkgGenID = lkgPtr.GenerationID
	}

	var protectedGenIDs []string

	// Protect generations referenced in the active transaction manifest
	data, err := os.ReadFile(c.manifestFile)
	if err == nil {
		var m TransactionManifest
		if DecodeJSONStrict(data, &m) == nil {
			if m.CandidateGenerationID != "" {
				protectedGenIDs = append(protectedGenIDs, m.CandidateGenerationID)
			}
			if m.PreviousGenerationID != "" {
				protectedGenIDs = append(protectedGenIDs, m.PreviousGenerationID)
			}
			if m.RollbackTargetGenerationID != "" {
				protectedGenIDs = append(protectedGenIDs, m.RollbackTargetGenerationID)
			}
			if m.LKGGenerationID != "" {
				protectedGenIDs = append(protectedGenIDs, m.LKGGenerationID)
			}
		}
	}

	// Protect generations that are actively being removed by the cleanup journal
	cjData, cjErr := os.ReadFile(c.cleanupJournalFile)
	if cjErr == nil {
		var cj CleanupJournal
		if DecodeJSONStrict(cjData, &cj) == nil {
			for _, file := range cj.Files {
				if strings.HasPrefix(file, "gen-") {
					protectedGenIDs = append(protectedGenIDs, file)
				}
			}
		}
	}

	// DraftJournal and PendingInputRecord do not currently hold GenerationID references.
	// If they are updated to reference generations in the future, parse them here.

	return c.genStore.RunRetentionGC(activeGenID, lkgGenID, protectedGenIDs, keepRecent)
}

func (c *ApplyCoordinator) MutateAndApply(
	ctx context.Context,
	mutateFn func() error,
	compileFn func(ctx context.Context) (*CompileResult, error),
) error {
	_, err := c.ApplyMutationWithOutcome(ctx, mutateFn, compileFn)
	return err
}

func (c *ApplyCoordinator) ApplyMutationWithOutcome(
	ctx context.Context,
	mutateFn func() error,
	compileFn func(ctx context.Context) (*CompileResult, error),
) (*MutationOutcome, error) {
	t0 := time.Now()
	var timing TransactionTiming

	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	timing.LockWait = time.Since(t0)

	ipcLock, err := strictfs.LockTransaction(c.cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("acquire transaction lock: %w", err)
	}
	defer ipcLock.Close()

	if c.State() == StateRecoveryRequired {
		return nil, ErrRecoveryRequired
	}
	if _, err := os.Stat(c.recoveryMarkerFile); err == nil {
		c.setState(StateRecoveryRequired)
		return nil, ErrRecoveryRequired
	}

	if _, err := os.Stat(c.manifestFile); err == nil {
		return nil, ErrTxInProgress
	}

	txid := GenerateTxID()
	c.activeTxID = txid
	timing.TxID = txid
	defer func() { c.activeTxID = "" }()

	var baseAppliedStoreDigest, baseAppliedDesiredStoreDigest, baseAppliedInputDigest, previousGenID string
	var verifiedGeneration uint64

	if c.appliedRecord != nil {
		baseAppliedStoreDigest = c.appliedRecord.AppliedStoreDigest
		baseAppliedDesiredStoreDigest = c.appliedRecord.AppliedDesiredStoreDigest
		baseAppliedInputDigest = c.appliedRecord.AppliedInputDigest
		verifiedGeneration = c.appliedRecord.Generation
		previousGenID = c.appliedRecord.GenerationID
	}

	baseDesiredStoreDigest, dErr := c.cfg.StoreTx.CurrentDesiredDigest()
	if dErr != nil {
		return nil, fmt.Errorf("failed to get current desired store digest: %w", dErr)
	}
	baseSnapshotStoreDigest, dErr := c.cfg.StoreTx.CurrentSnapshotDigest()
	if dErr != nil {
		return nil, fmt.Errorf("failed to get current store snapshot digest: %w", dErr)
	}

	if c.appliedRecord != nil {
		// Legacy records used a single snapshot digest. Upgrade them on the next
		// successful commit without treating metadata-only drift as corruption.
		if baseAppliedDesiredStoreDigest == "" {
			baseAppliedDesiredStoreDigest = baseDesiredStoreDigest
		}
		// The desired store is the editable model. A difference from the last
		// applied desired digest is therefore the normal condition that causes an
		// Apply (for example after adding, deleting or reordering a rule), not
		// evidence of runtime corruption. Runtime drift is guarded independently
		// by the active-config digest below. Keep the applied digest in the
		// manifest as lineage metadata, but build the candidate from the current
		// desired digest.

		actualConfigBytes, err := os.ReadFile(c.activeConfigFile)
		if err == nil {
			actualConfigDigest := strictfs.ComputeBytesDigest(actualConfigBytes)
			if c.appliedRecord.AppliedConfigDigest != actualConfigDigest {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("active config digest mismatch: applied=%s actual=%s", c.appliedRecord.AppliedConfigDigest, actualConfigDigest))
				return nil, fmt.Errorf("preflight active config digest mismatch: applied=%s actual=%s", c.appliedRecord.AppliedConfigDigest, actualConfigDigest)
			}
		}
	}

	lkgPtr, lkgErr := c.genStore.ReadLKGPointer()
	if lkgErr != nil && !errors.Is(lkgErr, os.ErrNotExist) && !os.IsNotExist(lkgErr) {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("read existing LKG pointer failed at transaction start: " + lkgErr.Error())
		return nil, fmt.Errorf("read existing LKG pointer: %w", lkgErr)
	}
	var lkgGenID string
	var lkgPointerDigest string
	if lkgPtr != nil {
		lkgGenID = lkgPtr.GenerationID
		prevBytes, rErr := os.ReadFile(c.genStore.LKGPointerFile())
		if rErr != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("read existing LKG pointer file failed: " + rErr.Error())
			return nil, fmt.Errorf("read existing LKG pointer file: %w", rErr)
		}
		lkgPointerDigest = strictfs.ComputeBytesDigest(prevBytes)
	}

	manifest := TransactionManifest{
		Version:                       1,
		TxID:                          txid,
		OperationKind:                 OperationApply,
		State:                         StateIdle,
		CreatedAt:                     time.Now(),
		UpdatedAt:                     time.Now(),
		PreviousGenerationID:          previousGenID,
		LKGGenerationID:               lkgGenID,
		PreviousLKGGenerationID:       lkgGenID,
		PreviousLKGPointerDigest:      lkgPointerDigest,
		BaseAppliedStoreDigest:        baseAppliedStoreDigest,
		BaseAppliedDesiredStoreDigest: baseAppliedDesiredStoreDigest,
		BaseDesiredStoreDigest:        baseDesiredStoreDigest,
		BaseAppliedInputDigest:        baseAppliedInputDigest,
		VerifiedActiveGeneration:      verifiedGeneration,
	}

	var snapPath string
	if mutateFn != nil {
		var err error
		snapPath, err = c.cfg.StoreTx.SnapshotFilePath(txid)
		if err != nil {
			return nil, fmt.Errorf("resolve pre-mutation snapshot path: %w", err)
		}
		manifest.PreMutationStoreSnapshotFile = snapPath

		// 1. Read pre-mutation bridge snapshot once from StoreTx.ListBridges()
		preMutationSnapshot := c.cfg.StoreTx.ListBridges()

		// 2. Enrich every c.appliedRecord.AppliedBridges entry against that snapshot
		if c.appliedRecord != nil && len(c.appliedRecord.AppliedBridges) > 0 {
			enrichedPrev := make([]BridgeRef, len(c.appliedRecord.AppliedBridges))
			seenPrevSlots := make(map[string]struct{}, len(c.appliedRecord.AppliedBridges))
			for i, b := range c.appliedRecord.AppliedBridges {
				eb, err := c.enrichLegacyBridgeRefLocked(b, preMutationSnapshot)
				if err != nil {
					return nil, fmt.Errorf("enrich legacy previous bridge %s: %w", b.SlotKey(), err)
				}
				if err := eb.ValidateComplete(); err != nil {
					return nil, fmt.Errorf("enriched previous bridge %s invalid: %w", eb.SlotKey(), err)
				}
				slot := eb.SlotKey()
				if _, exists := seenPrevSlots[slot]; exists {
					return nil, fmt.Errorf("duplicate slot key %q in enriched previous bridges", slot)
				}
				seenPrevSlots[slot] = struct{}{}
				enrichedPrev[i] = eb
			}
			// 4. Deep-copy it into manifest.PreviousBridges
			manifest.PreviousBridges = make([]BridgeRef, len(enrichedPrev))
			copy(manifest.PreviousBridges, enrichedPrev)
			manifest.PreviousBridgesDigest = BridgesDigest(manifest.PreviousBridges)
		}

		if err := c.initManifestLocked(&manifest, StatePreSnapshotWriteIntent); err != nil {
			return nil, err
		}

		tPre := time.Now()
		snapshotDigest, err := c.cfg.StoreTx.CreateSnapshotFileAt(txid, snapPath)
		timing.PreSnapshot = time.Since(tPre)
		if err != nil {
			return nil, c.abortPreMutationIntentLocked(&manifest, fmt.Errorf("create pre-mutation store snapshot: %w", err))
		}
		manifest.PreMutationStoreDigest = snapshotDigest

		if snapshotDigest != baseSnapshotStoreDigest {
			return nil, c.abortPreMutationIntentLocked(&manifest,
				fmt.Errorf("store changed while securing snapshot: snapshot preflight=%s snapshot=%s",
					baseSnapshotStoreDigest, snapshotDigest))
		}

		if c.hooks.FailAfterPreSnapshot {
			return nil, ErrSimulatedCrash
		}

		// 5. Persist PreviousBridges together with the secured pre-mutation snapshot
		if err := c.transitionManifestLocked(&manifest, StateSnapshotSecured); err != nil {
			return nil, c.abortEarlyLocked(&manifest, err)
		}

		// 6. Only then invoke mutateFn
		tMut := time.Now()
		mutErr := func() (retErr error) {
			defer func() {
				if r := recover(); r != nil {
					retErr = fmt.Errorf("panic during store mutation: %v", r)
				}
			}()
			return mutateFn()
		}()
		timing.Mutate = time.Since(tMut)

		if mutErr != nil {
			return nil, c.abortEarlyLocked(&manifest, mutErr)
		}

		targetStoreDigest, _ := c.cfg.StoreTx.CurrentDesiredDigest()
		manifest.TargetDesiredStoreDigest = targetStoreDigest
	} else {
		manifest.TargetDesiredStoreDigest = baseDesiredStoreDigest
	}

	tComp := time.Now()
	compileResult, err := compileFn(ctx)
	timing.Compile = time.Since(tComp)
	if err != nil {
		return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("compile mihomo config: %w", err))
	}

	// Detect a concurrent writer that bypassed this coordinator while the
	// candidate was being compiled. This is an ordinary transaction conflict:
	// abort without poisoning the otherwise healthy runtime with a recovery
	// marker.
	compiledDesiredStoreDigest, digestErr := c.cfg.StoreTx.CurrentDesiredDigest()
	if digestErr != nil {
		return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("read desired store digest after compile: %w", digestErr))
	}
	if compiledDesiredStoreDigest != manifest.TargetDesiredStoreDigest {
		return nil, c.abortEarlyLocked(&manifest, fmt.Errorf(
			"desired store changed while compiling candidate: expected=%s actual=%s",
			manifest.TargetDesiredStoreDigest, compiledDesiredStoreDigest))
	}

	manifest.TargetInputDigest = compileResult.InputDigest
	manifest.CandidateConfigDigest = compileResult.ConfigDigest
	manifest.DesiredMode = compileResult.Mode

	// If mutateFn was nil (pure Apply), ensure manifest.PreviousBridges is populated from appliedRecord
	if len(manifest.PreviousBridges) == 0 && c.appliedRecord != nil && len(c.appliedRecord.AppliedBridges) > 0 {
		preSnap := c.cfg.StoreTx.ListBridges()
		enrichedPrev := make([]BridgeRef, len(c.appliedRecord.AppliedBridges))
		seenPrevSlots := make(map[string]struct{}, len(c.appliedRecord.AppliedBridges))
		for i, b := range c.appliedRecord.AppliedBridges {
			eb, err := c.enrichLegacyBridgeRefLocked(b, preSnap)
			if err != nil {
				return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("enrich legacy previous bridge %s: %w", b.SlotKey(), err))
			}
			if err := eb.ValidateComplete(); err != nil {
				return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("enriched previous bridge %s invalid: %w", eb.SlotKey(), err))
			}
			slot := eb.SlotKey()
			if _, exists := seenPrevSlots[slot]; exists {
				return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("duplicate slot key %q in enriched previous bridges", slot))
			}
			seenPrevSlots[slot] = struct{}{}
			enrichedPrev[i] = eb
		}
		manifest.PreviousBridges = enrichedPrev
		manifest.PreviousBridgesDigest = BridgesDigest(manifest.PreviousBridges)
	}

	// 1. Validate all compileResult.TargetBridges as complete
	// 2. Reject duplicate SlotKeys
	seenTargetSlots := make(map[string]struct{}, len(compileResult.TargetBridges))
	targetBridges := make([]BridgeRef, len(compileResult.TargetBridges))
	for i, b := range compileResult.TargetBridges {
		if err := b.ValidateComplete(); err != nil {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("compileResult target bridge %d invalid: %w", i, err))
		}
		slot := b.SlotKey()
		if _, exists := seenTargetSlots[slot]; exists {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("duplicate slot key %q in target bridges", slot))
		}
		seenTargetSlots[slot] = struct{}{}
		targetBridges[i] = b
	}
	// 3. Deep-copy into manifest.TargetBridges
	manifest.TargetBridges = targetBridges
	// 4. Store TargetBridgesDigest in manifest
	manifest.TargetBridgesDigest = BridgesDigest(targetBridges)

	newGen := manifest.VerifiedActiveGeneration + 1
	newGenID := fmt.Sprintf("gen-%06d-%s", newGen, txid)
	manifest.CandidateGenerationID = newGenID

	if compileResult.Mode != RuntimeOff {
		manifest.ConfigPresent = true
		candidatePath := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("config.yaml.candidate.%s", txid))
		manifest.CandidateConfigFile = candidatePath
	} else {
		manifest.ConfigPresent = false
	}

	if mutateFn != nil {
		postSnapPath, err := c.cfg.StoreTx.SnapshotFilePath(txid + "-post")
		if err != nil {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("resolve post-mutation snapshot path: %w", err))
		}
		manifest.CandidatePostMutationStoreSnapshotFile = postSnapPath
	}

	// 5. Checkpoint the manifest before candidate config write/promotion, runtime restart or bridge reconciliation
	if manifest.Sequence == 0 {
		if err := c.initManifestLocked(&manifest, StateCandidateWriteIntent); err != nil {
			return nil, c.abortEarlyLocked(&manifest, err)
		}
	} else {
		if err := c.transitionManifestLocked(&manifest, StateCandidateWriteIntent); err != nil {
			return nil, c.abortEarlyLocked(&manifest, err)
		}
	}
	c.crashAt("after_target_bridges_persist")

	if mutateFn != nil {
		tPost := time.Now()
		postDigest, err := c.cfg.StoreTx.CreateSnapshotFileAt(txid+"-post", manifest.CandidatePostMutationStoreSnapshotFile)
		timing.PostSnapshot = time.Since(tPost)
		if err != nil {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("create post-mutation store snapshot: %w", err))
		}
		manifest.CandidatePostMutationStoreDigest = postDigest
		if c.hooks.FailAfterPostSnapshot {
			return nil, ErrSimulatedCrash
		}
	}
	targetSnapshotStoreDigest := manifest.CandidatePostMutationStoreDigest
	if targetSnapshotStoreDigest == "" {
		targetSnapshotStoreDigest = baseSnapshotStoreDigest
	}

	if compileResult.Mode != RuntimeOff {
		tCand := time.Now()
		if err := strictfs.StrictWriteAtomic(manifest.CandidateConfigFile, compileResult.ConfigYAML, 0600); err != nil {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("write candidate config: %w", err))
		}
		timing.CandidateWrite = time.Since(tCand)
		if c.hooks.FailAfterCandidateWrite {
			return nil, ErrSimulatedCrash
		}
		tVal := time.Now()
		if err := c.cfg.Validator.ValidateConfigFile(ctx, manifest.CandidateConfigFile); err != nil {
			return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("validate candidate config: %w", err))
		}
		timing.Validate = time.Since(tVal)
	}

	if err := c.transitionManifestLocked(&manifest, StateCandidateBuilt); err != nil {
		return nil, c.abortEarlyLocked(&manifest, err)
	}

	newRec := AppliedGenerationRecord{
		Version:                   1,
		BridgeIdentityVersion:     CurrentBridgeIdentityVersion,
		GenerationID:              newGenID,
		Generation:                newGen,
		AppliedStoreDigest:        targetSnapshotStoreDigest,
		AppliedDesiredStoreDigest: manifest.TargetDesiredStoreDigest,
		AppliedConfigDigest:       manifest.CandidateConfigDigest,
		AppliedInputDigest:        manifest.TargetInputDigest,
		AppliedListeners:          compileResult.RequiredListeners,
		AppliedBridges:            compileResult.TargetBridges,
		AppliedBridgesDigest:      manifest.TargetBridgesDigest,
		RuntimeMode:               compileResult.Mode,
		AppliedAt:                 time.Now(),
	}

	if err := c.genStore.PublishStagedBundle(
		newGenID,
		newGen,
		compileResult.ConfigYAML,
		manifest.CandidatePostMutationStoreSnapshotFile,
		newRec,
		c.DaemonEpoch(),
	); err != nil {
		return nil, c.abortEarlyLocked(&manifest, fmt.Errorf("publish LKG generation bundle: %w", err))
	}

	if err := c.transitionManifestLocked(&manifest, StateCandidatePublished); err != nil {
		return nil, c.abortEarlyLocked(&manifest, err)
	}

	tRuntime := time.Now()
	if compileResult.Mode == RuntimeOff {
		manifest.ApplyPath = ApplyPathFullRestart
		timing.Path = ApplyPathFullRestart

		if err := c.transitionManifestLocked(&manifest, StateRuntimeIntent); err != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
		}
		// Safe transactional order: stop runtime first, ensuring previous active config is intact if stop fails
		tStop := time.Now()
		if err := c.stopControlledLocked(ctx, manifest.TxID, "RuntimeOff"); err != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("failed to stop operator for RuntimeOff: %w", err))
		}
		timing.StopWait = time.Since(tStop)
		c.processReceipt = nil

		if c.hooks.FailActiveConfigUnlink {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("failpoint: failed to unlink active config for RuntimeOff"))
		}
		if err := strictfs.StrictUnlink(c.activeConfigFile); err != nil && !os.IsNotExist(err) {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("failed to unlink active config for RuntimeOff: %w", err))
		}
		if statErr := c.statActiveConfigAbsenceLocked(); statErr != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, statErr)
		}
		if err := c.transitionManifestLocked(&manifest, StateRuntimeApplied); err != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
		}
		if err := c.transitionManifestLocked(&manifest, StateRuntimeVerified); err != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
		}
		if err := c.transitionManifestLocked(&manifest, StateBridgesReconciling); err != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
		}
		var beforeBridges []BridgeRef
		if len(manifest.PreviousBridges) > 0 {
			beforeBridges = manifest.PreviousBridges
		} else if c.appliedRecord != nil {
			beforeBridges = c.appliedRecord.AppliedBridges
		}
		targetBridges := manifest.TargetBridges
		if len(targetBridges) == 0 && compileResult != nil {
			targetBridges = compileResult.TargetBridges
		}
		bridgeErr := c.syncBridgesLocked(ctx, &manifest, beforeBridges, targetBridges)
		if bridgeErr != nil {
			return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("bridge sync failed: %w", bridgeErr))
		}
		c.counters.FullRestartCount++
	} else {
		preBytes, _ := os.ReadFile(manifest.PreMutationStoreSnapshotFile)
		postBytes, _ := os.ReadFile(manifest.CandidatePostMutationStoreSnapshotFile)
		activeConfigBytes, _ := os.ReadFile(c.activeConfigFile)
		changeKind := ClassifyMutation(
			preBytes, postBytes,
			activeConfigBytes, compileResult.ConfigYAML,
			c.appliedRecord,
			compileResult,
			c.cfg.Operator,
			manifest.TargetBridgesDigest,
			manifest.PreviousBridgesDigest,
		)

		reloader := c.cfg.ConfigReloader
		if reloader == nil && c.cfg.Operator != nil {
			reloader, _ = c.cfg.Operator.(ConfigReloader)
		}
		running, pid := false, 0
		if c.cfg.Operator != nil {
			running, pid = c.cfg.Operator.IsRunning()
		}

		canHotReload := changeKind == ChangeKindRuleOnly && reloader != nil && running && pid > 0

		if canHotReload {
			manifest.ApplyPath = ApplyPathHotReload
			timing.Path = ApplyPathHotReload

			if err := c.transitionManifestLocked(&manifest, StateSwapIntent); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			outcome, swapErr := strictfs.RenameAndResolve(manifest.CandidateConfigFile, c.activeConfigFile, "", manifest.CandidateConfigDigest)
			if swapErr != nil && outcome != strictfs.OutcomeNewApplied {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("swap candidate to active config: %w", swapErr))
			}

			if err := c.transitionManifestLocked(&manifest, StateSwapApplied); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			if err := c.transitionManifestLocked(&manifest, StateSwapVerified); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}

			if err := c.transitionManifestLocked(&manifest, StateRuntimeIntent); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}

			tReload := time.Now()
			reloadErr := c.reloadControlledLocked(ctx, manifest.TxID, newGen, manifest.CandidateConfigDigest, compileResult.RequiredListeners)
			timing.ReloadWait = time.Since(tReload)
			if reloadErr != nil {
				tRb := time.Now()
				hotRbErr := c.hotRollbackControlledLocked(ctx, &manifest)
				timing.Rollback = time.Since(tRb)
				if hotRbErr != nil {
					c.counters.RollbackCount++
					return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("hot reload failed (%v) and hot rollback failed (%v)", reloadErr, hotRbErr))
				}
				c.counters.RollbackCount++
				return nil, fmt.Errorf("runtime reload rejected: %w (hot rollback succeeded)", reloadErr)
			}

			if err := c.transitionManifestLocked(&manifest, StateRuntimeApplied); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			if err := c.transitionManifestLocked(&manifest, StateRuntimeVerified); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}

			// Fast path: bridge reconciliation is NOT needed for RuleOnly!
			c.counters.HotReloadCount++
		} else {
			manifest.ApplyPath = ApplyPathFullRestart
			timing.Path = ApplyPathFullRestart

			if err := c.transitionManifestLocked(&manifest, StateSwapIntent); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			outcome, swapErr := strictfs.RenameAndResolve(manifest.CandidateConfigFile, c.activeConfigFile, "", manifest.CandidateConfigDigest)
			if swapErr != nil && outcome != strictfs.OutcomeNewApplied {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("swap candidate to active config: %w", swapErr))
			}

			if err := c.transitionManifestLocked(&manifest, StateSwapApplied); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			if err := c.transitionManifestLocked(&manifest, StateSwapVerified); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}

			if err := c.transitionManifestLocked(&manifest, StateRuntimeIntent); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			tRestart := time.Now()
			restartErr := c.restartControlledLocked(ctx, manifest.TxID, newGen, manifest.CandidateConfigDigest, compileResult.RequiredListeners)
			timing.StartWait = time.Since(tRestart)
			if restartErr != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("runtime restart failed: %w", restartErr))
			}
			if err := c.transitionManifestLocked(&manifest, StateRuntimeApplied); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			if err := c.transitionManifestLocked(&manifest, StateRuntimeVerified); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}

			if err := c.transitionManifestLocked(&manifest, StateBridgesReconciling); err != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
			}
			var beforeBridges []BridgeRef
			if len(manifest.PreviousBridges) > 0 {
				beforeBridges = manifest.PreviousBridges
			} else if c.appliedRecord != nil {
				beforeBridges = c.appliedRecord.AppliedBridges
			}
			targetBridges := manifest.TargetBridges
			if len(targetBridges) == 0 && compileResult != nil {
				targetBridges = compileResult.TargetBridges
			}
			bridgeErr := c.syncBridgesLocked(ctx, &manifest, beforeBridges, targetBridges)
			if bridgeErr != nil {
				return nil, c.handleApplyFailureLocked(ctx, &manifest, fmt.Errorf("bridge sync failed: %w", bridgeErr))
			}
			c.counters.FullRestartCount++
		}
	}
	timing.RuntimeApply = time.Since(tRuntime)

	tCommit := time.Now()
	if err := c.transitionManifestLocked(&manifest, StateCommitIntent); err != nil {
		return nil, c.handleApplyFailureLocked(ctx, &manifest, err)
	}

	rec := AppliedGenerationRecord{
		Version:                   1,
		BridgeIdentityVersion:     CurrentBridgeIdentityVersion,
		Generation:                newGen,
		GenerationID:              newGenID,
		AppliedAt:                 time.Now(),
		AppliedStoreDigest:        targetSnapshotStoreDigest,
		AppliedDesiredStoreDigest: manifest.TargetDesiredStoreDigest,
		AppliedConfigDigest:       manifest.CandidateConfigDigest,
		AppliedInputDigest:        manifest.TargetInputDigest,
		AppliedListeners:          compileResult.RequiredListeners,
		AppliedBridges:            manifest.TargetBridges,
		AppliedBridgesDigest:      manifest.TargetBridgesDigest,
		RuntimeMode:               compileResult.Mode,
		ApplyPath:                 manifest.ApplyPath,
		ProcessReceipt:            c.processReceipt,
	}

	commitErr := c.executeFinalCommitLocked(ctx, &manifest, rec)
	if commitErr != nil {
		return nil, commitErr
	}
	timing.Commit = time.Since(tCommit)
	timing.TotalDuration = time.Since(t0)
	c.lastTiming = &timing

	return &MutationOutcome{
		TxID:         txid,
		ApplyPath:    manifest.ApplyPath,
		Generation:   newGen,
		Timing:       &timing,
		ServerTiming: timing.ServerTimingHeader(),
	}, nil
}

func (c *ApplyCoordinator) handleApplyFailureLocked(ctx context.Context, m *TransactionManifest, origErr error) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	rollbackErr := c.rollbackActiveLocked(rollbackCtx, m)

	var cleanupErr error
	if rollbackErr == nil && m.State != StateRecoveryRequired {
		cleanupErr = c.cleanupTxArtifactsLocked(m)
	}

	if rollbackErr != nil || cleanupErr != nil {
		c.setState(StateRecoveryRequired)
		markerMsg := fmt.Sprintf("apply failed (%v), recovery failed (rollback: %v, cleanup: %v)", origErr, rollbackErr, cleanupErr)
		if m != nil && m.TxID != "" {
			markerMsg = fmt.Sprintf("[%s] %s", m.TxID, markerMsg)
		}
		_ = c.writeRecoveryMarkerIfAbsentLocked(markerMsg)
		var errs []error
		errs = append(errs, origErr)
		if rollbackErr != nil {
			errs = append(errs, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		if cleanupErr != nil {
			errs = append(errs, fmt.Errorf("cleanup failed: %w", cleanupErr))
		}
		return errors.Join(errs...)
	}

	if m.State == StateRecoveryRequired || c.state == StateRecoveryRequired {
		c.setState(StateRecoveryRequired)
		return errors.Join(origErr, fmt.Errorf("rollback required manual recovery"))
	}

	c.setState(StateIdle)
	return origErr
}

func (c *ApplyCoordinator) stopControlledLocked(ctx context.Context, txID string, reason string) error {
	if c.cfg.Operator == nil {
		return nil
	}
	running, pid := c.cfg.Operator.IsRunning()
	if !running && pid == 0 {
		return nil
	}
	if err := c.cfg.Operator.StopAndWait(ctx); err != nil {
		markerMsg := fmt.Sprintf("%s: stop operator failed: %v", reason, err)
		if txID != "" {
			markerMsg = fmt.Sprintf("[%s] %s", txID, markerMsg)
		}
		c.setState(StateRecoveryRequired)
		writeErr := c.writeRecoveryMarkerLocked(markerMsg)
		return errors.Join(fmt.Errorf("%s: %w", reason, err), writeErr)
	}
	if stillRunning, stillPid := c.cfg.Operator.IsRunning(); stillRunning || stillPid != 0 {
		err := fmt.Errorf("%w: operator still running (running=%v, PID %d) after stop in %s", ErrProcessNotReaped, stillRunning, stillPid, reason)
		markerMsg := err.Error()
		if txID != "" {
			markerMsg = fmt.Sprintf("[%s] %s", txID, markerMsg)
		}
		c.setState(StateRecoveryRequired)
		writeErr := c.writeRecoveryMarkerLocked(markerMsg)
		return errors.Join(err, writeErr)
	}
	return nil
}

func (c *ApplyCoordinator) restartControlledLocked(ctx context.Context, txID string, targetGeneration uint64, expectedDigest string, listeners []ListenerSpec) error {
	actDigest, err := strictfs.ComputeFileDigest(c.activeConfigFile)
	if err != nil || actDigest != expectedDigest {
		return fmt.Errorf("active config digest mismatch before spawn: got %s, want %s", actDigest, expectedDigest)
	}
	if err := c.stopControlledLocked(ctx, txID, "restart"); err != nil {
		return fmt.Errorf("stop running mihomo: %w", err)
	}
	ensureRulesDir(c.cfg.ConfigDir)
	if err := c.cfg.Operator.Start(); err != nil {
		return fmt.Errorf("start fresh mihomo: %w", err)
	}
	postDigest, err := strictfs.ComputeFileDigest(c.activeConfigFile)
	if err != nil || postDigest != expectedDigest {
		return fmt.Errorf("active config modified during spawn: got %s, want %s", postDigest, expectedDigest)
	}

	running, pid := c.cfg.Operator.IsRunning()
	if !running || pid <= 0 {
		return fmt.Errorf("operator reported not running after start (pid=%d)", pid)
	}

	binPath := c.cfg.ConfigDir
	if opBin, ok := c.cfg.Operator.(interface{ Binary() string }); ok {
		binPath = opBin.Binary()
	}

	ident, err := c.verifier.CaptureIdentity("", pid, binPath, c.cfg.ConfigDir, targetGeneration)
	if err != nil {
		return fmt.Errorf("capture process identity: %w", err)
	}

	for _, l := range listeners {
		if err := c.verifier.VerifySocketOwnership("", l.Address, int(l.Port), l.Network, pid); err != nil {
			return fmt.Errorf("verify required listener %s:%d: %w", l.Address, l.Port, err)
		}
	}

	c.processReceipt = &ProcessReceipt{
		RuntimeProcessIdentity: ident,
		DaemonEpoch:            c.DaemonEpoch(),
		AppliedGeneration:      targetGeneration,
		VerifiedAt:             time.Now(),
	}

	return nil
}

func (c *ApplyCoordinator) reloadControlledLocked(ctx context.Context, txID string, targetGeneration uint64, expectedDigest string, listeners []ListenerSpec) error {
	if c.hooks.FailHotReload {
		return errors.New("failpoint: simulated hot reload failure")
	}

	actDigest, err := strictfs.ComputeFileDigest(c.activeConfigFile)
	if err != nil || actDigest != expectedDigest {
		return fmt.Errorf("active config digest mismatch before reload: got %s, want %s", actDigest, expectedDigest)
	}

	preRunning, prePID := c.cfg.Operator.IsRunning()
	if !preRunning || prePID <= 0 {
		return fmt.Errorf("operator reported not running before reload (pid=%d)", prePID)
	}

	reloader := c.cfg.ConfigReloader
	if reloader == nil && c.cfg.Operator != nil {
		reloader, _ = c.cfg.Operator.(ConfigReloader)
	}
	if reloader == nil {
		return fmt.Errorf("operator does not implement ConfigReloader")
	}

	reloadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := reloader.ReloadConfig(reloadCtx, c.activeConfigFile, true); err != nil {
		return fmt.Errorf("controller reload failed: %w", err)
	}

	postDigest, err := strictfs.ComputeFileDigest(c.activeConfigFile)
	if err != nil || postDigest != expectedDigest {
		return fmt.Errorf("active config modified during reload: got %s, want %s", postDigest, expectedDigest)
	}

	postRunning, postPID := c.cfg.Operator.IsRunning()
	if !postRunning || postPID != prePID {
		return fmt.Errorf("process identity changed during reload: was pid %d, now running=%v pid=%d", prePID, postRunning, postPID)
	}

	binPath := c.cfg.ConfigDir
	if opBin, ok := c.cfg.Operator.(interface{ Binary() string }); ok {
		binPath = opBin.Binary()
	}

	ident, err := c.verifier.CaptureIdentity("", postPID, binPath, c.cfg.ConfigDir, targetGeneration)
	if err != nil {
		return fmt.Errorf("capture process identity after reload: %w", err)
	}

	for _, l := range listeners {
		netw := l.Network
		if netw == "" {
			netw = l.GetNetwork()
		}
		if err := c.verifier.VerifySocketOwnership("", l.Address, int(l.Port), netw, postPID); err != nil {
			return fmt.Errorf("verify required listener %s:%d after reload: %w", l.Address, l.Port, err)
		}
	}

	c.processReceipt = &ProcessReceipt{
		RuntimeProcessIdentity: ident,
		DaemonEpoch:            c.DaemonEpoch(),
		AppliedGeneration:      targetGeneration,
		VerifiedAt:             time.Now(),
	}

	return nil
}

func (c *ApplyCoordinator) hotRollbackControlledLocked(ctx context.Context, m *TransactionManifest) error {
	if c.hooks.FailHotRollback {
		return errors.New("failpoint: simulated hot rollback failure")
	}

	reloader := c.cfg.ConfigReloader
	if reloader == nil && c.cfg.Operator != nil {
		reloader, _ = c.cfg.Operator.(ConfigReloader)
	}
	if reloader == nil {
		return errors.New("reloader not available for hot rollback")
	}

	running, pid := c.cfg.Operator.IsRunning()
	if !running || pid <= 0 {
		return errors.New("process not running for hot rollback")
	}

	// 1. Restore previous config
	if m.LKGGenerationID != "" {
		_, prevConfigPath, _, err := c.genStore.ReadGenerationBundle(m.LKGGenerationID)
		if err != nil {
			return fmt.Errorf("read LKG bundle for hot rollback: %w", err)
		}
		data, err := os.ReadFile(prevConfigPath)
		if err != nil {
			return fmt.Errorf("read LKG config for hot rollback: %w", err)
		}
		if err := strictfs.StrictWriteAtomic(c.activeConfigFile, data, 0600); err != nil {
			return fmt.Errorf("restore active config for hot rollback: %w", err)
		}
	} else {
		return errors.New("no LKG generation for hot rollback")
	}

	// 2. Hot reload previous config
	rbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := reloader.ReloadConfig(rbCtx, c.activeConfigFile, true); err != nil {
		return fmt.Errorf("hot reload of previous config failed: %w", err)
	}

	// 3. Verify process PID remained intact
	postRunning, postPID := c.cfg.Operator.IsRunning()
	if !postRunning || postPID != pid {
		return fmt.Errorf("process identity lost during hot rollback (was %d, now running=%v pid=%d)", pid, postRunning, postPID)
	}

	// 4. Restore store snapshot
	if m.PreMutationStoreSnapshotFile != "" {
		if err := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); err != nil {
			return fmt.Errorf("restore pre-mutation store snapshot: %w", err)
		}
	}

	// 5. Cleanup candidate artifacts
	if m.CandidateConfigFile != "" {
		_ = strictfs.StrictUnlink(m.CandidateConfigFile)
	}
	if m.CandidatePostMutationStoreSnapshotFile != "" {
		_ = c.cfg.StoreTx.RemoveSnapshotFile(m.CandidatePostMutationStoreSnapshotFile)
	}

	m.State = StateIdle
	_ = c.cleanupTxArtifactsLocked(m)
	return nil
}

// verifyBridgeWithdrawAllowedLocked enforces strict fail-closed foreign bridge inspection and ownership verification.
// An error during bridge inspection halts reconciliation fail-closed.
// A bridge can only be withdrawn if proven owned by AWGM via exact matching OwnerUUID or explicit allowed legacy owner.
func (c *ApplyCoordinator) verifyBridgeWithdrawAllowedLocked(ctx context.Context, ref BridgeRef) error {
	exactRuntime, isExact := c.cfg.BridgeRuntime.(ExactBridgeRuntime)
	if !isExact {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge runtime %T does not implement ExactBridgeRuntime: ownership cannot be proven", c.cfg.BridgeRuntime))
		return fmt.Errorf("%w: bridge runtime %T does not implement ExactBridgeRuntime: ownership cannot be proven", ErrForeignBridgeOwnership, c.cfg.BridgeRuntime)
	}

	obs, obsErr := exactRuntime.InspectBridge(ctx, ref)
	if obsErr != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge inspection failed on %s: %v", ref.KernelInterface, obsErr))
		return fmt.Errorf("%w: inspecting bridge %s: %v", ErrForeignBridgeOwnership, ref.KernelInterface, obsErr)
	}
	if !obs.Exists {
		return nil
	}

	if obs.OwnerUUID != "" {
		if ref.OwnerUUID == "" || obs.OwnerUUID != ref.OwnerUUID {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign bridge ownership conflict on %s: owned by %q, want %q", ref.KernelInterface, obs.OwnerUUID, ref.OwnerUUID))
			return fmt.Errorf("%w: refusing to withdraw foreign bridge %s: owned by %q (expected %q)", ErrForeignBridgeOwnership, ref.KernelInterface, obs.OwnerUUID, ref.OwnerUUID)
		}
		return nil
	}

	if obs.LegacyOwner != "" {
		isLegacyAllowed := (obs.LegacyOwner == "awg-manager" || obs.LegacyOwner == "awgm")
		if ref.LegacyOwner != "" && obs.LegacyOwner == ref.LegacyOwner {
			isLegacyAllowed = true
		}
		if !isLegacyAllowed {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign bridge legacy owner conflict on %s: legacy owner %q not recognized", ref.KernelInterface, obs.LegacyOwner))
			return fmt.Errorf("%w: refusing to withdraw foreign bridge %s: unrecognized legacy owner %q", ErrForeignBridgeOwnership, ref.KernelInterface, obs.LegacyOwner)
		}
		return nil
	}

	c.setState(StateRecoveryRequired)
	_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("unmanaged bridge detected on %s: no verified ownership", ref.KernelInterface))
	return fmt.Errorf("%w: refusing to withdraw unmanaged bridge %s: no verified ownership in OS", ErrForeignBridgeOwnership, ref.KernelInterface)
}

// verifyBridgePublishAllowedLocked enforces strict fail-closed foreign bridge inspection and ownership verification
// prior to publishing/creating a bridge. If a bridge already exists in the OS (obs.Exists), publishing to it is
// only allowed if exact ownership by AWGM is proven (matching OwnerUUID or recognized LegacyOwner).
// If the bridge runtime does not support exact inspection, or if the bridge is foreign, unmanaged,
// or has an unrecognized legacy owner, or if InspectBridge fails,
// it halts fail-closed with ErrForeignBridgeOwnership and StateRecoveryRequired without mutating the OS.
func (c *ApplyCoordinator) verifyBridgePublishAllowedLocked(ctx context.Context, ref BridgeRef) error {
	exactRuntime, isExact := c.cfg.BridgeRuntime.(ExactBridgeRuntime)
	if !isExact {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge runtime %T does not implement ExactBridgeRuntime: ownership cannot be proven", c.cfg.BridgeRuntime))
		return fmt.Errorf("%w: bridge runtime %T does not implement ExactBridgeRuntime: ownership cannot be proven", ErrForeignBridgeOwnership, c.cfg.BridgeRuntime)
	}

	obs, obsErr := exactRuntime.InspectBridge(ctx, ref)
	if obsErr != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge inspection failed on %s: %v", ref.KernelInterface, obsErr))
		return fmt.Errorf("%w: inspecting bridge %s: %v", ErrForeignBridgeOwnership, ref.KernelInterface, obsErr)
	}
	if !obs.Exists {
		return nil
	}

	if obs.OwnerUUID != "" {
		if ref.OwnerUUID == "" || obs.OwnerUUID != ref.OwnerUUID {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign bridge ownership conflict on %s: owned by %q, want %q", ref.KernelInterface, obs.OwnerUUID, ref.OwnerUUID))
			return fmt.Errorf("%w: refusing to publish to foreign bridge %s: owned by %q (expected %q)", ErrForeignBridgeOwnership, ref.KernelInterface, obs.OwnerUUID, ref.OwnerUUID)
		}
		return nil
	}

	if obs.LegacyOwner != "" {
		isLegacyAllowed := (obs.LegacyOwner == "awg-manager" || obs.LegacyOwner == "awgm")
		if ref.LegacyOwner != "" && obs.LegacyOwner == ref.LegacyOwner {
			isLegacyAllowed = true
		}
		if !isLegacyAllowed {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign bridge legacy owner conflict on %s: legacy owner %q not recognized", ref.KernelInterface, obs.LegacyOwner))
			return fmt.Errorf("%w: refusing to publish to foreign bridge %s: unrecognized legacy owner %q", ErrForeignBridgeOwnership, ref.KernelInterface, obs.LegacyOwner)
		}
		return nil
	}

	c.setState(StateRecoveryRequired)
	_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("unmanaged bridge detected on %s: refusing to overwrite existing unmanaged bridge", ref.KernelInterface))
	return fmt.Errorf("%w: cannot create bridge %s: already exists without verified ownership in OS", ErrForeignBridgeOwnership, ref.KernelInterface)
}

func (c *ApplyCoordinator) syncBridgesLocked(ctx context.Context, m *TransactionManifest, before, target []BridgeRef) error {
	if len(before) == 0 && len(target) == 0 {
		return nil
	}
	if c.cfg.BridgeRuntime == nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("bridge runtime is nil but bridges are present")
		return fmt.Errorf("%w: bridge runtime is nil", ErrForeignBridgeOwnership)
	}
	exactRuntime, isExact := c.cfg.BridgeRuntime.(ExactBridgeRuntime)
	if !isExact {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge runtime %T does not implement ExactBridgeRuntime", c.cfg.BridgeRuntime))
		return fmt.Errorf("%w: bridge runtime %T does not implement ExactBridgeRuntime", ErrForeignBridgeOwnership, c.cfg.BridgeRuntime)
	}

	beforeMap := make(map[string]BridgeRef, len(before))
	for _, b := range before {
		slot := b.SlotKey()
		if _, exists := beforeMap[slot]; exists {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("duplicate slot key %q in before bridges", slot))
			return fmt.Errorf("%w: duplicate slot key %q in before bridges", ErrForeignBridgeOwnership, slot)
		}
		beforeMap[slot] = b
	}

	targetMap := make(map[string]BridgeRef, len(target))
	for _, b := range target {
		slot := b.SlotKey()
		if _, exists := targetMap[slot]; exists {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("duplicate slot key %q in target bridges", slot))
			return fmt.Errorf("%w: duplicate slot key %q in target bridges", ErrForeignBridgeOwnership, slot)
		}
		targetMap[slot] = b
	}

	if m.BridgeOperations == nil {
		m.BridgeOperations = []BridgeOperation{}
	}

	var toCreate []BridgeRef
	var toWithdraw []BridgeRef

	// 1. Process target bridges: pure creations, retained bridges, and replacement targets
	for slot, targetRef := range targetMap {
		oldRef, hasOld := beforeMap[slot]
		if !hasOld {
			// Pure creation: only in target
			if err := c.verifyBridgePublishAllowedLocked(ctx, targetRef); err != nil {
				return err
			}
			toCreate = append(toCreate, targetRef)
			continue
		}

		// Present in both: check if retained (SameOwnerAndEndpoint)
		if oldRef.SameOwnerAndEndpoint(targetRef) {
			// Retained bridge inspection & safe self-heal:
			obs, obsErr := exactRuntime.InspectBridge(ctx, targetRef)
			if obsErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("retained bridge inspection failed on %s: %v", targetRef.KernelInterface, obsErr))
				return fmt.Errorf("%w: inspecting retained bridge %s: %v", ErrForeignBridgeOwnership, targetRef.KernelInterface, obsErr)
			}
			if !obs.Exists {
				// Missing own bridge in NDMS! Safely self-heal via durable create intent
				if err := c.verifyBridgePublishAllowedLocked(ctx, targetRef); err != nil {
					return err
				}
				toCreate = append(toCreate, targetRef)
			} else {
				if obs.OwnerUUID != "" && obs.OwnerUUID != targetRef.OwnerUUID {
					if m.OperationKind == OperationRegenerate && strings.HasPrefix(obs.OwnerUUID, "awg-manager:mihomo:") {
						c.log("warn", "coordinator.reconcile", fmt.Sprintf("stale mihomo bridge in slot %s during regeneration (owned by %s): self-healing ownership to %s", targetRef.KernelInterface, obs.OwnerUUID, targetRef.OwnerUUID))
						toCreate = append(toCreate, targetRef)
					} else {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign retained bridge conflict on %s: owned by %q, want %q", targetRef.KernelInterface, obs.OwnerUUID, targetRef.OwnerUUID))
						return fmt.Errorf("%w: foreign retained bridge %s: owned by %q (expected %q)", ErrForeignBridgeOwnership, targetRef.KernelInterface, obs.OwnerUUID, targetRef.OwnerUUID)
					}
				}
				if obs.OwnerUUID == "" && obs.LegacyOwner == "" {
					if m.OperationKind == OperationRegenerate {
						c.log("warn", "coordinator.reconcile", fmt.Sprintf("unlabelled bridge in own slot %s during regeneration: self-healing ownership to %s", targetRef.KernelInterface, targetRef.OwnerUUID))
						toCreate = append(toCreate, targetRef)
					} else {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("unmanaged retained bridge detected on %s", targetRef.KernelInterface))
						return fmt.Errorf("%w: unmanaged retained bridge detected on %s", ErrForeignBridgeOwnership, targetRef.KernelInterface)
					}
				}
				if obs.LegacyOwner != "" {
					// Legacy owner present: queue create to migrate to canonical owner
					if err := c.verifyBridgePublishAllowedLocked(ctx, targetRef); err != nil {
						return err
					}
					toCreate = append(toCreate, targetRef)
				}
				// Canonical match: no mutation required
			}
		} else {
			// Controlled replacement: in both, but owner or listen port differs!
			obs, obsErr := exactRuntime.InspectBridge(ctx, oldRef)
			if obsErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("replacement bridge inspection failed on %s: %v", oldRef.KernelInterface, obsErr))
				return fmt.Errorf("%w: inspecting replacement bridge %s: %v", ErrForeignBridgeOwnership, oldRef.KernelInterface, obsErr)
			}
			if obs.Exists {
				if oldRef.OwnerUUID != targetRef.OwnerUUID && obs.OwnerUUID != "" && obs.OwnerUUID == targetRef.OwnerUUID {
					// Live bridge already has target owner (e.g. crash after publish).
					// Withdrawal of oldRef is already complete.
					opIDA := fmt.Sprintf("%s-%s-%s-%s", m.TxID, "withdraw", oldRef.SlotKey(), oldRef.Digest())
					legacyOpIDA := fmt.Sprintf("%s-%s-%s", m.TxID, "withdraw", oldRef.KernelInterface)
					for i, op := range m.BridgeOperations {
						if (op.OperationID == opIDA || op.OperationID == legacyOpIDA) && op.State != BridgeOpVerified {
							op.State = BridgeOpVerified
							m.BridgeOperations[i] = op
							_ = c.transitionManifestLocked(m, m.State)
							break
						}
					}
					toCreate = append(toCreate, targetRef)
				} else if obs.OwnerUUID == oldRef.OwnerUUID || (obs.LegacyOwner != "" && obs.OwnerUUID == "") {
					// Live bridge belongs to oldRef. Must withdraw oldRef first, then publish targetRef.
					toWithdraw = append(toWithdraw, oldRef)
					toCreate = append(toCreate, targetRef)
				} else {
					// Foreign or unmanaged occupant on this slot
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("foreign/unmanaged occupant in slot %s during replacement: owner=%q legacy=%q", slot, obs.OwnerUUID, obs.LegacyOwner))
					return fmt.Errorf("%w: foreign or unmanaged bridge occupant in slot %s", ErrForeignBridgeOwnership, slot)
				}
			} else {
				// Old bridge is already absent. We can safely withdraw oldRef (idempotent) and publish targetRef.
				toWithdraw = append(toWithdraw, oldRef)
				toCreate = append(toCreate, targetRef)
			}
		}
	}

	// 2. Process pure withdrawals: only in before
	for _, oldRef := range beforeMap {
		if _, hasTarget := targetMap[oldRef.SlotKey()]; !hasTarget {
			if err := c.verifyBridgeWithdrawAllowedLocked(ctx, oldRef); err != nil {
				return err
			}
			toWithdraw = append(toWithdraw, oldRef)
		}
	}

	// Sort deterministically by slot key, then digest
	sort.SliceStable(toWithdraw, func(i, j int) bool {
		if toWithdraw[i].SlotKey() != toWithdraw[j].SlotKey() {
			return toWithdraw[i].SlotKey() < toWithdraw[j].SlotKey()
		}
		return toWithdraw[i].Digest() < toWithdraw[j].Digest()
	})

	sort.SliceStable(toCreate, func(i, j int) bool {
		if toCreate[i].SlotKey() != toCreate[j].SlotKey() {
			return toCreate[i].SlotKey() < toCreate[j].SlotKey()
		}
		return toCreate[i].Digest() < toCreate[j].Digest()
	})

	trackOp := func(ref BridgeRef, action string) error {
		if ref.OwnerUUID == "" {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge %s has empty OwnerUUID; mutation forbidden", ref.KernelInterface))
			return fmt.Errorf("%w: bridge %s has empty OwnerUUID; mutation forbidden", ErrForeignBridgeOwnership, ref.KernelInterface)
		}

		opID := fmt.Sprintf("%s-%s-%s-%s", m.TxID, action, ref.SlotKey(), ref.Digest())
		legacyOpID := fmt.Sprintf("%s-%s-%s", m.TxID, action, ref.KernelInterface)
		opIndex := -1
		for i, op := range m.BridgeOperations {
			if op.OperationID == opID || op.OperationID == legacyOpID {
				opIndex = i
				break
			}
		}

		var op BridgeOperation
		if opIndex >= 0 {
			op = m.BridgeOperations[opIndex]
			// Check if this is a legacy operation record
			if op.TargetDigest == "" || op.BridgeRef.IsLegacy() {
				// Explicit legacy migration path: enrich only when exactly one complete identity is provable
				if c.cfg.StoreTx == nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("unprovable legacy bridge operation %s: storeTx is nil", op.OperationID))
					return fmt.Errorf("%w: unprovable legacy bridge operation %s: storeTx is nil", ErrForeignBridgeOwnership, op.OperationID)
				}
				storeSnap := c.cfg.StoreTx.ListBridges()
				eb, err := c.enrichLegacyBridgeRefLocked(op.BridgeRef, storeSnap)
				if err != nil || eb.ValidateComplete() != nil || eb.Digest() != ref.Digest() {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("unprovable legacy bridge operation %s: enrichment failed (%v)", op.OperationID, err))
					return fmt.Errorf("%w: unprovable legacy bridge operation %s", ErrForeignBridgeOwnership, op.OperationID)
				}
				// Checkpoint the upgraded operation before executing/replaying it
				op.BridgeRef = eb
				op.TargetDigest = eb.Digest()
				op.OperationID = opID
				m.BridgeOperations[opIndex] = op
				if err := c.transitionManifestLocked(m, m.State); err != nil {
					return err
				}
			} else {
				// New-schema bridge operation: validate complete identity
				if op.TargetDigest != ref.Digest() ||
					op.BridgeRef.ValidateComplete() != nil ||
					op.BridgeRef.Digest() != ref.Digest() {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("corrupt bridge operation journal for %s: identity mismatch (got %s, want %s)", opID, op.TargetDigest, ref.Digest()))
					return fmt.Errorf("%w: corrupt bridge operation journal %s: identity mismatch", ErrForeignBridgeOwnership, opID)
				}
			}
		} else {
			op = BridgeOperation{
				OperationID:  opID,
				Action:       action,
				TargetDigest: ref.Digest(),
				BridgeRef:    ref,
				State:        BridgeOpIntent,
				Attempts:     0,
			}
			m.BridgeOperations = append(m.BridgeOperations, op)
			opIndex = len(m.BridgeOperations) - 1
		}

		if op.State == BridgeOpVerified {
			return nil
		}

		var err error
		if op.State != BridgeOpApplied {
			op.State = BridgeOpIntent
			op.Attempts++
			m.BridgeOperations[opIndex] = op
			if err := c.transitionManifestLocked(m, m.State); err != nil {
				return fmt.Errorf("persist intent %s: %w", action, err)
			}

			c.crashAt("before_bridge_" + action)

			if action == "create" {
				if err := c.verifyBridgePublishAllowedLocked(ctx, ref); err != nil {
					return err
				}
				err = exactRuntime.PublishBridge(ctx, ref)
			} else {
				if err := c.verifyBridgeWithdrawAllowedLocked(ctx, ref); err != nil {
					return err
				}
				err = exactRuntime.WithdrawBridge(ctx, ref)
			}

			if err != nil {
				op.LastError = err.Error()
				m.BridgeOperations[opIndex] = op
				if cpErr := c.transitionManifestLocked(m, m.State); cpErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("%s bridge %s failed (%v), and checkpoint also failed: %v", action, ref.KernelInterface, err, cpErr))
					return fmt.Errorf("%s bridge %s failed (%w), and checkpoint also failed: %v", action, ref.KernelInterface, err, cpErr)
				}
				return fmt.Errorf("%s bridge %s: %w", action, ref.KernelInterface, err)
			}

			c.crashAt("after_bridge_" + action + "_before_checkpoint")

			op.State = BridgeOpApplied
			m.BridgeOperations[opIndex] = op
			if c.hooks.FailManifestPersistBridgeOpState == BridgeOpApplied {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge %s side-effect succeeded but checkpoint failed: failpoint", ref.KernelInterface))
				return fmt.Errorf("bridge %s side-effect succeeded but checkpoint failed: failpoint", ref.KernelInterface)
			}
			if cpErr := c.transitionManifestLocked(m, m.State); cpErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge %s side-effect succeeded but checkpoint failed: %v", ref.KernelInterface, cpErr))
				return fmt.Errorf("bridge %s side-effect succeeded but checkpoint failed: %w", ref.KernelInterface, cpErr)
			}

			c.crashAt("after_bridge_" + action + "_checkpoint")
		}

		// Verification phase (READ-ONLY)
		if action == "create" {
			obs, obsErr := exactRuntime.InspectBridge(ctx, ref)
			if obsErr != nil {
				err = obsErr
			} else if !obs.Exists {
				err = fmt.Errorf("bridge %s create postcondition failed: bridge does not exist", ref.KernelInterface)
			} else if ref.OwnerUUID != "" && obs.OwnerUUID != ref.OwnerUUID {
				err = fmt.Errorf("%w: bridge %s create postcondition failed: owner mismatch: got %q, want %q", ErrForeignBridgeOwnership, ref.KernelInterface, obs.OwnerUUID, ref.OwnerUUID)
			} else if obs.LegacyOwner != "" {
				err = fmt.Errorf("%w: bridge %s create postcondition failed: legacy owner not migrated: %q", ErrForeignBridgeOwnership, ref.KernelInterface, obs.LegacyOwner)
			}
		} else {
			obs, obsErr := exactRuntime.InspectBridge(ctx, ref)
			if obsErr != nil {
				err = obsErr
			} else if obs.Exists {
				err = fmt.Errorf("bridge %s withdraw postcondition failed: bridge still exists", ref.KernelInterface)
			}
		}

		if err != nil {
			op.LastError = err.Error()
			m.BridgeOperations[opIndex] = op
			if errors.Is(err, ErrForeignBridgeOwnership) {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(err.Error())
			}
			if cpErr := c.transitionManifestLocked(m, m.State); cpErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("verify %s bridge %s failed (%v), and checkpoint also failed: %v", action, ref.KernelInterface, err, cpErr))
				return fmt.Errorf("verify %s bridge %s failed (%w), and checkpoint also failed: %v", action, ref.KernelInterface, err, cpErr)
			}
			return fmt.Errorf("verify %s bridge %s: %w", action, ref.KernelInterface, err)
		}

		c.crashAt("after_bridge_" + action + "_verified_before_checkpoint")

		op.State = BridgeOpVerified
		m.BridgeOperations[opIndex] = op
		if cpErr := c.transitionManifestLocked(m, m.State); cpErr != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("bridge %s verified but checkpoint failed: %v", ref.KernelInterface, cpErr))
			return fmt.Errorf("bridge %s verified but checkpoint failed: %w", ref.KernelInterface, cpErr)
		}

		c.crashAt("after_bridge_" + action + "_verified_checkpoint")

		return nil
	}

	// Execution order: ALL withdrawals FIRST, ALL creations SECOND
	for _, b := range toWithdraw {
		if err := trackOp(b, "withdraw"); err != nil {
			return err
		}
	}

	for _, b := range toCreate {
		if err := trackOp(b, "create"); err != nil {
			return err
		}
	}

	return nil
}

func (c *ApplyCoordinator) rollbackActiveLocked(ctx context.Context, m *TransactionManifest) error {
	c.log("info", "coordinator.rollback", "Starting full state rollback to pre-transaction snapshot")

	originalState := m.State
	if err := c.transitionManifestLocked(m, StateRollbackInProgress); err != nil {
		c.log("error", "coordinator.rollback", "Failed to transition to rollback_in_progress")
		return err
	}

	activeConfigModified := true
	switch originalState {
	case StateIdle, StatePreSnapshotWriteIntent, StateSnapshotSecured, StateCandidateWriteIntent, StateCandidateBuilt, StateCandidatePublished, StateRecoveryIntent:
		activeConfigModified = false
	case StateSwapIntent:
		if m.CandidateConfigDigest != "" {
			d, err := strictfs.ComputeFileDigest(c.activeConfigFile)
			if err == nil && d == m.CandidateConfigDigest {
				activeConfigModified = true
			} else {
				activeConfigModified = false
			}
		} else {
			activeConfigModified = false
		}
	case StateRuntimeIntent:
		if m.DesiredMode != RuntimeOff {
			activeConfigModified = true
		} else {
			activeConfigModified = true
		}
	}

	var rollbackErrs []error

	if originalState != StatePreSnapshotWriteIntent && m.PreMutationStoreSnapshotFile != "" {
		if err := c.cfg.StoreTx.RestoreSnapshotFile(m.PreMutationStoreSnapshotFile); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to restore store snapshot: %w", err))
			_ = c.writeRecoveryMarkerLocked("rollback restore store snapshot failed: " + err.Error())
		}
	}

	if activeConfigModified {
		if err := c.stopControlledLocked(ctx, m.TxID, "rollback"); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to stop operator during rollback: %w", err))
			m.State = StateRecoveryRequired
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("[%s] rollback aborted: process stop failed (%v)", m.TxID, err))
			return errors.Join(rollbackErrs...)
		}
	}

	var restored bool

	if !activeConfigModified {
		restored = true
		c.log("info", "coordinator.rollback", "Active config was not modified by this transaction, skipping restore")
	} else if m.LKGGenerationID != "" {
		_, bundleConfigPath, _, err := c.genStore.ReadGenerationBundle(m.LKGGenerationID)
		if err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("read previous generation bundle failed: %w", err))
			_ = c.writeRecoveryMarkerLocked("rollback bundle read failed: " + err.Error())
		} else if bundleConfigPath != "" {
			data, err := os.ReadFile(bundleConfigPath)
			if err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("read previous bundle config failed: %w", err))
				_ = c.writeRecoveryMarkerLocked("rollback bundle config read failed: " + err.Error())
			} else {
				if wErr := strictfs.StrictWriteAtomic(c.activeConfigFile, data, 0644); wErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to write LKG to active config: %w", wErr))
					_ = c.writeRecoveryMarkerLocked("rollback write LKG failed: " + wErr.Error())
				} else {
					restored = true
				}
			}
		} else {
			if uErr := strictfs.StrictUnlink(c.activeConfigFile); uErr != nil && !os.IsNotExist(uErr) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to unlink active config (empty LKG): %w", uErr))
				_ = c.writeRecoveryMarkerLocked("rollback unlink active config failed: " + uErr.Error())
			} else {
				restored = true
			}
		}
	}

	if !restored && len(rollbackErrs) == 0 {
		if _, err := os.Stat(c.lkgConfigFile); err == nil {
			if data, err := os.ReadFile(c.lkgConfigFile); err == nil {
				if wErr := strictfs.StrictWriteAtomic(c.activeConfigFile, data, 0644); wErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to write legacy LKG to active config: %w", wErr))
					_ = c.writeRecoveryMarkerLocked("rollback write legacy LKG failed: " + wErr.Error())
				} else {
					restored = true
				}
			}
		}
	}

	if !restored && len(rollbackErrs) == 0 {
		if uErr := strictfs.StrictUnlink(c.activeConfigFile); uErr != nil && !os.IsNotExist(uErr) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to unlink active config: %w", uErr))
			_ = c.writeRecoveryMarkerLocked("rollback unlink active config failed: " + uErr.Error())
		}
	}

	var targetBridges []BridgeRef
	if len(m.PreviousBridges) > 0 {
		if m.PreviousBridgesDigest == "" || m.PreviousBridgesDigest != BridgesDigest(m.PreviousBridges) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("%w: corrupt previous_bridges_digest in manifest", ErrForeignBridgeOwnership))
			_ = c.writeRecoveryMarkerLocked("corrupt manifest previous_bridges_digest during rollback")
		}
		for i, b := range m.PreviousBridges {
			if err := b.ValidateComplete(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("%w: incomplete previous_bridges[%d]: %v", ErrForeignBridgeOwnership, i, err))
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("incomplete previous_bridges[%d] during rollback: %v", i, err))
			}
		}
		targetBridges = m.PreviousBridges
	} else if c.appliedRecord != nil {
		targetBridges = c.appliedRecord.AppliedBridges
	}
	beforeBridges := m.TargetBridges
	if len(beforeBridges) > 0 {
		if m.TargetBridgesDigest == "" || m.TargetBridgesDigest != BridgesDigest(beforeBridges) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("%w: corrupt target_bridges_digest in manifest", ErrForeignBridgeOwnership))
			_ = c.writeRecoveryMarkerLocked("corrupt manifest target_bridges_digest during rollback")
		}
		for i, b := range beforeBridges {
			if err := b.ValidateComplete(); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("%w: incomplete target_bridges[%d]: %v", ErrForeignBridgeOwnership, i, err))
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("incomplete target_bridges[%d] during rollback: %v", i, err))
			}
		}
	}

	if c.cfg.BridgeRuntime != nil && (len(beforeBridges) > 0 || len(targetBridges) > 0) && len(rollbackErrs) == 0 {
		if err := c.syncBridgesLocked(ctx, m, beforeBridges, targetBridges); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("failed to rollback bridges: %w", err))
			_ = c.writeRecoveryMarkerLocked("rollback sync bridges failed: " + err.Error())
		} else {
			if reg, ok := c.cfg.BridgeRuntime.(DurableBridgeRegistry); ok {
				if err := reg.ReplaceDurableBridges(targetBridges); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("immediate rollback bridge registry replacement failed: %w", err))
					_ = c.writeRecoveryMarkerLocked("immediate rollback bridge registry replacement failed: " + err.Error())
				}
			}
		}
	}

	var finalRollbackErr error
	targetState := StateRolledBack
	if len(rollbackErrs) > 0 {
		finalRollbackErr = errors.Join(rollbackErrs...)
		c.log("error", "coordinator.rollback", "Rollback failed, requiring manual recovery: "+finalRollbackErr.Error())
		targetState = StateRecoveryRequired
	}

	if err := c.transitionManifestLocked(m, targetState); err != nil {
		return err
	}

	return finalRollbackErr
}

func (c *ApplyCoordinator) initManifestLocked(m *TransactionManifest, firstState ManifestState) error {
	m.Sequence = 1
	m.State = firstState
	m.UpdatedAt = time.Now()

	if err := m.ValidateSchemaForPhase(); err != nil {
		return fmt.Errorf("phase schema validation failed for state %s: %w", firstState, err)
	}
	if err := c.validateManifestOwnershipLocked(m); err != nil {
		return fmt.Errorf("manifest ownership validation failed for state %s: %w", firstState, err)
	}

	if c.hooks.FailManifestPersistAtState != "" && c.hooks.FailManifestPersistAtState == firstState {
		return fmt.Errorf("failpoint: manifest write failed for state %s", firstState)
	}

	if err := c.casManifest(nil, m); err != nil {
		return err
	}
	c.setState(firstState)
	return nil
}

// verifyAuthoritativeGenerationLocked performs pure read-only verification of the authoritative
// verified-active.json and lkg.pointer.json records on disk against rec. It does NOT modify or write
// any files. On any error, it leaves the coordinator in StateRecoveryRequired with recovery.marker written.
func (c *ApplyCoordinator) verifyAuthoritativeGenerationLocked(rec AppliedGenerationRecord, advanceLKG bool) error {
	// Read back verified-active.json and verify complete structural equality
	vaData, err := os.ReadFile(c.verifiedActiveFile)
	if err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("verify verified-active read: " + err.Error())
		return fmt.Errorf("verify verified-active read: %w", err)
	}
	var verifyRec AppliedGenerationRecord
	if err := DecodeJSONStrict(vaData, &verifyRec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("verify verified-active parse: " + err.Error())
		return fmt.Errorf("verify verified-active parse: %w", err)
	}

	if eq, reason := CanonicalEqualAppliedRecords(verifyRec, rec); !eq {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("verified-active structural equality mismatch during verification: " + reason)
		return fmt.Errorf("%w: verified-active mismatch: %s", ErrVerificationFailed, reason)
	}

	// Read back LKG pointer and verify
	ptr, err := c.genStore.ReadLKGPointer()
	if err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("verify LKG pointer read: " + err.Error())
		return fmt.Errorf("verify LKG pointer read: %w", err)
	}

	if advanceLKG {
		if ptr.GenerationID != rec.GenerationID || ptr.GenerationNumber != rec.Generation ||
			ptr.AppliedConfigDigest != rec.AppliedConfigDigest || ptr.AppliedStoreDigest != rec.AppliedStoreDigest ||
			ptr.UpdatedEpoch != c.DaemonEpoch() {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("LKG pointer generation/epoch mismatch during advance verification")
			return fmt.Errorf("%w: LKG pointer mismatch after advance", ErrVerificationFailed)
		}
	} else {
		// Non-advancing verification: verify existing pointer matches target LKG record without mutating pointer
		if ptr.GenerationID != rec.GenerationID || ptr.GenerationNumber != rec.Generation ||
			ptr.AppliedConfigDigest != rec.AppliedConfigDigest || ptr.AppliedStoreDigest != rec.AppliedStoreDigest {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("LKG pointer mismatch during non-advancing verification")
			return fmt.Errorf("%w: LKG pointer mismatch against target LKG", ErrVerificationFailed)
		}
	}

	// Verify bundle digests if bundle exists (fail-closed on any error or missing bundle)
	if rec.GenerationID != "" {
		gm, cfgPath, snapPath, bErr := c.genStore.ReadGenerationBundle(rec.GenerationID)
		if bErr != nil || gm == nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("generation bundle read/verify failed for %s: %v", rec.GenerationID, bErr))
			return fmt.Errorf("%w: read generation bundle %s: %v", ErrVerificationFailed, rec.GenerationID, bErr)
		}
		if gm.GenerationID != rec.GenerationID || gm.GenerationNumber != rec.Generation {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("generation bundle ID/number mismatch: got %s (%d), want %s (%d)", gm.GenerationID, gm.GenerationNumber, rec.GenerationID, rec.Generation))
			return fmt.Errorf("%w: bundle generation ID/number mismatch", ErrVerificationFailed)
		}
		if gm.AppliedConfigDigest != rec.AppliedConfigDigest || gm.AppliedStoreDigest != rec.AppliedStoreDigest ||
			gm.AppliedInputDigest != rec.AppliedInputDigest || gm.RuntimeMode != rec.RuntimeMode {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("generation bundle digest/mode mismatch")
			return fmt.Errorf("%w: bundle digest mismatch", ErrVerificationFailed)
		}
		if len(gm.AppliedConfigDigest) == 64 && cfgPath != "" && gm.RuntimeMode != RuntimeOff {
			actualCfgDigest, cErr := strictfs.ComputeFileDigest(cfgPath)
			if cErr != nil || actualCfgDigest != gm.AppliedConfigDigest {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("bundle config file sha256 mismatch")
				return fmt.Errorf("%w: bundle config sha256 mismatch", ErrVerificationFailed)
			}
		}
		if len(gm.AppliedStoreDigest) == 64 && snapPath != "" {
			actualSnapDigest, sErr := strictfs.ComputeFileDigest(snapPath)
			if sErr != nil || actualSnapDigest != gm.AppliedStoreDigest {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("bundle store snapshot file sha256 mismatch")
				return fmt.Errorf("%w: bundle store snapshot sha256 mismatch", ErrVerificationFailed)
			}
		}
	}

	c.mu.Lock()
	c.appliedRecord = &rec
	c.mu.Unlock()
	if c.cfg.BridgeRuntime != nil {
		if reg, ok := c.cfg.BridgeRuntime.(DurableBridgeRegistry); ok {
			if err := reg.ReplaceDurableBridges(rec.AppliedBridges); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("commit bridge registry replacement failed: " + err.Error())
				return fmt.Errorf("commit bridge registry replacement: %w", err)
			}
		}
	}

	return nil
}

func (c *ApplyCoordinator) ensureProcessProofLocked(ctx context.Context, txID string, rec AppliedGenerationRecord) error {
	if rec.RuntimeMode == RuntimeOff {
		c.processReceipt = nil
		return nil
	}

	// 1. If we already have a valid receipt for this exact generation, nothing to do
	if c.processReceipt != nil &&
		c.processReceipt.AppliedGeneration == rec.Generation &&
		c.processReceipt.RuntimeProcessIdentity.Generation == rec.Generation &&
		c.processReceipt.PID > 0 {
		return nil
	}

	// 2. If operator is running, capture identity proof for this generation
	if c.cfg.Operator != nil {
		if running, pid := c.cfg.Operator.IsRunning(); running && pid > 0 {
			verifier := c.cfg.Verifier
			if verifier == nil {
				verifier = &NoopProcessVerifier{}
			}
			binPath := c.cfg.ConfigDir
			if opBin, ok := c.cfg.Operator.(interface{ Binary() string }); ok {
				binPath = opBin.Binary()
			}
			identity, err := verifier.CaptureIdentity("", pid, binPath, c.cfg.ConfigDir, rec.Generation)
			if err == nil {
				c.processReceipt = &ProcessReceipt{
					RuntimeProcessIdentity: identity,
					DaemonEpoch:            c.DaemonEpoch(),
					AppliedGeneration:      rec.Generation,
					VerifiedAt:             time.Now(),
				}
				return nil
			}
		}
	}

	// 3. If operator is not running, restart/start it controlled
	if c.cfg.Operator != nil {
		if _, statErr := os.Stat(c.activeConfigFile); os.IsNotExist(statErr) && rec.GenerationID != "" && c.genStore != nil {
			if _, bundleCfg, _, err := c.genStore.ReadGenerationBundle(rec.GenerationID); err == nil && bundleCfg != "" {
				if cfgData, err := os.ReadFile(bundleCfg); err == nil {
					_ = strictfs.StrictWriteAtomic(c.activeConfigFile, cfgData, 0600)
				}
			}
		}
		if err := c.restartControlledLocked(ctx, txID, rec.Generation, rec.AppliedConfigDigest, rec.AppliedListeners); err != nil {
			return err
		}
		if c.processReceipt != nil && c.processReceipt.AppliedGeneration == rec.Generation {
			return nil
		}
	}

	if c.processReceipt == nil {
		return fmt.Errorf("%w: missing process receipt for running runtime", ErrProcessProofFailed)
	}
	return nil
}

func (c *ApplyCoordinator) validateProcessReceiptLocked(rec AppliedGenerationRecord) error {
	if rec.RuntimeMode == RuntimeOff {
		if c.processReceipt != nil {
			return fmt.Errorf("%w: receipt must be nil for RuntimeOff (got PID %d)", ErrProcessProofFailed, c.processReceipt.PID)
		}
		return nil
	}
	if c.processReceipt == nil {
		return fmt.Errorf("%w: missing process receipt for running runtime", ErrProcessProofFailed)
	}
	if c.processReceipt.AppliedGeneration != rec.Generation {
		return fmt.Errorf("%w: process receipt generation mismatch: got %d, want %d", ErrProcessProofFailed, c.processReceipt.AppliedGeneration, rec.Generation)
	}
	if c.processReceipt.RuntimeProcessIdentity.Generation != rec.Generation {
		return fmt.Errorf("%w: process identity generation mismatch: got %d, want %d", ErrProcessProofFailed, c.processReceipt.RuntimeProcessIdentity.Generation, rec.Generation)
	}
	if c.processReceipt.PID <= 0 || c.processReceipt.ProcStartTicks == 0 || c.processReceipt.ExecutablePath == "" || c.processReceipt.ConfigDir == "" {
		return fmt.Errorf("%w: process receipt identity has incomplete proof", ErrProcessProofFailed)
	}
	return nil
}

// persistAndVerifyGenerationLocked atomically writes verified-active.json, optionally advances
// the LKG pointer (if advanceLKG is true), and then verifies them read-only via verifyAuthoritativeGenerationLocked.
// On any error, it leaves the coordinator in StateRecoveryRequired with recovery.marker written.
func (c *ApplyCoordinator) persistAndVerifyGenerationLocked(rec AppliedGenerationRecord, advanceLKG bool) error {
	if err := c.ensureProcessProofLocked(context.Background(), "", rec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("process proof capture failed before persist generation: " + err.Error())
		return err
	}
	if err := c.validateProcessReceiptLocked(rec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("process receipt validation failed before persist generation: " + err.Error())
		return err
	}
	rec.ProcessReceipt = c.processReceipt

	if c.hooks.FailAuthoritativeWriteAfterPromote {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("failpoint: forbidden authoritative write after promote")
		return errors.New("failpoint: forbidden authoritative write after promote")
	}
	if c.hooks.FailCommitVerifiedActive {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("failpoint: commit verified active failed")
		return ErrRecoveryRequired
	}

	recBytes, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("marshal verified active: " + err.Error())
		return fmt.Errorf("marshal verified active: %w", err)
	}

	if err := c.authWriter.WriteAuthoritative(c.verifiedActiveFile, recBytes, 0600); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("write verified active: " + err.Error())
		return fmt.Errorf("write verified active: %w", err)
	}
	if c.hooks.OnAuthoritativeWrite != nil {
		c.hooks.OnAuthoritativeWrite("write", c.verifiedActiveFile)
	}

	if advanceLKG {
		if c.hooks.FailAuthoritativeWriteAfterPromote {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("failpoint: forbidden authoritative write after promote")
			return errors.New("failpoint: forbidden authoritative write after promote")
		}
		if c.hooks.FailAdvanceLKGPointer {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("failpoint: advance LKG pointer failed")
			return fmt.Errorf("failpoint: advance LKG pointer failed")
		}
		ptr := LKGPointer{
			Version:                   1,
			GenerationID:              rec.GenerationID,
			GenerationNumber:          rec.Generation,
			AppliedConfigDigest:       rec.AppliedConfigDigest,
			AppliedStoreDigest:        rec.AppliedStoreDigest,
			AppliedDesiredStoreDigest: rec.AppliedDesiredStoreDigest,
			UpdatedEpoch:              c.DaemonEpoch(),
			UpdatedAt:                 time.Now(),
		}
		ptrBytes, err := json.MarshalIndent(ptr, "", "  ")
		if err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("marshal LKG pointer: " + err.Error())
			return fmt.Errorf("marshal LKG pointer: %w", err)
		}
		if err := c.authWriter.WriteAuthoritative(c.genStore.LKGPointerFile(), ptrBytes, 0600); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("update LKG pointer: " + err.Error())
			return fmt.Errorf("update LKG pointer: %w", err)
		}
		if c.hooks.OnAuthoritativeWrite != nil {
			c.hooks.OnAuthoritativeWrite("write", c.genStore.LKGPointerFile())
		}
	}

	return c.verifyAuthoritativeGenerationLocked(rec, advanceLKG)
}

func (c *ApplyCoordinator) executeStagedCommitLocked(ctx context.Context, manifest *TransactionManifest, rec AppliedGenerationRecord, advanceLKG bool) error {
	if err := c.ensureProcessProofLocked(ctx, manifest.TxID, rec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("process proof capture failed before staged commit: " + err.Error())
		return err
	}
	if err := c.validateProcessReceiptLocked(rec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("process receipt validation failed before staged commit: " + err.Error())
		return err
	}
	rec.ProcessReceipt = c.processReceipt

	// 1. Prepare staged verified-active.json
	stagedVA := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("verified-active.json.staged.%s", manifest.TxID))
	recBytes, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal candidate verified active: %w", err)
	}
	if err := strictfs.StrictWriteAtomic(stagedVA, recBytes, 0600); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("write staged verified-active: " + err.Error())
		return fmt.Errorf("write staged verified active: %w", err)
	}
	manifest.CandidateRecordDigest = strictfs.ComputeBytesDigest(recBytes)

	// 2. If advancing LKG, prepare staged lkg.pointer.json
	stagedPtr := filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("lkg.pointer.json.staged.%s", manifest.TxID))
	if advanceLKG {
		if manifest.PreviousLKGGenerationID == "" {
			prevPtr, pErr := c.genStore.ReadLKGPointer()
			if pErr != nil {
				if !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("read existing LKG pointer failed before staged commit: " + pErr.Error())
					return fmt.Errorf("read existing LKG pointer: %w", pErr)
				}
				// Pointer does not exist: first-generation case
			} else if prevPtr != nil {
				manifest.PreviousLKGGenerationID = prevPtr.GenerationID
				prevBytes, rErr := os.ReadFile(c.genStore.LKGPointerFile())
				if rErr != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("read existing LKG pointer file for digest failed: " + rErr.Error())
					return fmt.Errorf("read existing LKG pointer file: %w", rErr)
				}
				manifest.PreviousLKGPointerDigest = strictfs.ComputeBytesDigest(prevBytes)
			}
		}
		if manifest.PreviousLKGPointerDigest == "" {
			if prevBytes, rErr := os.ReadFile(c.genStore.LKGPointerFile()); rErr == nil {
				manifest.PreviousLKGPointerDigest = strictfs.ComputeBytesDigest(prevBytes)
			} else if !os.IsNotExist(rErr) {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read existing LKG pointer file for digest failed: " + rErr.Error())
				return fmt.Errorf("read existing LKG pointer file: %w", rErr)
			}
		}
		newPtr := LKGPointer{
			Version:                   1,
			GenerationID:              rec.GenerationID,
			GenerationNumber:          rec.Generation,
			AppliedConfigDigest:       rec.AppliedConfigDigest,
			AppliedStoreDigest:        rec.AppliedStoreDigest,
			AppliedDesiredStoreDigest: rec.AppliedDesiredStoreDigest,
			UpdatedEpoch:              c.DaemonEpoch(),
			UpdatedAt:                 time.Now(),
		}
		ptrBytes, pErr := json.MarshalIndent(newPtr, "", "  ")
		if pErr != nil {
			return fmt.Errorf("marshal staged lkg pointer: %w", pErr)
		}
		if err := strictfs.StrictWriteAtomic(stagedPtr, ptrBytes, 0600); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("write staged lkg pointer: " + err.Error())
			return fmt.Errorf("write staged lkg pointer: %w", err)
		}
		manifest.CandidatePointerDigest = strictfs.ComputeBytesDigest(ptrBytes)
	}

	// 3. Verify staged files before promotion
	c.crashAt("regenerate_staged_write")
	vaStagedData, err := os.ReadFile(stagedVA)
	if err != nil || strictfs.ComputeBytesDigest(vaStagedData) != manifest.CandidateRecordDigest {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("staged verified-active digest mismatch")
		return fmt.Errorf("%w: staged verified-active digest mismatch", ErrVerificationFailed)
	}
	var vaStagedRec AppliedGenerationRecord
	if err := DecodeJSONStrict(vaStagedData, &vaStagedRec); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("staged verified-active invalid: " + err.Error())
		return fmt.Errorf("%w: staged verified-active decode: %v", ErrVerificationFailed, err)
	}
	if advanceLKG {
		ptrStagedData, err := os.ReadFile(stagedPtr)
		if err != nil || strictfs.ComputeBytesDigest(ptrStagedData) != manifest.CandidatePointerDigest {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("staged pointer digest mismatch")
			return fmt.Errorf("%w: staged pointer digest mismatch", ErrVerificationFailed)
		}
		var ptrStaged LKGPointer
		if err := DecodeJSONStrict(ptrStagedData, &ptrStaged); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("staged pointer invalid: " + err.Error())
			return fmt.Errorf("%w: staged pointer decode: %v", ErrVerificationFailed, err)
		}
	}

	// 4. Checkpoint StateCommitIntent with candidate digests
	if manifest.State != StateCommitIntent {
		if err := c.transitionManifestLocked(manifest, StateCommitIntent); err != nil {
			return err
		}
	} else {
		if err := c.checkpointManifestLocked(manifest); err != nil {
			return err
		}
	}

	c.crashAt("regenerate_commit_intent")

	// 5. Promote staged verified-active file
	vaData, vaErr := os.ReadFile(c.verifiedActiveFile)
	vaAlreadyPromoted := (vaErr == nil && strictfs.ComputeBytesDigest(vaData) == manifest.CandidateRecordDigest)
	if !vaAlreadyPromoted {
		if c.hooks.FailCommitVerifiedActive {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("failpoint: commit verified active failed")
			return fmt.Errorf("%w: failpoint: commit verified active failed", ErrRecoveryRequired)
		}
		if err := c.authWriter.RenameAuthoritative(stagedVA, c.verifiedActiveFile); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("promote staged verified-active failed: " + err.Error())
			return fmt.Errorf("promote staged verified-active: %w", err)
		}
		if c.hooks.OnAuthoritativeWrite != nil {
			c.hooks.OnAuthoritativeWrite("rename", c.verifiedActiveFile)
		}
		if err := strictfs.FsyncDirectory(c.cfg.ConfigDir); err != nil {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("fsync config dir after verified-active: " + err.Error())
			return fmt.Errorf("fsync config dir: %w", err)
		}
	} else {
		_ = strictfs.StrictUnlink(stagedVA)
	}

	c.crashAt("regenerate_verified_active_promoted")

	// 6. Promote staged LKG pointer
	if advanceLKG {
		ptrData, ptrErr := os.ReadFile(c.genStore.LKGPointerFile())
		ptrAlreadyPromoted := (ptrErr == nil && strictfs.ComputeBytesDigest(ptrData) == manifest.CandidatePointerDigest)
		if !ptrAlreadyPromoted {
			if c.hooks.FailAdvanceLKGPointer || c.genStore.hooks.FailPointerWrite {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("failpoint: advance LKG pointer failed")
				return errors.New("failpoint: advance LKG pointer failed")
			}
			if err := c.authWriter.RenameAuthoritative(stagedPtr, c.genStore.LKGPointerFile()); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("promote staged lkg pointer failed: " + err.Error())
				return fmt.Errorf("promote staged lkg pointer: %w", err)
			}
			if c.hooks.OnAuthoritativeWrite != nil {
				c.hooks.OnAuthoritativeWrite("rename", c.genStore.LKGPointerFile())
			}
			if err := strictfs.FsyncDirectory(filepath.Dir(c.genStore.LKGPointerFile())); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("fsync pointer dir: " + err.Error())
				return fmt.Errorf("fsync pointer dir: %w", err)
			}
		} else {
			_ = strictfs.StrictUnlink(stagedPtr)
		}
	}

	c.crashAt("regenerate_pointer_promoted")

	// 7. Full verification of authoritative records (READ-ONLY, zero second-write)
	if err := c.verifyAuthoritativeGenerationLocked(rec, advanceLKG); err != nil {
		return err
	}

	// 8. Durable transition to committed
	targetState := StateCommitted
	if manifest.OperationKind == OperationRegenerate {
		targetState = StateRecoveryCommitted
	}
	if err := c.transitionManifestLocked(manifest, targetState); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("transition to committed failed: " + err.Error())
		return fmt.Errorf("committed transition: %w", err)
	}

	c.crashAt("regenerate_post_commit_state")

	// 9. Clean up staged files if any remain
	_ = strictfs.StrictUnlink(stagedVA)
	if advanceLKG {
		_ = strictfs.StrictUnlink(stagedPtr)
	}

	// 10. Terminal cleanup
	if err := c.cleanupTxArtifactsLocked(manifest); err != nil {
		c.log("error", "coordinator.commit", "commit succeeded with partial cleanup failure: "+err.Error())
		return fmt.Errorf("commit succeeded with partial cleanup failure: %w", err)
	}

	c.setState(StateIdle)
	return nil
}

func (c *ApplyCoordinator) executeFinalCommitLocked(ctx context.Context, manifest *TransactionManifest, rec AppliedGenerationRecord) error {
	if c.hooks.FailRollForwardCandidateCommit {
		return errors.New("failpoint: candidate final commit failed")
	}
	return c.executeStagedCommitLocked(ctx, manifest, rec, true)
}

func (c *ApplyCoordinator) setState(state ManifestState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

func (c *ApplyCoordinator) writeRecoveryMarkerLocked(msg string) error {
	return strictfs.StrictWriteAtomic(c.recoveryMarkerFile, []byte(msg), 0600)
}

// writeRecoveryMarkerIfAbsentLocked preserves the transaction-specific marker
// that identifies which durable migration is allowed to clear it on recovery.
func (c *ApplyCoordinator) writeRecoveryMarkerIfAbsentLocked(msg string) error {
	if _, err := os.Stat(c.recoveryMarkerFile); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat recovery marker: %w", err)
	}
	return c.writeRecoveryMarkerLocked(msg)
}

func (c *ApplyCoordinator) archiveAndPrepareRecoveryManifestLocked(next *TransactionManifest) error {
	existingData, err := os.ReadFile(c.manifestFile)
	if err == nil {
		var existingM TransactionManifest
		if jErr := DecodeJSONStrict(existingData, &existingM); jErr == nil {
			prevDigest := strictfs.ComputeBytesDigest(existingData)
			archiveBasename := fmt.Sprintf("transaction.manifest.interrupted.%s.json", existingM.TxID)
			archivePath := filepath.Join(c.cfg.ConfigDir, archiveBasename)

			// Step 1: Copy atomically to archive file
			if wErr := strictfs.StrictWriteAtomic(archivePath, existingData, 0600); wErr != nil {
				return fmt.Errorf("archive existing manifest to %s: %w", archiveBasename, wErr)
			}
			// Step 2: Fsync ConfigDir
			if fErr := strictfs.FsyncDirectory(c.cfg.ConfigDir); fErr != nil {
				return fmt.Errorf("fsync config dir after archive: %w", fErr)
			}

			// Step 3: Populate link in new manifest
			next.PreviousManifestDigest = prevDigest
			next.ArchivedManifestFile = archiveBasename

			// Step 4: Perform CAS replacement from existingM to next
			return c.casManifest(&existingM, next)
		} else {
			_ = strictfs.StrictRename(c.manifestFile, c.manifestFile+".corrupt")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing manifest before recovery: %w", err)
	}

	// No valid manifest existed, create-only CAS
	return c.casManifest(nil, next)
}

func (c *ApplyCoordinator) rollbackToGenerationLocked(ctx context.Context, targetGenID string, startedFromDegraded bool) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	ctx = rollbackCtx
	gm, _, _, bundleErr := c.genStore.ReadGenerationBundle(targetGenID)
	if bundleErr != nil || gm == nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("read LKG generation bundle %s failed: %v", targetGenID, bundleErr))
		return fmt.Errorf("read LKG generation bundle %s: %w", targetGenID, bundleErr)
	}
	if gm.IsLegacyBridgeIdentity() || gm.BridgeIdentityVersion != CurrentBridgeIdentityVersion {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("cannot rollback to legacy generation %s without upgraded durable identity", targetGenID))
		return fmt.Errorf("cannot rollback to legacy generation %s without upgraded durable identity", targetGenID)
	}

	txid := GenerateTxID()
	m := TransactionManifest{
		Version:                     1,
		TxID:                        txid,
		OperationKind:               OperationRollback,
		State:                       StateRollbackIntent,
		Sequence:                    1,
		CandidateGenerationID:       targetGenID,
		RollbackTargetGenerationID:  targetGenID,
		LKGGenerationID:             targetGenID,
		PreviousLKGGenerationID:     targetGenID,
		StartedFromRecoveryRequired: startedFromDegraded,
		TargetDesiredStoreDigest:    gm.AppliedDesiredStoreDigest,
		CandidateConfigDigest:       gm.AppliedConfigDigest,
		CandidateConfigFile:         "",
		TargetBridges:               gm.AppliedBridges,
		TargetBridgesDigest:         BridgesDigest(gm.AppliedBridges),
		DesiredMode:                 gm.RuntimeMode,
		CreatedAt:                   time.Now(),
		UpdatedAt:                   time.Now(),
	}
	if markerBytes, mErr := os.ReadFile(c.recoveryMarkerFile); mErr == nil {
		m.PreviousRecoveryMarkerDigest = strictfs.ComputeBytesDigest(markerBytes)
	}
	if c.appliedRecord != nil {
		m.PreviousBridges = c.appliedRecord.AppliedBridges
		if len(m.PreviousBridges) > 0 {
			m.PreviousBridgesDigest = BridgesDigest(m.PreviousBridges)
		}
	}

	if err := c.archiveAndPrepareRecoveryManifestLocked(&m); err != nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked("persist rollback intent failed: " + err.Error())
		return fmt.Errorf("persist rollback intent: %w", err)
	}

	return c.resumeRollbackTransactionLocked(ctx, &m)
}

func (c *ApplyCoordinator) resumeRollbackTransactionLocked(ctx context.Context, m *TransactionManifest) error {
	c.crashAt("rollback_intent")
	targetGenID := m.CandidateGenerationID
	if targetGenID == "" {
		targetGenID = m.PreviousLKGGenerationID
	}
	gm, configPath, storeSnapshotPath, bundleErr := c.genStore.ReadGenerationBundle(targetGenID)
	if bundleErr != nil || gm == nil {
		c.setState(StateRecoveryRequired)
		_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("read LKG bundle %s for rollback resume failed: %v", targetGenID, bundleErr))
		return fmt.Errorf("read LKG bundle %s for rollback resume: %w", targetGenID, bundleErr)
	}

	// 1. Restore store snapshot (if in StateRollbackIntent)
	if m.State == StateRollbackIntent {
		c.crashAt("rollback_store_restore")
		if storeSnapshotPath != "" && c.cfg.StoreTx != nil {
			if err := c.cfg.StoreTx.RestoreSnapshotFile(storeSnapshotPath); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("restore store snapshot from LKG failed: " + err.Error())
				return fmt.Errorf("restore store snapshot from LKG: %w", err)
			}
		}
		if err := c.transitionManifestLocked(m, StateRollbackStoreRestored); err != nil {
			return err
		}
	}

	c.crashAt("rollback_post_store_restore")

	// 2. Restore active config (if in StateRollbackStoreRestored)
	if m.State == StateRollbackStoreRestored {
		c.crashAt("rollback_config_promote")
		if c.hooks.FailAtomicConfigWrite {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("failpoint: atomic config write failed")
			return fmt.Errorf("failpoint: atomic config write failed")
		}
		if gm.RuntimeMode != RuntimeOff {
			cfgBytes, err := os.ReadFile(configPath)
			if err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read LKG config failed: " + err.Error())
				return fmt.Errorf("read LKG config: %w", err)
			}
			if err := strictfs.StrictWriteAtomic(c.activeConfigFile, cfgBytes, 0600); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("restore active config from LKG failed: " + err.Error())
				return fmt.Errorf("restore active config from LKG: %w", err)
			}
		} else {
			if err := strictfs.StrictUnlink(c.activeConfigFile); err != nil && !os.IsNotExist(err) {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("unlink active config during rollback failed: " + err.Error())
				return fmt.Errorf("unlink active config during rollback: %w", err)
			}
			if statErr := c.statActiveConfigAbsenceLocked(); statErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("absence check during rollback failed: " + statErr.Error())
				return fmt.Errorf("absence check during rollback: %w", statErr)
			}
		}
		if err := c.transitionManifestLocked(m, StateRollbackConfigPromoted); err != nil {
			return err
		}
	}

	c.crashAt("rollback_post_config_promote")

	// 3. Restart and verify runtime (if in StateRollbackConfigPromoted)
	if m.State == StateRollbackConfigPromoted {
		c.crashAt("rollback_runtime_verify")
		if err := c.stopControlledLocked(ctx, m.TxID, "rollback"); err != nil {
			return fmt.Errorf("stop former process during rollback: %w", err)
		}
		if gm.RuntimeMode != RuntimeOff {
			if err := c.restartControlledLocked(ctx, m.TxID, gm.GenerationNumber, gm.AppliedConfigDigest, gm.AppliedListeners); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("[%s] restart mihomo with LKG config failed: %v", m.TxID, err))
				return fmt.Errorf("restart mihomo with LKG config: %w", err)
			}
		} else {
			c.processReceipt = nil
		}
		if err := c.transitionManifestLocked(m, StateRollbackRuntimeVerified); err != nil {
			return err
		}
	}

	c.crashAt("rollback_post_runtime_verify")

	// 4. Sync bridges (if in StateRollbackRuntimeVerified)
	if m.State == StateRollbackRuntimeVerified {
		c.crashAt("rollback_bridge_sync")
		if c.cfg.BridgeRuntime != nil {
			var beforeBridges []BridgeRef
			if len(m.PreviousBridges) > 0 {
				if m.PreviousBridgesDigest == "" || m.PreviousBridgesDigest != BridgesDigest(m.PreviousBridges) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("corrupt manifest previous_bridges_digest during resumed rollback")
					return fmt.Errorf("%w: corrupt previous_bridges_digest in manifest", ErrForeignBridgeOwnership)
				}
				for i, b := range m.PreviousBridges {
					if err := b.ValidateComplete(); err != nil {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("incomplete previous_bridges[%d] during resumed rollback: %v", i, err))
						return fmt.Errorf("%w: incomplete previous_bridges: %v", ErrForeignBridgeOwnership, err)
					}
				}
				beforeBridges = m.PreviousBridges
			} else if len(m.TargetBridges) > 0 {
				if m.TargetBridgesDigest == "" || m.TargetBridgesDigest != BridgesDigest(m.TargetBridges) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("corrupt manifest target_bridges_digest during resumed rollback")
					return fmt.Errorf("%w: corrupt target_bridges_digest in manifest", ErrForeignBridgeOwnership)
				}
				for i, b := range m.TargetBridges {
					if err := b.ValidateComplete(); err != nil {
						c.setState(StateRecoveryRequired)
						_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("incomplete target_bridges[%d] during resumed rollback: %v", i, err))
						return fmt.Errorf("%w: incomplete target_bridges: %v", ErrForeignBridgeOwnership, err)
					}
				}
				beforeBridges = m.TargetBridges
			}
			targetBridges := gm.AppliedBridges
			for i, b := range targetBridges {
				if err := b.ValidateComplete(); err != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("incomplete gm.AppliedBridges[%d] during resumed rollback: %v", i, err))
					return fmt.Errorf("%w: incomplete gm.AppliedBridges: %v", ErrForeignBridgeOwnership, err)
				}
			}

			if err := c.syncBridgesLocked(ctx, m, beforeBridges, targetBridges); err != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("sync bridges during rollback failed: " + err.Error())
				return fmt.Errorf("sync bridges during rollback: %w", err)
			}
			if reg, ok := c.cfg.BridgeRuntime.(DurableBridgeRegistry); ok {
				if err := reg.ReplaceDurableBridges(targetBridges); err != nil {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("resumed rollback bridge registry replacement failed: " + err.Error())
					return fmt.Errorf("resumed rollback bridge registry replacement: %w", err)
				}
			}
		}
		if err := c.transitionManifestLocked(m, StateRollbackBridgesVerified); err != nil {
			return err
		}
	}

	c.crashAt("rollback_post_bridge_sync")

	// 5. Persist and verify generation (restoring LKG pointer if candidate pointer was promoted before crash)
	if m.State == StateRollbackBridgesVerified {
		rec := AppliedGenerationRecord{
			Version:                   1,
			BridgeIdentityVersion:     gm.BridgeIdentityVersion,
			GenerationID:              gm.GenerationID,
			Generation:                gm.GenerationNumber,
			AppliedStoreDigest:        gm.AppliedStoreDigest,
			AppliedDesiredStoreDigest: gm.AppliedDesiredStoreDigest,
			AppliedConfigDigest:       gm.AppliedConfigDigest,
			AppliedInputDigest:        gm.AppliedInputDigest,
			AppliedBridges:            gm.AppliedBridges,
			AppliedBridgesDigest:      gm.AppliedBridgesDigest,
			AppliedListeners:          gm.AppliedListeners,
			RuntimeMode:               gm.RuntimeMode,
			ProcessReceipt:            c.processReceipt,
			AppliedAt:                 time.Now(),
		}
		restorePointer := false
		currPtr, pErr := c.genStore.ReadLKGPointer()
		if pErr != nil || currPtr == nil || currPtr.GenerationID != gm.GenerationID {
			restorePointer = true
		}
		if err := c.persistAndVerifyGenerationLocked(rec, restorePointer); err != nil {
			return fmt.Errorf("persist and verify LKG generation: %w", err)
		}
		if err := c.transitionManifestLocked(m, StateRollbackCommitted); err != nil {
			return err
		}
	}

	c.crashAt("rollback_post_commit")

	// 6. Terminal cleanup
	if m.State == StateRollbackCommitted {
		if err := c.cleanupTxArtifactsLocked(m); err != nil {
			c.log("error", "coordinator.rollback", "rollback succeeded with partial cleanup failure: "+err.Error())
			return fmt.Errorf("rollback succeeded with partial cleanup failure: %w", err)
		}
		c.setState(StateIdle)
	}

	return nil
}

func (c *ApplyCoordinator) casManifest(expected, next *TransactionManifest) error {
	if expected != nil {
		checkData, err := os.ReadFile(c.manifestFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("cas failed: expected manifest %s (seq %d) but manifest file does not exist", expected.TxID, expected.Sequence)
			}
			return fmt.Errorf("read manifest for cas: %w", err)
		}
		var checkM TransactionManifest
		if err := DecodeJSONStrict(checkData, &checkM); err != nil {
			// Corrupt manifest, quarantine it
			_ = strictfs.StrictRename(c.manifestFile, c.manifestFile+".corrupt")
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("corrupted manifest JSON during cas: " + err.Error())
			return ErrRecoveryRequired
		}
		if checkM.TxID != expected.TxID || checkM.Sequence != expected.Sequence {
			return fmt.Errorf("concurrent modification detected (expected seq %d, found %d)", expected.Sequence, checkM.Sequence)
		}
	} else {
		// create-only CAS: expected == nil
		checkData, err := os.ReadFile(c.manifestFile)
		if err == nil {
			var checkM TransactionManifest
			if DecodeJSONStrict(checkData, &checkM) == nil {
				if checkM.TxID == next.TxID && checkM.Sequence == next.Sequence && checkM.State == next.State {
					return nil
				}
			}
			return fmt.Errorf("create-only manifest cas failed: manifest already exists on disk in state %s", checkM.State)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check manifest existence for cas: %w", err)
		}
	}

	newData, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	if err := strictfs.StrictWriteAtomic(c.manifestFile, newData, 0600); err != nil {
		// fallback for idempotency
		checkData, checkErr := os.ReadFile(c.manifestFile)
		if checkErr == nil {
			var checkM TransactionManifest
			if DecodeJSONStrict(checkData, &checkM) == nil {
				if checkM.TxID == next.TxID && checkM.Sequence == next.Sequence && checkM.State == next.State {
					if syncErr := c.syncActiveTransactionRolesLocked(next); syncErr != nil {
						return fmt.Errorf("sync active transaction roles after ambiguous manifest write: %w", syncErr)
					}
					return nil
				}
			}
		}
		return fmt.Errorf("write manifest (ambiguous outcome): %w", err)
	}
	if err := c.syncActiveTransactionRolesLocked(next); err != nil {
		return fmt.Errorf("sync active transaction roles after manifest write: %w", err)
	}
	return nil
}

func (c *ApplyCoordinator) syncActiveTransactionRolesLocked(m *TransactionManifest) error {
	if c.cfg.BridgeRuntime == nil {
		return nil
	}
	reg, ok := c.cfg.BridgeRuntime.(DurableBridgeRegistry)
	if !ok {
		return nil
	}
	if m == nil {
		return reg.SetActiveTransactionRoles(nil)
	}
	roles := &ActiveTransactionRoles{
		TxID:                 m.TxID,
		PreviousGenerationID: m.PreviousGenerationID,
		TargetGenerationID:   m.CandidateGenerationID,
		PreviousBridges:      m.PreviousBridges,
		TargetBridges:        m.TargetBridges,
	}
	return reg.SetActiveTransactionRoles(roles)
}

func (c *ApplyCoordinator) checkpointManifestLocked(m *TransactionManifest) error {
	expected := m.Clone()
	next := m.Clone()
	next.Sequence++
	next.UpdatedAt = time.Now()

	if c.hooks.FailManifestPersistAtState != "" && c.hooks.FailManifestPersistAtState == next.State {
		return fmt.Errorf("failpoint: manifest write failed for state %s", next.State)
	}

	if err := c.casManifest(expected, next); err != nil {
		return err
	}
	*m = *next
	return nil
}

func (c *ApplyCoordinator) transitionManifestLocked(m *TransactionManifest, nextState ManifestState) error {
	if nextState != m.State && !m.State.IsValidNext(nextState) {
		return fmt.Errorf("invalid state transition: %s -> %s", m.State, nextState)
	}

	expected := m.Clone()
	next := m.Clone()
	next.Sequence++
	next.State = nextState
	next.UpdatedAt = time.Now()

	if err := next.ValidateSchemaForPhase(); err != nil {
		return fmt.Errorf("phase schema validation failed for state %s: %w", nextState, err)
	}
	if err := c.validateManifestOwnershipLocked(next); err != nil {
		return fmt.Errorf("manifest ownership validation failed for state %s: %w", nextState, err)
	}

	if c.hooks.FailManifestPersistAtState != "" && c.hooks.FailManifestPersistAtState == nextState {
		return fmt.Errorf("failpoint: manifest write failed for state %s", nextState)
	}

	if err := c.casManifest(expected, next); err != nil {
		return err
	}

	*m = *next
	c.setState(nextState)
	return nil
}

// ExportSafeEvidence produces a safely redacted diagnostic export of the coordinator and runtime state.
func (c *ApplyCoordinator) ExportSafeEvidence(ctx context.Context) (*RecoveryEvidenceDTO, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dto := &RecoveryEvidenceDTO{
		GeneratedAt: time.Now(),
		State:       c.state,
		ActiveTxID:  c.activeTxID,
		DaemonEpoch: c.daemonEpoch,
	}

	// 1. Recovery marker facts
	if reason, modTime, ok := sanitizeRecoveryMarkerFact(c.recoveryMarkerFile); ok {
		dto.RecoveryReason = reason
		dto.RecoveryMarker = &RecoveryMarkerFact{
			Timestamp: modTime,
			Reason:    reason,
		}
	}

	// 2. Applied Record facts
	if c.appliedRecord != nil {
		dto.AppliedRecord = &AppliedRecordFact{
			GenerationID:        c.appliedRecord.GenerationID,
			Generation:          c.appliedRecord.Generation,
			AppliedAt:           c.appliedRecord.AppliedAt,
			AppliedConfigDigest: c.appliedRecord.AppliedConfigDigest,
			AppliedStoreDigest:  c.appliedRecord.AppliedStoreDigest,
			AppliedInputDigest:  c.appliedRecord.AppliedInputDigest,
		}
		if c.appliedRecord.ProcessReceipt != nil {
			receipt := c.appliedRecord.ProcessReceipt
			dto.ProcessReceipt = &ProcessReceiptFact{
				PID:            receipt.PID,
				ProcStartTicks: receipt.ProcStartTicks,
				ExecutableName: SanitizePathToBase(receipt.ExecutablePath),
			}
			if len(c.appliedRecord.AppliedListeners) > 0 {
				dto.ProcessReceipt.Listeners = append([]ListenerSpec{}, c.appliedRecord.AppliedListeners...)
			}
		}
	}

	// Fallback for ProcessReceipt if not attached to appliedRecord
	if dto.ProcessReceipt == nil && c.processReceipt != nil {
		dto.ProcessReceipt = &ProcessReceiptFact{
			PID:            c.processReceipt.PID,
			ProcStartTicks: c.processReceipt.ProcStartTicks,
			ExecutableName: SanitizePathToBase(c.processReceipt.ExecutablePath),
		}
	}

	// 3. Manifest facts
	if mBytes, err := os.ReadFile(c.manifestFile); err == nil {
		var m TransactionManifest
		if err := DecodeJSONStrict(mBytes, &m); err == nil {
			dto.ManifestFacts = &ManifestFacts{
				TxID:            m.TxID,
				State:           m.State,
				TargetDigest:    m.TargetDesiredStoreDigest,
				CandidateDigest: m.CandidateConfigDigest,
				CreatedAt:       m.CreatedAt,
				UpdatedAt:       m.UpdatedAt,
			}
		}
	}

	// 4. Current bridges (safe allowlisted projection into BridgeFact)
	if c.cfg.BridgeRuntime != nil {
		if curBridges, err := c.cfg.BridgeRuntime.ListActiveBridges(ctx); err == nil {
			facts := make([]BridgeFact, 0, len(curBridges))
			for _, b := range curBridges {
				facts = append(facts, BridgeFact{
					ProxyIndex:      b.ProxyIndex,
					ProxyInterface:  b.ProxyInterface,
					KernelInterface: b.KernelInterface,
					ListenPort:      b.ListenPort,
					OwnerUUID:       b.OwnerUUID,
					Generation:      b.Generation,
				})
			}
			dto.Bridges = facts
		}
	}

	return dto, nil
}

// Reconcile performs administrative recovery actions for a degraded or faulted coordinator.
func (c *ApplyCoordinator) Reconcile(ctx context.Context, action string, force bool) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	ipcLock, err := strictfs.LockTransaction(c.cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("acquire transaction lock: %w", err)
	}
	defer ipcLock.Close()

	switch action {
	case "rollback_to_lkg":
		ptr, ptrErr := c.genStore.ReadLKGPointer()
		if ptrErr != nil || ptr == nil || ptr.GenerationID == "" {
			if _, statErr := os.Stat(c.lkgConfigFile); statErr == nil {
				lkgBytes, readErr := os.ReadFile(c.lkgConfigFile)
				if readErr != nil {
					return fmt.Errorf("read legacy LKG config: %w", readErr)
				}
				if err := c.stopControlledLocked(ctx, "", "legacy fallback restore"); err != nil {
					return err
				}
				if c.hooks.FailAtomicConfigWrite {
					_ = c.writeRecoveryMarkerLocked("failpoint: atomic config write failed")
					return fmt.Errorf("failpoint: atomic config write failed")
				}
				if cpErr := strictfs.StrictWriteAtomic(c.activeConfigFile, lkgBytes, 0600); cpErr != nil {
					_ = c.writeRecoveryMarkerLocked("fallback restore active from lkg failed: " + cpErr.Error())
					return fmt.Errorf("fallback restore active config: %w", cpErr)
				}
				if c.hooks.FailCleanupRecoveryMarker {
					_ = c.writeRecoveryMarkerLocked("failpoint: cleanup recovery marker failed")
					c.setState(StateRecoveryRequired)
					return fmt.Errorf("failpoint: cleanup recovery marker failed")
				}
				if err := strictfs.StrictUnlink(c.recoveryMarkerFile); err != nil && !os.IsNotExist(err) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("cleanup recovery marker failed: " + err.Error())
					return fmt.Errorf("cleanup recovery marker: %w", err)
				}
				if err := strictfs.StrictUnlink(c.manifestFile); err != nil && !os.IsNotExist(err) {
					c.setState(StateRecoveryRequired)
					_ = c.writeRecoveryMarkerLocked("cleanup manifest file failed: " + err.Error())
					return fmt.Errorf("cleanup manifest file: %w", err)
				}
				c.setState(StateIdle)
				c.log("info", "reconcile", "rolled back using legacy lkg file")
				return nil
			}
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("cannot rollback to LKG: missing or invalid LKG pointer")
			return fmt.Errorf("cannot rollback to LKG: %w", ptrErr)
		}
		wasDegraded := c.IsDegraded() || c.state == StateRecoveryRequired
		if err := c.rollbackToGenerationLocked(ctx, ptr.GenerationID, wasDegraded); err != nil {
			return err
		}
		c.log("info", "reconcile", fmt.Sprintf("successfully rolled back to LKG generation %s", ptr.GenerationID))
		return nil

	case "regenerate_from_desired":
		// Step 1: compile desired
		if c.cfg.Compiler == nil {
			return errors.New("regenerate_from_desired requires compiler configured")
		}

		compileResult, err := c.cfg.Compiler(ctx)
		if err != nil {
			return fmt.Errorf("compile from desired: %w", err)
		}

		txid := GenerateTxID()
		var nextGen uint64 = 1
		if c.appliedRecord != nil {
			nextGen = c.appliedRecord.Generation + 1
		}
		genID := fmt.Sprintf("gen-%06d-%s", nextGen, txid)

		// Step 2: snapshot store
		var snapPath string
		var currentSnapshotStoreDigest string
		var currentDesiredStoreDigest string
		c.crashAt("regenerate_pre_snapshot")
		if c.cfg.StoreTx != nil {
			snapPath, _ = c.cfg.StoreTx.SnapshotFilePath(txid)
			if snapPath != "" {
				digest, snapErr := c.cfg.StoreTx.CreateSnapshotFileAt(txid, snapPath)
				if snapErr != nil {
					return fmt.Errorf("snapshot store for regeneration: %w", snapErr)
				}
				currentSnapshotStoreDigest = digest
			} else {
				currentSnapshotStoreDigest, _ = c.cfg.StoreTx.CurrentSnapshotDigest()
			}
			currentDesiredStoreDigest, err = c.cfg.StoreTx.CurrentDesiredDigest()
			if err != nil {
				return fmt.Errorf("digest desired store for regeneration: %w", err)
			}
		}
		c.crashAt("regenerate_post_snapshot")

		// Step 3: validate candidate (mihomo -t)
		var candidatePath string
		c.crashAt("regenerate_candidate_validation")
		if compileResult.Mode != RuntimeOff {
			candidatePath = filepath.Join(c.cfg.ConfigDir, fmt.Sprintf("config.yaml.candidate.%s", txid))
			if err := strictfs.StrictWriteAtomic(candidatePath, compileResult.ConfigYAML, 0600); err != nil {
				return fmt.Errorf("write candidate config: %w", err)
			}
			defer func() { _ = os.Remove(candidatePath) }()

			if c.cfg.Validator != nil {
				if err := c.cfg.Validator.ValidateConfigFile(ctx, candidatePath); err != nil {
					return fmt.Errorf("validate regenerated candidate: %w", err)
				}
			}
		}

		// Step 4: durable RecoveryIntent (TransactionManifest in StateRecoveryIntent)
		wasDegraded := c.IsDegraded() || c.state == StateRecoveryRequired
		m := TransactionManifest{
			Version:                     1,
			TxID:                        txid,
			OperationKind:               OperationRegenerate,
			State:                       StateRecoveryIntent,
			Sequence:                    1,
			CandidateGenerationID:       genID,
			TargetDesiredStoreDigest:    currentDesiredStoreDigest,
			TargetInputDigest:           compileResult.InputDigest,
			CandidateConfigDigest:       compileResult.ConfigDigest,
			CandidateConfigFile:         candidatePath,
			ConfigPresent:               compileResult.Mode != RuntimeOff,
			DesiredMode:                 compileResult.Mode,
			TargetBridges:               compileResult.TargetBridges,
			TargetBridgesDigest:         BridgesDigest(compileResult.TargetBridges),
			StartedFromRecoveryRequired: wasDegraded,
			CreatedAt:                   time.Now(),
			UpdatedAt:                   time.Now(),
		}
		if c.appliedRecord != nil {
			m.PreviousBridges = c.appliedRecord.AppliedBridges
			if len(m.PreviousBridges) > 0 {
				m.PreviousBridgesDigest = BridgesDigest(m.PreviousBridges)
			}
		}
		if markerBytes, mErr := os.ReadFile(c.recoveryMarkerFile); mErr == nil {
			m.PreviousRecoveryMarkerDigest = strictfs.ComputeBytesDigest(markerBytes)
		}
		ptr, pErr := c.genStore.ReadLKGPointer()
		if pErr != nil && !errors.Is(pErr, os.ErrNotExist) && !os.IsNotExist(pErr) {
			c.setState(StateRecoveryRequired)
			_ = c.writeRecoveryMarkerLocked("read existing LKG pointer failed during regenerate: " + pErr.Error())
			return fmt.Errorf("read existing LKG pointer during regenerate: %w", pErr)
		}
		if ptr != nil {
			m.PreviousLKGGenerationID = ptr.GenerationID
			pBytes, rErr := os.ReadFile(c.genStore.LKGPointerFile())
			if rErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("read existing LKG pointer file failed: " + rErr.Error())
				return fmt.Errorf("read existing LKG pointer file: %w", rErr)
			}
			m.PreviousLKGPointerDigest = strictfs.ComputeBytesDigest(pBytes)
		}

		if err := c.archiveAndPrepareRecoveryManifestLocked(&m); err != nil {
			return fmt.Errorf("archive and prepare recovery manifest: %w", err)
		}
		c.crashAt("regenerate_recovery_intent")

		// Step 5: PublishStagedBundle (BEFORE active config swap and restart!)
		c.crashAt("regenerate_pre_publish")
		if c.hooks.FailBundlePublish {
			_ = c.writeRecoveryMarkerLocked("failpoint: bundle publish failed")
			c.setState(StateRecoveryRequired)
			return fmt.Errorf("failpoint: bundle publish failed")
		}

		rec := AppliedGenerationRecord{
			Version:                   1,
			BridgeIdentityVersion:     CurrentBridgeIdentityVersion,
			GenerationID:              genID,
			Generation:                nextGen,
			AppliedStoreDigest:        currentSnapshotStoreDigest,
			AppliedDesiredStoreDigest: currentDesiredStoreDigest,
			AppliedConfigDigest:       compileResult.ConfigDigest,
			AppliedInputDigest:        compileResult.InputDigest,
			AppliedBridges:            compileResult.TargetBridges,
			AppliedBridgesDigest:      BridgesDigest(compileResult.TargetBridges),
			AppliedListeners:          compileResult.RequiredListeners,
			RuntimeMode:               compileResult.Mode,
			ProcessReceipt:            c.processReceipt,
			AppliedAt:                 time.Now(),
		}

		if err := c.genStore.PublishStagedBundle(genID, nextGen, compileResult.ConfigYAML, snapPath, rec, c.DaemonEpoch()); err != nil {
			_ = c.writeRecoveryMarkerLocked("publish staged bundle failed: " + err.Error())
			c.setState(StateRecoveryRequired)
			return fmt.Errorf("publish staged bundle: %w", err)
		}
		if snapPath != "" && c.cfg.StoreTx != nil {
			_ = c.cfg.StoreTx.RemoveSnapshotFile(snapPath)
		}
		c.crashAt("regenerate_post_publish")

		// Step 6: durable CandidatePublished
		if err := c.transitionManifestLocked(&m, StateCandidatePublished); err != nil {
			return fmt.Errorf("transition to candidate_published: %w", err)
		}
		c.crashAt("regenerate_candidate_published")

		if compileResult.Mode != RuntimeOff {
			// Step 7: promote active config (StrictWriteAtomic)
			c.crashAt("regenerate_pre_promote")
			if c.hooks.FailAtomicConfigWrite {
				_ = c.writeRecoveryMarkerLocked("failpoint: atomic config write failed")
				c.setState(StateRecoveryRequired)
				return fmt.Errorf("failpoint: atomic config write failed")
			}
			if err := strictfs.StrictWriteAtomic(c.activeConfigFile, compileResult.ConfigYAML, 0600); err != nil {
				_ = c.writeRecoveryMarkerLocked("promote candidate config failed: " + err.Error())
				c.setState(StateRecoveryRequired)
				return fmt.Errorf("promote candidate to active: %w", err)
			}
			c.crashAt("regenerate_mid_promote")

			// Step 8: durable ConfigPromoted
			if err := c.transitionManifestLocked(&m, StateConfigPromoted); err != nil {
				return fmt.Errorf("transition to config_promoted: %w", err)
			}
			c.crashAt("regenerate_post_promote")

			// Step 9: restart and verify target process/listeners (restartControlledLocked)
			c.crashAt("regenerate_restart")
			if err := c.restartControlledLocked(ctx, m.TxID, nextGen, compileResult.ConfigDigest, compileResult.RequiredListeners); err != nil {
				_ = c.writeRecoveryMarkerLocked(fmt.Sprintf("[%s] restart mihomo with regenerated config failed: %v", m.TxID, err))
				c.setState(StateRecoveryRequired)
				return fmt.Errorf("restart mihomo with regenerated config: %w", err)
			}
			c.crashAt("regenerate_mid_restart")
		} else {
			// Step 7 & 9 in RuntimeOff: Transactional ordering:
			// 1. First stop the operator and verify postcondition (PID == 0).
			// Active config remains intact on disk so that a stop failure does not leave an orphaned running process without config!
			c.crashAt("regenerate_restart")
			if err := c.stopControlledLocked(ctx, m.TxID, "regenerate RuntimeOff"); err != nil {
				return fmt.Errorf("stop operator for RuntimeOff: %w", err)
			}
			c.processReceipt = nil
			c.crashAt("regenerate_mid_restart")

			// 2. Unlink active config strictly (fail closed on any error)
			c.crashAt("regenerate_pre_promote")
			if c.hooks.FailActiveConfigUnlink {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("failpoint: active config unlink failed")
				return fmt.Errorf("failpoint: active config unlink failed")
			}
			if err := strictfs.StrictUnlink(c.activeConfigFile); err != nil && !os.IsNotExist(err) {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("unlink active config for RuntimeOff failed: " + err.Error())
				return fmt.Errorf("unlink active config for RuntimeOff: %w", err)
			}
			if statErr := c.statActiveConfigAbsenceLocked(); statErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked(statErr.Error())
				return statErr
			}
			c.crashAt("regenerate_mid_promote")

			// Step 8: durable ConfigPromoted
			if err := c.transitionManifestLocked(&m, StateConfigPromoted); err != nil {
				return fmt.Errorf("transition to config_promoted: %w", err)
			}
			c.crashAt("regenerate_post_promote")
		}
		c.crashAt("regenerate_mid_restart")

		// Step 10: durable RuntimeVerified (and sync bridges)
		if err := c.transitionManifestLocked(&m, StateRuntimeVerified); err != nil {
			return fmt.Errorf("transition to runtime_verified: %w", err)
		}
		c.crashAt("regenerate_post_restart")
		if c.cfg.BridgeRuntime != nil {
			cb, bErr := c.cfg.BridgeRuntime.ListActiveBridges(ctx)
			if bErr != nil {
				c.setState(StateRecoveryRequired)
				_ = c.writeRecoveryMarkerLocked("list active bridges during regeneration failed: " + bErr.Error())
				return fmt.Errorf("list active bridges during regeneration: %w", bErr)
			}
			if len(compileResult.TargetBridges) > 0 || len(cb) > 0 {
				c.crashAt("regenerate_bridge_sync")
				if err := c.transitionManifestLocked(&m, StateBridgesReconciling); err != nil {
					return fmt.Errorf("transition to bridges_reconciling: %w", err)
				}
				if sErr := c.syncBridgesLocked(ctx, &m, cb, compileResult.TargetBridges); sErr != nil {
					return fmt.Errorf("sync bridges during regeneration: %w", sErr)
				}
				c.crashAt("regenerate_post_bridge")
			}
		}

		// Step 11: staged commit via executeStagedCommitLocked
		rec.ProcessReceipt = c.processReceipt
		if err := c.executeStagedCommitLocked(ctx, &m, rec, true); err != nil {
			return fmt.Errorf("staged commit during regeneration: %w", err)
		}

		c.log("info", "reconcile", fmt.Sprintf("successfully regenerated and applied generation %s", genID))
		return nil

	case "clear_marker":
		return errors.New("clear_marker is permanently forbidden: administrative recovery requires verifiable rollback_to_lkg or regenerate_from_desired")

	default:
		return fmt.Errorf("unsupported recovery action: %s", action)
	}
}

const maxRecoveryMarkerSizeBytes = 1024

// sanitizeRecoveryMarkerFact parses and sanitizes the on-disk recovery marker file
// using a strict bounded allowlist projection. Raw marker bytes are never echoed.
func sanitizeRecoveryMarkerFact(filePath string) (string, time.Time, bool) {
	fi, err := os.Lstat(filePath)
	if err != nil {
		return "", time.Time{}, false
	}
	modTime := fi.ModTime()
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return "invalid_marker_file", modTime, true
	}
	if fi.Size() > maxRecoveryMarkerSizeBytes {
		return "oversized_marker", modTime, true
	}
	if fi.Size() == 0 {
		return "empty_marker", modTime, true
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "unreadable_marker", modTime, true
	}
	if len(data) == 0 {
		return "empty_marker", modTime, true
	}

	// Reject binary content (null bytes or unprintable non-ASCII control characters)
	for _, b := range data {
		if b == 0 || (b < 0x20 && b != '\r' && b != '\n' && b != '\t') {
			return "binary_marker", modTime, true
		}
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "empty_marker", modTime, true
	}

	// Bounded reason classifier: only known reason categories / codes are mapped.
	// Arbitrary untyped prose or unknown content is never echoed.
	lower := strings.ToLower(trimmed)

	// Connection failure category
	if strings.HasPrefix(lower, "failed connecting to ") {
		return "connection_failed", modTime, true
	}

	switch {
	case strings.Contains(lower, "initial fault"):
		return "initial_fault", modTime, true
	case strings.Contains(lower, "fault-marker") || strings.Contains(lower, "fault_marker"):
		return "fault_marker", modTime, true
	case strings.Contains(lower, "needs regen"):
		return "needs_regen", modTime, true
	case strings.Contains(lower, "corrupt state") || strings.Contains(lower, "corrupted state"):
		return "corrupted_state", modTime, true
	case strings.Contains(lower, "pre-existing failure") || strings.Contains(lower, "pre-existing failure marker"):
		return "pre_existing_failure", modTime, true
	case strings.Contains(lower, "migration recovery failed"):
		return "migration_recovery_failed", modTime, true
	case strings.Contains(lower, "transaction recovery failed"):
		return "transaction_recovery_failed", modTime, true
	case strings.Contains(lower, "legacy bridge startup migration failed") || strings.Contains(lower, "legacy bridge migration"):
		return "legacy_bridge_migration_failed", modTime, true
	case strings.Contains(lower, "active config digest mismatch"):
		return "active_config_digest_mismatch", modTime, true
	case strings.Contains(lower, "backup active digest verification failed"):
		return "backup_active_digest_verification_failed", modTime, true
	case strings.Contains(lower, "backup pointer digest verification failed"):
		return "backup_pointer_digest_verification_failed", modTime, true
	case strings.Contains(lower, "unrecoverable draft journal"):
		return "unrecoverable_draft_journal", modTime, true
	case strings.Contains(lower, "quarantine failed"):
		return "quarantine_failed", modTime, true
	case strings.Contains(lower, "startup bridge registry replacement failed"):
		return "startup_bridge_registry_replacement_failed", modTime, true
	case strings.Contains(lower, "generation_mismatch"):
		return "generation_mismatch", modTime, true
	case strings.Contains(lower, "process_verification_failed"):
		return "process_verification_failed", modTime, true
	case strings.Contains(lower, "missing_verified_active"):
		return "missing_verified_active", modTime, true
	case strings.Contains(lower, "marker_mismatch"):
		return "marker_mismatch", modTime, true
	case strings.Contains(lower, "lkg pointer mismatch"):
		return "lkg_pointer_mismatch", modTime, true
	case strings.Contains(lower, "persist migration intent failed"):
		return "persist_migration_intent_failed", modTime, true
	case strings.Contains(lower, "migration publish"):
		return "migration_publish_failed", modTime, true
	case strings.Contains(lower, "migration write"):
		return "migration_write_failed", modTime, true
	case strings.Contains(lower, "migration transition"):
		return "migration_transition_failed", modTime, true
	case strings.Contains(lower, "migration failpoint"):
		return "migration_failpoint", modTime, true
	case strings.Contains(lower, "cleanup failed") || strings.Contains(lower, "unlink "):
		return "cleanup_failed", modTime, true
	case strings.Contains(lower, "fsync failed") || strings.Contains(lower, "fsync "):
		return "fsync_failed", modTime, true
	case isSafeIdentifier(trimmed):
		return trimmed, modTime, true
	default:
		return "unrecognized_recovery_marker", modTime, true
	}
}

func isSafeIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func ensureRulesDir(dir string) {
	if dir == "" {
		return
	}
	rulesDir := filepath.Join(dir, "rules")
	_ = os.MkdirAll(rulesDir, 0755)
	susaninPath := filepath.Join(rulesDir, "susanin.yaml")
	if _, err := os.Stat(susaninPath); os.IsNotExist(err) {
		_ = os.WriteFile(susaninPath, []byte("payload: []\n"), 0644)
	}
}
