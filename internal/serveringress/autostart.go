package serveringress

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BootLifecycle represents the runtime lifecycle contract for ingress services.
// Implementations MUST NOT mutate or persist configuration when starting or stopping runtime.
type BootLifecycle interface {
	StartConfigured() error
	ShutdownRuntime(ctx context.Context) error
	IsRunning() bool
}

// StartIngressIfSafe orchestrates fail-closed, transactional ingress auto-start.
// It checks all recovery gates, verifies non-nil lifecycles for enabled components,
// and ensures that any startup failure cleanly compensates by shutting down only
// components that were transitioned from stopped to running by this helper.
func StartIngressIfSafe(
	ctx context.Context,
	coordRecoveryReq bool, coordRecoveryReason string,
	xrayRecoveryReq bool, xrayRecoveryReason string,
	tgRecoveryReq bool, tgRecoveryReason string,
	xrayEnabled bool, xray BootLifecycle,
	tgEnabled bool, tg BootLifecycle,
	disp BootLifecycle,
) (bool, error) {
	// 1. Fail-closed recovery gate checks
	if coordRecoveryReq {
		return false, fmt.Errorf("auto-start blocked by coordinator recovery gate: %s", coordRecoveryReason)
	}
	if xrayRecoveryReq {
		return false, fmt.Errorf("auto-start blocked by xray recovery gate: %s", xrayRecoveryReason)
	}
	if tgRecoveryReq {
		return false, fmt.Errorf("auto-start blocked by tgwebproxy recovery gate: %s", tgRecoveryReason)
	}

	// 2. Fail-closed: enabled components MUST have non-nil lifecycle
	if xrayEnabled && xray == nil {
		return false, errors.New("xray is enabled but lifecycle is unavailable")
	}
	if tgEnabled && tg == nil {
		return false, errors.New("telegram ingress is enabled but lifecycle is unavailable")
	}
	if (xrayEnabled || tgEnabled) && disp == nil {
		return false, errors.New("ingress is enabled but dispatcher lifecycle is unavailable")
	}

	var startedByHelper []BootLifecycle

	compensate := func(cause error) (bool, error) {
		// Use independent timeout context so compensation does not fail if parent ctx was cancelled
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var compErrs []error
		// Stop ONLY components started by this helper, in reverse order
		for i := len(startedByHelper) - 1; i >= 0; i-- {
			if err := startedByHelper[i].ShutdownRuntime(cleanupCtx); err != nil {
				compErrs = append(compErrs, err)
			}
		}
		if len(compErrs) > 0 {
			return false, errors.Join(cause, fmt.Errorf("boot compensation failed: %w", errors.Join(compErrs...)))
		}
		return false, cause
	}

	// 3. Start Xray backend if enabled and not already running
	if xrayEnabled && xray != nil {
		if !xray.IsRunning() {
			if err := xray.StartConfigured(); err != nil {
				return compensate(fmt.Errorf("start xray: %w", err))
			}
			startedByHelper = append(startedByHelper, xray)
		}
	}

	// 4. Start Telegram backend if enabled and not already running
	if tgEnabled && tg != nil {
		if !tg.IsRunning() {
			if err := tg.StartConfigured(); err != nil {
				return compensate(fmt.Errorf("start tg: %w", err))
			}
			startedByHelper = append(startedByHelper, tg)
		}
	}

	// 5. Start Dispatcher frontend if backend(s) running and not already running
	if (xrayEnabled || tgEnabled) && disp != nil {
		if !disp.IsRunning() {
			if err := disp.StartConfigured(); err != nil {
				return compensate(fmt.Errorf("start dispatcher: %w", err))
			}
			startedByHelper = append(startedByHelper, disp)
		}
	}

	return true, nil
}
