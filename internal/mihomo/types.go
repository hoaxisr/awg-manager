package mihomo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ManifestState represents the sequential transaction states for configuration apply.
type ManifestState string

// CurrentBridgeIdentityVersion defines the active schema version for complete durable bridge identity.
const CurrentBridgeIdentityVersion = 1

// OperationKind specifies whether a manifest executes normal apply or administrative recovery.
type OperationKind string

const (
	OperationApply      OperationKind = "apply"
	OperationRegenerate OperationKind = "regenerate"
	OperationRollback   OperationKind = "rollback"
	OperationMigration  OperationKind = "migration"
)

var (
	ErrExecutableMismatch       = errors.New("mihomo: executable path mismatch")
	ErrForeignBridgeOwnership   = errors.New("mihomo: bridge owned by foreign entity")
	ErrControllerSocketMismatch = errors.New("mihomo: controller socket owned by alien pid")
	ErrListenerUnavailable      = errors.New("mihomo: required listener unavailable")
	ErrProcessProofFailed       = errors.New("mihomo: process proof verification failed")
	ErrVerificationFailed       = errors.New("mihomo: verification failed")
	ErrProcessNotReaped         = errors.New("mihomo: process not reaped within timeout after SIGKILL")
)

const (
	StateIdle                        ManifestState = "idle"
	StatePreSnapshotWriteIntent      ManifestState = "pre_snapshot_write_intent"
	StateSnapshotSecured             ManifestState = "snapshot_secured"
	StateCandidateWriteIntent        ManifestState = "candidate_write_intent"
	StateCandidateBuilt              ManifestState = "candidate_built"
	StateCandidatePublished          ManifestState = "candidate_published"
	StateSwapIntent                  ManifestState = "swap_intent"
	StateSwapApplied                 ManifestState = "swap_applied"
	StateSwapVerified                ManifestState = "swap_verified"
	StateRuntimeIntent               ManifestState = "runtime_intent"
	StateRuntimeApplied              ManifestState = "runtime_applied"
	StateRuntimeVerified             ManifestState = "runtime_verified"
	StateBridgesReconciling          ManifestState = "bridges_reconciling"
	StateCommitIntent                ManifestState = "commit_intent"
	StateCommitted                   ManifestState = "committed"
	StateAbortInProgress             ManifestState = "abort_in_progress"
	StateRollbackInProgress          ManifestState = "rollback_in_progress"
	StateRolledBack                  ManifestState = "rolled_back"
	StateTerminalCleanup             ManifestState = "terminal_cleanup"
	StateRecoveryRequired            ManifestState = "recovery_required"
	StateRecoveryIntent              ManifestState = "recovery_intent"
	StateConfigPromoted              ManifestState = "config_promoted"
	StateRecoveryCommitted           ManifestState = "recovery_committed"
	StateRollbackIntent              ManifestState = "rollback_intent"
	StateRollbackStoreRestored       ManifestState = "rollback_store_restored"
	StateRollbackConfigPromoted      ManifestState = "rollback_config_promoted"
	StateRollbackRuntimeVerified     ManifestState = "rollback_runtime_verified"
	StateRollbackBridgesVerified     ManifestState = "rollback_bridges_verified"
	StateRollbackCommitted           ManifestState = "rollback_committed"
	StateMigrationIntent             ManifestState = "migration_intent"
	StateMigrationBundlePublished    ManifestState = "candidate_bundle_published"
	StateMigrationActiveWriteIntent  ManifestState = "verified_active_write_intent"
	StateMigrationActiveWritten      ManifestState = "verified_active_written"
	StateMigrationPointerWriteIntent ManifestState = "pointer_write_intent"
	StateMigrationPointerWritten     ManifestState = "pointer_written"
	StateMigrationCommitted          ManifestState = "migration_committed"
)

// DraftState represents the states in DraftJournal.
type DraftState string

const (
	DraftSnapshotSecured DraftState = "snapshot_secured"
	DraftStoreMutated    DraftState = "store_mutated"
	DraftPending         DraftState = "pending"
	DraftConsuming       DraftState = "consuming"
)

// RuntimeMode defines the enforcement level of the Mihomo routing engine.
type RuntimeMode string

const (
	RuntimeEnforced   RuntimeMode = "enforced"
	RuntimePermissive RuntimeMode = "permissive"
	RuntimeOff        RuntimeMode = "off"
)

// ProcessState represents whether the runtime process is expected to run.
type ProcessState string

const (
	ProcessRunning ProcessState = "running"
	ProcessStopped ProcessState = "stopped"
)

