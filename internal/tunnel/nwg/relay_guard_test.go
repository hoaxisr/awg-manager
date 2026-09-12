package nwg

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// relayGuardFixture: поднятый обфусцированный туннель с записью стража.
func relayGuardFixture(t *testing.T) (*OperatorNativeWG, *captureNDMS, *fakeObfRunner, *storage.AWGTunnel) {
	t.Helper()
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	op.SetTunnelLookup(func(string) (*storage.AWGTunnel, error) { return st, nil })
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !op.guardHas(st.ID) {
		t.Fatal("подготовка: записи стража нет")
	}
	n.reset()
	return op, n, fr, st
}

// Полный проход стража, а не прямой вызов обработчика: именно на этом стыке
// запись viaRelay может уехать не в ту ветку.
func TestGuardSweep_RelayTargetChanged_RestartsRelay(t *testing.T) {
	op, n, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)
	startsBefore := fr.startCount(st.ID)

	op.guardSweep(context.Background())

	if got := fr.startCount(st.ID); got != startsBefore+1 {
		t.Fatalf("релей не перезапущен: стартов %d, было %d", got, startsBefore)
	}
	if !strings.Contains(n.joined(), `"host":"198.51.100.7"`) {
		t.Fatalf("маршрут до нового адреса не поставлен:\n%s", n.joined())
	}
	// В kernel-ветку (wg set) запись про релей уходить не должна.
	if strings.Contains(n.joined(), `"wireguard"`) && strings.Contains(n.joined(), `"endpoint"`) {
		t.Fatalf("релейная запись ушла в чужую ветку стража:\n%s", n.joined())
	}
}

// Отказ старта релея не должен выключать присмотр навсегда: реестр сдвигается
// только после успеха, иначе следующий проход решит, что адрес не менялся.
func TestGuardSweep_RelayStartFailed_RetriesNextSweep(t *testing.T) {
	op, n, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)
	fr.setFailStart(errors.New("loopback-порт занят"))

	op.guardSweep(context.Background())

	if e, _ := op.guardGet(st.ID); e.endpoint != "203.0.113.5:51824" {
		t.Fatalf("реестр сдвинут при неудаче: %q", e.endpoint)
	}
	// Маршрут не трогаем: иначе каждая неудача — команда в NDMS и запись
	// конфигурации, а проход повторяется каждые 20 секунд.
	if rm := n.routeRemovals(); len(rm) > 0 {
		t.Fatalf("маршрут снят при неудачном рестарте: %v", rm)
	}
	if strings.Contains(n.joined(), `"host":"198.51.100.7"`) {
		t.Fatalf("маршрут поставлен при неудачном рестарте:\n%s", n.joined())
	}

	// Вторая попытка — уже успешная.
	fr.setFailStart(nil)
	startsBefore := fr.startCount(st.ID)
	op.guardSweep(context.Background())

	if got := fr.startCount(st.ID); got != startsBefore+1 {
		t.Fatalf("следующий проход не повторил попытку: стартов %d, было %d", got, startsBefore)
	}
	if e, _ := op.guardGet(st.ID); e.endpoint != "198.51.100.7:51824" {
		t.Fatalf("после успеха реестр не сдвинулся: %q", e.endpoint)
	}
}

// Новый адрес обязан пережить рестарт демона: по нему снимается host-route.
func TestGuardSweep_RelayTargetChanged_PersistsAddress(t *testing.T) {
	op, _, _, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)
	var gotID, gotIP string
	op.SetResolvedIPPersister(func(id, ip string) { gotID, gotIP = id, ip })

	op.guardSweep(context.Background())

	if gotID != st.ID || gotIP != "198.51.100.7" {
		t.Fatalf("адрес не сохранён: id=%q ip=%q", gotID, gotIP)
	}
}

// Страж — такой же писатель host-route, как действия оркестратора, и обязан
// брать тот же замок.
func TestGuardSweep_RelayTargetChanged_TakesTunnelLock(t *testing.T) {
	op, _, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)
	var owner atomic.Value
	op.SetTunnelLock(func(_ context.Context, _, o string, work func() error) error {
		owner.Store(o)
		return errors.New("туннель занят")
	})
	startsBefore := fr.startCount(st.ID)

	op.guardSweep(context.Background())

	if owner.Load() == nil {
		t.Fatal("страж работал в обход замка")
	}
	if got := fr.startCount(st.ID); got != startsBefore {
		t.Fatalf("релей перезапущен при занятом замке: стартов %d, было %d", got, startsBefore)
	}
}

// Ротирующее подмножество A-записей выглядит как бесконечная смена адреса:
// без выдержки релей перезапускался бы каждый проход, разрывая живую сессию.
func TestGuardSweep_RelayRestartCooldown(t *testing.T) {
	op, _, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)

	op.guardSweep(context.Background())
	afterFirst := fr.startCount(st.ID)

	stubGuardLookup(t, []string{"203.0.113.5"}, nil) // «вернулся» прежний адрес
	op.guardSweep(context.Background())

	if got := fr.startCount(st.ID); got != afterFirst {
		t.Fatalf("рестарт внутри выдержки: стартов %d, было %d", got, afterFirst)
	}
}

// Внеочередные проходы прорежены самим циклом стража: хук публичный и без
// авторизации, а каждый проход стоит резолва на каждую запись.
func TestGuardLoop_NudgeIsThrottled(t *testing.T) {
	calls := countingGuardLookup(t, "203.0.113.5")
	op, _, _, _ := relayGuardFixture(t)

	op.NudgeEndpointGuard()
	waitFor(t, 2*time.Second, func() bool { return calls.Load() >= 1 })
	after := calls.Load()

	op.NudgeEndpointGuard() // сразу следом, внутри выдержки
	time.Sleep(300 * time.Millisecond)

	if got := calls.Load(); got != after {
		t.Fatalf("второй пинок подряд дал лишний проход: резолвов %d, было %d", got, after)
	}
}

