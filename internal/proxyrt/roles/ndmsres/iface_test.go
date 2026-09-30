package ndmsres

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// fakeRouter — модель NDMS: и Query, и Commands над одной картой. Тесты
// утверждают ИСХОД приведения (что стало с моделью), а не форму вызовов.
type fakeRouter struct {
	ifaces map[string]*IfaceFacts
	global map[string]bool
	acl    map[string]bool
	defrt  map[string]bool
	qErr   error
	failOn string // имя команды, которая отказывает
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{
		ifaces: map[string]*IfaceFacts{},
		global: map[string]bool{}, acl: map[string]bool{}, defrt: map[string]bool{},
	}
}

func (f *fakeRouter) Iface(_ context.Context, name string) (IfaceFacts, bool, error) {
	if f.qErr != nil {
		return IfaceFacts{}, false, f.qErr
	}
	if fc, ok := f.ifaces[name]; ok {
		return *fc, true, nil
	}
	return IfaceFacts{}, false, nil
}

func (f *fakeRouter) HasIPGlobal(_ context.Context, name string) (bool, error) {
	return f.global[name], f.qErr
}

func (f *fakeRouter) HasPermitAllACL(_ context.Context, name string) (bool, error) {
	return f.acl[name], f.qErr
}

func (f *fakeRouter) HasDefaultRoute(_ context.Context, name string) (bool, error) {
	return f.defrt[name], f.qErr
}

func (f *fakeRouter) cmd(name string) error {
	if f.failOn == name {
		return errors.New(name + ": rci отказал")
	}
	return nil
}

func (f *fakeRouter) CreateOpkgTunWithSecurityLevel(_ context.Context, name, desc, level string) error {
	if err := f.cmd("create"); err != nil {
		return err
	}
	f.ifaces[name] = &IfaceFacts{Description: desc, SecurityLevel: level}
	return nil
}

func (f *fakeRouter) DeleteOpkgTun(_ context.Context, name string) error {
	if err := f.cmd("delete"); err != nil {
		return err
	}
	delete(f.ifaces, name)
	return nil
}

func (f *fakeRouter) SetDescription(_ context.Context, name, d string) error {
	if err := f.cmd("set-description"); err != nil {
		return err
	}
	f.ifaces[name].Description = d
	return nil
}

func (f *fakeRouter) SetSecurityLevel(_ context.Context, name, l string) error {
	if err := f.cmd("set-security-level"); err != nil {
		return err
	}
	f.ifaces[name].SecurityLevel = l
	return nil
}

func (f *fakeRouter) SetIPGlobal(_ context.Context, name string) error {
	if err := f.cmd("ip-global"); err != nil {
		return err
	}
	f.global[name] = true
	return nil
}

func (f *fakeRouter) ClearIPGlobal(_ context.Context, name string) error {
	if err := f.cmd("clear-ip-global"); err != nil {
		return err
	}
	f.global[name] = false
	return nil
}

func (f *fakeRouter) SetAddress(_ context.Context, name, addr, mask string) error {
	if err := f.cmd("set-address"); err != nil {
		return err
	}
	f.ifaces[name].Address, f.ifaces[name].Mask = addr, mask
	return nil
}

func (f *fakeRouter) ClearAddress(_ context.Context, name string) error {
	if err := f.cmd("clear-address"); err != nil {
		return err
	}
	f.ifaces[name].Address, f.ifaces[name].Mask = "", ""
	return nil
}

func (f *fakeRouter) SetMTU(_ context.Context, name string, mtu int) error {
	if err := f.cmd("set-mtu"); err != nil {
		return err
	}
	f.ifaces[name].MTU = mtu
	return nil
}

func (f *fakeRouter) InterfaceUp(_ context.Context, name string) error {
	if err := f.cmd("up"); err != nil {
		return err
	}
	// Модель честна к create-on-reference: up НЕСУЩЕСТВУЮЩЕГО создаёт его —
	// ровно как NDMS (ndms_iface.go:381-387). Тест ниже ловит этот класс.
	if _, ok := f.ifaces[name]; !ok {
		f.ifaces[name] = &IfaceFacts{}
	}
	f.ifaces[name].AdminUp = true
	return nil
}

func (f *fakeRouter) InterfaceDown(_ context.Context, name string) error {
	if err := f.cmd("down"); err != nil {
		return err
	}
	if _, ok := f.ifaces[name]; !ok {
		f.ifaces[name] = &IfaceFacts{}
	}
	f.ifaces[name].AdminUp = false
	return nil
}

