package listenfirewall

import (
	"context"
	"time"
)

const reconcileInterval = 15 * time.Second

// RunningPortsProvider returns listen ports that should stay open now.
type RunningPortsProvider func() []PortSpec

// StartReconciler periodically restores INPUT rules for running proxy servers.
func StartReconciler(ctx context.Context, provider RunningPortsProvider) {
	if provider == nil {
		return
	}
	go reconcileLoop(ctx, provider)
}

func reconcileLoop(ctx context.Context, provider RunningPortsProvider) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	Reconcile(ctx, provider())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			Reconcile(ctx, provider())
		}
	}
}
