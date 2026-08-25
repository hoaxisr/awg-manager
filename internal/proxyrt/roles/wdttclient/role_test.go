package wdttclient

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/linkres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
)

// --- минимальные фейки зависимостей роли ---

type fakeLink struct {
	st   awgmproto.State
	err  error
	snap *control.Snapshot
}

func (f *fakeLink) State(context.Context) (awgmproto.State, error) {
	if f.err != nil {
		return awgmproto.State{}, f.err
	}
	return f.st, nil
}

func (f *fakeLink) Snapshot() (control.Snapshot, bool) {
	if f.snap == nil {
		return control.Snapshot{}, false
	}
	return *f.snap, true
}

func (f *fakeLink) AttachTun(context.Context, string, *os.File) error { return nil }
func (f *fakeLink) DetachTun(context.Context) error                   { return nil }

type nilRunner struct{}

func (nilRunner) Start(context.Context, []string) (int, error) { return 1, nil }
func (nilRunner) Stop(context.Context, int) error              { return nil }
func (nilRunner) AlivePID() (int, bool)                        { return 0, false }

type nilGate struct{}

func (nilGate) Check(context.Context, string, string, string, []string) error { return nil }

// countCmds — счётчик мутаций NDMS: гейт валидации доказывается нулём
// вызовов к роутеру, а не отсутствием шагов в плане.
type countCmds struct{ n int }

func (c *countCmds) CreateOpkgTunWithSecurityLevel(context.Context, string, string, string) error {
	c.n++
	return nil
}
func (c *countCmds) DeleteOpkgTun(context.Context, string) error              { c.n++; return nil }
func (c *countCmds) SetDescription(context.Context, string, string) error     { c.n++; return nil }
func (c *countCmds) SetSecurityLevel(context.Context, string, string) error   { c.n++; return nil }
func (c *countCmds) SetIPGlobal(context.Context, string) error                { c.n++; return nil }
func (c *countCmds) SetAddress(context.Context, string, string, string) error { c.n++; return nil }
func (c *countCmds) ClearAddress(context.Context, string) error               { c.n++; return nil }
func (c *countCmds) SetMTU(context.Context, string, int) error                { c.n++; return nil }
func (c *countCmds) InterfaceUp(context.Context, string) error                { c.n++; return nil }
func (c *countCmds) InterfaceDown(context.Context, string) error              { c.n++; return nil }
func (c *countCmds) SetPermitAllACL(context.Context, string) error            { c.n++; return nil }
func (c *countCmds) RemovePermitAllACL(context.Context, string) error         { c.n++; return nil }
func (c *countCmds) EnsureDefaultRouteCandidacy(context.Context, string) error {
	c.n++
	return nil
}

type memQuery struct{ facts map[string]ndmsres.IfaceFacts }

func (m memQuery) Iface(_ context.Context, name string) (ndmsres.IfaceFacts, bool, error) {
	f, ok := m.facts[name]
	return f, ok, nil
}
func (m memQuery) HasIPGlobal(context.Context, string) (bool, error)     { return false, nil }
func (m memQuery) HasPermitAllACL(context.Context, string) (bool, error) { return false, nil }
func (m memQuery) HasDefaultRoute(context.Context, string) (bool, error) { return false, nil }

type nilPolicies struct{}

func (nilPolicies) List(context.Context) ([]ndms.Policy, error) { return nil, nil }

type nilPermit struct{}

func (nilPermit) PermitInterface(context.Context, string, string, int) error { return nil }

type nilHooks struct{}

func (nilHooks) OnTunnelStart(context.Context, string, string) error { return nil }
func (nilHooks) OnTunnelStop(context.Context, string) error          { return nil }

type memRegistry struct{ m map[string]linkres.ExitInfo }

func (r *memRegistry) Lookup(id string) (linkres.ExitInfo, bool) {
	e, ok := r.m[id]
	return e, ok
}

func (r *memRegistry) Ensure(e linkres.ExitInfo) error {
	r.m[e.ID] = e
	return nil
}

type nilSync struct{}

