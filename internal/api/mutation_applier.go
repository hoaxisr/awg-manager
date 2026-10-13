package api

import (
	"context"
	"errors"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
)

var (
	ErrRecoveryRequired = errors.New("RECOVERY_REQUIRED")
)

// NativeMutationApplier coordinates transactional mutations against the Mihomo engine.
type NativeMutationApplier interface {
	ApplyNativeMutation(ctx context.Context, mutateFn func() error) error
	ApplyNativeMutationWithOutcome(ctx context.Context, mutateFn func() error) (*mihomo.MutationOutcome, error)
	ApplyDraftOnly(ctx context.Context, mutateFn func() error) error
	ApplyPendingDraft(ctx context.Context) error
	IsDegraded() bool
	CheckMutationAllowed() error
	Reconcile(ctx context.Context, action string, force bool) error
	ExportEvidence(ctx context.Context) (*mihomo.RecoveryEvidenceDTO, error)
}