func (f *fakeRouter) SetPermitAllACL(_ context.Context, name string) error {
	if err := f.cmd("acl"); err != nil {
		return err
	}
	f.acl[name] = true
	return nil
}

func (f *fakeRouter) RemovePermitAllACL(_ context.Context, name string) error {
	if err := f.cmd("acl-remove"); err != nil {
		return err
	}
	delete(f.acl, name)
	return nil
}

func (f *fakeRouter) EnsureDefaultRouteCandidacy(_ context.Context, name string) error {
	if err := f.cmd("default-candidacy"); err != nil {
		return err
	}
	f.defrt[name] = true
	return nil
}

// drive прогоняет observe→plan→apply до пустого плана (максимум 5 проходов) —
// маленький аналог цикла движка для одного ресурса.
func drive(t *testing.T, r proxyrt.Resource) int {
	t.Helper()
	applied := 0
	for pass := 0; pass < 5; pass++ {
		obs, err := r.Observe(context.Background())
		if err != nil {
			t.Fatalf("observe: %v", err)
		}
		if !obs.Known {
			t.Fatalf("наблюдение недоступно: %+v", obs)
		}
		steps := r.Plan(obs)
		if len(steps) == 0 {
			return applied
		}
		for _, s := range steps {
			if err := r.Apply(context.Background(), s); err != nil {
				t.Fatalf("apply %s: %v", s.Op, err)
			}
			applied++
		}
	}
	t.Fatal("ресурс не сошёлся за 5 проходов")
	return applied
}

func TestIfaceCreatesWithLabelLevelMTU(t *testing.T) {
	rt := newFakeRouter()
	res := NewIface("ndms_interface", rt, rt)
	res.SetDesired(IfaceDesired{Present: true, Name: "OpkgTun17",
		Description: "AWGM WDTT", SecurityLevel: "private", MTU: 1280})

	drive(t, res)

	got := rt.ifaces["OpkgTun17"]
	if got == nil || got.Description != "AWGM WDTT" || got.SecurityLevel != "private" || got.MTU != 1280 {
		t.Fatalf("интерфейс не приведён: %+v", got)
	}
}

func TestIfaceRepairsDriftButNotMTU(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun17"] = &IfaceFacts{Description: "чужое", SecurityLevel: "private", MTU: 1280}
	res := NewIface("ndms_interface", rt, rt)
	res.SetDesired(IfaceDesired{Present: true, Name: "OpkgTun17",
		Description: "AWGM WDTT", SecurityLevel: "public", MTU: 1300})

	drive(t, res)

	got := rt.ifaces["OpkgTun17"]
	if got.Description != "AWGM WDTT" || got.SecurityLevel != "public" {
		t.Fatalf("дрейф не починен: %+v", got)
	}
	// Владелец MTU — ndms_address: MTU сервера (RAWCONF) не должен воевать с
	// константой интерфейса (I2 — иначе инстанс никогда не settled).
	if got.MTU != 1280 {
		t.Fatalf("Iface перетёр MTU (владелец — ndms_address): %+v", got)
	}
}

func TestIfaceDisabledNeverCreates(t *testing.T) {
	// «Интерфейса нет — не создаём» (спека §4.2): disabled-желаемое — «нет
	// ЛИБО down без адреса», отсутствие уже удовлетворяет.
	rt := newFakeRouter()
	res := NewIface("ndms_interface", rt, rt)
	res.SetDesired(IfaceDesired{Present: false, Name: "OpkgTun17",
		Description: "AWGM WDTT", SecurityLevel: "private", MTU: 1280})

	if n := drive(t, res); n != 0 {
		t.Fatalf("disabled на пустом месте сделал %d шагов", n)
	}
	if _, ok := rt.ifaces["OpkgTun17"]; ok {
		t.Fatal("создан интерфейс при disabled — класс PR #734")
	}
}

