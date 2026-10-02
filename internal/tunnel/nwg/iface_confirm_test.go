package nwg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Тесты F546: команды по интерфейсу туннеля — только по Confirmed из свежего
// списка. Оракул — FakeNDMS харнесса newLifecycleOperator: он видит чтения,
// команды command-слоя и батчи транспорта.

func awgObfuscatedIface() storage.AWGInterface {
	return storage.AWGInterface{
		Address:        "10.0.0.2/32",
		MTU:            1420,
		PrivateKey:     "k",
		AWGObfuscation: storage.AWGObfuscation{Jc: 4, Jmin: 40, Jmax: 70, H1: "10-20", H2: "2", H3: "3", H4: "4"},
	}
}

func (p *procStub) addWrites() int { return p.countWritesTo("/proc/awg_proxy/add") }
func (p *procStub) delWrites() int { return p.countWritesTo("/proc/awg_proxy/del") }

// Старт по снятому WireguardN: ни одной команды (любая `interface Wireguard0 …`
// создала бы его заново), слот kmod не собирается, ошибка — ErrInterfaceGone.
func TestStart_InterfaceGone_NoCommands(t *testing.T) {
	for _, tc := range []struct {
		name      string
		asc, asc3 bool
	}{{"native", true, true}, {"proxy", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			o, stub, poster, f, srv := newLifecycleOperator(t, tc.asc, tc.asc3)
			f.Remove("Wireguard0")
			err := o.Start(context.Background(), nwgStored(awgObfuscatedIface()))
			if !errors.Is(err, tunnel.ErrInterfaceGone) {
				t.Fatalf("want ErrInterfaceGone, got %v", err)
			}
			if !strings.Contains(err.Error(), "Wireguard0") {
				t.Fatalf("имени интерфейса нет в ошибке: %v", err)
			}
			if len(poster.list()) != 0 || srv.log.posts() != 0 || stub.addWrites() != 0 || f.E != 0 || f.Phantoms != 0 {
				t.Fatalf("no RCI commands, no kmod slot, no E: posts=%v batches=%d add=%d E=%d phantoms=%d",
					poster.list(), srv.log.posts(), stub.addWrites(), f.E, f.Phantoms)
			}
		})
	}
}

// R13/F550: список не прочитался — старт отказывает, ни одной команды и слота.
// Иначе на холодном старте команды ушли бы поверх живого туннеля вслепую.
func TestStart_ListError_NoCommands(t *testing.T) {
	for _, tc := range []struct {
		name      string
		asc, asc3 bool
	}{{"native", true, true}, {"proxy", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			o, stub, poster, f, srv := newLifecycleOperator(t, tc.asc, tc.asc3)
			f.FailList(errors.New("rci down"))
			err := o.Start(context.Background(), nwgStored(awgObfuscatedIface()))
			if err == nil || errors.Is(err, tunnel.ErrInterfaceGone) {
				t.Fatalf("want list error, got %v", err)
			}
			if len(poster.list()) != 0 || srv.log.posts() != 0 || stub.addWrites() != 0 || f.E != 0 {
				t.Fatalf("posts=%v batches=%d add=%d E=%d", poster.list(), srv.log.posts(), stub.addWrites(), f.E)
			}
		})
	}
}

func TestDelete_InterfaceGone_LocalOnly(t *testing.T) {
	o, stub, poster, f, srv := newLifecycleOperator(t, false, false)
	f.Remove("Wireguard0")
	if _, err := o.kmod.AddTunnel("awg0", defaultCfg()); err != nil {
		t.Fatal(err)
	}
	st := nwgStored(awgObfuscatedIface())
	st.PingCheck = &storage.TunnelPingCheck{Enabled: true}
	if err := o.Delete(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if stub.delWrites() != 1 {
		t.Fatal("kmod slot must be removed")
	}
	if srv.log.posts() != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("batch posts=%d E=%d phantoms=%d", srv.log.posts(), f.E, f.Phantoms)
	}
	for _, p := range poster.list() {
		if strings.Contains(p, `"interface":{"Wireguard0"`) || strings.Contains(p, `"name":"Wireguard0"`) ||
			strings.Contains(p, "interface Wireguard0") {
			t.Fatalf("command addressed to a gone interface: %s", p)
		}
	}
	if !poster.has(`"ping-check":{"profile":{"awgm-awg0":{"no":true}}}`) {
		t.Fatalf("orphan ping-check profile must still be removed: %v", poster.list())
	}
	if o.queries.Interfaces.HasPending() {
		t.Fatal("no pending after delete")
	}
}