// BridgeRef uniquely references an active or candidate proxy bridge interface.
type BridgeRef struct {
	ProxyIndex      int    `json:"proxy_index"`
	ProxyInterface  string `json:"proxy_interface"`
	KernelInterface string `json:"kernel_interface"`
	ListenPort      int    `json:"listen_port,omitempty"`
	LegacyOwner     string `json:"legacy_owner,omitempty"`
	OwnerUUID       string `json:"owner_uuid,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
}

// SlotKey returns a stable key identifying the bridge's interface slot.
func (r BridgeRef) SlotKey() string {
	if r.ProxyIndex > 0 && r.ProxyInterface != "" && r.KernelInterface != "" {
		return fmt.Sprintf("%d:%s:%s", r.ProxyIndex, r.ProxyInterface, r.KernelInterface)
	}
	if r.ProxyIndex > 0 {
		return fmt.Sprintf("%d", r.ProxyIndex)
	}
	if r.ProxyInterface != "" {
		return r.ProxyInterface
	}
	if r.KernelInterface != "" {
		return r.KernelInterface
	}
	return "0"
}

// ValidateComplete verifies that every required identity field is valid and present.
func (r BridgeRef) ValidateComplete() error {
	if r.ProxyIndex <= 0 {
		return fmt.Errorf("bridge ref proxy index %d must be positive", r.ProxyIndex)
	}
	expectedIface := fmt.Sprintf("Proxy%d", r.ProxyIndex)
	if r.ProxyInterface != expectedIface {
		return fmt.Errorf("bridge ref proxy interface mismatch: got %q, expected %q", r.ProxyInterface, expectedIface)
	}
	if strings.TrimSpace(r.KernelInterface) == "" {
		return errors.New("bridge ref kernel interface cannot be empty")
	}
	if strings.TrimSpace(r.OwnerUUID) == "" {
		return errors.New("bridge ref owner uuid cannot be empty")
	}
	if r.ListenPort < 1 || r.ListenPort > 65535 {
		return fmt.Errorf("bridge ref invalid listen port %d: must be in range 1..65535", r.ListenPort)
	}
	return nil
}

// ValidateCompleteForPublish verifies that the ref is complete and ready for publishing.
func (r BridgeRef) ValidateCompleteForPublish() error {
	return r.ValidateComplete()
}

// IsLegacy reports true if any required identity field is missing or invalid.
func (r BridgeRef) IsLegacy() bool {
	return r.ValidateComplete() != nil
}

// SameOwnerAndEndpoint reports true if two refs share the same slot, canonical owner and listen port.
func (r BridgeRef) SameOwnerAndEndpoint(other BridgeRef) bool {
	if r.SlotKey() != other.SlotKey() {
		return false
	}
	if r.ListenPort != other.ListenPort {
		return false
	}
	if r.OwnerUUID != "" && other.OwnerUUID != "" {
		return r.OwnerUUID == other.OwnerUUID
	}
	if r.LegacyOwner != "" && other.LegacyOwner != "" {
		return r.LegacyOwner == other.LegacyOwner
	}
	return false
}

func (r BridgeRef) Digest() string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// BridgesDigest computes a deterministic SHA256 hex digest of a slice of BridgeRef.
func BridgesDigest(bridges []BridgeRef) string {
	if len(bridges) == 0 {
		return ""
	}
	sorted := make([]BridgeRef, len(bridges))
	copy(sorted, bridges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ProxyIndex != sorted[j].ProxyIndex {
			return sorted[i].ProxyIndex < sorted[j].ProxyIndex
		}
		if sorted[i].ProxyInterface != sorted[j].ProxyInterface {
			return sorted[i].ProxyInterface < sorted[j].ProxyInterface
		}
		if sorted[i].KernelInterface != sorted[j].KernelInterface {
			return sorted[i].KernelInterface < sorted[j].KernelInterface
		}
		if sorted[i].ListenPort != sorted[j].ListenPort {
			return sorted[i].ListenPort < sorted[j].ListenPort
		}
		if sorted[i].OwnerUUID != sorted[j].OwnerUUID {
			return sorted[i].OwnerUUID < sorted[j].OwnerUUID
		}
		return sorted[i].LegacyOwner < sorted[j].LegacyOwner
	})
	b, _ := json.Marshal(sorted)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ObservedBridge reports the actual OS state of a bridge interface.
type ObservedBridge struct {
	BridgeRef
	Exists     bool   `json:"exists"`
	Up         bool   `json:"up"`
	AssignedIP string `json:"assigned_ip,omitempty"`
}

// BridgeOpState defines the execution phase of a bridge creation or withdrawal.
type BridgeOpState string

const (
	BridgeOpIntent   BridgeOpState = "intent"
	BridgeOpApplied  BridgeOpState = "applied"
	BridgeOpVerified BridgeOpState = "verified"
)

// BridgeOperation journals a single bridge lifecycle operation.
type BridgeOperation struct {
	OperationID    string        `json:"operation_id"`
	Action         string        `json:"action"` // "create" | "withdraw" | "update"
	TargetDigest   string        `json:"target_digest"`
	BridgeRef      BridgeRef     `json:"bridge_ref,omitempty"`
	State          BridgeOpState `json:"state"`
	Attempts       int           `json:"attempts"`
	LastError      string        `json:"last_error,omitempty"`
	ObservedResult string        `json:"observed_result,omitempty"`
}

// ListenerSpec defines an inbound listener required for traffic interception.
type ListenerSpec struct {
	Network  string `json:"network"` // "tcp" | "udp"
	Family   string `json:"family"`  // "ipv4" | "ipv6" | "any"
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Purpose  string `json:"purpose"`
	Protocol string `json:"protocol,omitempty"` // Backwards compatibility alias for Network
}

// GetNetwork returns the effective network ("tcp" or "udp").
func (l ListenerSpec) GetNetwork() string {
	if l.Network != "" {
		return l.Network
	}
	return l.Protocol
}

// RuntimeProcessIdentity contains facts about the OS process verified from /proc.
type RuntimeProcessIdentity struct {
	PID            int    `json:"pid"`
	ProcStartTicks uint64 `json:"proc_start_ticks"`
	Generation     uint64 `json:"generation"`
	ExecutablePath string `json:"executable_path"`
	ConfigDir      string `json:"config_dir"`
}

// ProcessReceipt supplements RuntimeProcessIdentity with coordinator metadata.
type ProcessReceipt struct {
	RuntimeProcessIdentity
	DaemonEpoch       string    `json:"daemon_epoch"`
	AppliedGeneration uint64    `json:"applied_generation"`
	VerifiedAt        time.Time `json:"verified_at"`
}

// CompileResult represents the output of pure configuration compilation.
type CompileResult struct {
	ConfigYAML        []byte
	ConfigDigest      string
	InputDigest       string
	RequiredListeners []ListenerSpec
	TargetBridges     []BridgeRef
	Mode              RuntimeMode
}

// BridgeTransition partitions bridge adjustments into additions, removals, and unchanges.
type BridgeTransition struct {
	ToCreate   []BridgeRef
	ToWithdraw []BridgeRef
	ToPreserve []BridgeRef
}

// DraftJournal tracks the lifecycle of pending drafts in the native store before apply.
type DraftJournal struct {
	Version                  int        `json:"version"` // 1
	TxID                     string     `json:"txid"`
	State                    DraftState `json:"state"`
	AssociatedApplyTxID      string     `json:"associated_apply_txid,omitempty"`
	DraftSnapshotFile        string     `json:"draft_snapshot_file"`
	BaseDesiredStoreDigest   string     `json:"base_desired_store_digest"`
	TargetDesiredStoreDigest string     `json:"target_desired_store_digest"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// SourceID identifies an authoritative compile input component.
type SourceID string

const (
	SourceSettings        SourceID = "settings"
	SourceRouterConfig    SourceID = "router_config"
	SourceSubscriptions   SourceID = "slots_subscriptions"
	SourceTunnels         SourceID = "slots_tunnels"
	SourceAWG             SourceID = "slots_awg"
	SourceNativeResources SourceID = "native_resources"
	SourceDynamicCloud    SourceID = "dynamic_cloud"
	SourceTunIface        SourceID = "tun_iface"
)

// SourceVersionVector captures atomic revisions and SHA-256 digests across all input sources.
type SourceVersionVector struct {
	Revisions map[SourceID]uint64 `json:"revisions,omitempty"`
	Digests   map[SourceID]string `json:"digests,omitempty"`
}

func (v SourceVersionVector) ComputeDigest() string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// PendingInputRecord tracks durable convergence requests across restarts.
type PendingInputRecord struct {
	Version                int       `json:"version"` // 1
	MonotonicGeneration    uint64    `json:"monotonic_generation"`
	TxID                   string    `json:"txid"`
	OperationID            string    `json:"operation_id,omitempty"`
	BaseAppliedInputDigest string    `json:"base_applied_input_digest,omitempty"`
	TargetInputDigest      string    `json:"target_input_digest,omitempty"`
	Source                 string    `json:"source,omitempty"`
	Sources                []string  `json:"sources,omitempty"`
	Reason                 string    `json:"reason,omitempty"`
	State                  string    `json:"state"` // "intent" | "pending" | "converging"
	CreatedAt              time.Time `json:"created_at"`
	MergedAt               time.Time `json:"merged_at,omitempty"`
}

// AppliedGenerationRecord tracks the currently verified and active configuration running in the OS.
type AppliedGenerationRecord struct {
	Version                   int             `json:"version"` // 1
	BridgeIdentityVersion     int             `json:"bridge_identity_version,omitempty"`
	Generation                uint64          `json:"generation"`
	GenerationID              string          `json:"generation_id,omitempty"`
	AppliedAt                 time.Time       `json:"applied_at"`
	AppliedStoreDigest        string          `json:"applied_store_digest"` // exact store.snapshot.json bytes
	AppliedDesiredStoreDigest string          `json:"applied_desired_store_digest,omitempty"`
	AppliedConfigDigest       string          `json:"applied_config_digest"`
	AppliedInputDigest        string          `json:"applied_input_digest"`
	AppliedListeners          []ListenerSpec  `json:"applied_listeners"`
	AppliedBridges            []BridgeRef     `json:"applied_bridges"`
	AppliedBridgesDigest      string          `json:"applied_bridges_digest,omitempty"`
	RuntimeMode               RuntimeMode     `json:"runtime_mode"`
	ApplyPath                 ApplyPath       `json:"apply_path,omitempty"`
	ProcessReceipt            *ProcessReceipt `json:"process_receipt,omitempty"`
}

// GenerationManifest is the metadata preserved in each immutable generation bundle.
type GenerationManifest struct {
	Version                   int            `json:"version"` // 1
	BridgeIdentityVersion     int            `json:"bridge_identity_version,omitempty"`
	GenerationID              string         `json:"generation_id"`
	GenerationNumber          uint64         `json:"generation_number"`
	ArchivedAt                time.Time      `json:"archived_at"`
	AppliedStoreDigest        string         `json:"applied_store_digest"` // exact store.snapshot.json bytes
	AppliedDesiredStoreDigest string         `json:"applied_desired_store_digest,omitempty"`
	AppliedConfigDigest       string         `json:"applied_config_digest"`
	AppliedInputDigest        string         `json:"applied_input_digest"`
	AppliedListeners          []ListenerSpec `json:"applied_listeners"`
	AppliedBridges            []BridgeRef    `json:"applied_bridges"`
	AppliedBridgesDigest      string         `json:"applied_bridges_digest,omitempty"`
	RuntimeMode               RuntimeMode    `json:"runtime_mode"`
}

// LKGPointer points to the current verified last-known-good generation bundle.
type LKGPointer struct {
	Version                   int       `json:"version"` // 1
	GenerationID              string    `json:"generation_id"`
	GenerationNumber          uint64    `json:"generation_number"`
	AppliedConfigDigest       string    `json:"applied_config_digest"`
	AppliedStoreDigest        string    `json:"applied_store_digest"`
	AppliedDesiredStoreDigest string    `json:"applied_desired_store_digest,omitempty"`
	UpdatedEpoch              string    `json:"updated_epoch"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// TransactionManifest records the active compile/apply transaction state on disk.
type TransactionManifest struct {
	Version   int           `json:"version"` // 1
	Sequence  uint64        `json:"sequence"`
	TxID      string        `json:"txid"`
	State     ManifestState `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`

	// Cross-generation pointers
	PreviousGenerationID       string `json:"previous_generation_id,omitempty"`
	RollbackTargetGenerationID string `json:"rollback_target_generation_id,omitempty"`
	CandidateGenerationID      string `json:"candidate_generation_id,omitempty"`
	LKGGenerationID            string `json:"lkg_generation_id,omitempty"`
	VerifiedActiveGeneration   uint64 `json:"verified_active_generation"`

	// Digests and Files
	BaseAppliedStoreDigest        string `json:"base_applied_store_digest,omitempty"`
	BaseAppliedDesiredStoreDigest string `json:"base_applied_desired_store_digest,omitempty"`
	BaseDesiredStoreDigest        string `json:"base_desired_store_digest,omitempty"`
	TargetDesiredStoreDigest      string `json:"target_desired_store_digest,omitempty"`
	BaseAppliedInputDigest        string `json:"base_applied_input_digest,omitempty"`
	TargetInputDigest             string `json:"target_input_digest,omitempty"`

	PreMutationStoreSnapshotFile           string `json:"pre_mutation_store_snapshot_file,omitempty"`
	PreMutationStoreDigest                 string `json:"pre_mutation_store_digest,omitempty"`
	CandidatePostMutationStoreSnapshotFile string `json:"candidate_post_mutation_store_snapshot_file,omitempty"`
	CandidatePostMutationStoreDigest       string `json:"candidate_post_mutation_store_digest,omitempty"`

	PreviousConfigDigest  string `json:"previous_config_digest,omitempty"`
	CandidateConfigDigest string `json:"candidate_config_digest,omitempty"`
	CandidateConfigFile   string `json:"candidate_config_file,omitempty"`
	ConfigPresent         bool   `json:"config_present"`

	// Operation Kind
	OperationKind OperationKind `json:"operation_kind,omitempty"`
	ApplyPath     ApplyPath     `json:"apply_path,omitempty"`

	// Recovery Archive & Prior State
	PreviousManifestDigest       string `json:"previous_manifest_digest,omitempty"`
	ArchivedManifestFile         string `json:"archived_manifest_file,omitempty"`
	PreviousLKGGenerationID      string `json:"previous_lkg_generation_id,omitempty"`
	PreviousLKGPointerDigest     string `json:"previous_lkg_pointer_digest,omitempty"`
	PreviousRecordDigest         string `json:"previous_record_digest,omitempty"`
	PreviousRecordFile           string `json:"previous_record_file,omitempty"`
	PreviousLKGPointerFile       string `json:"previous_lkg_pointer_file,omitempty"`
	CandidateRecordDigest        string `json:"candidate_record_digest,omitempty"`
	CandidatePointerDigest       string `json:"candidate_pointer_digest,omitempty"`
	StartedFromRecoveryRequired  bool   `json:"started_from_recovery_required,omitempty"`
	PreviousRecoveryMarkerDigest string `json:"previous_recovery_marker_digest,omitempty"`

	// Bridges
	PreviousBridges       []BridgeRef       `json:"previous_bridges,omitempty"`
	PreviousBridgesDigest string            `json:"previous_bridges_digest,omitempty"`
	TargetBridges         []BridgeRef       `json:"target_bridges,omitempty"`
	TargetBridgesDigest   string            `json:"target_bridges_digest,omitempty"`
	BridgeOperations      []BridgeOperation `json:"bridge_operations,omitempty"`

	// Runtime Mode
	DesiredMode RuntimeMode `json:"desired_mode"`

	// Errors and Outcomes
	FailureCode      string `json:"failure_code,omitempty"`
	FailedStage      string `json:"failed_stage,omitempty"`
	SanitizedSummary string `json:"sanitized_summary,omitempty"`
	FailureReason    string `json:"failure_reason,omitempty"`
}

// MigrationJournal records one-time legacy resource migration state.
type MigrationJournal struct {
	Version     int       `json:"version"` // 1
	State       string    `json:"state"`   // "in_progress" | "completed"
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Imported    []string  `json:"imported"`
}

// CleanupJournal records items that must be securely removed during rollback or cleanup.
type CleanupJournal struct {
	Version  int      `json:"version"` // 1
	Sequence uint64   `json:"sequence"`
	TxID     string   `json:"txid"`
	Files    []string `json:"files,omitempty"` // basenames of files in ConfigDir
}

// ValidateSchema verifies CleanupJournal integrity.
func (cj *CleanupJournal) ValidateSchema() error {
	if cj.Version != 1 {
		return fmt.Errorf("unsupported cleanup journal version: %d (want 1)", cj.Version)
	}
	if err := ValidateTxID(cj.TxID); err != nil {
		return fmt.Errorf("cleanup journal txid: %w", err)
	}
	for _, file := range cj.Files {
		if err := ValidateBasename(file); err != nil {
			return fmt.Errorf("cleanup journal file basename invalid: %w", err)
		}
	}
	return nil
}

// EvidenceExport contains allowlisted structural facts for diagnostic analysis without secrets.
type EvidenceExport struct {
	ExportedAt        time.Time      `json:"exported_at"`
	DaemonEpoch       string         `json:"daemon_epoch"`
	CurrentState      ManifestState  `json:"current_state"`
	ActiveGeneration  uint64         `json:"active_generation"`
	FailureCode       string         `json:"failure_code,omitempty"`
	FailedStage       string         `json:"failed_stage,omitempty"`
	SanitizedSummary  string         `json:"sanitized_summary,omitempty"`
	ConfigDigest      string         `json:"config_digest,omitempty"`
	ConfigSizeBytes   int64          `json:"config_size_bytes,omitempty"`
	RuleCount         int            `json:"rule_count"`
	ProxyGroupCount   int            `json:"proxy_group_count"`
	TargetInputDigest string         `json:"target_input_digest,omitempty"`
	ObservedListeners []ListenerSpec `json:"observed_listeners,omitempty"`
	ActiveBridges     []string       `json:"active_bridges,omitempty"`
	QuarantineList    []string       `json:"quarantine_basenames,omitempty"`
}

// MihomoOperator defines process management methods required by ApplyCoordinator.
type MihomoOperator interface {
	IsRunning() (bool, int)
	StopAndWait(ctx context.Context) error
	Start() error
}

// ConfigReloader triggers dynamic reload of active configuration via external controller.
type ConfigReloader interface {
	ReloadConfig(ctx context.Context, configPath string, force bool) error
}

// NativeStoreTx provides the transactional capabilities required by ApplyCoordinator.
type NativeStoreTx interface {
	SnapshotFilePath(txid string) (string, error)
	CreateSnapshotFile(txid string) (string, error)
	CreateSnapshotFileAt(txid, targetPath string) (digest string, err error)
	RestoreSnapshotFile(snapshotPath string) error
	RemoveSnapshotFile(snapshotPath string) error
	CurrentDigest() (string, error)
	CurrentDesiredDigest() (string, error)
	CurrentSnapshotDigest() (string, error)
	ListBridges() []BridgeRef
}

// BridgeRuntime abstracts bridge interface management in the OS (NDMS / policy routing).
type BridgeRuntime interface {
	ApplyBridges(ctx context.Context, bridges []BridgeRef) error
	WithdrawBridges(ctx context.Context, bridges []BridgeRef) error
	VerifyBridges(ctx context.Context, bridges []BridgeRef) error
	ListActiveBridges(ctx context.Context) ([]BridgeRef, error)
}

// ExactBridgeRuntime provides exact, atomic, verifiable bridge operations.
type ExactBridgeRuntime interface {
	PublishBridge(ctx context.Context, ref BridgeRef) error
	WithdrawBridge(ctx context.Context, ref BridgeRef) error
	InspectBridge(ctx context.Context, ref BridgeRef) (ObservedBridge, error)
	ListObservedBridges(ctx context.Context) ([]ObservedBridge, error)
}

// ActiveTransactionRoles defines the role-aware bridge identity references in an active transaction.
type ActiveTransactionRoles struct {
	TxID                 string      `json:"txid"`
	PreviousGenerationID string      `json:"previous_generation_id,omitempty"`
	TargetGenerationID   string      `json:"target_generation_id,omitempty"`
	PreviousBridges      []BridgeRef `json:"previous_bridges,omitempty"`
	TargetBridges        []BridgeRef `json:"target_bridges,omitempty"`
}

// DurableBridgeRegistry is an interface implemented by bridge runtimes
// that maintain an in-memory cache of durable bridge references and active transaction roles.
type DurableBridgeRegistry interface {
	ReplaceDurableBridges(bridges []BridgeRef) error
	SetActiveTransactionRoles(roles *ActiveTransactionRoles) error
}

// ConfigFileValidator validates candidate configuration files.
type ConfigFileValidator interface {
	ValidateConfigFile(ctx context.Context, configPath string) error
}

var (
	txidRegex = regexp.MustCompile(`^[0-9]{14,24}(?:-[a-zA-Z0-9_-]+)?$`)
	baseRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
)

// ValidateTxID checks if a transaction identifier is well-formed.
func ValidateTxID(id string) error {
	if !txidRegex.MatchString(id) {
		return fmt.Errorf("invalid transaction ID %q: must match %s", id, txidRegex.String())
	}
	return nil
}

// ValidateBasename ensures a filename contains no directory traversal elements.
func ValidateBasename(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("basename cannot be empty, '.', or '..'")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("basename %q contains path separators", name)
	}
	if !baseRegex.MatchString(name) {
		return fmt.Errorf("basename %q contains invalid characters", name)
	}
	return nil
}

// AssertPathConfined verifies that targetPath is strictly inside allowedDir.
func AssertPathConfined(targetPath, allowedDir string) error {
	cleanTarget := filepath.Clean(targetPath)
	cleanAllowed := filepath.Clean(allowedDir)

	rel, err := filepath.Rel(cleanAllowed, cleanTarget)
	if err != nil {
		return fmt.Errorf("determine relative path of %s from %s: %w", cleanTarget, cleanAllowed, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s escapes allowed directory %s (rel: %s)", cleanTarget, cleanAllowed, rel)
	}
	return nil
}

// ValidateSchema verifies TransactionManifest integrity.
func (m *TransactionManifest) ValidateSchema() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version: %d (want 1)", m.Version)
	}
	if err := ValidateTxID(m.TxID); err != nil {
		return fmt.Errorf("manifest txid: %w", err)
	}
	if m.State == "" {
		return errors.New("manifest state cannot be empty")
	}
	seenTargetSlots := make(map[string]struct{}, len(m.TargetBridges))
	for _, br := range m.TargetBridges {
		slot := br.SlotKey()
		if _, exists := seenTargetSlots[slot]; exists {
			return fmt.Errorf("duplicate slot key %q in target_bridges", slot)
		}
		seenTargetSlots[slot] = struct{}{}
	}
	seenPrevSlots := make(map[string]struct{}, len(m.PreviousBridges))
	for _, br := range m.PreviousBridges {
		slot := br.SlotKey()
		if _, exists := seenPrevSlots[slot]; exists {
			return fmt.Errorf("duplicate slot key %q in previous_bridges", slot)
		}
		seenPrevSlots[slot] = struct{}{}
	}
	if len(m.TargetBridges) > 0 {
		expectedDigest := BridgesDigest(m.TargetBridges)
		if m.TargetBridgesDigest != "" && m.TargetBridgesDigest != expectedDigest {
			return fmt.Errorf("target_bridges_digest mismatch: got %q, expected %q", m.TargetBridgesDigest, expectedDigest)
		}
	} else if m.TargetBridgesDigest != "" {
		return fmt.Errorf("target_bridges_digest must be empty when target_bridges is empty, got %q", m.TargetBridgesDigest)
	}
	if len(m.PreviousBridges) > 0 {
		expectedDigest := BridgesDigest(m.PreviousBridges)
		if m.PreviousBridgesDigest != "" && m.PreviousBridgesDigest != expectedDigest {
			return fmt.Errorf("previous_bridges_digest mismatch: got %q, expected %q", m.PreviousBridgesDigest, expectedDigest)
		}
	} else if m.PreviousBridgesDigest != "" {
		return fmt.Errorf("previous_bridges_digest must be empty when previous_bridges is empty, got %q", m.PreviousBridgesDigest)
	}
	if m.CandidateGenerationID != "" {
		if err := ValidateBasename(m.CandidateGenerationID); err != nil {
			return fmt.Errorf("manifest candidate_generation_id: %w", err)
		}
	}
	if m.PreviousGenerationID != "" {
		if err := ValidateBasename(m.PreviousGenerationID); err != nil {
			return fmt.Errorf("manifest previous_generation_id: %w", err)
		}
	}
	if m.LKGGenerationID != "" {
		if err := ValidateBasename(m.LKGGenerationID); err != nil {
			return fmt.Errorf("manifest lkg_generation_id: %w", err)
		}
	}
	if m.RollbackTargetGenerationID != "" {
		if err := ValidateBasename(m.RollbackTargetGenerationID); err != nil {
			return fmt.Errorf("manifest rollback_target_generation_id: %w", err)
		}
	}
	if m.PreviousLKGGenerationID != "" {
		if err := ValidateBasename(m.PreviousLKGGenerationID); err != nil {
			return fmt.Errorf("manifest previous_lkg_generation_id: %w", err)
		}
	}
	return nil
}

// Clone creates a deep copy of the transaction manifest.
func (m *TransactionManifest) Clone() *TransactionManifest {
	if m == nil {
		return nil
	}
	cpy := *m
	if m.BridgeOperations != nil {
		cpy.BridgeOperations = make([]BridgeOperation, len(m.BridgeOperations))
		copy(cpy.BridgeOperations, m.BridgeOperations)
	}
	if m.PreviousBridges != nil {
		cpy.PreviousBridges = make([]BridgeRef, len(m.PreviousBridges))
		copy(cpy.PreviousBridges, m.PreviousBridges)
	}
	if m.TargetBridges != nil {
		cpy.TargetBridges = make([]BridgeRef, len(m.TargetBridges))
		copy(cpy.TargetBridges, m.TargetBridges)
	}
	return &cpy
}

// ValidateSchemaForPhase verifies the presence of required fields based on the manifest's current state.
func (m *TransactionManifest) ValidateSchemaForPhase() error {
	if err := m.ValidateSchema(); err != nil {
		return err
	}

	validateStructuralPath := func(field, path, txid string) error {
		if path == "" {
			return nil
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "\\") {
			return fmt.Errorf("%s must be an absolute path: %s", field, path)
		}
		if filepath.ToSlash(filepath.Clean(path)) != filepath.ToSlash(path) {
			return fmt.Errorf("%s is not clean: %s", field, path)
		}
		if strings.Contains(path, "..") {
			return fmt.Errorf("%s contains path traversal", field)
		}
		if txid != "" && !strings.Contains(filepath.Base(path), txid) {
			return fmt.Errorf("%s basename %q does not contain txid %q", field, filepath.Base(path), txid)
		}
		return nil
	}

	if err := validateStructuralPath("pre_mutation_store_snapshot_file", m.PreMutationStoreSnapshotFile, m.TxID); err != nil {
		return err
	}
	if err := validateStructuralPath("candidate_post_mutation_store_snapshot_file", m.CandidatePostMutationStoreSnapshotFile, m.TxID); err != nil {
		return err
	}
	requiredConfigTxID := m.TxID
	if m.OperationKind == OperationRollback || m.OperationKind == OperationMigration {
		requiredConfigTxID = ""
	}
	if err := validateStructuralPath("candidate_config_file", m.CandidateConfigFile, requiredConfigTxID); err != nil {
		return err
	}
	if err := validateStructuralPath("previous_record_file", m.PreviousRecordFile, m.TxID); err != nil {
		return err
	}
	if err := validateStructuralPath("previous_lkg_pointer_file", m.PreviousLKGPointerFile, m.TxID); err != nil {
		return err
	}

	switch m.State {
	case StateSnapshotSecured, StateCandidateWriteIntent, StateCandidateBuilt, StateCandidatePublished,
		StateSwapIntent, StateSwapApplied, StateSwapVerified,
		StateRuntimeIntent, StateRuntimeApplied, StateRuntimeVerified,
		StateBridgesReconciling, StateCommitIntent, StateCommitted,
		StateRollbackInProgress, StateRolledBack, StateRollbackIntent,
		StateRollbackStoreRestored, StateRollbackConfigPromoted, StateRollbackRuntimeVerified,
		StateRollbackBridgesVerified, StateRollbackCommitted,
		StateMigrationIntent, StateMigrationBundlePublished, StateMigrationActiveWriteIntent,
		StateMigrationActiveWritten, StateMigrationPointerWriteIntent, StateMigrationPointerWritten,
		StateMigrationCommitted:
		if len(m.PreviousBridges) > 0 {
			if m.PreviousBridgesDigest == "" {
				return errors.New("previous_bridges_digest is required when previous_bridges is non-empty")
			}
			expectedDigest := BridgesDigest(m.PreviousBridges)
			if m.PreviousBridgesDigest != expectedDigest {
				return fmt.Errorf("previous_bridges_digest mismatch: got %q, expected %q", m.PreviousBridgesDigest, expectedDigest)
			}
			for i, b := range m.PreviousBridges {
				if m.OperationKind == OperationMigration {
					if b.KernelInterface == "" {
						return fmt.Errorf("previous_bridges[%d] missing kernel interface", i)
					}
				} else {
					if err := b.ValidateComplete(); err != nil {
						return fmt.Errorf("previous_bridges[%d] incomplete: %w", i, err)
					}
				}
			}
		}
	}

	switch m.State {
	case StateCandidateWriteIntent, StateCandidateBuilt, StateCandidatePublished,
		StateSwapIntent, StateSwapApplied, StateSwapVerified,
		StateRuntimeIntent, StateRuntimeApplied, StateRuntimeVerified,
		StateBridgesReconciling, StateCommitIntent, StateCommitted,
		StateRollbackInProgress, StateRolledBack, StateRollbackIntent,
		StateRollbackStoreRestored, StateRollbackConfigPromoted, StateRollbackRuntimeVerified,
		StateRollbackBridgesVerified, StateRollbackCommitted,
		StateMigrationIntent, StateMigrationBundlePublished, StateMigrationActiveWriteIntent,
		StateMigrationActiveWritten, StateMigrationPointerWriteIntent, StateMigrationPointerWritten,
		StateMigrationCommitted:
		if len(m.TargetBridges) > 0 {
			if m.TargetBridgesDigest == "" {
				return errors.New("target_bridges_digest is required when target_bridges is non-empty")
			}
			expectedDigest := BridgesDigest(m.TargetBridges)
			if m.TargetBridgesDigest != expectedDigest {
				return fmt.Errorf("target_bridges_digest mismatch: got %q, expected %q", m.TargetBridgesDigest, expectedDigest)
			}
			for i, b := range m.TargetBridges {
				if err := b.ValidateComplete(); err != nil {
					return fmt.Errorf("target_bridges[%d] incomplete: %w", i, err)
				}
			}
		}
	}

	switch m.State {
	case StatePreSnapshotWriteIntent:
		if m.PreMutationStoreSnapshotFile == "" {
			return errors.New("pre_mutation_store_snapshot_file is required in pre_snapshot_write_intent state")
		}
	case StateSnapshotSecured:
		if m.PreMutationStoreSnapshotFile == "" {
			return errors.New("pre_mutation_store_snapshot_file is required in snapshot_secured state")
		}
	case StateCandidateWriteIntent:
		if m.CandidateGenerationID == "" {
			return errors.New("candidate_generation_id is required in candidate_write_intent state")
		}
		if m.ConfigPresent {
			if m.CandidateConfigFile == "" {
				return errors.New("candidate_config_file is required when config is present in candidate_write_intent state")
			}
			if m.CandidateConfigDigest == "" {
				return errors.New("candidate_config_digest is required when config is present in candidate_write_intent state")
			}
		}
	case StateCandidateBuilt:
		if m.CandidateGenerationID == "" {
			return errors.New("candidate_generation_id is required in candidate_built state")
		}
		if m.ConfigPresent {
			if m.CandidateConfigFile == "" {
				return errors.New("candidate_config_file is required when config is present in candidate_built state")
			}
			if m.CandidateConfigDigest == "" {
				return errors.New("candidate_config_digest is required when config is present")
			}
		}
	case StateCommitIntent:
		if m.CandidateGenerationID == "" {
			return errors.New("candidate_generation_id is required in commit_intent state")
		}
	case StateRecoveryIntent:
		if m.CandidateGenerationID == "" {
			return errors.New("candidate_generation_id is required in recovery_intent state")
		}
	case StateRecoveryCommitted:
		if m.CandidateGenerationID == "" {
			return errors.New("candidate_generation_id is required in recovery_committed state")
		}
	case StateRollbackIntent, StateRollbackStoreRestored, StateRollbackConfigPromoted, StateRollbackRuntimeVerified, StateRollbackBridgesVerified, StateRollbackCommitted:
		if m.RollbackTargetGenerationID == "" && m.LKGGenerationID == "" && m.CandidateGenerationID == "" && m.PreviousLKGGenerationID == "" {
			return fmt.Errorf("target generation ID is required in %s state", m.State)
		}
	case StateMigrationIntent, StateMigrationBundlePublished, StateMigrationActiveWriteIntent,
		StateMigrationActiveWritten, StateMigrationPointerWriteIntent, StateMigrationPointerWritten,
		StateMigrationCommitted:
		if m.OperationKind != OperationMigration {
			return fmt.Errorf("operation_kind must be %q in %s state", OperationMigration, m.State)
		}
		if m.CandidateGenerationID == "" {
			return fmt.Errorf("candidate_generation_id is required in %s state", m.State)
		}
		if err := ValidateBasename(m.CandidateGenerationID); err != nil {
			return fmt.Errorf("candidate_generation_id in %s state: %w", m.State, err)
		}
		if m.PreviousRecordFile == "" {
			return fmt.Errorf("previous_record_file is required in %s state", m.State)
		}
		if m.PreviousRecordDigest == "" {
			return fmt.Errorf("previous_record_digest is required in %s state", m.State)
		}
		if m.PreviousGenerationID != "" {
			if m.PreviousLKGPointerFile == "" {
				return fmt.Errorf("previous_lkg_pointer_file is required for generation %s in %s state", m.PreviousGenerationID, m.State)
			}
			if m.PreviousLKGPointerDigest == "" {
				return fmt.Errorf("previous_lkg_pointer_digest is required for generation %s in %s state", m.PreviousGenerationID, m.State)
			}
		}
		if m.State == StateMigrationActiveWritten || m.State == StateMigrationPointerWriteIntent ||
			m.State == StateMigrationPointerWritten || m.State == StateMigrationCommitted {
			if m.CandidateRecordDigest == "" {
				return fmt.Errorf("candidate_record_digest is required in %s state", m.State)
			}
		}
	}
	return nil
}

// ValidateSchema verifies DraftJournal integrity.
func (dj *DraftJournal) ValidateSchema() error {
	if dj.Version != 1 {
		return fmt.Errorf("unsupported draft journal version: %d (want 1)", dj.Version)
	}
	if err := ValidateTxID(dj.TxID); err != nil {
		return fmt.Errorf("draft journal txid: %w", err)
	}
	if dj.State == "" {
		return errors.New("draft journal state cannot be empty")
	}
	return nil
}

// ValidateSchema verifies PendingInputRecord integrity.
func (rec *PendingInputRecord) ValidateSchema() error {
	if rec.Version != 1 {
		return fmt.Errorf("unsupported pending input version: %d (want 1)", rec.Version)
	}
	if rec.TxID != "" {
		if err := ValidateTxID(rec.TxID); err != nil {
			return fmt.Errorf("pending input txid: %w", err)
		}
	}
	if rec.State != "intent" && rec.State != "pending" && rec.State != "converging" {
		return fmt.Errorf("invalid pending input state: %q", rec.State)
	}
	return nil
}

// ValidateSchema verifies MigrationJournal integrity.
func (mj *MigrationJournal) ValidateSchema() error {
	if mj.Version != 1 {
		return fmt.Errorf("unsupported migration journal version: %d (want 1)", mj.Version)
	}
	if mj.State != "in_progress" && mj.State != "completed" {
		return fmt.Errorf("invalid migration journal state: %q", mj.State)
	}
	if mj.StartedAt.IsZero() {
		return errors.New("migration journal started_at cannot be zero")
	}
	return nil
}

// ValidateSchema verifies LKGPointer integrity.
func (ptr *LKGPointer) ValidateSchema() error {
	if ptr.Version != 1 {
		return fmt.Errorf("unsupported lkg pointer version: %d (want 1)", ptr.Version)
	}
	if err := ValidateBasename(ptr.GenerationID); err != nil {
		return fmt.Errorf("lkg pointer generation_id: %w", err)
	}
	return nil
}

// IsLegacyBridgeIdentity returns true if the manifest uses the legacy unversioned bridge identity.
func (gm *GenerationManifest) IsLegacyBridgeIdentity() bool {
	if gm.BridgeIdentityVersion == 0 {
		return true
	}
	for _, b := range gm.AppliedBridges {
		if b.IsLegacy() {
			return true
		}
	}
	return false
}

// UnmarshalJSON implements json.Unmarshaler for GenerationManifest.
// It enforces strict JSON decoding (rejecting unknown fields) while providing backwards
// compatibility for legacy v1 manifests containing a deprecated "process_receipt" field.
// Any legacy process_receipt is intentionally discarded and never written to new manifests
// or trusted as authoritative runtime state.
func (gm *GenerationManifest) UnmarshalJSON(data []byte) error {
	if gm == nil {
		return fmt.Errorf("unmarshal into nil *GenerationManifest")
	}
	type Alias GenerationManifest
	aux := struct {
		*Alias
		DeprecatedProcessReceipt *json.RawMessage `json:"process_receipt,omitempty"`
	}{
		Alias: (*Alias)(gm),
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&aux); err != nil {
		return err
	}
	var dummy struct{}
	if err := dec.Decode(&dummy); err != io.EOF {
		return fmt.Errorf("unexpected trailing data in JSON payload")
	}
	return nil
}

// ValidateSchema verifies GenerationManifest integrity.
func (gm *GenerationManifest) ValidateSchema() error {
	if gm.Version != 1 {
		return fmt.Errorf("unsupported generation manifest version: %d (want 1)", gm.Version)
	}
	if err := ValidateBasename(gm.GenerationID); err != nil {
		return fmt.Errorf("generation manifest generation_id: %w", err)
	}
	switch gm.BridgeIdentityVersion {
	case 0:
		// Legacy generation manifest recognized explicitly.
		return nil
	case CurrentBridgeIdentityVersion:
		expectedDigest := BridgesDigest(gm.AppliedBridges)
		if gm.AppliedBridgesDigest != expectedDigest {
			return fmt.Errorf("generation manifest applied_bridges_digest mismatch: got %q, want %q", gm.AppliedBridgesDigest, expectedDigest)
		}
		seenSlots := make(map[string]struct{}, len(gm.AppliedBridges))
		for i, b := range gm.AppliedBridges {
			if err := b.ValidateComplete(); err != nil {
				return fmt.Errorf("generation manifest bridge[%d] incomplete (%s): %w", i, b.SlotKey(), err)
			}
			slot := b.SlotKey()
			if _, exists := seenSlots[slot]; exists {
				return fmt.Errorf("generation manifest duplicate bridge slot %q", slot)
			}
			seenSlots[slot] = struct{}{}
		}
		return nil
	default:
		return fmt.Errorf("unsupported bridge identity version: %d (want %d or 0 for legacy)", gm.BridgeIdentityVersion, CurrentBridgeIdentityVersion)
	}
}

// IsLegacyBridgeIdentity returns true if the applied record uses the legacy unversioned bridge identity.
func (rec *AppliedGenerationRecord) IsLegacyBridgeIdentity() bool {
	if rec.BridgeIdentityVersion == 0 {
		return true
	}
	for _, b := range rec.AppliedBridges {
		if b.IsLegacy() {
			return true
		}
	}
	return false
}

// ValidateSchema verifies AppliedGenerationRecord integrity.
func (rec *AppliedGenerationRecord) ValidateSchema() error {
	if rec.Version != 1 {
		return fmt.Errorf("unsupported applied record version: %d (want 1)", rec.Version)
	}
	if rec.AppliedConfigDigest == "" && rec.RuntimeMode != RuntimeOff {
		return errors.New("applied record applied_config_digest cannot be empty for active mode")
	}
	switch rec.BridgeIdentityVersion {
	case 0:
		// Legacy bridge identity: recognized explicitly.
		// Legacy records are only valid as input to startup migration.
		return nil
	case CurrentBridgeIdentityVersion:
		expectedDigest := BridgesDigest(rec.AppliedBridges)
		if rec.AppliedBridgesDigest != expectedDigest {
			return fmt.Errorf("applied record applied_bridges_digest mismatch: got %q, want %q", rec.AppliedBridgesDigest, expectedDigest)
		}
		seenSlots := make(map[string]struct{}, len(rec.AppliedBridges))
		for i, b := range rec.AppliedBridges {
			if err := b.ValidateComplete(); err != nil {
				return fmt.Errorf("applied record bridge[%d] incomplete (%s): %w", i, b.SlotKey(), err)
			}
			slot := b.SlotKey()
			if _, exists := seenSlots[slot]; exists {
				return fmt.Errorf("applied record duplicate bridge slot %q", slot)
			}
			seenSlots[slot] = struct{}{}
		}
		return nil
	default:
		return fmt.Errorf("unsupported bridge identity version: %d (want %d or 0 for legacy)", rec.BridgeIdentityVersion, CurrentBridgeIdentityVersion)
	}
}

// TransactionJournal records sequential transaction steps.
type TransactionJournal struct {
	Version   int           `json:"version"`
	TxID      string        `json:"txid"`
	CreatedAt time.Time     `json:"created_at"`
	Steps     []JournalStep `json:"steps"`
	State     ManifestState `json:"state"`
}

// JournalStep represents a single discrete transition.
type JournalStep struct {
	Timestamp time.Time     `json:"timestamp"`
	FromState ManifestState `json:"from_state"`
	ToState   ManifestState `json:"to_state"`
	Error     string        `json:"error,omitempty"`
}

// ManifestDiff computes changes between base and candidate configurations.
type ManifestDiff struct {
	ConfigChanged    bool     `json:"config_changed"`
	ListenersChanged bool     `json:"listeners_changed"`
	BridgesAdded     []string `json:"bridges_added,omitempty"`
	BridgesWithdrawn []string `json:"bridges_withdrawn,omitempty"`
}

// ComputeDiff calculates differences between two generation manifests.
func ComputeDiff(base, candidate *GenerationManifest) *ManifestDiff {
	diff := &ManifestDiff{}
	if base == nil {
		diff.ConfigChanged = true
		diff.ListenersChanged = true
		if candidate != nil {
			for _, b := range candidate.AppliedBridges {
				diff.BridgesAdded = append(diff.BridgesAdded, b.KernelInterface)
			}
		}
		return diff
	}
	if candidate == nil {
		return diff
	}

	diff.ConfigChanged = base.AppliedConfigDigest != candidate.AppliedConfigDigest
	diff.ListenersChanged = len(base.AppliedListeners) != len(candidate.AppliedListeners)

	baseBridges := make(map[string]bool)
	for _, b := range base.AppliedBridges {
		baseBridges[b.KernelInterface] = true
	}

	candidateBridges := make(map[string]bool)
	for _, b := range candidate.AppliedBridges {
		candidateBridges[b.KernelInterface] = true
		if !baseBridges[b.KernelInterface] {
			diff.BridgesAdded = append(diff.BridgesAdded, b.KernelInterface)
		}
	}

	for _, b := range base.AppliedBridges {
		if !candidateBridges[b.KernelInterface] {
			diff.BridgesWithdrawn = append(diff.BridgesWithdrawn, b.KernelInterface)
		}
	}

	return diff
}

// IsValidNext checks if transitioning from the current state to the next state is permitted by the state machine.
func (s ManifestState) IsValidNext(next ManifestState) bool {
	switch s {
	case StateIdle:
		return next == StatePreSnapshotWriteIntent || next == StateCandidateWriteIntent || next == StateMigrationIntent
	case StatePreSnapshotWriteIntent:
		return next == StateSnapshotSecured || next == StateTerminalCleanup || next == StateRecoveryRequired
	case StateSnapshotSecured:
		return next == StateCandidateWriteIntent || next == StateAbortInProgress || next == StateRollbackInProgress || next == StateTerminalCleanup
	case StateCandidateWriteIntent:
		return next == StateCandidateBuilt || next == StateAbortInProgress || next == StateRollbackInProgress || next == StateTerminalCleanup
	case StateCandidateBuilt:
		return next == StateCandidatePublished || next == StateAbortInProgress || next == StateRollbackInProgress || next == StateTerminalCleanup
	case StateCandidatePublished:
		return next == StateSwapIntent || next == StateRuntimeIntent || next == StateConfigPromoted || next == StateAbortInProgress || next == StateRollbackInProgress || next == StateTerminalCleanup || next == StateRecoveryRequired
	case StateConfigPromoted:
		return next == StateRuntimeVerified || next == StateRecoveryRequired || next == StateRollbackInProgress || next == StateTerminalCleanup
	case StateSwapIntent:
		return next == StateSwapApplied || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateSwapApplied:
		return next == StateSwapVerified || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateSwapVerified:
		return next == StateRuntimeIntent || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateRuntimeIntent:
		return next == StateRuntimeApplied || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateRuntimeApplied:
		return next == StateRuntimeVerified || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateRuntimeVerified:
		return next == StateBridgesReconciling || next == StateCommitIntent || next == StateRecoveryCommitted || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateBridgesReconciling:
		return next == StateCommitIntent || next == StateRollbackInProgress || next == StateRecoveryRequired
	case StateCommitIntent:
		return next == StateCommitted || next == StateRecoveryCommitted || next == StateRecoveryRequired
	case StateCommitted:
		return next == StateTerminalCleanup || next == StateRecoveryRequired
	case StateAbortInProgress:
		return next == StateTerminalCleanup || next == StateRecoveryRequired || next == StateRollbackInProgress
	case StateRollbackInProgress:
		return next == StateRolledBack || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRolledBack:
		return next == StateTerminalCleanup || next == StateRecoveryRequired
	case StateTerminalCleanup:
		return next == StateIdle || next == StateRecoveryRequired
	case StateRecoveryRequired:
		return next == StateAbortInProgress || next == StateRollbackInProgress || next == StateTerminalCleanup || next == StateRecoveryIntent || next == StateRollbackIntent || next == StateMigrationIntent
	case StateRecoveryIntent:
		return next == StateCandidatePublished || next == StateRecoveryRequired || next == StateRollbackInProgress || next == StateTerminalCleanup
	case StateRecoveryCommitted:
		return next == StateTerminalCleanup || next == StateIdle || next == StateRecoveryRequired
	case StateRollbackIntent:
		return next == StateRollbackStoreRestored || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRollbackStoreRestored:
		return next == StateRollbackConfigPromoted || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRollbackConfigPromoted:
		return next == StateRollbackRuntimeVerified || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRollbackRuntimeVerified:
		return next == StateRollbackBridgesVerified || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRollbackBridgesVerified:
		return next == StateRollbackCommitted || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateRollbackCommitted:
		return next == StateTerminalCleanup || next == StateIdle || next == StateRecoveryRequired
	case StateMigrationIntent:
		return next == StateMigrationBundlePublished || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationBundlePublished:
		return next == StateMigrationActiveWriteIntent || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationActiveWriteIntent:
		return next == StateMigrationActiveWritten || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationActiveWritten:
		return next == StateMigrationPointerWriteIntent || next == StateMigrationCommitted || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationPointerWriteIntent:
		return next == StateMigrationPointerWritten || next == StateMigrationCommitted || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationPointerWritten:
		return next == StateMigrationCommitted || next == StateRecoveryRequired || next == StateTerminalCleanup
	case StateMigrationCommitted:
		return next == StateTerminalCleanup || next == StateIdle || next == StateRecoveryRequired
	default:
		return false
	}
}

// BridgeFact represents a safe, allowlisted projection of bridge identity and state facts.
type BridgeFact struct {
	ProxyIndex      int    `json:"proxy_index"`
	ProxyInterface  string `json:"proxy_interface"`
	KernelInterface string `json:"kernel_interface"`
	ListenPort      int    `json:"listen_port,omitempty"`
	OwnerUUID       string `json:"owner_uuid,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
}

// RecoveryEvidenceDTO is a redacted, typed diagnostic export returned during degraded or recovery state.
type RecoveryEvidenceDTO struct {
	GeneratedAt    time.Time           `json:"generated_at"`
	State          ManifestState       `json:"state"`
	ActiveTxID     string              `json:"active_tx_id,omitempty"`
	DaemonEpoch    string              `json:"daemon_epoch,omitempty"`
	RecoveryReason string              `json:"recovery_reason,omitempty"`
	RecoveryMarker *RecoveryMarkerFact `json:"recovery_marker,omitempty"`
	AppliedRecord  *AppliedRecordFact  `json:"applied_record,omitempty"`
	ProcessReceipt *ProcessReceiptFact `json:"process_receipt,omitempty"`
	ManifestFacts  *ManifestFacts      `json:"manifest_facts,omitempty"`
	Bridges        []BridgeFact        `json:"bridges,omitempty"`
	SanitizedLogs  []string            `json:"sanitized_logs,omitempty"`
}

type RecoveryMarkerFact struct {
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

type AppliedRecordFact struct {
	GenerationID        string    `json:"generation_id"`
	Generation          uint64    `json:"generation"`
	AppliedAt           time.Time `json:"applied_at"`
	AppliedConfigDigest string    `json:"applied_config_digest"`
	AppliedStoreDigest  string    `json:"applied_store_digest"`
	AppliedInputDigest  string    `json:"applied_input_digest"`
}

type ProcessReceiptFact struct {
	PID            int            `json:"pid"`
	ProcStartTicks uint64         `json:"proc_start_ticks"`
	ExecutableName string         `json:"executable_name"`
	Listeners      []ListenerSpec `json:"listeners,omitempty"`
}

type ManifestFacts struct {
	TxID            string        `json:"tx_id"`
	State           ManifestState `json:"state"`
	TargetDigest    string        `json:"target_digest"`
	CandidateDigest string        `json:"candidate_digest"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

var (
	// Multiline private key blocks (RSA, EC, DSA, OPENSSH, etc.).
	// Specifically requires "PRIVATE KEY" so public "CERTIFICATE" blocks are preserved.
	pemPrivateKeyRegex = regexp.MustCompile(`(?s)-----BEGIN[A-Z0-9_\s\-]+PRIVATE KEY-----.*?-----END[A-Z0-9_\s\-]+PRIVATE KEY-----`)

	// Full HTTP Authorization headers (Bearer, Basic, etc.)
	authHeaderRegex = regexp.MustCompile(`(?i)(["']?Authorization["']?\s*:\s*["']?(?:Bearer|Basic)\s+)([^\r\n"';,]+)`)

	// Standalone Bearer tokens in text/prose/query/configs
	standaloneBearerRegex = regexp.MustCompile(`(?i)(^|[^\w-])(Bearer\s+)([^\s\r\n"';,}{\[\]()]+)`)

	// URI userinfo: scheme://user:pass@host or scheme://user@host
	uriUserInfoRegex = regexp.MustCompile(`(?i)([a-zA-Z][a-zA-Z0-9+\-.]*://)([^:\s/@]+)(?::([^@\s/]+))?(@)`)

	// URL query parameters with exact credential keys
	urlQueryCredRegex = regexp.MustCompile(`(?i)([?&](?:password|passwd|secret|token|access_token|api_key|auth_key|private_key|uuid)=)([^&#\s]+)`)

	// Quoted assignments in JSON, YAML, or configs (double and single quotes):
	// Key must have boundary before it (not preceded by alphanumeric, _, or -)
	quotedDoubleRegex = regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9_\-])(["']?)(password|passwd|secret|token|access_token|api_key|auth_key|private_key|uuid)(["']?)(\s*[:=]\s*)"([^"\r\n]*)"`)
	quotedSingleRegex = regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9_\-])(["']?)(password|passwd|secret|token|access_token|api_key|auth_key|private_key|uuid)(["']?)(\s*[:=]\s*)'([^'\r\n]*)'`)

	// Unquoted assignments in YAML, ENV, or configs (value must not start with quotes or brackets):
	unquotedAssignRegex = regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9_\-])(["']?)(password|passwd|secret|token|access_token|api_key|auth_key|private_key|uuid)(["']?)(\s*[:=]\s*)([^"'\s\r\n,;}{\[\]#][^\r\n,;}{\[\]#]*)`)
)

// RedactSecrets replaces sensitive credentials, tokens, and passwords in a string with [REDACTED].
func RedactSecrets(s string) string {
	if s == "" {
		return ""
	}

	// 1. Multiline private keys (preserves certificates)
	s = pemPrivateKeyRegex.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")

	// 2. Authorization headers (Bearer, Basic) and standalone Bearer tokens
	s = authHeaderRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := authHeaderRegex.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		prefix := sub[1]
		val := sub[2]
		trimmedVal := strings.TrimRight(val, " \t")
		trailingSpace := val[len(trimmedVal):]
		if trimmedVal == "[REDACTED]" {
			return m
		}
		return prefix + "[REDACTED]" + trailingSpace
	})
	s = standaloneBearerRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := standaloneBearerRegex.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		boundary := sub[1]
		prefix := sub[2]
		rawToken := sub[3]
		if rawToken == "[REDACTED]" {
			return m
		}
		trailingPunct := ""
		for len(rawToken) > 0 {
			last := rawToken[len(rawToken)-1]
			if last == '.' || last == ',' || last == ';' || last == '!' || last == '?' || last == ')' || last == ':' {
				trailingPunct = string(last) + trailingPunct
				rawToken = rawToken[:len(rawToken)-1]
			} else {
				break
			}
		}
		if rawToken == "[REDACTED]" || rawToken == "" {
			return m
		}
		return boundary + prefix + "[REDACTED]" + trailingPunct
	})

	// 3. URI userinfo: redact user and password
	s = uriUserInfoRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := uriUserInfoRegex.FindStringSubmatch(m)
		if len(sub) < 5 {
			return m
		}
		scheme := sub[1]
		if sub[3] != "" {
			return scheme + "[REDACTED]:[REDACTED]@"
		}
		return scheme + "[REDACTED]@"
	})

	// 4. URL query parameters with credential keys
	s = urlQueryCredRegex.ReplaceAllString(s, "${1}[REDACTED]")

	// 5. Quoted key assignments (JSON / YAML / Config)
	s = quotedDoubleRegex.ReplaceAllString(s, `${1}${2}${3}${4}${5}"[REDACTED]"`)
	s = quotedSingleRegex.ReplaceAllString(s, `${1}${2}${3}${4}${5}'[REDACTED]'`)

	// 6. Unquoted key assignments (YAML / ENV / Config)
	s = unquotedAssignRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := unquotedAssignRegex.FindStringSubmatch(m)
		if len(sub) < 7 {
			return m
		}
		val := sub[6]
		trimmed := strings.TrimRight(val, " \t")
		trailing := val[len(trimmed):]
		return sub[1] + sub[2] + sub[3] + sub[4] + sub[5] + "[REDACTED]" + trailing
	})

	return s
}

