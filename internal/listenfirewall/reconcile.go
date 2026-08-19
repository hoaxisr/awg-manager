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

// skipIdleTick — тик можно пропустить целиком: открывать нечего, а снос уже
// сделан. Правила сами не появляются, пока ни один прокси-сервер не работает,
// поэтому сверять их каждые 15 с не за чем; NDMS-хук на этот случай тоже уже
// снят. Первый холостой тик после простоя всё же сверяется — им и убираются
// правила остановленного сервера.
func skipIdleTick(desired []PortSpec, idleSwept bool) bool {
	return idleSwept && len(desired) == 0
}

func reconcileLoop(ctx context.Context, provider RunningPortsProvider) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	idleSwept := false
	tick := func() {
		desired := provider()
		if skipIdleTick(desired, idleSwept) {
			return
		}
		Reconcile(ctx, desired)
		idleSwept = len(desired) == 0
	}
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