// Список не прочитался: локальные шаги (слот, страж) сделаны, NDMS-часть — нет,
// ошибка наружу, чтобы запись туннеля осталась для повтора.
func TestDelete_ListError_FailsClosedAfterLocalSteps(t *testing.T) {
	o, stub, poster, f, srv := newLifecycleOperator(t, false, false)
	if _, err := o.kmod.AddTunnel("awg0", defaultCfg()); err != nil {
		t.Fatal(err)
	}
	o.guardRegister("awg0", guardEntry{iface: "nwg0", pubkey: "pk", endpoint: "203.0.113.10:5060",
		spec: "vpn.example.com:5060", name: "Wireguard0", mode: guardNDMS})
	f.FailList(errors.New("rci down"))
	st := nwgStored(awgObfuscatedIface())
	st.PingCheck = &storage.TunnelPingCheck{Enabled: true}
	if err := o.Delete(context.Background(), st); err == nil {
		t.Fatal("want error on unreadable list")
	}
	if stub.delWrites() != 1 {
		t.Fatal("kmod slot must be removed even on list error")
	}
	if o.guardHas("awg0") {
		t.Fatal("guard must be unregistered even on list error")
	}
	if srv.log.posts() != 0 || len(poster.list()) != 0 {
		t.Fatalf("no NDMS commands on list error: batches=%d posts=%v", srv.log.posts(), poster.list())
	}
}

