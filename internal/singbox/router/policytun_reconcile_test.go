package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ---------------------------------------------------------------------------
// Фейк running-config: считает чтения и инвалидации — по ним проверяется, что
// медленный RCI не дёргается на каждом здоровом тике.
// ---------------------------------------------------------------------------

type fakeRunningConfig struct {
	lines       []string
	fresh       []string // если задано — что отдаёт чтение ПОСЛЕ инвалидации (протухший кэш)
	reads       int
	invalidated int
}

func (f *fakeRunningConfig) Lines(context.Context) ([]string, error) {
	f.reads++
	return f.lines, nil
}

func (f *fakeRunningConfig) InvalidateAll() {
	f.invalidated++
	if f.fresh != nil {
		f.lines = f.fresh
	}
}

// healthyPolicyTunRC — running-config провижининга «всё на месте»: дефолты
// v4/v6 на tun, `ip global` в блоке интерфейса, permit в политике.
func healthyPolicyTunRC(ndmsName string) []string {
	return []string{
		"interface " + ndmsName,
		"    description awgm policy-tun",
		"    security-level public",
		"    ip global 65500",
		"    up",
		"!",
		"ip route default " + ndmsName,
		"ipv6 route default " + ndmsName,
		"ip policy Policy0",
		"    permit global " + ndmsName,
		"!",
	}
}

// provisionPolicyTunForReconcile поднимает режим, помечает интерфейс живым и
// чистит лог — дальше в логе только то, что сделал reconcile.
func provisionPolicyTunForReconcile(t *testing.T, h *policyTunEnableHarness) storage.SingboxRouterSettings {
	t.Helper()
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (provision for reconcile): %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.log.calls = nil
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	return sr
}

// ---------------------------------------------------------------------------
// Парсеры running-config
// ---------------------------------------------------------------------------

// Форма элементов массива `message`, снятая с живого роутера (2026-08-10) ТЕМ
// ЖЕ каналом, которым ходит продукт: `GET /rci/show/running-config`
// (`transport/client.go:224-234` → `query/runningconfig.go:39-49`). Вывод
// `ndmc` для этой цели негоден: CLI форматирует сам, и его отступы — свойство
// CLI, а не данных.
//
// ЗНАЧЕНИЯ вымышленные: описания политик пользователя в репозитории не нужны.
// Тесту нужна форма строк, а не чужая конфигурация.
//
// Что здесь зафиксировано, кроме самих строк:
//   - ведущие пробелы тела блока RCI СОХРАНЯЕТ — на этом стоит весь блочный
//     разбор («строка с отступом = тело»);
//   - `!` приходит отдельным элементом без отступа, то есть закрывает блок;
//   - служебная шапка `! $$$ …` и пустые строки идут в том же массиве;
//   - ЗАПРЕТ печатается той же тройкой слов, что и разрешение:
//     `no permit global <iface>`. Поиск подстрокой принимал бы его за
//     разрешение — продукт не ставил бы permit, и режим оставался бы молча
//     мёртвым.
var liveRouterPolicyRC = []string{
	"! $$$ Agent: http/rci",
	"! $$$ Model: Router Model",
	"",
	"ip policy Policy0",
	"    description Policy_A",
	"    permit global Wireguard1",
	"    no permit global PPPoE0",
	"    no permit global Wireguard5",
	"    no permit global Wireguard6",
	"!",
	"ip policy Policy1",
	"    description Policy_B",
	"    permit global PPPoE0",
	"    no permit global Wireguard1",
	"    no permit global Wireguard5",
	"    no permit global Wireguard6",
	"    standalone",
	"!",
}

func TestPolicyTunPermitted_LiveRouterConfig(t *testing.T) {
	cases := []struct {
		iface, policy string
		want          bool
	}{
		{"Wireguard1", "Policy0", true},  // разрешён в целевой
		{"Wireguard1", "Policy1", false}, // в целевой он ЗАПРЕЩЁН (`no permit`)
		{"PPPoE0", "Policy0", false},     // запрещён здесь, разрешён в соседней
		{"PPPoE0", "Policy1", true},      // разрешён в целевой
		{"Wireguard5", "Policy0", false}, // только запрет
		{"Wireguard6", "Policy1", false}, // запрещён в обеих политиках
		{"Wireguard5", "", false},        // политика не выбрана: разрешения нет нигде
		{"Wireguard1", "", true},         // политика не выбрана: разрешён хоть где-то
		{"OpkgTun0", "Policy0", false},   // нас в конфиге ещё нет
		{"Wireguard1", "Policy9", false}, // целевой политики не существует
	}
	for _, c := range cases {
		if got := policyTunPermitted(liveRouterPolicyRC, c.iface, c.policy); got != c.want {
			t.Errorf("policyTunPermitted(%q, %q) = %v, want %v", c.iface, c.policy, got, c.want)
		}
	}
}

// Форма строк снята с RCI живых роутеров (2026-08-10), но ЗНАЧЕНИЯ здесь
// вымышленные: в конфигурации роутера соседствуют идентификаторы
// аутентификации, названия VPN-провайдеров и реальные адреса, и в репозитории
// им не место. Тесту нужна форма, а не чужие данные.
//
// Главное, что зафиксировано: `ip global <приоритет>` — строка в теле блока, и
// стоит она НЕ ТОЛЬКО у провайдера, но и у каждого VPN-выхода. Поэтому цель
// static-NAT не одна.
func TestGlobalEgressInterfaces_LiveRouterConfig(t *testing.T) {
	lines := []string{
		"interface PPPoE0",
		"    security-level public",
		"    ip mtu 1492",
		"    ip access-group _WEBADMIN_PPPoE0 in",
		"    ip global 32767",
		"!",
		"interface Wireguard0",
		"    description \"VPN A\"",
		"    security-level public",
		"    ip address 10.0.0.2 255.255.255.255",
		"    ip global 12287",
		"!",
		"interface Wireguard1",
		"    description \"VPN B\"",
		"    security-level public",
		"    ip address 10.0.1.2 255.255.255.255",
		"    ip global 6143",
		"!",
		// Домашний сегмент: адресация есть, `ip global` нет — выходом наружу
		// не является и целью static-NAT быть не может.
		"interface Home",
		"    security-level private",
		"    ip address 192.168.1.1 255.255.255.0",
		"!",
	}
	got := globalEgressInterfaces(lines)
	want := []string{"PPPoE0", "Wireguard0", "Wireguard1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("globalEgressInterfaces = %v, want %v", got, want)
	}
}