func (nilSync) List(context.Context, string) ([]linkres.LinkedTunnel, error) { return nil, nil }
func (nilSync) Sync(context.Context, string, string) (int, error)            { return 0, nil }
func (nilSync) SetState(context.Context, string, bool) (int, error)          { return 0, nil }

type nilOcc struct{}

func (nilOcc) OccupiedLocalListenPorts(context.Context) (map[int]bool, error) {
	return map[int]bool{}, nil
}

func newRole(t *testing.T, link *fakeLink) (*Role, *memRegistry) {
	t.Helper()
	r, reg, _ := newRoleCmds(t, link)
	return r, reg
}

// liveSync — связь с ОДНИМ поднятым туннелем. Пустой nilSync шов «роль →
// ресурс» не проверяет вовсе: при пустом списке любое желаемое состояние даёт
// нулевое расхождение, и константа вместо намерения проходит незамеченной.
type liveSync struct{ running bool }

// List СВЕРЯЕТ идентификатор клиента: роль обязана передать в ресурс имя
// своего инстанса, и без сверки чужое имя прошло бы незамеченным — список
// пуст, расхождение нулевое, шага нет, и всё молча.
func (l *liveSync) List(_ context.Context, clientID string) ([]linkres.LinkedTunnel, error) {
	if clientID != "default" {
		return nil, nil
	}
	return []linkres.LinkedTunnel{
		{ID: "t1", Endpoint: "127.0.0.1:9000", Running: l.running, Lifecycle: true},
	}, nil
}
func (l *liveSync) Sync(context.Context, string, string) (int, error)   { return 0, nil }
func (l *liveSync) SetState(context.Context, string, bool) (int, error) { return 0, nil }