func TestIfaceDisabledKeepsExistingIface(t *testing.T) {
	// disabled НЕ удаляет живой интерфейс (спека §4.2): удаление — только
	// sweeper при deleted. Без этого случая страж «Present=false удаляет»
	// не ловится ни одним тестом (наблюдение исполнителя задачи 5).
	rt := newFakeRouter()
	rt.ifaces["OpkgTun17"] = &IfaceFacts{Description: "AWGM WDTT", SecurityLevel: "private", MTU: 1280}
	res := NewIface("ndms_interface", rt, rt)
	res.SetDesired(IfaceDesired{Present: false, Name: "OpkgTun17",
		Description: "AWGM WDTT", SecurityLevel: "private", MTU: 1280})

	if n := drive(t, res); n != 0 {
		t.Fatalf("disabled на живом интерфейсе сделал %d шагов", n)
	}
	if _, ok := rt.ifaces["OpkgTun17"]; !ok {
		t.Fatal("интерфейс удалён при disabled — root cause PR #544")
	}
}

func TestIfaceQueryErrorMakesNoSteps(t *testing.T) {
	rt := newFakeRouter()
	rt.qErr = errors.New("rci недоступен")
	res := NewIface("ndms_interface", rt, rt)
	res.SetDesired(IfaceDesired{Present: true, Name: "OpkgTun17",
		Description: "AWGM WDTT", SecurityLevel: "private", MTU: 1280})

	obs, err := res.Observe(context.Background())
	if err == nil && obs.Known {
		t.Fatalf("недоступный RCI обязан давать unknown: %+v, %v", obs, err)
	}
}

func TestAddressAppliesFromCallback(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun18"] = &IfaceFacts{Description: "x", MTU: 1300}
	res := NewAddress("ndms_address", rt, rt)
	res.SetDesired(AddressDesired{Name: "OpkgTun18", Want: func() (AddrWant, bool, string) {
		// MTU 1280 ≠ фактических 1300: Address — ЕДИНСТВЕННЫЙ владелец MTU
		// и обязан довести его до значения из наблюдения процесса (I2).
		return AddrWant{Address: "10.70.0.5", Mask: "255.255.255.255", MTU: 1280}, true, ""
	}})

	drive(t, res)

	got := rt.ifaces["OpkgTun18"]
	if got.Address != "10.70.0.5" || got.Mask != "255.255.255.255" {
		t.Fatalf("адрес не приведён: %+v", got)
	}
	if got.MTU != 1280 {
		t.Fatalf("MTU не доведён владельцем: %+v", got)
	}
}

func TestAddressUnknownWantIsWaiting(t *testing.T) {
	// Адрес выдаёт VPS; пока RAWCONF не пришёл — waiting с названным триггером,
	// а не settled и не failed.
	rt := newFakeRouter()
	rt.ifaces["OpkgTun18"] = &IfaceFacts{}
	res := NewAddress("ndms_address", rt, rt)
	res.SetDesired(AddressDesired{Name: "OpkgTun18", Want: func() (AddrWant, bool, string) {
		return AddrWant{}, false, "адрес ещё не выдан сервером"
	}})

	obs, err := res.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Known {
		t.Fatalf("неизвестное желаемое обязано давать Unknown: %+v", obs)
	}
}

func TestAddressClearedWhenDisabled(t *testing.T) {
	// disabled: адрес СНЯТ (root cause слоу-RCI: NDMS-адрес без kernel-адреса
	// крутит nginx-reload вечно, PR #544).
	rt := newFakeRouter()
	rt.ifaces["OpkgTun18"] = &IfaceFacts{Address: "10.70.0.5", Mask: "255.255.255.255"}
	res := NewAddress("ndms_address", rt, rt)
	res.SetDesired(AddressDesired{Name: "OpkgTun18", Clear: true})

	drive(t, res)

	if rt.ifaces["OpkgTun18"].Address != "" {
		t.Fatal("адрес обязан быть снят")
	}
}

func TestAddressAbsentIfaceMakesNoSteps(t *testing.T) {
	rt := newFakeRouter()
	res := NewAddress("ndms_address", rt, rt)
	res.SetDesired(AddressDesired{Name: "OpkgTun18", Want: func() (AddrWant, bool, string) {
		return AddrWant{Address: "10.70.0.5", Mask: "255.255.255.255"}, true, ""
	}})
	obs, _ := res.Observe(context.Background())
	if steps := res.Plan(obs); len(steps) != 0 {
		t.Fatalf("адрес на несуществующем интерфейсе — create-on-reference: %v", steps)
	}
}