func TestPolicyTunRunningConfigParsers(t *testing.T) {
	lines := []string{
		"ip route default OpkgTun0",
		"ipv6 route default OpkgTun0",
		"ip policy Policy0", "    permit global OpkgTun0",
	}
	v4, v6 := policyTunDefaultRoutePresent(lines, "OpkgTun0")
	if !v4 || !v6 {
		t.Fatalf("routes: v4=%v v6=%v", v4, v6)
	}
	if !policyTunPermitted(lines, "OpkgTun0", "Policy0") {
		t.Fatal("permit not found")
	}
	if policyTunPermitted(lines, "OpkgTun1", "Policy0") {
		t.Fatal("false positive OpkgTun1 (префикс-ловушка)")
	}
	// Разрешение в чужой политике целевую не покрывает.
	if policyTunPermitted(lines, "OpkgTun0", "Policy1") {
		t.Fatal("permit в Policy0 не разрешение для Policy1")
	}
	// Политика не выбрана — годится любая.
	if !policyTunPermitted(lines, "OpkgTun0", "") {
		t.Fatal("без имени политики разрешение в любой должно засчитываться")
	}
	// Вне блока политики permit не бывает: те же слова в теле чужого блока
	// разрешением не являются.
	outside := []string{
		"interface OpkgTun0", "    description permit global OpkgTun0", "!",
	}
	if policyTunPermitted(outside, "OpkgTun0", "") {
		t.Fatal("false positive: permit вне блока ip policy")
	}
	// Внутри блока политики матч идёт с начала строки: те же слова в её
	// description разрешением не являются.
	inDescr := []string{
		"ip policy Policy0", "    description permit global OpkgTun0", "!",
	}
	if policyTunPermitted(inDescr, "OpkgTun0", "Policy0") {
		t.Fatal("false positive: слова permit global в description политики")
	}
	// Из блока обязан быть ВЫХОД: целевая политика пуста и стоит раньше чужой,
	// разрешение в которой нашим не является.
	targetFirst := []string{
		"ip policy Policy0", "!",
		"ip policy Policy1", "    permit global OpkgTun0", "!",
	}
	if policyTunPermitted(targetFirst, "OpkgTun0", "Policy0") {
		t.Fatal("false positive: разрешение из блока, следующего за целевым")
	}
	// Снятое разрешение разрешением не является: RCI хранит исторические
	// `no …`-строки (см. policies.go), и первым токеном правила идёт `no`.
	revoked := []string{
		"ip policy Policy0", "    no permit global OpkgTun0", "!",
	}
	if policyTunPermitted(revoked, "OpkgTun0", "Policy0") {
		t.Fatal("false positive: снятое разрешение (no permit)")
	}
	if v4x, _ := policyTunDefaultRoutePresent([]string{"ip route default OpkgTun01"}, "OpkgTun0"); v4x {
		t.Fatal("false positive: OpkgTun01 не должен матчить OpkgTun0")
	}
	// Хвостовые токены NDMS (метрика/автоматика) матч не ломают.
	if v4x, _ := policyTunDefaultRoutePresent([]string{"ip route default OpkgTun0 auto"}, "OpkgTun0"); !v4x {
		t.Fatal("хвостовые токены не должны ломать матч маршрута")
	}
}

// `ip global` ищется ТОЛЬКО внутри блока своего интерфейса: та же строка под
// чужим интерфейсом не считается.
func TestPolicyTunIPGlobalPresent(t *testing.T) {
	if !policyTunIPGlobalPresent(healthyPolicyTunRC("OpkgTun0"), "OpkgTun0") {
		t.Error("ip global в блоке своего интерфейса не найден")
	}
	foreign := []string{
		"interface OpkgTun1",
		"    ip global 65500",
		"!",
		"interface OpkgTun0",
		"    up",
		"!",
	}
	if policyTunIPGlobalPresent(foreign, "OpkgTun0") {
		t.Error("ip global чужого интерфейса не должен считаться нашим")
	}
}

// ---------------------------------------------------------------------------
// Reconcile: drift-heal
// ---------------------------------------------------------------------------

// Пропал дефолт-маршрут в running-config → он ставится заново (v4 и v6
// раздельно), остальное не трогается.
func TestReconcilePolicyTun_ReaddsMissingDefaultRoute(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	rc := &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ipv6 route default OpkgTun0",
		"ip policy Policy0", "    permit global OpkgTun0",
	}}
	h.svc.deps.RunningConfig = rc

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDefaultRoute:OpkgTun0") {
		t.Errorf("пропавший v4-дефолт должен быть переустановлен: %v", h.log.calls)
	}
	if h.log.has("SetIPv6DefaultRoute:OpkgTun0") {
		t.Errorf("присутствующий v6-дефолт трогать не нужно: %v", h.log.calls)
	}
	// Нездоровое состояние → кэш running-config сбрасывается, чтобы решение
	// принималось по свежим данным.
	if rc.invalidated == 0 {
		t.Error("при дрейфе кэш running-config обязан инвалидироваться")
	}
}

// Позитив к предыдущему тесту: v6-дефолт ПРОПАЛ (wantV6 — пул FakeIPPool6 задан
// обвязкой) → переустанавливается. Раньше проверялся только негатив, и
// `if wantV6 && !v6` → `if false` оставался зелёным.
func TestReconcilePolicyTun_ReaddsMissingIPv6DefaultRoute(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	rc := &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip policy Policy0", "    permit global OpkgTun0",
	}}
	h.svc.deps.RunningConfig = rc

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetIPv6DefaultRoute:OpkgTun0") {
		t.Errorf("пропавший v6-дефолт обязан быть переустановлен: %v", h.log.calls)
	}
}

// Диспатч: в policy-tun Reconcile уходит в свой арм (а не в installed-switch
// tproxy, который при отсутствующих цепочках гнал бы Enable каждый тик), и реап,
// идущий первым, ingress-заворот не сносит.
func TestReconcile_DispatchesPolicyTun(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.IngressInterfaces = []string{"iface:nwg3"}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip policy Policy0", "    permit global OpkgTun0",
	}}
	// Заворот в steady state: любые мутации таблицы 700 в этом тике = дрейф,
	// внесённый нами самими (свип реапа).
	rec := &ingressRecorder{
		natDump:   "-P PREROUTING ACCEPT\n",
		ruleDump:  ruleDumpFor("nwg3"),
		routeDump: "throw 10.0.0.0/8\nthrow 172.16.0.0/12\nthrow 192.168.0.0/16\ndefault dev opkgtun0\n",
	}
	h.svc.deps.IPTables = rec.tables()

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Арм policy-tun: пропавшие дефолт-маршруты переустановлены, интерфейс НЕ
	// пересоздан.
	if !h.log.has("SetDefaultRoute:OpkgTun0") {
		t.Errorf("Reconcile должен уходить в арм policy-tun: %v", h.log.calls)
	}
	if h.log.has("Create:OpkgTun0:public") {
		t.Errorf("живой интерфейс не должен пересоздаваться: %v", h.log.calls)
	}
	// Реап заворот не снял и ensure его не пересобирал: ни одной мутации нашей
	// таблицы за тик.
	for _, call := range rec.ip {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "table "+fakeIPIngressTableStr()) {
			t.Errorf("ingress-заворот policy-tun пересобирается на ровном месте: %v", rec.ip)
		}
	}
}