func newRoleSync(t *testing.T, sync linkres.EndpointSync) *Role {
	t.Helper()
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wt-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: &countCmds{}, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		Policies: nilPolicies{}, Permit: nilPermit{},
		Hooks: nilHooks{}, Registry: &memRegistry{m: map[string]linkres.ExitInfo{}},
		Sync: sync, Occ: nilOcc{}, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// linkedOf достаёт ресурс связи из ведомости. Отсутствие — провал теста, а не
// нулевой указатель: пропажа ресурса и есть проверяемый регресс.
func linkedOf(t *testing.T, res []proxyrt.Resource) proxyrt.Resource {
	t.Helper()
	for _, r := range res {
		if r.ID() == "linked_endpoint" {
			return r
		}
	}
	t.Fatalf("linked_endpoint не объявлен: %v", ids(res))
	return nil
}

// Шов «роль → ресурс»: намерение обязано доехать до ЖЕЛАЕМОГО состояния
// связанных туннелей. Состав ведомости этого не ловит — с константой вместо
// намерения он тот же самый, а выключенный клиент туннель не опустит никогда.
// Проверяется в обоих режимах: связь переживает смену режима на raw.
func TestIntentReachesLinkedResource(t *testing.T) {
	wgCfg := rawCfg()
	wgCfg.Mode = "wg"
	wgCfg.NdmsIface, wgCfg.RawIface = "", ""
	for _, mode := range []struct {
		name string
		cfg  roles.WdttClientConfig
	}{{"raw", rawCfg()}, {"wg", wgCfg}} {
		for _, c := range []struct {
			intent  proxyrt.Intent
			running bool
			op      string
		}{
			{proxyrt.IntentEnabled, false, "start"},
			{proxyrt.IntentDisabled, true, "stop"},
		} {
			role := newRoleSync(t, &liveSync{running: c.running})
			le := linkedOf(t, role.Resources(c.intent, mode.cfg, proxyrt.NewObservations()))
			obs, err := le.Observe(context.Background())
			if err != nil {
				t.Fatalf("%s/%s: observe: %v", mode.name, c.intent, err)
			}
			steps := le.Plan(obs)
			if len(steps) != 1 || steps[0].Op != c.op {
				t.Fatalf("%s/%s: план %+v, ожидали %q", mode.name, c.intent, steps, c.op)
			}
		}
	}
}

func newRoleCmds(t *testing.T, link *fakeLink) (*Role, *memRegistry, *countCmds) {
	t.Helper()
	reg := &memRegistry{m: map[string]linkres.ExitInfo{}}
	cmds := &countCmds{}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wt-client",
		Link: link, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: cmds, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		Policies: nilPolicies{}, Permit: nilPermit{},
		Hooks: nilHooks{}, Registry: reg,
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, reg, cmds
}

func rawCfg() roles.WdttClientConfig {
	return roles.WdttClientConfig{
		Mode: "raw", Listen: "127.0.0.1:9000", Peer: "vps:56003", Password: "pw",
		VKHashes: "h", NdmsIface: "OpkgTun18", RawIface: "opkgtun18",
		CaptchaMode: "rjs", Policies: []roles.PolicyPermit{{Name: "Policy0"}},
	}
}

func ids(res []proxyrt.Resource) []proxyrt.ResourceID {
	var out []proxyrt.ResourceID
	for _, r := range res {
		out = append(out, r.ID())
	}
	return out
}

func TestRawChainOrderIsTheSingleLedger(t *testing.T) {
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	got := ids(role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{
		"listen_port", "process", "linked_endpoint", "ndms_interface",
		"ndms_address", "ndms_admin_state", "policy_exit", "tun_handoff",
		"policy_membership", "client_routes", "routable_exit",
	}
	if len(got) != len(want) {
		t.Fatalf("состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок: %v, ожидали %v", got, want)
		}
	}
}

func TestWgChainIsShort(t *testing.T) {
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	cfg := rawCfg()
	cfg.Mode = "wg"
	cfg.NdmsIface, cfg.RawIface = "", ""
	got := ids(role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{"listen_port", "process", "linked_endpoint"}
	if len(got) != len(want) {
		t.Fatalf("wg-состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wg-порядок: %v", got)
		}
	}
}

func TestWgDisabledLedgerKeepsLinkedEndpoint(t *testing.T) {
	// M11: у выключенного wg-клиента нет ни доводки endpoint'а, ни приговора
	// порта — но linked_endpoint в ведомости ОБЯЗАН быть: опустить связанный
	// туннель больше некому, а туннель с адресом мёртвого процесса остаётся
	// «работающим» и тянет на себя маршруты (амендмент B).
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	cfg := rawCfg()
	cfg.Mode = "wg"
	cfg.NdmsIface, cfg.RawIface = "", ""
	got := ids(role.Resources(proxyrt.IntentDisabled, cfg, proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{"process", "linked_endpoint"}
	if len(got) != len(want) {
		t.Fatalf("disabled wg-состав: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("disabled wg-порядок: %v", got)
		}
	}
}

func TestDisabledDeclaresSameResourcesDifferentDesired(t *testing.T) {
	// G1: disabled — не «другой код», а другое желаемое тех же ресурсов.
	// Интерфейс с адресом обязан получить clear-address и down, НЕ удаление.
	link := &fakeLink{err: control.ErrNoSocket}
	role, _ := newRole(t, link)
	q := memQuery{facts: map[string]ndmsres.IfaceFacts{
		"OpkgTun18": {Address: "10.70.0.5", Mask: "255.255.255.255", AdminUp: true},
	}}
	role.deps.Query = q
	role.rebuildForTest() // пересоздать ресурсы с новым Query (хелпер теста)

	res := role.Resources(proxyrt.IntentDisabled, rawCfg(), proxyrt.NewObservations())
	byID := map[proxyrt.ResourceID]proxyrt.Resource{}
	for _, r := range res {
		byID[r.ID()] = r
	}
	if _, ok := byID["tun_handoff"]; ok {
		t.Fatal("выключенному инстансу нечего прикреплять")
	}

	addr := byID["ndms_address"]
	obs, err := addr.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := addr.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "clear-address" {
		t.Fatalf("disabled: адрес обязан сниматься (PR #544): %v", steps)
	}

	admin := byID["ndms_admin_state"]
	obs, _ = admin.Observe(context.Background())
	steps = admin.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "down" {
		t.Fatalf("disabled: интерфейс обязан опускаться: %v", steps)
	}
}

func TestAddressWantDerivedFromProcessSnapshot(t *testing.T) {
	link := &fakeLink{snap: &control.Snapshot{
		State: awgmproto.State{Address: "10.70.0.7", MTU: 1280},
		At:    time.Now(),
	}}
	role, _ := newRole(t, link)
	res := role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations())
	var addr proxyrt.Resource
	for _, r := range res {
		if r.ID() == "ndms_address" {
			addr = r
		}
	}
	obs, err := addr.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Known {
		t.Fatalf("адрес известен из снимка процесса: %+v", obs)
	}
	// Интерфейса в NDMS нет (memQuery пуст) — шагов нет (create-on-reference).
	if steps := addr.Plan(obs); len(steps) != 0 {
		t.Fatalf("нет интерфейса — нет адресных шагов: %v", steps)
	}
}

func TestAddressUnknownUntilRawconf(t *testing.T) {
	link := &fakeLink{snap: &control.Snapshot{
		State: awgmproto.State{}, // процесс жив, адреса ещё нет
		At:    time.Now(),
	}}
	role, _ := newRole(t, link)
	res := role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations())
	for _, r := range res {
		if r.ID() != "ndms_address" {
			continue
		}
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Known {
			t.Fatalf("до RAWCONF адрес обязан быть Unknown (waiting): %+v", obs)
		}
	}
}

func TestRawResourcesAreLongLived(t *testing.T) {
	// I5: reconcile зовёт Resources дважды за проход и применяет по второму
	// списку. Пересозданный ресурс терял бы защёлки (ClientRoutes.notified,
	// окно старта и backoff процесса) — уведомления пошли бы в цикле.
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	first := role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations())
	second := role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations())
	if len(first) != len(second) {
		t.Fatalf("состав поплыл между вызовами: %v против %v", ids(first), ids(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("ресурс %s пересоздан между вызовами Resources", first[i].ID())
		}
	}
}

