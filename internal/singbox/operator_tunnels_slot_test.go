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
// ApplyConfig — 0%), поэтому снятие легаси-протокола не уронило ни одного теста.
//
// ЧЕГО ЭТИ ТЕСТЫ НЕ ДЕЛАЮТ: они не отличают запись через оркестратор от
// снятой прямой записи. На счастливом пути та и другая наблюдательно
// эквивалентны — обе кладут файл и взводят reload (SaveAndValidate,
// draft.go:479-496, тоже взводит его безусловно, skip-gate там нет).
// Различия — в отсутствии окна с битым файлом на диске при провале валидации
// и в записи под локом оркестратора; ни то, ни другое из юнита не наблюдаемо.
// Единственный наблюдаемый различитель — поведение без оркестратора, он в
// TestApplyConfig_WithoutOrchestratorFails.
//
// ЧТО ОНИ ДЕЛАЮТ: закрывают нулевое покрытие пути записи слота и поймают
// регресс «перестало писаться / HasUserTunnels сломался / слот не
// зарегистрирован».
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

// Пустой конфиг даёт пустой слот, и HasUserTunnels на нём false — то есть
// SlotTunnels не считается работой и демона не держит.
//
// Это НЕ проверка того, что RemoveTunnel перестал сам звать proc.Stop и
// os.Remove: сам Operator.RemoveTunnel из теста недрайвится — o.proxyMgr
// разыменовывает nil без NDMS-клиента (proxy.go:106), и внедрить его нечем.
// Пока proxyMgr не станет внедряемым, эта половина правки покрытию недоступна.
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

// fakeProxies — второй адаптер шва ndmsProxies. В проде за швом ProxyManager
// поверх RCI; здесь — запись вызовов, чтобы пути, трогающие NDMS-прокси,
// вообще запускались из теста.
type fakeProxies struct{ removed []int }

func (f *fakeProxies) EnsureProxy(context.Context, int, int, string) error { return nil }
func (f *fakeProxies) NextFreeIndex(context.Context, map[int]bool) (int, error) {
	return 0, nil
}
func (f *fakeProxies) RemoveProxy(_ context.Context, index int) error {
	f.removed = append(f.removed, index)
	return nil
}
func (f *fakeProxies) RemoveOrphanSingboxProxies(context.Context, map[string]bool, map[int]bool, map[int]bool) error {
	return nil
}
func (f *fakeProxies) ListNativeProxies(context.Context, map[string]bool, map[int]bool, map[int]bool) ([]string, error) {
	return nil, nil
}
func (f *fakeProxies) SyncProxies(context.Context, []TunnelInfo) error { return nil }

// Удаление ПОСЛЕДНЕГО туннеля: слот остаётся на месте пустым, а не удаляется.
// Прежде RemoveTunnel на этой ветке сам звал proc.Stop и os.Remove(слот),
// дублируя решение оркестратора, — на старом коде этот тест падает на первом
// же Stat.
func TestRemoveTunnel_LastOneLeavesEmptySlot(t *testing.T) {
	op, slotPath := newOrchedOperator(t)
	proxies := &fakeProxies{}
	op.proxyMgr = proxies

	cfg := NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig (seed): %v", err)
	}

	if err := op.RemoveTunnel(context.Background(), "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}

	if _, err := os.Stat(slotPath); err != nil {
		t.Fatalf("слот удалён, ожидался пустой файл на месте: %v", err)
	}
	if op.HasUserTunnels() {
		t.Fatal("HasUserTunnels=true после удаления единственного туннеля")
	}
	// Разбор NDMS идёт ПОСЛЕ записи конфига и не пропускается.
	if len(proxies.removed) != 1 {
		t.Errorf("RemoveProxy вызван %d раз, ожидался 1", len(proxies.removed))
	}
}