// Пропал `ip global` → интерфейс исчез из списка выходов политики; ставим снова.
func TestReconcilePolicyTun_ReassertsIPGlobal(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    security-level public", "!",
		"ip route default OpkgTun0",
		"ipv6 route default OpkgTun0",
		"ip policy Policy0", "    permit global OpkgTun0",
	}}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetIPGlobal:OpkgTun0") {
		t.Errorf("пропавший ip global должен быть переустановлен: %v", h.log.calls)
	}
}

// Анти-churn: всё на месте → со второго тика НИ ОДНОЙ мутации RCI и ни одной
// инвалидации кэша running-config (он читается по TTL).
func TestReconcilePolicyTun_NoMutationWhenNoDrift(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	rc := &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.RunningConfig = rc

	// Первый тик одноразово ассертит permit-ACL (upgrade-путь) — допустимая
	// мутация, и только один раз.
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (первый тик): %v", err)
	}
	h.log.calls = nil

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("здоровый тик обязан быть без мутаций NDMS, получено %v", h.log.calls)
	}
	if rc.invalidated != 0 {
		t.Errorf("здоровое состояние не должно сбрасывать кэш running-config (invalidated=%d)", rc.invalidated)
	}
}

// Интерфейс исчез (краш/ручное удаление) → reprovision через enable-путь.
func TestReconcilePolicyTun_ReprovisionsWhenIfaceGone(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("исчезнувший интерфейс должен быть провижинен заново: %v", h.log.calls)
	}
}

// Мёртвый sing-box: рестарт — зона watchdog, reconcile его НЕ спавнит.
func TestReconcilePolicyTun_NoSingboxStart(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	sb := h.svc.deps.Singbox.(*fakeSingbox)
	sb.startCalls = 0
	sb.isRunningFn = func() (bool, int) { return false, 0 }

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if sb.startCalls != 0 {
		t.Errorf("reconcile не должен рестартить движок, startCalls = %d", sb.startCalls)
	}
}

// Запаркованный слот 20 при живом интерфейсе — дрейф: возвращаем в конфиг.
func TestReconcilePolicyTun_RepromotesParkedSlot(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := h.svc.deps.Orch.SetEnabledSilent(orchestrator.SlotRouter, false); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !slotEnabled(t, h.svc, orchestrator.SlotRouter) {
		t.Error("запаркованный слот 20 должен вернуться в конфиг")
	}
}

// Ingress-заворот: reconcile его переустанавливает (сброс firewall NDMS,
// смена состава ingress-интерфейсов через UpdateSettings).
func TestReconcilePolicyTun_EnsuresIngress(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.IngressInterfaces = []string{"iface:nwg3"}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	rec := &ingressRecorder{natDump: "-P PREROUTING ACCEPT\n", ruleDump: ruleDumpFor()}
	h.svc.deps.IPTables = rec.tables()

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if n := rec.ipCalls("rule", "add", "iif", "nwg3", "table", fakeIPIngressTableStr()); n != 1 {
		t.Errorf("ingress-заворот не восстановлен: %v", rec.ip)
	}
	for _, call := range rec.ipt {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "PREROUTING 1") || strings.Contains(joined, "DNAT") {
			t.Errorf("DNAT в policy-tun не ставится: %v", rec.ipt)
		}
	}
}

// Краш между удержанием интерфейса и записью персиста: на диске Provisioned
// остался истиной, интерфейс жив (NDMS-объект переживает и down, и снятие
// адресов), а tun-инбаунд выключение уже вырезало. Такое состояние не
// самозаживает: enable no-op'ится по гарду provisioned+live, а heal возвращает
// только маршруты и permit — адрес, up и инбаунд не возвращает никто, и режим
// числится включённым при мёртвом трафике. Тик обязан это переустановить.
func TestReconcilePolicyTun_ReprovisionsWhenTunInboundGone(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0")
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	// Ровно то, что делает шаг 4 выключения: инбаунд вырезан из слота 20.
	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("loadAppliedRouterConfig: %v", err)
	}
	cfg.Inbounds = filterPolicyTunInbound(cfg.Inbounds)
	if err := h.svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}
	h.log.calls = nil

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	// Переустановка — это адрес и подъём интерфейса; их не делает ни один heal.
	if !h.log.has("SetAddress:OpkgTun0:172.18.0.1:255.255.255.252") || !h.log.has("InterfaceUp:OpkgTun0") {
		t.Errorf("недоделанное состояние обязано переустанавливаться: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 {
		t.Errorf("PolicyTun persist = %+v, want provisioned index 0", st)
	}
	after, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("loadAppliedRouterConfig (after): %v", err)
	}
	if len(filterPolicyTunInbound(after.Inbounds)) == len(after.Inbounds) {
		t.Error("tun-инбаунд обязан вернуться в слот")
	}
}

// Конфиг слота не прочитался — «не знаем» ≠ «инбаунд пропал»: цена ложного
// срабатывания здесь полный re-provision живого режима.
func TestReconcilePolicyTun_NoReprovisionWhenConfigUnreadable(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := os.WriteFile(filepath.Join(h.dir, "20-router.json"), []byte("{{{ не json"), 0644); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	h.log.calls = nil

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if h.log.has("SetAddress:OpkgTun0:172.18.0.1:255.255.255.252") {
		t.Errorf("нечитаемый конфиг не повод переустанавливать режим: %v", h.log.calls)
	}
}

// Обратное: инбаунд на месте — тик ничего не переустанавливает. Иначе штатный
// рестарт sing-box уходил бы в полный re-provision каждые 30 с.
func TestReconcilePolicyTun_NoReprovisionWhenInboundPresent(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if h.log.has("SetAddress:OpkgTun0:172.18.0.1:255.255.255.252") {
		t.Errorf("здоровое состояние переустанавливать нельзя: %v", h.log.calls)
	}
}