// Интерфейс есть: `no interface` по Confirmed, запись забыта, E/фантомов нет.
func TestDelete_Present_NoInterfaceThenForget(t *testing.T) {
	o, _, _, f, srv := newLifecycleOperator(t, false, false)
	if err := o.Delete(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if f.Has("Wireguard0") {
		t.Fatalf("Wireguard0 не снят: %v", f.Posts)
	}
	if rec, _ := o.queries.Interfaces.Get(context.Background(), "Wireguard0"); rec != nil {
		t.Fatal("record must be forgotten after successful delete")
	}
	if f.E != 0 || f.Phantoms != 0 || srv.log.posts() != 1 {
		t.Fatalf("E=%d phantoms=%d batches=%d", f.E, f.Phantoms, srv.log.posts())
	}
}

// Батч сноса отказал не по «интерфейса нет»: запись в кэше остаётся (иначе её
// индекс освободился бы, и следующий Create писал бы поверх живого), ошибка наружу.
func TestDelete_BatchFailure_KeepsRecord(t *testing.T) {
	o, _, _, _, srv := newLifecycleOperator(t, false, false)
	srv.respond = func(string) (string, bool) {
		return `[{"status":"error","message":"interface is busy"},{}]`, true
	}
	if err := o.Delete(context.Background(), nwgStored(awgObfuscatedIface())); err == nil {
		t.Fatal("want error on failed delete batch")
	}
	if rec, _ := o.queries.Interfaces.Get(context.Background(), "Wireguard0"); rec == nil {
		t.Fatal("record must stay in cache when delete failed")
	}
}

// Батч сноса ответил «интерфейса нет» — снято до нас: запись забывается, это успех.
func TestDelete_BatchMissingInterface_Forgets(t *testing.T) {
	o, _, _, _, srv := newLifecycleOperator(t, false, false)
	srv.respond = func(string) (string, bool) {
		return `[{"status":"error","message":"unable to find interface \"Wireguard0\""},{}]`, true
	}
	if err := o.Delete(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if rec, _ := o.queries.Interfaces.Get(context.Background(), "Wireguard0"); rec != nil {
		t.Fatal("record must be forgotten when NDMS says it is missing")
	}
}

// Создание пакетом: сначала только `interface WireguardN`, затем чтение
// списка (Confirm), затем настройки тем же именем по Confirmed.
func TestCreateViaBatch_CreateThenConfirmThenSettings(t *testing.T) {
	o, _, _, f, srv := newLifecycleOperator(t, false, false)
	f.ExpectCreate("Wireguard1")
	idx, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatalf("index = %d, want 1", idx)
	}
	bodies, listsAt := srv.sent()
	if len(bodies) != 2 {
		t.Fatalf("want 2 batches, got %d: %v", len(bodies), bodies)
	}
	if strings.TrimSpace(bodies[0]) != `[{"interface":{"name":"Wireguard1"}}]` {
		t.Fatalf("first batch must be the bare create: %s", bodies[0])
	}
	if listsAt[1] <= listsAt[0] {
		t.Fatalf("settings sent without a list read after create: lists %v", listsAt)
	}
	if !strings.Contains(bodies[1], `"name":"Wireguard1"`) || !strings.Contains(bodies[1], `"description":"n"`) {
		t.Fatalf("settings batch: %s", bodies[1])
	}
	if f.E != 0 || f.Phantoms != 0 || len(f.Created) != 1 {
		t.Fatalf("E=%d phantoms=%d created=%v", f.E, f.Phantoms, f.Created)
	}
}

// NDMS ответил «created», а в списке записи нет за всё ожидание: ошибка,
// настройки не шлются (они создали бы интерфейс фантомом), созданное
// снесено (R54) — сироты нет, E == 0.
func TestCreateViaBatch_AbsentAfterCreate_Error(t *testing.T) {
	o, _, _, f, srv := newLifecycleOperator(t, false, false)
	f.ExpectCreate("Wireguard1")
	f.HideCreated(100)
	o.queries.Interfaces.SetCreatedBackoff()
	_, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if !errors.Is(err, query.ErrNotListed) || !strings.Contains(err.Error(), "Wireguard1") {
		t.Fatalf("want ErrNotListed naming Wireguard1, got %v", err)
	}
	bodies, _ := srv.sent()
	if len(bodies) != 2 || strings.TrimSpace(bodies[1]) != `{"interface":{"Wireguard1":{"no":true}}}` {
		t.Fatalf("want create + drop, no settings: %q", bodies)
	}
	if f.Has("Wireguard1") || f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("has=%v E=%d phantoms=%d", f.Has("Wireguard1"), f.E, f.Phantoms)
	}
}

// Два создания подряд, хуки ifcreated не доставлены: индекс второго выбирается
// по карте, которую заполнил Confirm первого, — разные индексы, без фантомов.
func TestCreateViaBatch_BackToBack_OracleDistinct(t *testing.T) {
	o, _, _, f, _ := newLifecycleOperator(t, false, false)
	f.ExpectCreate("Wireguard1", "Wireguard2")
	a, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a != 1 || b != 2 {
		t.Fatalf("indexes A=%d B=%d, want 1 and 2", a, b)
	}
	if f.E != 0 || f.Phantoms != 0 || len(f.Created) != 2 {
		t.Fatalf("E=%d phantoms=%d created=%v", f.E, f.Phantoms, f.Created)
	}
}

// Stop по снятому: останавливать нечего — nil, слот снят, команд нет.
// Список не прочитался — ошибка после локальных шагов.
func TestStop_GoneAndListError(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		o, stub, poster, f, srv := newLifecycleOperator(t, false, false)
		f.Remove("Wireguard0")
		_, _ = o.kmod.AddTunnel("awg0", defaultCfg())
		if err := o.Stop(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
			t.Fatal(err)
		}
		if stub.delWrites() != 1 || srv.log.posts() != 0 || len(poster.list()) != 0 || f.E != 0 || f.Phantoms != 0 {
			t.Fatalf("del=%d batches=%d posts=%v E=%d phantoms=%d", stub.delWrites(), srv.log.posts(), poster.list(), f.E, f.Phantoms)
		}
	})
	t.Run("list error", func(t *testing.T) {
		o, stub, poster, f, srv := newLifecycleOperator(t, false, false)
		f.FailList(errors.New("rci down"))
		_, _ = o.kmod.AddTunnel("awg0", defaultCfg())
		if err := o.Stop(context.Background(), nwgStored(awgObfuscatedIface())); err == nil {
			t.Fatal("want error on unreadable list")
		}
		if stub.delWrites() != 1 || srv.log.posts() != 0 || len(poster.list()) != 0 {
			t.Fatalf("del=%d batches=%d posts=%v", stub.delWrites(), srv.log.posts(), poster.list())
		}
	})
}