func TestAdminStateUpAndDown(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun18"] = &IfaceFacts{Address: "10.70.0.5"}
	res := NewAdminState("ndms_admin_state", rt, rt)
	res.SetDesired(AdminDesired{Name: "OpkgTun18", Up: true})
	drive(t, res)
	if !rt.ifaces["OpkgTun18"].AdminUp {
		t.Fatal("интерфейс обязан подняться")
	}

	res.SetDesired(AdminDesired{Name: "OpkgTun18", Up: false})
	drive(t, res)
	if rt.ifaces["OpkgTun18"].AdminUp {
		t.Fatal("интерфейс обязан опуститься")
	}
}

// Отказ роутера на снятии того, чего уже нет, ошибкой не считается: цель
// достигнута. Проверяется формой отказа RCI — «system failed [0xcffd0217]».
func TestClearAddressIgnoresMissingObject(t *testing.T) {
	if err := ignoreMissingObject(nil); err != nil {
		t.Fatalf("успех стал ошибкой: %v", err)
	}
	missing := errors.New(`router reported error: "OpkgTun0": system failed [0xcffd0217].`)
	if err := ignoreMissingObject(missing); err != nil {
		t.Fatalf("отказ по отсутствующему объекту не погашен: %v", err)
	}
	other := errors.New("rci: connection refused")
	if err := ignoreMissingObject(other); err == nil {
		t.Fatal("чужая ошибка проглочена")
	}
}

// То же для снятия адреса: `clear address` по записи без устройства роутер
// отвергает («system failed [0xcffd0217]»), и шаг повторялся бы вечно.
func TestAddressBrokenIfaceNotCleared(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun0"] = &IfaceFacts{Address: "10.70.0.1", Mask: "255.255.0.0", Broken: true}
	res := NewAddress("ndms_address", rt, rt)
	res.SetDesired(AddressDesired{Name: "OpkgTun0", Clear: true})

	if n := drive(t, res); n != 0 {
		t.Fatalf("clear-address по записи без устройства: %d шагов", n)
	}
	if rt.ifaces["OpkgTun0"].Address == "" {
		t.Fatal("адрес снят, хотя роутер такую команду отвергает")
	}
}

// Запись в состоянии error — устройства за ней нет, и роутер отвергает `down`
// («OpkgTun0: system failed [0xcffd01b9]»). Планировать шаг, который заведомо
// провалится, значит повторять его на каждом прогоне вечно: стенд 2026-08-28
// намолотил 21 отказ за семь минут после остановки сервера.
func TestAdminStateBrokenIfaceNotLowered(t *testing.T) {
	rt := newFakeRouter()
	rt.ifaces["OpkgTun0"] = &IfaceFacts{AdminUp: true, Broken: true}
	res := NewAdminState("ndms_admin_state", rt, rt)
	res.SetDesired(AdminDesired{Name: "OpkgTun0", Up: false})

	if n := drive(t, res); n != 0 {
		t.Fatalf("down по записи без устройства: %d шагов", n)
	}
}

func TestAdminStateAbsentIfaceNeverRaised(t *testing.T) {
	// Зеркало случая ниже для Up=true: `interface X up` на несуществующем
	// СОЗДАЁТ его с чужими описанием и уровнем. Случай с Up=false страж
	// «убрать проверку !obs.Exists» не ловит — желаемое там совпадает с
	// умолчанием наблюдения (наблюдение исполнителя задачи 5).
	rt := newFakeRouter()
	res := NewAdminState("ndms_admin_state", rt, rt)
	res.SetDesired(AdminDesired{Name: "OpkgTun18", Up: true})
	if n := drive(t, res); n != 0 {
		t.Fatalf("up по несуществующему интерфейсу: %d шагов", n)
	}
	if _, ok := rt.ifaces["OpkgTun18"]; ok {
		t.Fatal("интерфейс создан по обращению — create-on-reference")
	}
}

func TestAdminStateAbsentIfaceNeverTouched(t *testing.T) {
	// `interface X up false` на несуществующем СОЗДАЁТ его — цикл PR #734.
	rt := newFakeRouter()
	res := NewAdminState("ndms_admin_state", rt, rt)
	res.SetDesired(AdminDesired{Name: "OpkgTun18", Up: false})
	if n := drive(t, res); n != 0 {
		t.Fatalf("down по несуществующему интерфейсу: %d шагов", n)
	}
	if _, ok := rt.ifaces["OpkgTun18"]; ok {
		t.Fatal("интерфейс создан по обращению — create-on-reference")
	}
}