// policy-tun пишет тот же 20-router.json, что и tproxy, но идёт своим путём
// реконсиляции (reconcilePolicyTun, а не reconcileInstalled) — без отдельного
// вызова heal1140SlotMigration здесь слот, поднятый до миграции на sing-box
// 1.14, остался бы на старой форме до первой ручной правки маршрутизации.
// Мирроит TestHeal1140SlotMigration_RewritesLegacySlot (router-slot ветка).
func TestReconcilePolicyTun_Heal1140SlotMigration(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	legacy := `{
		"inbounds": [{
			"type": "tun", "tag": "tun-in", "interface_name": "OpkgTun0",
			"address": ["172.18.0.1/30"], "mtu": 1400, "stack": "gvisor",
			"udp_timeout": "5m0s", "auto_route": false, "auto_redirect": false,
			"strict_route": false, "gso": false, "endpoint_independent_nat": false
		}],
		"outbounds": [{"type": "direct", "tag": "direct"}],
		"route": {
			"rule_set": [{
				"tag": "geosite-x", "type": "remote", "format": "binary",
				"url": "https://example.com/x.srs", "update_interval": "24h",
				"download_detour": "direct"
			}],
			"rules": [{"action": "route", "rule_set": ["geosite-x"], "outbound": "direct"}],
			"final": "direct"
		}
	}`
	activePath := filepath.Join(h.dir, "20-router.json")
	if err := os.WriteFile(activePath, []byte(legacy), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	raw, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	for _, want := range []string{`"http_clients"`, `"http_client"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("migrated slot missing %s: %s", want, raw)
		}
	}
	for _, gone := range []string{"download_detour", "gso", "endpoint_independent_nat"} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("migrated slot still has legacy key %q: %s", gone, raw)
		}
	}

	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (второй тик): %v", err)
	}

	after, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("второй тик переписал уже мигрированный слот (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
}

// F114: смена udpTimeout/udpNatMax в настройках доезжает до tun-in без
// Disable/Enable — до фикса tun-in строится только на enable
// (ensurePolicyTunInbound), и UpdateSettings оставался мёртвым до
// перезапуска режима.
func TestReconcilePolicyTun_HealsUDPSettings(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	sr.UDPTimeout = "10m0s"
	sr.UDPNATMax = 8192

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("loadAppliedRouterConfig: %v", err)
	}
	var tun *Inbound
	for i := range cfg.Inbounds {
		if cfg.Inbounds[i].Tag == "tun-in" {
			tun = &cfg.Inbounds[i]
		}
	}
	if tun == nil {
		t.Fatal("tun-in инбаунд отсутствует")
	}
	if tun.UDPTimeout != "10m0s" || tun.UDPNATMax != 8192 {
		t.Errorf("tun-in UDPTimeout=%q UDPNATMax=%d, want 10m0s/8192", tun.UDPTimeout, tun.UDPNATMax)
	}
	ruleOK := false
	for _, r := range cfg.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			ruleOK = r.UDPTimeout == "10m0s"
		}
	}
	if !ruleOK {
		t.Error("route-options правило udp_timeout не обновлено до 10m0s")
	}

	activePath := filepath.Join(h.dir, "20-router.json")
	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (второй тик): %v", err)
	}
	after, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("второй тик без изменений переписал слот (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
}

// F114 fix round 1: guard в healTunUDPSettings обязан ловить не только
// расхождение полей tun-in, но и пропавшее/устаревшее route-options
// правило — иначе при уже верных полях инбаунда heal no-op'ится навсегда,
// хотя правило снято. Инбаунд не трогаем (sr не меняем — дефолт "5m0s"),
// вырезаем ТОЛЬКО правило.
func TestReconcilePolicyTun_HealsMissingUDPTimeoutRule(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("loadAppliedRouterConfig: %v", err)
	}
	cfg.EnsureUDPTimeoutRule("") // снимает правило, ничего не добавляя
	if err := h.svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}

	after, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatalf("loadAppliedRouterConfig (после): %v", err)
	}
	ruleOK := false
	for _, r := range after.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			ruleOK = r.UDPTimeout == "5m0s"
		}
	}
	if !ruleOK {
		t.Error("route-options правило udp_timeout не восстановлено")
	}

	activePath := filepath.Join(h.dir, "20-router.json")
	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (второй тик): %v", err)
	}
	afterStat, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !afterStat.ModTime().Equal(before.ModTime()) {
		t.Errorf("второй тик без изменений переписал слот (before=%v after=%v)", before.ModTime(), afterStat.ModTime())
	}
}

// Permit пропал (или не встал при включении: отказ RCI повторное включение не
// ретраит — оно no-op'ится по гарду provisioned+live) → drift-heal доставляет его.
func TestReconcilePolicyTun_PermitsWhenMissing(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	sr := provisionPolicyTunForReconcile(t, h)
	pol.permits = nil // permit включения в счёт не идёт
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip route default OpkgTun0",
		"ipv6 route default OpkgTun0",
		"ip policy Policy0", "!",
	}}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	want := []string{"Policy0:OpkgTun0:0"}
	if !reflect.DeepEqual(pol.permits, want) {
		t.Errorf("permits = %v, want %v", pol.permits, want)
	}
}

// Permit на месте → drift-heal его не переставляет: order=0 каждый тик тасовал
// бы список выходов политики.
func TestReconcilePolicyTun_NoPermitWhenPresent(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	sr := provisionPolicyTunForReconcile(t, h)
	pol.permits = nil
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if len(pol.permits) != 0 {
		t.Errorf("стоящий permit не должен переставляться, получено %v", pol.permits)
	}
}

// Permit стоит в ЧУЖОЙ политике: для целевой это не разрешение — устройства
// сидят в ней, а выхода у неё нет. Разрешением где угодно детект
// удовлетворяться не имеет права, иначе режим молча мёртв (и смена PolicyName
// на работающем режиме не доезжает никогда).
func TestReconcilePolicyTun_PermitsWhenOtherPolicyPermitted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy1")
	sr := provisionPolicyTunForReconcile(t, h)
	pol.permits = nil
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip route default OpkgTun0",
		"ipv6 route default OpkgTun0",
		"ip policy Policy0", "    permit global OpkgTun0", "!",
		"ip policy Policy1", "!",
	}}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	want := []string{"Policy1:OpkgTun0:0"}
	if !reflect.DeepEqual(pol.permits, want) {
		t.Errorf("permits = %v, want %v", pol.permits, want)
	}
}

// Кэш running-config протух: permit пользователь поставил мимо нас. Свежее
// чтение обязано пересчитать и permitted — иначе heal слал бы order=0 каждый
// тик, переставляя список выходов политики.
func TestReconcilePolicyTun_NoPermitAfterStaleCacheRefresh(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	sr := provisionPolicyTunForReconcile(t, h)
	pol.permits = nil
	h.svc.deps.RunningConfig = &fakeRunningConfig{
		lines: []string{ // по кэшу permit'а нет
			"interface OpkgTun0", "    ip global 65500", "!",
			"ip route default OpkgTun0",
			"ipv6 route default OpkgTun0",
			"ip policy Policy0", "!",
		},
		fresh: healthyPolicyTunRC("OpkgTun0"), // а на роутере он уже стоит
	}

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if len(pol.permits) != 0 {
		t.Errorf("решение о permit обязано приниматься по свежему конфигу, получено %v", pol.permits)
	}
}

// ---------------------------------------------------------------------------
// QoS: единственный netfilter режима — DSCP-диспатч. UpdateSettings
// завершается Reconcile'ом, поэтому runtime-изменения классов применяются тут.
// ---------------------------------------------------------------------------

func TestReconcilePolicyTun_QoSClassesRemoved_Uninstalls(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
	h.svc.deps.WANIPCollector = &fakeWANIPCollector{}
	h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }
	provisionPolicyTunForReconcile(t, h)

	// Пользователь удалил классы: сохраняем настройки без них и гоним reconcile.
	all, _ = h.store.Load()
	all.SingboxRouter.QoSClasses = nil
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	uninstalled := false
	ipt := newStubIPTables(func(context.Context, string) error {
		t.Error("без классов QoS переустановки netfilter быть не должно")
		return nil
	})
	ipt.cleanupHook = func() { uninstalled = true }
	h.svc.deps.IPTables = ipt

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !uninstalled {
		t.Error("удаление всех классов QoS обязано снять цепочки (Uninstall)")
	}
	// Оверлей qos-правил запаркован вместе с ними.
	if st, ok := h.svc.slotSnapshot(orchestrator.SlotQoSRoutes); ok && st.Enabled {
		t.Error("слот 18-qos-routes должен быть запаркован без классов")
	}
}

// Изменение состава классов → переустановка DSCP-спека.
func TestReconcilePolicyTun_QoSClassesChanged_Reinstalls(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
	h.svc.deps.WANIPCollector = &fakeWANIPCollector{}
	h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }
	provisionPolicyTunForReconcile(t, h)

	all, _ = h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 26, Name: "Video", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	var restoreInput string
	installs := 0
	h.svc.deps.IPTables = newStubIPTables(func(_ context.Context, in string) error {
		installs++
		restoreInput = in
		return nil
	})

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if installs != 1 {
		t.Fatalf("IPTables.Install calls = %d, want 1", installs)
	}
	if !strings.Contains(restoreInput, "--dscp 26") {
		t.Errorf("новый класс не попал в спек:\n%s", restoreInput)
	}
	if strings.Contains(restoreInput, "--dport 53 -j TPROXY") {
		t.Errorf("policy-tun не перехватывает DNS:\n%s", restoreInput)
	}

	// Второй тик без изменений — ни одной переустановки.
	installs = 0
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (второй тик): %v", err)
	}
	if installs != 0 {
		t.Errorf("без изменений классов переустановки быть не должно, installs = %d", installs)
	}
}

// Классы не менялись, но цепочки DSCP-диспатча пропали мимо NDMS (ручной
// iptables -F, сбой хука) → переустановка; целые цепочки → ноль установок.
func TestReconcilePolicyTun_QoSChainsWiped_Reinstalls(t *testing.T) {
	setup := func(t *testing.T) (*policyTunEnableHarness, storage.SingboxRouterSettings) {
		t.Helper()
		h := newPolicyTunEnableHarness(t, "")
		all, _ := h.store.Load()
		all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
			{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
		}
		if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
			t.Fatalf("Save: %v", err)
		}
		h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
		h.svc.deps.WANIPCollector = &fakeWANIPCollector{}
		h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
		h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }
		sr := provisionPolicyTunForReconcile(t, h)
		h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
		return h, sr
	}

	t.Run("wiped", func(t *testing.T) {
		h, sr := setup(t)
		installs := 0
		ipt := newStubIPTables(func(context.Context, string) error {
			installs++
			return nil
		})
		// Дамп без цепочек — Probe видит пропажу перехвата.
		ipt.runIPTablesOut = func(context.Context, ...string) (string, error) {
			return "-P PREROUTING ACCEPT\n", nil
		}
		h.svc.deps.IPTables = ipt

		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcilePolicyTun: %v", err)
		}
		if installs != 1 {
			t.Errorf("пропавшие цепочки при неизменных классах должны переустанавливаться, installs = %d", installs)
		}
	})

	t.Run("intact", func(t *testing.T) {
		h, sr := setup(t)
		installs := 0
		h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error {
			installs++
			return nil
		})

		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcilePolicyTun: %v", err)
		}
		if installs != 0 {
			t.Errorf("целые цепочки и неизменные классы = ноль установок, installs = %d", installs)
		}
	})

	t.Run("probe error", func(t *testing.T) {
		h, sr := setup(t)
		installs := 0
		ipt := newStubIPTables(func(context.Context, string) error {
			installs++
			return nil
		})
		ipt.runIPTablesOut = func(context.Context, ...string) (string, error) {
			return "", errors.New("транзиентный сбой -S")
		}
		h.svc.deps.IPTables = ipt

		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcilePolicyTun: %v", err)
		}
		if installs != 0 {
			t.Errorf("ошибка пробы = «неизвестно», переустановки быть не должно, installs = %d", installs)
		}
	})
}

// ---------------------------------------------------------------------------
// Реап: в policy-tun полный свип ingress снёс бы НАШ заворот на каждом тике
// (Reconcile зовёт реап первым). Снимается только DNAT-половина.
// ---------------------------------------------------------------------------

func TestReap_PolicyTunKeepsIngressRoutes(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun, Enabled: true})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	rec := &ingressRecorder{
		natDump: "-P PREROUTING ACCEPT\n" +
			"-A PREROUTING -i nwg3 -p udp -m udp --dport 53 -m comment --comment " + FakeIPIngressTag +
			" -j DNAT --to-destination 172.18.0.2:53\n",
		ruleDump: ruleDumpFor("nwg3"),
	}
	svc := newTestService(t, Deps{Settings: store, IPTables: rec.tables()})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	for _, call := range rec.ip {
		joined := strings.Join(call, " ")
		if strings.HasPrefix(joined, "rule del") || strings.HasPrefix(joined, "route flush") {
			t.Errorf("реап в policy-tun не должен снимать наш заворот: %v", rec.ip)
		}
	}
	// Протухший DNAT прежнего режима fakeip обязан быть снят: policy-tun его не
	// ставит, а ensure с NoDNAT правила DNAT не трогает вовсе.
	removed := false
	for _, call := range rec.ipt {
		if strings.Contains(strings.Join(call, " "), "-D PREROUTING") {
			removed = true
		}
	}
	if !removed {
		t.Errorf("протухший DNAT-перехват DNS должен сниматься: %v", rec.ipt)
	}
}

func TestReap_PolicyTunKeepsOwnDNATRules(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun, Enabled: true})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	rec := &ingressRecorder{
		natDump: "-P PREROUTING ACCEPT\n" +
			"-A PREROUTING -i nwg3 -p udp --dport 53 -m comment --comment " + FakeIPIngressTag + " -j DNAT --to-destination 172.18.0.2:53\n" +
			"-A PREROUTING -m connmark --mark 0xffffaab -p udp --dport 53 -m comment --comment " + PolicyTunDNSTag + " -j DNAT --to-destination 172.18.0.2:53\n",
		ruleDump: ruleDumpFor("nwg3"),
	}
	svc := newTestService(t, Deps{Settings: store, IPTables: rec.tables()})

	if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
	}
	for _, call := range rec.ipt {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "-D PREROUTING") && strings.Contains(joined, PolicyTunDNSTag) {
			t.Fatalf("реап снёс правило policy-tun: %s", joined)
		}
	}
	removedFakeIP := false
	for _, call := range rec.ipt {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "-D PREROUTING") && strings.Contains(joined, FakeIPIngressTag) {
			removedFakeIP = true
		}
	}
	if !removedFakeIP {
		t.Errorf("протухший DNAT прежнего fakeip обязан сниматься: %v", rec.ipt)
	}
}

// ---------------------------------------------------------------------------
// Готовность: carrier tun, без DNS-инпутов fakeip
// ---------------------------------------------------------------------------

func TestSingboxReady_PolicyTunCarrier(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun, Enabled: true})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{Settings: store, Singbox: singbox})
	stubTunReadyProbe(t, func(string) bool { return true })

	// Без персиста policy-tun готовность fail-closed.
	if svc.singboxReady(context.Background(), true) {
		t.Error("без провижининга policy-tun готовность должна быть false")
	}
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	if !svc.singboxReady(context.Background(), true) {
		t.Error("процесс жив + carrier → policy-tun готов")
	}
}

// ---------------------------------------------------------------------------
// Статус и Issue
// ---------------------------------------------------------------------------

func TestGetStatus_PolicyTun(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	// Опция валидна только со списком сегментов (NormalizeSingboxRouterSettings).
	armSourcePreserve(t, h, []string{"Home"})
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return false }

	// До провижининга поля пустые.
	h.svc.deps.IPTables = errProbeIPTables()
	st0, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st0.PolicyTunIface != "" || st0.PolicyTunNDMSName != "" {
		t.Errorf("до провижининга имена интерфейса пусты, получено %q/%q", st0.PolicyTunIface, st0.PolicyTunNDMSName)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.PolicyTunIface != "opkgtun0" || st.PolicyTunNDMSName != "OpkgTun0" {
		t.Errorf("имена интерфейса = %q/%q, want opkgtun0/OpkgTun0", st.PolicyTunIface, st.PolicyTunNDMSName)
	}
	if st.PolicyTunSourcePreserve == nil || !*st.PolicyTunSourcePreserve {
		t.Errorf("PolicyTunSourcePreserve = %v, want true", st.PolicyTunSourcePreserve)
	}
	if !st.Installed {
		t.Error("Installed должен быть true при provisioned policy-tun")
	}
	if !st.Active {
		t.Error("процесс жив + carrier + дефолт в running-config → Active")
	}
	if issueOfKind(st.Issues, issuePolicyTunUnbound) != nil {
		t.Errorf("permit есть — issue не нужен: %+v", st.Issues)
	}

	// Интерфейс не разрешён ни в одной политике → warning-issue, и Active
	// остаётся true (маршрут на месте, не разрешён только выход политики).
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip route default OpkgTun0",
	}}
	st2, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	iss := issueOfKind(st2.Issues, issuePolicyTunUnbound)
	if iss == nil {
		t.Fatalf("ожидался issue %q: %+v", issuePolicyTunUnbound, st2.Issues)
	}
	if iss.Severity != "warning" {
		t.Errorf("severity = %q, want warning", iss.Severity)
	}
	if !strings.Contains(iss.Message, "OpkgTun0") {
		t.Errorf("сообщение должно называть интерфейс: %q", iss.Message)
	}

	// Выключенный движок не светит ни одного policy-tun поля (урок PE-G).
	all, _ := h.store.Load()
	all.SingboxRouter.Enabled = false
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st3, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st3.PolicyTunIface != "" || st3.PolicyTunNDMSName != "" || st3.PolicyTunSourcePreserve != nil {
		t.Errorf("при Enabled=false все поля policy-tun пусты, получено %+v", st3)
	}
	if issueOfKind(st3.Issues, issuePolicyTunUnbound) != nil {
		t.Error("выключенный движок не должен ругаться на политику")
	}
}

// Permit стоит в чужой политике: для статуса это «не разрешён», и сообщение
// обязано назвать целевую — иначе пользователь смотрит на живой permit и не
// понимает, о чём issue.
func TestGetStatus_PolicyTunUnboundNamesTargetPolicy(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.withPolicy(t, "Policy1")
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return false }
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip route default OpkgTun0",
		"ip policy Policy0", "    permit global OpkgTun0", "!",
	}}

	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	iss := issueOfKind(st.Issues, issuePolicyTunUnbound)
	if iss == nil {
		t.Fatalf("permit в чужой политике не разрешение — ожидался issue: %+v", st.Issues)
	}
	if !strings.Contains(iss.Message, "Policy1") {
		t.Errorf("сообщение должно называть целевую политику: %q", iss.Message)
	}
}

func issueOfKind(issues []Issue, kind string) *Issue {
	for i := range issues {
		if issues[i].Kind == kind {
			return &issues[i]
		}
	}
	return nil
}

// status.policyTunSourcePreserve — ПРИМЕНЁННОЕ состояние, а не эхо настроек:
// static-NAT ставится только при подъёме режима, поэтому включение опции вживую
// обязано светиться расхождением с настройками до перезапуска режима (на нём
// держится подсказка в карточке).
func TestGetStatus_PolicyTunSourcePreserveIsApplied(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return false }
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}

	// Опцию включили вживую: настройки — да, применения (записей) — нет.
	setSourcePreserve(t, h, true, []string{"Home"})
	h.svc.deps.IPTables = errProbeIPTables()
	st, err := h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.PolicyTunSourcePreserve == nil {
		t.Fatal("PolicyTunSourcePreserve = nil, want непустой указатель в режиме policy-tun")
	}
	if *st.PolicyTunSourcePreserve {
		t.Error("применённое должно быть false: сегменты на static-NAT ещё не переведены")
	}

	// Записи появились (режим подняли заново) — статус становится true.
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.OpkgTun.PolicyTun = &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: natModeDynamic}}}
	if err := h.store.SetOpkgTunState(all.OpkgTun); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.IPTables = errProbeIPTables()
	st, err = h.svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus (applied): %v", err)
	}
	if st.PolicyTunSourcePreserve == nil || !*st.PolicyTunSourcePreserve {
		t.Errorf("PolicyTunSourcePreserve = %v, want true при записанных сегментах", st.PolicyTunSourcePreserve)
	}
}

// ---------------------------------------------------------------------------
// Спек ingress-заворота policy-tun: марки политик + перехват DNS
// ---------------------------------------------------------------------------

func newServiceWithExits(t *testing.T, exits []query.PolicyDefaultExit, err error) *ServiceImpl {
	t.Helper()
	return newTestService(t, Deps{
		Policies:  &fakeAccessPolicyProvider{exits: exits, exitsErr: err},
		FakeIPTun: FakeIPTunParams{TunAddr4: "172.18.0.1/30"},
	})
}

func TestPolicyTunIngressSpecMarksAndDNS(t *testing.T) {
	s := newServiceWithExits(t, []query.PolicyDefaultExit{{Name: "Policy1", Mark: "0xffffaab"}}, nil)
	spec, ok := s.policyTunIngressSpec(context.Background(), "opkgtun3", "OpkgTun3", storage.SingboxRouterSettings{})
	if !ok {
		t.Fatal("спек обязан быть применим")
	}
	if spec.Tag != PolicyTunDNSTag {
		t.Errorf("Tag = %q, want %q", spec.Tag, PolicyTunDNSTag)
	}
	if spec.NoDNAT {
		t.Error("NoDNAT в policy-tun больше не выставляется")
	}
	if spec.TunDNS != "172.18.0.2" {
		t.Errorf("TunDNS = %q", spec.TunDNS)
	}
	if len(spec.Marks) != 1 || spec.Marks[0] != "0xffffaab" {
		t.Errorf("Marks = %v", spec.Marks)
	}
}

func TestPolicyTunIngressSpecSkipsTickOnRCIError(t *testing.T) {
	s := newServiceWithExits(t, nil, errors.New("rci timeout"))
	if _, ok := s.policyTunIngressSpec(context.Background(), "opkgtun3", "OpkgTun3", storage.SingboxRouterSettings{}); ok {
		t.Error("ошибка чтения марок обязана давать skip, а не пустой спек")
	}
}

func TestPolicyTunIngressSpecEmptyOnNoPolicies(t *testing.T) {
	s := newServiceWithExits(t, nil, nil)
	spec, ok := s.policyTunIngressSpec(context.Background(), "opkgtun3", "OpkgTun3", storage.SingboxRouterSettings{})
	if !ok {
		t.Fatal("успешно прочитанный пустой набор — это применимый спек")
	}
	if len(spec.Marks) != 0 || spec.dnatHalf() {
		t.Error("без политик перехвата быть не должно")
	}
}

func TestPolicyTunIngressSpecHonorsBypass53(t *testing.T) {
	// Выключатель неделим по протоколам: перехват ставится и на udp, и на tcp,
	// поэтому 53 в bypass-списке ЛЮБОГО протокола гасит его целиком.
	// Формат bypass — "PORT-PORT UDP|TCP" (parseExtraPorts, presets.go:157).
	for _, extra := range []string{"50-60 UDP", "53 TCP"} {
		t.Run(extra, func(t *testing.T) {
			s := newServiceWithExits(t, []query.PolicyDefaultExit{{Name: "Policy1", Mark: "0xffffaab"}}, nil)
			sr := storage.SingboxRouterSettings{BypassExtraPorts: extra}
			spec, ok := s.policyTunIngressSpec(context.Background(), "opkgtun3", "OpkgTun3", sr)
			if !ok {
				t.Fatal("спек применим")
			}
			if spec.dnatHalf() {
				t.Error("53 в bypass — перехвата быть не должно")
			}
			// Маршрутная половина заворота обязана уцелеть: без NoDNAT active()
			// прочитал бы спек как неактивный и снёс бы ingress-заворот целиком.
			if !spec.NoDNAT {
				t.Error("NoDNAT не выставлен — заворот будет снесён вместе с перехватом")
			}
		})
	}
}

func TestPolicyTunIngressSpecKeepsRouteHalfWithoutTunDNS(t *testing.T) {
	// Адрес DNS туннеля не вычисляется (битый TunAddr4) — перехвата нет, но
	// маршрутная половина заворота обязана остаться применимой.
	s := newTestService(t, Deps{
		Policies:  &fakeAccessPolicyProvider{},
		FakeIPTun: FakeIPTunParams{TunAddr4: "не-адрес"},
	})
	sr := storage.SingboxRouterSettings{}
	spec, ok := s.policyTunIngressSpec(context.Background(), "opkgtun3", "OpkgTun3", sr)
	if !ok {
		t.Fatal("спек применим")
	}
	if !spec.NoDNAT {
		t.Error("NoDNAT не выставлен при невычислимом адресе DNS")
	}
	if spec.dnatHalf() {
		t.Error("перехват без адреса DNS невозможен")
	}
}

func TestPortRangesContain(t *testing.T) {
	ranges := []PortRange{{From: 50, To: 60}, {From: 443, To: 443}}
	for _, c := range []struct {
		port int
		want bool
	}{{53, true}, {50, true}, {60, true}, {49, false}, {61, false}, {443, true}, {80, false}} {
		if got := portRangesContain(ranges, c.port); got != c.want {
			t.Errorf("portRangesContain(%d) = %v, want %v", c.port, got, c.want)
		}
	}
}

// policyTunQoSSpecInputHarness поднимает policy-tun с одним активным классом
// QoS и возвращает готовый к тику харнесс. Мутация ОДНОГО входа спека — на
// вызывающем.
func policyTunQoSSpecInputHarness(t *testing.T) (*policyTunEnableHarness, *fakeWANIPCollector) {
	t.Helper()
	h := newPolicyTunEnableHarness(t, "")
	all, _ := h.store.Load()
	all.SingboxRouter.QoSClasses = []storage.SingboxQoSClass{
		{DSCP: 46, Name: "VoIP", Outbound: "direct", Enabled: true},
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	wan := &fakeWANIPCollector{ips: []string{"203.0.113.1/32"}}
	h.svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
	h.svc.deps.WANIPCollector = wan
	h.svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	h.svc.deps.XtDscpProbe = func(context.Context) bool { return true }
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	return h, wan
}

// tickPolicyTunQoS гоняет один тик reconcile со счётчиком установок.
func tickPolicyTunQoS(t *testing.T, h *policyTunEnableHarness, sr storage.SingboxRouterSettings) (int, string) {
	t.Helper()
	installs, last := 0, ""
	h.svc.deps.IPTables = newStubIPTables(func(_ context.Context, in string) error {
		installs++
		last = in
		return nil
	})
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	return installs, last
}

// Д1: netfilter policy-tun сравнивал ОДИН вход спека из семи — состав классов
// QoS. Смена bypass-портов, bypass-подсетей и WAN-адреса роутера не давала
// переустановки; в цепочках оставался RETURN на старый адрес, а трафик на
// новый адрес роутера уходил петлёй в sing-box до рестарта демона.
func TestReconcilePolicyTun_QoSBypassPortsChanged_Reinstalls(t *testing.T) {
	h, _ := policyTunQoSSpecInputHarness(t)

	all, _ := h.store.Load()
	all.SingboxRouter.BypassExtraPorts = "5555 UDP"
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)

	installs, last := tickPolicyTunQoS(t, h, sr)
	if installs != 1 {
		t.Fatalf("смена входа спека обязана переустановить правила: installs = %d, want 1", installs)
	}
	if !strings.Contains(last, "5555") {
		t.Errorf("новый порт обхода не попал в правила:\n%s", last)
	}

	// Второй тик без изменений — тишина.
	installs, _ = tickPolicyTunQoS(t, h, sr)
	if installs != 0 {
		t.Errorf("повторный тик без изменений: installs = %d, want 0", installs)
	}
}

// Д1, тот же корень: пользовательские bypass-подсети.
func TestReconcilePolicyTun_QoSBypassSubnetsChanged_Reinstalls(t *testing.T) {
	h, _ := policyTunQoSSpecInputHarness(t)

	all, _ := h.store.Load()
	all.SingboxRouter.BypassExtraSubnets = "10.9.9.0/24"
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)

	installs, last := tickPolicyTunQoS(t, h, sr)
	if installs != 1 {
		t.Fatalf("смена входа спека обязана переустановить правила: installs = %d, want 1", installs)
	}
	if !strings.Contains(last, "10.9.9.0/24") {
		t.Errorf("новая подсеть обхода не попала в правила:\n%s", last)
	}

	installs, _ = tickPolicyTunQoS(t, h, sr)
	if installs != 0 {
		t.Errorf("повторный тик без изменений: installs = %d, want 0", installs)
	}
}

// Д1, самый дорогой случай: сменился внешний адрес роутера (переподнятый WAN).
// Настройки не меняются вовсе — вход приходит от коллектора.
func TestReconcilePolicyTun_QoSWANIPChanged_Reinstalls(t *testing.T) {
	h, wan := policyTunQoSSpecInputHarness(t)
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)

	wan.ips = []string{"198.51.100.7/32"}

	installs, last := tickPolicyTunQoS(t, h, sr)
	if installs != 1 {
		t.Fatalf("смена входа спека обязана переустановить правила: installs = %d, want 1", installs)
	}
	if !strings.Contains(last, "198.51.100.7/32") {
		t.Errorf("новый адрес роутера не попал в правила:\n%s", last)
	}
	if strings.Contains(last, "203.0.113.1/32") {
		t.Errorf("RETURN на старый адрес роутера обязан уйти:\n%s", last)
	}

	installs, _ = tickPolicyTunQoS(t, h, sr)
	if installs != 0 {
		t.Errorf("повторный тик без изменений: installs = %d, want 0", installs)
	}
}

// Адрес KeenDNS приходит с роутера, а не из настроек, и в обход правил его
// заводит тот же билдер спека. В режиме tproxy это закреплено
// TestReconcileInstalled_KeenDNSCIDRChangeReinstalls, а в policy-tun не было
// закреплено ничем: билдер мог перестать спрашивать адрес, и обход KeenDNS
// молча пропал бы ровно в одном из двух режимов.
func TestReconcilePolicyTun_QoSKeenDNSCIDRChanged_Reinstalls(t *testing.T) {
	h, _ := policyTunQoSSpecInputHarness(t)
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)

	h.svc.setKeenDNSBypass([]string{"78.47.125.180"})

	installs, last := tickPolicyTunQoS(t, h, sr)
	if installs != 1 {
		t.Fatalf("появление адреса KeenDNS обязано переустановить правила: installs = %d, want 1", installs)
	}
	if !strings.Contains(last, "78.47.125.180/32") {
		t.Errorf("адрес KeenDNS не попал в правила обхода:\n%s", last)
	}

	installs, _ = tickPolicyTunQoS(t, h, sr)
	if installs != 0 {
		t.Errorf("повторный тик без изменений: installs = %d, want 0", installs)
	}
}

// Страховка инварианта (краснота компиляционная, не поведенческая: до снимка
// поля appliedSpec не существовало): применённый спек, отличающийся ТОЛЬКО
// режимным флагом, обязан переустанавливаться. Продакшн-пути сюда нет — смена
// режима идёт через SwitchRoutingMode, чей Disable для policy-tun безусловно
// зовёт Uninstall. Тест держит инвариант «сравнивается весь спек» на случай,
// если такой путь появится.
func TestReconcilePolicyTun_QoSSpecModeFlagChanged_Reinstalls(t *testing.T) {
	h, _ := policyTunQoSSpecInputHarness(t)
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)

	// Тот же спек, что вычислит тик, но с ДРУГИМ режимным флагом.
	applied := *h.svc.appliedSpec
	applied.DSCPOnly = false
	h.svc.appliedSpec = &applied

	installs, _ := tickPolicyTunQoS(t, h, sr)
	if installs != 1 {
		t.Fatalf("спек, отличающийся режимным флагом, обязан переустанавливаться: installs = %d, want 1", installs)
	}
}

// One-shot ассерт permit-ACL гейтится успехом пробы живости интерфейса: если
// проба упала, интерфейса может не быть вовсе, и permit-список уехал бы в
// конфиг роутера осиротевшим — снять его потом нечем.
func TestReconcilePolicyTun_SkipsPermitACLWhenProbeFailed(t *testing.T) {
	rcLines := []string{
		"interface OpkgTun0", "    ip global 65500", "!",
		"ip policy Policy0", "    permit global OpkgTun0",
	}
	t.Run("проба упала — ACL не ставится", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		sr := provisionPolicyTunForReconcile(t, h)
		h.svc.deps.RunningConfig = &fakeRunningConfig{lines: rcLines}
		h.svc.deps.OpkgTunIndices = &recIndices{err: errors.New("probe")}
		h.svc.policyTunACLAsserted = false

		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcilePolicyTun: %v", err)
		}
		if h.log.has("SetPermitACL:OpkgTun0") {
			t.Errorf("permit-ACL поставлен при упавшей пробе: %v", h.log.calls)
		}
		if h.svc.policyTunACLAsserted {
			t.Error("флаг one-shot взведён без успешной постановки ACL")
		}
		// v6-близнец гейта — своя строка кода и свой флаг: обвязка задаёт
		// FakeIPPool6, значит TunAddr6 непуст и по адресу разрешать есть что.
		if h.log.has("SetPermitACLv6:OpkgTun0") {
			t.Errorf("v6-permit-ACL поставлен при упавшей пробе: %v", h.log.calls)
		}
		if h.svc.policyTunACLv6Asserted {
			t.Error("v6-флаг one-shot взведён без успешной постановки ACL")
		}
	})
	t.Run("проба прошла — ACL ставится", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		sr := provisionPolicyTunForReconcile(t, h)
		h.svc.deps.RunningConfig = &fakeRunningConfig{lines: rcLines}
		h.svc.policyTunACLAsserted = false

		if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcilePolicyTun: %v", err)
		}
		if !h.log.has("SetPermitACL:OpkgTun0") {
			t.Errorf("permit-ACL не поставлен при здоровой пробе: %v", h.log.calls)
		}
		if !h.svc.policyTunACLAsserted {
			t.Error("флаг one-shot не взведён после успешной постановки ACL")
		}
		if !h.log.has("SetPermitACLv6:OpkgTun0") {
			t.Errorf("v6-permit-ACL не поставлен при здоровой пробе: %v", h.log.calls)
		}
		if !h.svc.policyTunACLv6Asserted {
			t.Error("v6-флаг one-shot не взведён после успешной постановки ACL")
		}
	})
}