func TestSuspendProxy_Gone_SlotRemovedNoCommands(t *testing.T) {
	o, stub, poster, f, srv := newLifecycleOperator(t, false, false)
	f.Remove("Wireguard0")
	_, _ = o.kmod.AddTunnel("awg0", defaultCfg())
	if err := o.SuspendProxy(context.Background(), nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if stub.delWrites() != 1 || srv.log.posts() != 0 || len(poster.list()) != 0 || f.Phantoms != 0 {
		t.Fatalf("del=%d batches=%d posts=%v phantoms=%d", stub.delWrites(), srv.log.posts(), poster.list(), f.Phantoms)
	}
}

func TestPingCheck_InterfaceGone(t *testing.T) {
	o, _, poster, f, _ := newLifecycleOperator(t, false, false)
	f.Remove("Wireguard0")
	st := nwgStored(awgObfuscatedIface())
	err := o.ConfigurePingCheck(context.Background(), st, ndms.PingCheckConfig{Host: "8.8.8.8", Mode: "icmp"})
	if !errors.Is(err, tunnel.ErrInterfaceGone) {
		t.Fatalf("Configure: want ErrInterfaceGone, got %v", err)
	}
	if len(poster.list()) != 0 {
		t.Fatalf("Configure on gone iface sent %v", poster.list())
	}
	if err := o.RemovePingCheck(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	for _, p := range poster.list() {
		if strings.Contains(p, "Wireguard0") {
			t.Fatalf("Remove addressed a gone interface: %s", p)
		}
	}
	if !poster.has(`"ping-check":{"profile":{"awgm-awg0":{"no":true}}}`) || f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("orphan profile removal: %v E=%d phantoms=%d", poster.list(), f.E, f.Phantoms)
	}
}

// Бутовое восстановление слота и пересборка при правке — по снятому
// интерфейсу ErrInterfaceGone, слот не собирается, endpoint не шлётся.
func TestKmodSlot_InterfaceGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(o *OperatorNativeWG, st *storage.AWGTunnel) error
	}{
		{"RestoreKmodTunnel", func(o *OperatorNativeWG, st *storage.AWGTunnel) error {
			return o.RestoreKmodTunnel(context.Background(), st)
		}},
		{"SyncKmodSlot", func(o *OperatorNativeWG, st *storage.AWGTunnel) error {
			return o.SyncKmodSlot(context.Background(), st)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, stub, _, f, srv := newLifecycleOperator(t, false, false)
			f.Remove("Wireguard0")
			err := tc.call(o, nwgStored(awgObfuscatedIface()))
			if !errors.Is(err, tunnel.ErrInterfaceGone) {
				t.Fatalf("want ErrInterfaceGone, got %v", err)
			}
			if stub.addWrites() != 0 || srv.log.posts() != 0 || f.Phantoms != 0 {
				t.Fatalf("add=%d batches=%d phantoms=%d", stub.addWrites(), srv.log.posts(), f.Phantoms)
			}
		})
	}
}

