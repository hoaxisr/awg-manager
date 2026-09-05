package singbox

import (
	"path/filepath"
	"testing"

	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Гоча стенда 2026-06-17: SIGHUP при живом tun = TUNSETIFF busy → FATAL. Process
// решает Stop+Start по ReloadNeedsRestart, а её проводка к оркестратору
// (замыкание `ReloadNeedsRestart`, которое ставит `NewOperator`) не пиновалась:
// `return false` был зелёным.
func TestOperator_ReloadNeedsRestart_FollowsOrchestratorTun(t *testing.T) {
	op := NewOperator(OperatorDeps{Dir: t.TempDir()})
	if op.Process().ReloadNeedsRestart == nil {
		t.Fatal("NewOperator обязан привязать ReloadNeedsRestart")
	}
	if op.Process().ReloadNeedsRestart() {
		t.Fatal("без оркестратора рестарт не нужен")
	}

	proc := &integrationProc{} // не запущен → Reload делает Start
	orch := singboxorch.NewWithAppliedPath(op.ConfigDir(), proc,
		filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range singboxorch.KnownSlots() {
		if meta.Slot == singboxorch.SlotBase || meta.Slot == singboxorch.SlotRouter {
			if err := orch.Register(meta); err != nil {
				t.Fatalf("register %s: %v", meta.Slot, err)
			}
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := orch.SaveSilent(singboxorch.SlotBase,
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`)); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	withTun := `{"inbounds":[{"type":"tun","tag":"tun-in","interface_name":"opkgtun3","address":["172.19.7.1/30"]}],"route":{"final":"direct"}}`
	if err := orch.SaveSilent(singboxorch.SlotRouter, []byte(withTun)); err != nil {
		t.Fatalf("seed router: %v", err)
	}
	if err := orch.SetEnabledSilent(singboxorch.SlotRouter, true); err != nil {
		t.Fatalf("enable router: %v", err)
	}
	if err := orch.ReloadNow(); err != nil {
		t.Fatalf("ReloadNow (tun): %v", err)
	}
	if !orch.CurrentHasTun() {
		t.Fatal("фикстура: после Reload с tun-inbound CurrentHasTun обязан быть true")
	}

	op.SetOrch(orch)
	if !op.Process().ReloadNeedsRestart() {
		t.Fatal("при живом tun Reload обязан быть Stop+Start, а не SIGHUP")
	}

	noTun := `{"route":{"final":"direct"}}`
	if err := orch.SaveSilent(singboxorch.SlotRouter, []byte(noTun)); err != nil {
		t.Fatalf("router без tun: %v", err)
	}
	if err := orch.ReloadNow(); err != nil {
		t.Fatalf("ReloadNow (no tun): %v", err)
	}
	if op.Process().ReloadNeedsRestart() {
		t.Fatal("без tun рестарт не нужен — SIGHUP")
	}
}
