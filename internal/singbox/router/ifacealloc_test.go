package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// foreignPins — источник занятости с ЧУЖИМИ заявленными номерами. Заявленный,
// а не анонимный: аноним пин перебивает, и тест про «чужой номер не выдаётся»
// прошёл бы вхолостую.
func foreignPins(indices ...int) opkgtun.Source {
	return opkgtun.Source{Name: "чужие записи", Read: func(context.Context) (opkgtun.Taken, error) {
		out := make(opkgtun.Taken, len(indices))
		for _, i := range indices {
			out[i] = opkgtun.ProxyHolder("wdtt-client:чужой", "", "чужой")
		}
		return out, nil
	}}
}

// Номер, занятый записью туннеля, которую ещё ни разу не включали, живым
// интерфейсом не выглядит: OpkgTun для kernel-туннеля создаётся только первым
// стартом. Режим роутера обязан такой номер пропускать — иначе он заберёт его
// себе, а включение туннеля усыновит чужой интерфейс.
func TestPolicyTunEnable_SkipsForeignPin(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true, 1: true, 2: true}}
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, foreignPins(3))

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if h.log.has("Create:OpkgTun3:public") {
		t.Errorf("номер 3 занят чужой записью и не должен выдаваться: %v", h.log.calls)
	}
	if !h.log.has("Create:OpkgTun4:public") {
		t.Errorf("ожидался следующий свободный номер 4, получено %v", h.log.calls)
	}
}

// Собственный удержанный номер режим узнаёт по КЛЮЧУ держателя, а не вычитанием
// своей записи из занятости: на handover вычитать нечего — номер там принадлежит
// другому режиму.
func TestPolicyTunEnable_OwnHoldSurvivesForeignPins(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, foreignPins(7))
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 {
		t.Errorf("удержанный свой номер обязан пережить чужие записи, got %+v", st)
	}
}

// Недосчёт занятых номеров — единственное направление, дающее коллизию.
func TestPolicyTunEnable_FailsClosedOnOccupancyError(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, opkgtun.Source{
		Name: "недоступный",
		Read: func(context.Context) (opkgtun.Taken, error) {
			return nil, errors.New("хранилище недоступно")
		},
	})

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("сбой поставщика занятости обязан останавливать включение")
	}
}

// Пин на свой прежний номер: запись наша, устройства на номере нет — значит
// претендовать вправе. Permit'ы пользователя закреплены за именем, и переезд их
// рвёт.
func TestPinFor(t *testing.T) {
	prev := &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 4}
	ourScan := func(context.Context, string) ([]string, error) { return []string{"OpkgTun4"}, nil }
	foreignScan := func(context.Context, string) ([]string, error) { return nil, nil }
	deadScan := func(context.Context, string) ([]string, error) { return nil, errors.New("RCI молчит") }

	cases := []struct {
		name string
		scan func(context.Context, string) ([]string, error)
		prev *storage.OpkgTunState
		live map[int]bool
		want opkgTunPin
	}{
		{"записи нет — претензии нет", nil, nil, nil, noPin},
		// Устройства нет, но запись NDMS его переживает и могла остаться от
		// чужого: претендуем СТРОГО, не перебивая безключевого держателя.
		{"устройства нет — строгая претензия", foreignScan, prev, map[int]bool{}, opkgTunPin{index: 4}},
		{"устройство наше по описанию — доказанная", ourScan, prev, map[int]bool{4: true}, opkgTunPin{index: 4, proven: true}},
		{"на номере доказанно чужой — претензии нет", foreignScan, prev, map[int]bool{4: true}, noPin},
		{"скан упал при живом своём — претензии нет", deadScan, prev, map[int]bool{4: true}, noPin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := newTestService(t, Deps{OpkgTunScan: c.scan})
			if got := svc.pinFor(context.Background(), c.prev, c.live, policyTunDescription); got != c.want {
				t.Fatalf("претензия = %+v, ждали %+v", got, c.want)
			}
		})
	}
}

