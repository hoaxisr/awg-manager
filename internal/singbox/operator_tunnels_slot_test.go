package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: t.TempDir()})
	return op, filepath.Join(op.ConfigDir(), "10-tunnels.json")
}

// newOrchedOperatorWithDeps — как newOrchedOperator, но с произвольными
// OperatorDeps: нужен тестам base-конфига (ApplyLogLevel/ApplyClashPort/
// ApplyBootstrapDNS), которым важны колбэки настроек, а не значение,
// возвращаемое хелпером-обёрткой.
func newOrchedOperatorWithDeps(t *testing.T, deps OperatorDeps) *Operator {
	t.Helper()
	// Пустой Binary = дефолтный ХОСТ-путь /opt/sbin/sing-box, а ApplyConfig и
	// AddTunnels зовут singboxFeaturesCached → на машине, где бинарь есть, это
	// exec чужого бинаря и запись сайдкара /opt/sbin/sing-box.meta.json рядом с
	// ним. Фейк из TempDir держит пробу внутри теста.
	if deps.Binary == "" {
		deps.Binary = fakeBinary(t, deps.Dir)
	}
	op := NewOperator(deps)
	orch := singboxorch.NewWithAppliedPath(op.ConfigDir(), op.Process(), filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range singboxorch.KnownSlots() {
		switch meta.Slot {
		case singboxorch.SlotBase, singboxorch.SlotTunnels, singboxorch.SlotRouter:
		default:
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
	return op
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
// os.Remove — её делает TestRemoveTunnel_LastOneLeavesEmptySlot ниже, через
// шов ndmsProxies.
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

// Битая ссылка в ЧУЖОМ слоте не должна запирать CRUD туннелей.
// validateDraftLocked проверяет ссылки по всем слотам и считает
// unknown-outbound жёсткой ошибкой, а прежний preflightConfigDir этого не
// делал: на develop такая ссылка ломала только reload и показывалась в
// lastReloadValidation, а туннели добавлялись и удалялись.
//
// Тест разборчивый: без ветки mergedAlreadyInvalid ApplyConfig возвращает
// «validate: … unknown-outbound».
func TestApplyConfig_ForeignDanglingRefDoesNotBlockCRUD(t *testing.T) {
	op, slotPath := newOrchedOperator(t)

	// Сосед с висячей ссылкой. Пишем через Save — он не валидирует, ровно так
	// такой слот и заводится руками через эксперт-редактор.
	broken := []byte(`{"route":{"rules":[{"inbound":["x"],"action":"route","outbound":"ghost"}]}}`)
	if err := op.orch.Save(singboxorch.SlotRouter, broken); err != nil {
		t.Fatalf("seed broken neighbour: %v", err)
	}
	// Валидация смотрит только на ВКЛЮЧЁННЫЕ слоты — выключенный сосед
	// невидим, и тест был бы вакуумным.
	if err := op.orch.SetEnabled(singboxorch.SlotRouter, true); err != nil {
		t.Fatalf("enable neighbour slot: %v", err)
	}

	cfg := NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig заперт чужой висячей ссылкой: %v", err)
	}
	if !op.HasUserTunnels() {
		t.Fatal("туннель не записан")
	}
	if _, err := os.Stat(slotPath); err != nil {
		t.Fatalf("слот не записан: %v", err)
	}
}

// Обратная сторона: СВОЯ поломка блокировать обязана, иначе ветка
// mergedAlreadyInvalid вырождается в «никогда не валидировать».
// Сосед ссылается на туннель A; пока A есть — merge валиден, и удаление A
// ломает merge именно нашей записью.
func TestApplyConfig_OwnBreakageStillBlocks(t *testing.T) {
	op, _ := newOrchedOperator(t)

	cfg := NewConfig()
	cfg.AddTunnelWithListenPort("A", "vless", "h", 1, 0, json.RawMessage(`{"type":"vless","tag":"A"}`))
	cfg.AddTunnelWithListenPort("B", "vless", "h", 2, 0, json.RawMessage(`{"type":"vless","tag":"B"}`))
	if err := op.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("ApplyConfig (seed): %v", err)
	}

	usesA := []byte(`{"route":{"rules":[{"inbound":["x"],"action":"route","outbound":"A"}]}}`)
	if err := op.orch.Save(singboxorch.SlotRouter, usesA); err != nil {
		t.Fatalf("seed neighbour: %v", err)
	}
	if err := op.orch.SetEnabled(singboxorch.SlotRouter, true); err != nil {
		t.Fatalf("enable neighbour slot: %v", err)
	}

	if err := cfg.RemoveTunnel("A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	if err := op.ApplyConfig(context.Background(), cfg); err == nil {
		t.Fatal("ApplyConfig прошёл, хотя merge сломан НАШЕЙ записью")
	}
}

// Двойной POST (nginx proxy_next_upstream, две вкладки) не должен родить второй
// туннель на тот же сервер: дедуп по (type, server, port, secret). Комментарий про
// «нужен живой sing-box» устарел — обвязки достаточно.
func TestAddTunnels_RejectsDuplicateOfExistingTunnel(t *testing.T) {
	op, _ := newOrchedOperator(t)
	op.proxyMgr = &fakeProxies{}
	ctx := context.Background()

	cfg := NewConfig()
	existing := json.RawMessage(`{"type":"vless","server":"h.example","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab"}`)
	if err := cfg.AddTunnelWithListenPort("A", "vless", "h.example", 443, 0, existing); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyConfig (seed): %v", err)
	}

	added, errs, err := op.AddTunnels(ctx,
		"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=h#A2")
	if err != nil {
		t.Fatalf("AddTunnels: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("дубль добавлен: %+v", added)
	}
	// Пин держится на тексте ошибки, а не на len(added)==0: без дедупа
	// AddTunnels падает раньше на "listen_port 1080 already in use" —
	// fakeProxies.NextFreeIndex всегда отдаёт 0.
	if len(errs) != 1 || !strings.Contains(errs[0].Err.Error(), `duplicate of existing tunnel "A"`) {
		t.Fatalf("ожидался BatchError о дубле, got %+v", errs)
	}
	got, err := op.loadOrInitConfig()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Tunnels()); n != 1 {
		t.Fatalf("в конфиге %d туннелей, want 1", n)
	}
}

// RemoveTunnel обязан снять ProxyN ИМЕННО удаляемого туннеля — индекс выводится
// из listen_port, и сдвиг на единицу снёс бы чужой интерфейс молча.
func TestRemoveTunnel_RemovesProxyOfThatTunnelOnly(t *testing.T) {
	op, _ := newOrchedOperator(t)
	proxies := &fakeProxies{}
	op.proxyMgr = proxies
	ctx := context.Background()

	cfg := NewConfig()
	if err := cfg.AddTunnelWithListenPort("A", "vless", "a.example", 443, 1083, json.RawMessage(`{"type":"vless","tag":"A"}`)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddTunnelWithListenPort("B", "vless", "b.example", 443, 1089, json.RawMessage(`{"type":"vless","tag":"B"}`)); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	if err := op.RemoveTunnel(ctx, "B"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	if !reflect.DeepEqual(proxies.removed, []int{9}) {
		t.Fatalf("RemoveProxy indexes = %v, want [9] (listen_port 1089 → Proxy9)", proxies.removed)
	}
}