func TestRoutableExitPublishedEvenWhenDown(t *testing.T) {
	// Регистрация при создании, Ready=false для лежачего (§5).
	role, reg := newRole(t, &fakeLink{err: control.ErrNoSocket})
	res := role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations())
	for _, r := range res {
		if r.ID() != "routable_exit" {
			continue
		}
		obs, _ := r.Observe(context.Background())
		steps := r.Plan(obs)
		if len(steps) != 1 {
			t.Fatalf("выход обязан публиковаться и лежачим: %v", steps)
		}
		if err := r.Apply(context.Background(), steps[0]); err != nil {
			t.Fatal(err)
		}
	}
	// Опубликован — и опубликован НЕ готовым: процесс не поднялся.
	got, ok := reg.Lookup(RawTunnelID("default"))
	if !ok {
		t.Fatal("выход не опубликован в реестре")
	}
	if got.Ready {
		t.Fatalf("процесс не поднялся — выход не может быть Ready: %+v", got)
	}
}

func TestResourcesDeclareWithoutTouchingRouter(t *testing.T) {
	// G1: Resources — чистая декларация состава и желаемого. Ни при каком
	// намерении и ни в одном режиме за её вызов к роутеру не уходит НИ ОДНОЙ
	// мутации: снятие выражается желаемым тех же ресурсов, а не рукописным
	// вызовом в ветке выхода.
	//
	// Счётчиком, а не составом списка: рукописный DeleteOpkgTun в ветке
	// disabled состав ведомости не меняет и потому невидим для стражей
	// порядка. Ровно этим и опасен — ведомость перестаёт быть ведомостью.
	wg := rawCfg()
	wg.Mode, wg.NdmsIface, wg.RawIface = "wg", "", ""
	for _, mode := range []struct {
		name string
		cfg  roles.WdttClientConfig
	}{{"raw", rawCfg()}, {"wg", wg}} {
		for _, intent := range []proxyrt.Intent{
			proxyrt.IntentEnabled, proxyrt.IntentDisabled, proxyrt.IntentDeleted,
		} {
			role, reg, cmds := newRoleCmds(t, &fakeLink{err: control.ErrNoSocket})
			role.Resources(intent, mode.cfg, proxyrt.NewObservations())
			if cmds.n != 0 {
				t.Fatalf("%s/%s: за сборку декларации к NDMS ушло %d команд", mode.name, intent, cmds.n)
			}
			if len(reg.m) != 0 {
				t.Fatalf("%s/%s: за сборку декларации опубликован выход: %v", mode.name, intent, reg.m)
			}
		}
	}
}