// Страж v4 по снятому интерфейсу: команды нет, запись снимается (Start
// зарегистрирует заново). Список не прочитался — команды нет, запись остаётся.
func TestGuardSweep_NDMSMode_ConfirmsInterface(t *testing.T) {
	entry := guardEntry{iface: "nwg0", pubkey: "pk", endpoint: "198.51.100.1:5060",
		spec: "vpn.example.com:5060", name: "Wireguard0", mode: guardNDMS}
	t.Run("gone", func(t *testing.T) {
		o, _, _, f, srv := newLifecycleOperator(t, true, false)
		_ = stubGuardLookup(t, []string{"203.0.113.9"}, nil)
		f.Remove("Wireguard0")
		o.guardRegister("awg0", entry)
		o.guardSweep(context.Background())
		if srv.log.posts() != 0 || f.Phantoms != 0 {
			t.Fatalf("batches=%d phantoms=%d", srv.log.posts(), f.Phantoms)
		}
		if o.guardHas("awg0") {
			t.Fatal("guard entry on a gone interface must be dropped")
		}
	})
	t.Run("list error", func(t *testing.T) {
		o, _, _, f, srv := newLifecycleOperator(t, true, false)
		_ = stubGuardLookup(t, []string{"203.0.113.9"}, nil)
		f.FailList(errors.New("rci down"))
		o.guardRegister("awg0", entry)
		o.guardSweep(context.Background())
		if srv.log.posts() != 0 {
			t.Fatalf("batches=%d", srv.log.posts())
		}
		if !o.guardHas("awg0") {
			t.Fatal("guard entry must survive a list error")
		}
	})
	t.Run("present", func(t *testing.T) {
		o, _, _, f, srv := newLifecycleOperator(t, true, false)
		_ = stubGuardLookup(t, []string{"203.0.113.9"}, nil)
		o.guardRegister("awg0", entry)
		o.guardSweep(context.Background())
		if last := srv.last(); srv.log.posts() != 1 || !strings.Contains(last, "203.0.113.9:5060") {
			t.Fatalf("batches=%d last=%s", srv.log.posts(), last)
		}
		if f.Phantoms != 0 || f.E != 0 {
			t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
		}
	})
}

// ifaceOf — Confirmed на интерфейс туннеля с отдельного оракула: синхронизациям
// iface отдаёт вызывающий (RequireIface), тестам их самих нужен готовый.
func ifaceOf(stored *storage.AWGTunnel) query.Confirmed {
	name := NewNWGNames(stored.NWGIndex).NDMSName
	f := query.NewFakeNDMS(ndms.Interface{ID: name, Type: "Wireguard"})
	c, _, ok, err := query.NewInterfaceStore(f, nil).Confirm(context.Background(), name)
	if err != nil || !ok {
		panic("ifaceOf: " + name)
	}
	return c
}

// F574: чужой Wireguard1 создан мимо нас, хук не доставлен — память его не
// знает. Индекс выбирается по свежему списку: Wireguard2, без фантомов.
func TestCreateViaBatch_ForeignNotInCache_Skipped(t *testing.T) {
	o, _, _, f, _ := newLifecycleOperator(t, false, false)
	if _, err := o.queries.Interfaces.Get(context.Background(), "Wireguard0"); err != nil { // тёплая карта
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	f.ExpectCreate("Wireguard2")
	idx, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if err != nil || idx != 2 {
		t.Fatalf("idx=%d err=%v, want 2", idx, err)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Список не прочитан — ошибка, ни одной команды (решение 4).
func TestCreateViaBatch_ListError_NoCreate(t *testing.T) {
	o, _, _, f, srv := newLifecycleOperator(t, false, false)
	f.FailList(errors.New("rci down"))
	if _, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface())); err == nil {
		t.Fatal("want error")
	}
	if bodies, _ := srv.sent(); len(bodies) != 0 {
		t.Fatalf("commands sent: %v", bodies)
	}
}

// Чужой Wireguard1 NDMS уже знает, но ни списком, ни хуком не показал:
// выбран он, NDMS отвечает без «interface created» — ErrNotCreated, ни
// настроек, ни сноса, чужая запись цела.
func TestCreateViaBatch_HiddenForeign_ErrorNoSettings(t *testing.T) {
	o, _, _, f, srv := newLifecycleOperator(t, false, false)
	f.HideCreated(-1)
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	f.HideCreated(0)
	_, err := o.createViaBatch(context.Background(), nwgStored(awgObfuscatedIface()))
	if !errors.Is(err, command.ErrNotCreated) {
		t.Fatalf("err=%v", err)
	}
	if bodies, _ := srv.sent(); len(bodies) != 1 || !f.Has("Wireguard1") || f.E != 0 {
		t.Fatalf("bodies=%v has=%v E=%d", bodies, f.Has("Wireguard1"), f.E)
	}
}