// Строгая претензия не отбирает номер у безключевого держателя: запись NDMS
// могла остаться от ЧУЖОГО интерфейса, а создание поверх неё переписало бы
// чужие настройки (RCI создаёт интерфейс upsert-ом).
func TestReserveOpkgTun_StrictPinSparesForeignNDMSRecord(t *testing.T) {
	svc := newTestService(t, Deps{})
	svc.deps.OpkgTunPool = opkgtun.NewPool(16, opkgtun.Source{
		Name: "записи NDMS",
		Read: func(context.Context) (opkgtun.Taken, error) {
			return opkgtun.Taken{4: opkgtun.AnonHolder("запись NDMS OpkgTun4 («чужой»)")}, nil
		},
	})

	idx, res, err := svc.reserveOpkgTun(context.Background(),
		storage.OpkgTunModePolicyTun, opkgTunPin{index: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if idx == 4 {
		t.Fatal("строгая претензия села поверх чужой записи NDMS")
	}

	// Доказанное владение — наоборот, номер забирает: след на нём наш.
	idx2, res2, err := svc.reserveOpkgTun(context.Background(),
		storage.OpkgTunModePolicyTun, opkgTunPin{index: 4, proven: true})
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Close()
	if idx2 != 4 {
		t.Fatalf("доказанная претензия = %d, ждали 4", idx2)
	}
}

// Пин не гарантия: чужой ЗАЯВЛЕННЫЙ номер пул не отдаст, и режим уедет на
// другой. Проверяется через наблюдаемое поведение выдачи.
func TestReserveOpkgTun_PinLosesToClaimedForeign(t *testing.T) {
	svc := newTestService(t, Deps{})
	svc.deps.OpkgTunPool = opkgtun.NewPool(16, foreignPins(4))

	idx, res, err := svc.reserveOpkgTun(context.Background(),
		storage.OpkgTunModePolicyTun, opkgTunPin{index: 4, proven: true})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if idx == 4 {
		t.Fatal("пин сел поверх чужой заявленной записи")
	}
}

// Резервация держит номер до записи владения: пока она открыта, второй
// проситель его не получает.
func TestReserveOpkgTun_HoldsNumberUntilClosed(t *testing.T) {
	svc := newTestService(t, Deps{})
	pool := opkgtun.NewPool(16, opkgtun.Source{
		Name: "пусто", Read: func(context.Context) (opkgtun.Taken, error) { return nil, nil },
	})
	svc.deps.OpkgTunPool = pool

	first, res1, err := svc.reserveOpkgTun(context.Background(), storage.OpkgTunModeFakeIP, noPin)
	if err != nil {
		t.Fatal(err)
	}
	defer res1.Close()

	// Второй проситель — ДРУГОГО класса: у режимов роутера ключ общий, и
	// второй режим получил бы тот же номер по совпадению ключа (это и нужно
	// на handover). Здесь проверяется удержание от постороннего.
	res2, err := pool.Reserve(context.Background(), opkgtun.Want(opkgtun.TunnelHolder("", "туннель")))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Close()
	if res2.Numbers()[0] == first {
		t.Fatalf("оба получили %d: резервация номер не держит", first)
	}
}

// Handover: номер отобранного режима честится ТОЛЬКО когда интерфейс снесли МЫ
// САМИ. Пользовательские permit'ы закреплены за именем, и сохранить номер при
// смене режима — единственный способ их не оборвать.
func TestPolicyTunEnable_HandoverKeepsNumberWhenRemoved(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{4: true}}
	// Запись чужого режима на номере 4; скан подтверждает, что интерфейс наш,
	// значит release его снесёт и вернёт removed=true.
	h.svc.deps.OpkgTunScan = func(context.Context, string) ([]string, error) {
		return []string{"OpkgTun4"}, nil
	}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 4}); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 4 {
		t.Fatalf("запись = %+v; номер отобранного режима обязан сохраниться", st)
	}
}

// Обратная сторона: интерфейс на номере ДОКАЗАННО чужой, release его не
// трогает и возвращает removed=false. Претендовать на такой номер нельзя —
// режим уезжает на другой.
func TestPolicyTunEnable_HandoverMovesWhenForeignProven(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{4: true}}
	// Скан успешен и нашего имени не содержит: на номере посторонний.
	h.svc.deps.OpkgTunScan = func(context.Context, string) ([]string, error) { return nil, nil }
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 4}); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index == 4 {
		t.Fatalf("запись = %+v; на доказанно чужом номере оставаться нельзя", st)
	}
}

// Мёртвый свой интерфейс fakeip поднимается на СВОЁМ номере. До перехода на
// пул этой ветки у fakeip не было вовсе — он уезжал на низший свободный.
func TestFakeIPEnable_DeadOwnIfaceKeepsNumber(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 5}); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip-tun): %v", err)
	}
	if st := h.loadFakeIP(t); st == nil || st.Index != 5 {
		t.Fatalf("запись = %+v; мёртвый свой интерфейс обязан подняться на своём номере", st)
	}
}