// SanitizePathToBase reduces an absolute filesystem path to only its base filename.
func SanitizePathToBase(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

func checkJSONDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		keys := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected string key in object")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate key %q in JSON object", key)
			}
			keys[key] = struct{}{}
			if err := checkJSONDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume '}'
		return err
	} else if delim == '[' {
		for dec.More() {
			if err := checkJSONDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume ']'
		return err
	}
	return nil
}

// DecodeJSONStrict decodes JSON bytes with duplicate key rejection, unknown field rejection, and trailing garbage validation.
func DecodeJSONStrict(data []byte, target interface{}) error {
	// 1. Check duplicate keys and structural validity
	decCheck := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONDuplicateKeys(decCheck); err != nil {
		return err
	}
	var dummyCheck struct{}
	if err := decCheck.Decode(&dummyCheck); err != io.EOF {
		return fmt.Errorf("unexpected trailing data in JSON payload")
	}

	// 2. Decode into target with unknown field rejection
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var dummy struct{}
	if err := dec.Decode(&dummy); err != io.EOF {
		return fmt.Errorf("unexpected trailing data in JSON payload")
	}
	return nil
}

// Equal verifies complete structural equality of two AppliedGenerationRecords.
func (rec AppliedGenerationRecord) Equal(other AppliedGenerationRecord) (bool, string) {
	return CanonicalEqualAppliedRecords(rec, other)
}

