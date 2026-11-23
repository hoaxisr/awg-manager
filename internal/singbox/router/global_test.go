package router

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F811: слот Global, ссылающийся на несуществующий outbound, не применяется
// повторно на каждом тике (запись на флеш + reload раз в 30 с).
func TestReconcileGlobal_FailedReloadNotRetriedEveryTick(t *testing.T) {
	dir := t.TempDir()
	sb := newReadyTestSingbox(t)
	sb.dir = dir
	sb.isRunningFn = func() (bool, int) { return true, 4242 }
	orch := orchestrator.NewWithAppliedPath(dir, sb, filepath.Join(t.TempDir(), "applied.json"))
	t.Cleanup(orch.Close)
	for _, meta := range []orchestrator.SlotMeta{
		{Slot: orchestrator.SlotGlobal, Filename: "19z-global.json"},
		{Slot: orchestrator.SlotRouter, Filename: "20-router.json"},
	} {
		if err := orch.Register(meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	sr := storage.SingboxRouterSettings{Enabled: true, GlobalOutbound: "missing"}

	if err := svc.reconcileGlobal(sr, false); err == nil {
		t.Fatal("reload with a dangling outbound must fail")
	}
	before := sb.reloadCalls
	for i := 0; i < 3; i++ {
		if err := svc.reconcileGlobal(sr, false); err != nil {
			t.Fatalf("repeat tick: %v", err)
		}
	}
	if sb.reloadCalls != before {
		t.Fatalf("failed Global slot reloaded again %d times", sb.reloadCalls-before)
	}
	if svc.slotEnabled(orchestrator.SlotGlobal) {
		t.Fatal("failed Global slot must stay disabled")
	}

	svc.globalFailedAt = svc.globalFailedAt.Add(-globalRetryAfter)
	if err := svc.reconcileGlobal(sr, false); err == nil {
		t.Fatal("retry after backoff must try again and fail")
	}
}