// Спорные номера обязаны попасть в журнал: пул не отдаёт их никому, режим
// уезжает на другой номер, и это единственный след, по которому пользователь
// поймёт, почему его permit'ы порвались.
func TestReserveOpkgTun_WarnsAboutConflicts(t *testing.T) {
	log := &recordingAppLogger{}
	svc := newTestService(t, Deps{AppLog: log})
	svc.appLog = logging.NewScopedLogger(log, logging.GroupRouting, logging.SubSingboxRouter)
	claimedA := opkgtun.Source{Name: "записи туннелей", Read: func(context.Context) (opkgtun.Taken, error) {
		return opkgtun.Taken{4: opkgtun.TunnelHolder("awg4", "дом")}, nil
	}}
	claimedB := opkgtun.Source{Name: "записи прокси", Read: func(context.Context) (opkgtun.Taken, error) {
		return opkgtun.Taken{4: opkgtun.ProxyHolder("wdtt-client:vk", "", "vk")}, nil
	}}
	svc.deps.OpkgTunPool = opkgtun.NewPool(16, claimedA, claimedB)

	_, res, err := svc.reserveOpkgTun(context.Background(), storage.OpkgTunModePolicyTun, noPin)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	said := false
	for _, e := range log.entries {
		if strings.Contains(e, "спорные номера OpkgTun") {
			said = true
		}
	}
	if !said {
		t.Fatalf("о спорном номере не сказано: %v", log.entries)
	}
}

// На handover номер меняется, и сказать об этом обязаны: permit'ы пользователя
// закреплены за ИМЕНЕМ интерфейса, а permit живёт ровно столько, сколько
// интерфейс, и пересозданием одноимённого не воскресает (стенд 2026-08-18).
// Предупреждение — единственный след, по которому пользователь поймёт, почему
// они порвались.
//
// Сравнивать обязательно с ПРЕЖНЕЙ записью, чья бы она ни была: своя (prev) на
// handover пуста, и сравнение с ней молчит именно там, где номер и меняется.
func TestPolicyTunEnable_WarnsWhenHandoverChangesIndex(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	log := &recordingAppLogger{}
	h.svc.appLog = logging.NewScopedLogger(log, logging.GroupRouting, logging.SubSingboxRouter)
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	// Номер 5 держит ЧУЖАЯ запись прокси, поэтому претензия на него не честится
	// и режим уезжает.
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, foreignPins(5))
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Index: 5, Provisioned: true}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	st := h.loadPolicyTun(t)
	if st == nil || st.Index == 5 {
		t.Fatalf("запись = %+v; номер обязан был смениться, иначе тест проверяет не то", st)
	}
	said := false
	for _, e := range log.entries {
		if strings.Contains(e, "индекс OpkgTun изменился") {
			said = true
		}
	}
	if !said {
		t.Fatalf("о смене номера не сказано: %v", log.entries)
	}
}

// Тот же несущий сигнал у второго режима: ветка скопирована, и мутация в одной
// копии не ловится тестом другой.
func TestFakeIPTunEnable_WarnsWhenHandoverChangesIndex(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	log := &recordingAppLogger{}
	h.svc.appLog = logging.NewScopedLogger(log, logging.GroupRouting, logging.SubSingboxRouter)
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	h.svc.deps.OpkgTunPool = testOpkgTunPool(h.svc, foreignPins(5))
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Index: 5, Provisioned: true}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(fakeip-tun): %v", err)
	}

	st := h.loadFakeIP(t)
	if st == nil || st.Index == 5 {
		t.Fatalf("запись = %+v; номер обязан был смениться, иначе тест проверяет не то", st)
	}
	said := false
	for _, e := range log.entries {
		if strings.Contains(e, "индекс OpkgTun изменился") {
			said = true
		}
	}
	if !said {
		t.Fatalf("о смене номера не сказано: %v", log.entries)
	}
}

// Пул не проведён — отказ, а не паника посреди провижининга. Страж стоит в
// nil-гарде обоих режимов ДО первой мутации состояния; сними его, и
// вырожденная сборка роняла бы демона там, где обязана отказать.
func TestEnableRefusesWithoutOpkgTunPool(t *testing.T) {
	t.Run("policy-tun", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		h.svc.deps.OpkgTunPool = nil
		if err := h.svc.Enable(context.Background()); err == nil {
			t.Fatal("ждали отказ: пул номеров не проведён")
		}
	})
	t.Run("fakeip-tun", func(t *testing.T) {
		h := newFakeIPEnableHarness(t, "")
		h.svc.deps.OpkgTunPool = nil
		if err := h.svc.Enable(context.Background()); err == nil {
			t.Fatal("ждали отказ: пул номеров не проведён")
		}
	})
}
