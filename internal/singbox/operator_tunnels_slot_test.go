package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Путь записи 10-tunnels.json покрытия не имел вовсе (AddTunnels/RemoveTunnel/
// ApplyConfig — 0%), поэтому снятие легаси-протокола не уронило ни одного
// теста. Здесь закрепляется то, что у слота один писатель — оркестратор.
func newOrchedOperator(t *testing.T) (*Operator, string) {
	t.Helper()
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	orch := singboxorch.New(op.ConfigDir(), op.Process())
	for _, meta := range singboxorch.KnownSlots() {
		if meta.Slot != singboxorch.SlotBase && meta.Slot != singboxorch.SlotTunnels {
			continue
		}
		if err := orch.Register(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	op.SetOrch(orch)
	return op, filepath.Join(op.ConfigDir(), "10-tunnels.json")
}

func TestApplyConfig_WritesSlotThroughOrchestrator(t *testing.T) {
	op, slotPath := newOrchedOperator(t)

	cfg := NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	if _, err := os.Stat(slotPath); err != nil {
		t.Fatalf("слот не записан: %v", err)
	}
	if !op.HasUserTunnels() {
		t.Fatal("HasUserTunnels=false после записи туннеля")
	}
	// Побочных файлов легаси-протокола (rename в .bak) быть не должно.
	if _, err := os.Stat(slotPath + ".bak"); !os.IsNotExist(err) {
		t.Errorf("остался .bak от снятого протокола: %v", err)
	}
}

// Опустевший конфиг пишется как пустой слот, а не удаляется: SlotTunnels
// считается работой только через HasContent, и пустой слот её не даёт, так
// что гасить ли демона — решает reload оркестратора.
//
// Проверяется путь записи, а не сам Operator.RemoveTunnel: тот недрайвится из
// теста — o.proxyMgr.RemoveProxy разыменовывает nil без NDMS-клиента
// (proxy.go:106), и внедрить его нечем. Пробел предсуществующий, отдельно от
// этой правки.
func TestApplyConfig_EmptyConfigKeepsSlotFile(t *testing.T) {
	op, slotPath := newOrchedOperator(t)

	cfg := NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig (seed): %v", err)
	}
	if err := cfg.RemoveTunnel("A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig (empty): %v", err)
	}

	if _, err := os.Stat(slotPath); err != nil {
		t.Fatalf("пустой слот удалён, ожидался пустой файл на месте: %v", err)
	}
	if op.HasUserTunnels() {
		t.Fatal("HasUserTunnels=true на пустом слоте — демон остался бы жить")
	}
}

// Оркестратор обязателен: молчаливого легаси-пути больше нет, и его отсутствие
// должно быть видно ошибкой, а не тихой записью мимо валидации.
func TestApplyConfig_WithoutOrchestratorFails(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	err := op.ApplyConfig(context.Background(), NewConfig())
	if err == nil {
		t.Fatal("ApplyConfig без оркестратора = nil, ожидалась ошибка")
	}
	if _, statErr := os.Stat(filepath.Join(op.ConfigDir(), "10-tunnels.json")); !os.IsNotExist(statErr) {
		t.Errorf("слот записан мимо оркестратора: %v", statErr)
	}
}