// CanonicalEqualAppliedRecords verifies complete structural equality of two AppliedGenerationRecords.
func CanonicalEqualAppliedRecords(a, b AppliedGenerationRecord) (bool, string) {
	if a.Version != b.Version {
		return false, fmt.Sprintf("version mismatch: %d != %d", a.Version, b.Version)
	}
	if a.Generation != b.Generation {
		return false, fmt.Sprintf("generation mismatch: %d != %d", a.Generation, b.Generation)
	}
	if a.GenerationID != b.GenerationID {
		return false, fmt.Sprintf("generation_id mismatch: %s != %s", a.GenerationID, b.GenerationID)
	}
	if a.AppliedStoreDigest != b.AppliedStoreDigest {
		return false, fmt.Sprintf("applied_store_digest mismatch: %s != %s", a.AppliedStoreDigest, b.AppliedStoreDigest)
	}
	if a.AppliedConfigDigest != b.AppliedConfigDigest {
		return false, fmt.Sprintf("applied_config_digest mismatch: %s != %s", a.AppliedConfigDigest, b.AppliedConfigDigest)
	}
	if a.AppliedInputDigest != b.AppliedInputDigest {
		return false, fmt.Sprintf("applied_input_digest mismatch: %s != %s", a.AppliedInputDigest, b.AppliedInputDigest)
	}
	if a.RuntimeMode != b.RuntimeMode {
		return false, fmt.Sprintf("runtime_mode mismatch: %s != %s", a.RuntimeMode, b.RuntimeMode)
	}

	// Listeners comparison: sorted canonical comparison
	sortedListeners := func(in []ListenerSpec) []ListenerSpec {
		if len(in) == 0 {
			return nil
		}
		out := make([]ListenerSpec, len(in))
		copy(out, in)
		sort.Slice(out, func(i, j int) bool {
			if out[i].GetNetwork() != out[j].GetNetwork() {
				return out[i].GetNetwork() < out[j].GetNetwork()
			}
			if out[i].Port != out[j].Port {
				return out[i].Port < out[j].Port
			}
			if out[i].Address != out[j].Address {
				return out[i].Address < out[j].Address
			}
			if out[i].Purpose != out[j].Purpose {
				return out[i].Purpose < out[j].Purpose
			}
			return out[i].Family < out[j].Family
		})
		return out
	}

	la := sortedListeners(a.AppliedListeners)
	lb := sortedListeners(b.AppliedListeners)
	if len(la) != len(lb) {
		return false, fmt.Sprintf("applied_listeners count mismatch: %d != %d", len(la), len(lb))
	}
	for i := range la {
		if la[i].GetNetwork() != lb[i].GetNetwork() || la[i].Port != lb[i].Port || la[i].Address != lb[i].Address || la[i].Purpose != lb[i].Purpose {
			return false, fmt.Sprintf("listener mismatch at %d: %+v != %+v", i, la[i], lb[i])
		}
	}

	// Bridges comparison: sorted canonical comparison
	sortedBridges := func(in []BridgeRef) []BridgeRef {
		if len(in) == 0 {
			return nil
		}
		out := make([]BridgeRef, len(in))
		copy(out, in)
		sort.Slice(out, func(i, j int) bool {
			if out[i].ProxyIndex != out[j].ProxyIndex {
				return out[i].ProxyIndex < out[j].ProxyIndex
			}
			if out[i].ProxyInterface != out[j].ProxyInterface {
				return out[i].ProxyInterface < out[j].ProxyInterface
			}
			if out[i].KernelInterface != out[j].KernelInterface {
				return out[i].KernelInterface < out[j].KernelInterface
			}
			if out[i].ListenPort != out[j].ListenPort {
				return out[i].ListenPort < out[j].ListenPort
			}
			if out[i].OwnerUUID != out[j].OwnerUUID {
				return out[i].OwnerUUID < out[j].OwnerUUID
			}
			return out[i].LegacyOwner < out[j].LegacyOwner
		})
		return out
	}

	ba := sortedBridges(a.AppliedBridges)
	bb := sortedBridges(b.AppliedBridges)
	if len(ba) != len(bb) {
		return false, fmt.Sprintf("applied_bridges count mismatch: %d != %d", len(ba), len(bb))
	}
	for i := range ba {
		if ba[i].ProxyIndex != bb[i].ProxyIndex || ba[i].ProxyInterface != bb[i].ProxyInterface || ba[i].KernelInterface != bb[i].KernelInterface || ba[i].ListenPort != bb[i].ListenPort || ba[i].OwnerUUID != bb[i].OwnerUUID || ba[i].LegacyOwner != bb[i].LegacyOwner {
			return false, fmt.Sprintf("bridge mismatch at %d: %+v != %+v", i, ba[i], bb[i])
		}
	}

	// ProcessReceipt comparison
	if (a.ProcessReceipt == nil) != (b.ProcessReceipt == nil) {
		return false, "process_receipt presence mismatch"
	}
	if a.ProcessReceipt != nil && b.ProcessReceipt != nil {
		if a.ProcessReceipt.PID != b.ProcessReceipt.PID ||
			a.ProcessReceipt.ProcStartTicks != b.ProcessReceipt.ProcStartTicks ||
			a.ProcessReceipt.Generation != b.ProcessReceipt.Generation ||
			a.ProcessReceipt.ExecutablePath != b.ProcessReceipt.ExecutablePath ||
			a.ProcessReceipt.ConfigDir != b.ProcessReceipt.ConfigDir ||
			a.ProcessReceipt.DaemonEpoch != b.ProcessReceipt.DaemonEpoch ||
			a.ProcessReceipt.AppliedGeneration != b.ProcessReceipt.AppliedGeneration {
			return false, fmt.Sprintf("process_receipt mismatch: %+v != %+v", a.ProcessReceipt, b.ProcessReceipt)
		}
	}

	return true, ""
}