func TestRawDisabledLedgerIsExhaustive(t *testing.T) {
	// Состав disabled-ведомости пиннится ЦЕЛИКОМ, как у wg-ветки: потеря
	// ресурса из списка тестов состава не роняет, а выключенный инстанс
	// молча перестаёт чинить дрейф (описание, security-level, адрес, down).
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	got := ids(role.Resources(proxyrt.IntentDisabled, rawCfg(), proxyrt.NewObservations()))
	want := []proxyrt.ResourceID{
		"process", "linked_endpoint", "ndms_interface", "ndms_address",
		"ndms_admin_state", "client_routes", "routable_exit",
	}
	if len(got) != len(want) {
		t.Fatalf("disabled-состав: %v, ожидали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("disabled-порядок: %v, ожидали %v", got, want)
		}
	}
}

func TestDeletedDeclaresTheSameAsDisabled(t *testing.T) {
	// Удаление = выключение плюс уборка по меткам (§4.2): своей ветки
	// декларации у него нет и быть не должно — иначе вернулся бы рукописный
	// список снятия. Страж на равенство ведомостей: молчаливое «deleted не
	// disabled» оставило бы удаляемый инстанс работающим, а его ресурсы —
	// в желаемом enabled.
	role, _ := newRole(t, &fakeLink{err: control.ErrNoSocket})
	off := ids(role.Resources(proxyrt.IntentDisabled, rawCfg(), proxyrt.NewObservations()))
	gone := ids(role.Resources(proxyrt.IntentDeleted, rawCfg(), proxyrt.NewObservations()))
	if len(off) != len(gone) {
		t.Fatalf("ведомость deleted %v разошлась с disabled %v", gone, off)
	}
	for i := range off {
		if off[i] != gone[i] {
			t.Fatalf("ведомость deleted %v разошлась с disabled %v", gone, off)
		}
	}
}

func TestInvalidConfigTouchesNoNDMS(t *testing.T) {
	// I3 ревью: у клиента приговор Validate() прикрыт порядком (process выше
	// NDMS-хвоста), но опираться на порядок нельзя — гейт делает свойство
	// «невалидный конфиг не мутирует роутер» общим для всех четырёх ролей.
	role, reg, cmds := newRoleCmds(t, &fakeLink{err: control.ErrNoSocket})
	cfg := rawCfg()
	cfg.Password = "" // wt-client без -password выходит: отказ Validate

	res, phase := proxyrt.NewReconciler(role, cfg, proxyrt.ReconcileOpts{}).
		Run(context.Background(), proxyrt.IntentEnabled)

	if cmds.n != 0 {
		t.Fatalf("на невалидном конфиге к NDMS ушло %d вызовов: %+v", cmds.n, res.States)
	}
	if len(reg.m) != 0 {
		t.Fatalf("на невалидном конфиге опубликован выход: %+v", reg.m)
	}
	if phase != proxyrt.PhaseFailed {
		t.Fatalf("фаза %v, ожидали failed: %+v", phase, res.States)
	}
	reason := ""
	for _, s := range res.States {
		if s.Status == proxyrt.StatusFailed {
			reason = s.Error
		}
	}
	if !strings.Contains(reason, "password") {
		t.Fatalf("приговор обязан называть причину валидации: %q (%+v)", reason, res.States)
	}
}

// --- порядок permit'ов доезжает от конфига до ресурса членства ---

type onePolicy struct{ p ndms.Policy }

func (o onePolicy) List(context.Context) ([]ndms.Policy, error) { return []ndms.Policy{o.p}, nil }