// Но первый пинок до цикла доходит — иначе хук не делает ничего.
func TestGuardLoop_FirstNudgeWakesSweep(t *testing.T) {
	calls := countingGuardLookup(t, "203.0.113.5")
	op, _, _, _ := relayGuardFixture(t)

	op.NudgeEndpointGuard()

	waitFor(t, 2*time.Second, func() bool { return calls.Load() >= 1 })
}

// countingGuardLookup — подмена резолва со счётчиком для тестов, где проход
// идёт в горутине цикла: обычный stubGuardLookup считает в голый int, и
// -race ловит гонку на нём. Ставится ДО фикстуры: цикл стража поднимается
// первой регистрацией записи и сразу читает эту переменную.
func countingGuardLookup(t *testing.T, ips ...string) *atomic.Int64 {
	t.Helper()
	orig := guardLookupIPs
	var calls atomic.Int64
	guardLookupIPs = func(string) ([]string, error) {
		calls.Add(1)
		return ips, nil
	}
	t.Cleanup(func() { guardLookupIPs = orig })
	return &calls
}

// waitFor ждёт условие, опрашивая его; иначе тест зависел бы от одной паузы.
func waitFor(t *testing.T, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("условие не наступило за отведённое время")
}

// Правка пира не должна выключать присмотр за target'ом: endpoint у
// обфусцированного туннеля ВСЕГДА литерал 127.0.0.1:<порт>, и решение
// «резолвить нечего» относится к нему, а не к DDNS-имени релея.
func TestSyncPeer_KeepsRelayGuardEntry(t *testing.T) {
	op, _, _, st := relayGuardFixture(t)

	if err := op.SyncPeer(context.Background(), st, st.Peer.PublicKey); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	e, ok := op.guardGet(st.ID)
	if !ok || e.mode != guardRelay {
		t.Fatalf("правка пира сняла присмотр за target'ом: %+v ok=%v", e, ok)
	}
	if e.spec != st.Obfuscator.Target {
		t.Fatalf("страж следит не за target'ом: %q", e.spec)
	}
}

// Правка параметров релея через карточку тоже обязана оставлять стража.
func TestSyncObfuscator_RegistersRelayGuard(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored()

	if _, err := op.SyncObfuscator(context.Background(), st); err != nil {
		t.Fatalf("SyncObfuscator: %v", err)
	}

	e, ok := op.guardGet(st.ID)
	if !ok || e.mode != guardRelay || e.spec != st.Obfuscator.Target {
		t.Fatalf("после правки релея за target'ом никто не следит: %+v ok=%v", e, ok)
	}
}

// Запись туннеля не прочиталась — рестартовать нечего: параметры релея берутся
// из неё, и поднимать процесс по пустому месту нельзя.
func TestGuardSweep_RelayLookupFailed_KeepsRelay(t *testing.T) {
	op, _, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"198.51.100.7"}, nil)
	// Запись отдаём НЕ nil: иначе тест ловил бы nil-проверку, а не проверку
	// ошибки — стор может вернуть и устаревший снимок вместе с отказом.
	op.SetTunnelLookup(func(string) (*storage.AWGTunnel, error) { return st, errors.New("стор недоступен") })
	startsBefore := fr.startCount(st.ID)

	op.guardSweep(context.Background())

	if got := fr.startCount(st.ID); got != startsBefore {
		t.Fatalf("релей перезапущен без записи туннеля: стартов %d, было %d", got, startsBefore)
	}
	if e, _ := op.guardGet(st.ID); e.endpoint != "203.0.113.5:51824" {
		t.Fatalf("реестр сдвинут без переноса: %q", e.endpoint)
	}
}

// Резолв не изменился — трогать нечего: рестарт релея рвёт живую сессию, а
// постановка маршрута стоит команды в NDMS и записи конфигурации роутера.
func TestGuardSweep_RelayTargetUnchanged_DoesNothing(t *testing.T) {
	op, n, fr, st := relayGuardFixture(t)
	stubGuardLookup(t, []string{"203.0.113.5"}, nil) // тот же адрес, что дал Start
	startsBefore := fr.startCount(st.ID)

	op.guardSweep(context.Background())

	if got := fr.startCount(st.ID); got != startsBefore {
		t.Fatalf("релей перезапущен впустую: стартов %d, было %d", got, startsBefore)
	}
	if posts := n.joined(); strings.Contains(posts, `"route"`) {
		t.Fatalf("маршрут тронут впустую:\n%s", posts)
	}
}

// Правка релея не поднялась — маршрут не трогаем: команда в NDMS стоит записи
// конфигурации роутера, а вести её некуда, релей мёртв.
func TestSyncObfuscator_RelayStartFailed_KeepsRouteUntouched(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	fr.setFailStart(errors.New("loopback-порт занят"))

	if _, err := op.SyncObfuscator(context.Background(), st); err == nil {
		t.Fatal("отказ запуска релея обязан доехать до вызывающего")
	}

	if posts := n.joined(); strings.Contains(posts, `"route"`) {
		t.Fatalf("маршрут тронут при мёртвом релее:\n%s", posts)
	}
	if op.guardHas(st.ID) {
		t.Fatal("страж поставлен на туннель, релей которого не поднялся")
	}
}