func TestPolicyOrderReachesMembership(t *testing.T) {
	// Позиция permit'а — приоритет кандидатуры default route. Роль обязана
	// донести её из конфига до ресурса: потерянный Order уводит выход в хвост
	// политики после первого же апгрейда.
	reg := &memRegistry{m: map[string]linkres.ExitInfo{}}
	pol := onePolicy{p: ndms.Policy{Name: "Policy0",
		Interfaces: []ndms.PermittedIface{{Name: "ISP"}, {Name: "Wireguard0"}}}}
	r, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wt-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: nilRunner{}, Gate: nilGate{},
		Cmds: &countCmds{}, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		Policies: pol, Permit: nilPermit{},
		Hooks: nilHooks{}, Registry: reg,
		Sync: nilSync{}, Occ: nilOcc{}, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := rawCfg()
	// Ноль — риск C1: он же законная ВЕРХНЯЯ позиция. Хвост здесь равен 2,
	// так что подмена нуля на «не задано» видна.
	cfg.Policies = []roles.PolicyPermit{{Name: "Policy0", Order: orderPtr(0)}}
	var member proxyrt.Resource
	for _, res := range r.Resources(proxyrt.IntentEnabled, cfg, proxyrt.NewObservations()) {
		if res.ID() == "policy_membership" {
			member = res
		}
	}
	if member == nil {
		t.Fatal("ресурса policy_membership нет в ведомости")
	}
	obs, err := member.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps := member.Plan(obs)
	if len(steps) != 1 || steps[0].Args["order"] != "0" {
		t.Fatalf("order из конфига не доехал до шага: %v", steps)
	}
}

func orderPtr(v int) *int { return &v }

// failRunner — старт валится по требованию: единственный способ загнать
// процесс в паузу анти-флаппинга через публичную поверхность роли.
type failRunner struct{ err error }

func (f *failRunner) Start(context.Context, []string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return 4821, nil
}
func (f *failRunner) Stop(context.Context, int) error { return nil }
func (f *failRunner) AlivePID() (int, bool)           { return 0, false }

// procOf достаёт ресурс процесса из ведомости.
func procOf(t *testing.T, res []proxyrt.Resource) proxyrt.Resource {
	t.Helper()
	for _, r := range res {
		if r.ID() == roles.RProcess {
			return r
		}
	}
	t.Fatalf("process не объявлен: %v", ids(res))
	return nil
}

// Шов «роль → ресурс процесса»: сброс паузы анти-флаппинга обязан доезжать до
// процесса. Без него обновление подписки не поднимает клиента до пяти минут —
// ровно тогда, когда человек чинит его руками.
func TestResetStartBackoffReachesProcess(t *testing.T) {
	now := time.Unix(1700000000, 0)
	run := &failRunner{err: errors.New("нет бинаря")}
	role, err := New(Deps{
		Instance: "default", Binary: "/opt/bin/wt-client",
		Link: &fakeLink{err: control.ErrNoSocket}, Runner: run, Gate: nilGate{},
		Cmds: &countCmds{}, Query: memQuery{facts: map[string]ndmsres.IfaceFacts{}},
		Policies: nilPolicies{}, Permit: nilPermit{},
		Hooks: nilHooks{}, Registry: &memRegistry{m: map[string]linkres.ExitInfo{}},
		Sync: nilSync{}, Occ: nilOcc{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	proc := procOf(t, role.Resources(proxyrt.IntentEnabled, rawCfg(), proxyrt.NewObservations()))
	obs, err := proc.Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	steps := proc.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "start" {
		t.Fatalf("план %+v, ожидали start", steps)
	}
	if err := proc.Apply(context.Background(), steps[0]); err == nil {
		t.Fatal("первый старт обязан упасть")
	}
	if err := proc.Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "отложен") {
		t.Fatalf("повтор обязан быть отложен анти-флаппингом: %v", err)
	}

	role.ResetStartBackoff()
	run.err = nil
	// Часы не двигаются: если сброс не дошёл, старт останется отложенным.
	if err := proc.Apply(context.Background(), steps[0]); err != nil {
		t.Fatalf("сброс роли не дошёл до процесса: %v", err)
	}
}
