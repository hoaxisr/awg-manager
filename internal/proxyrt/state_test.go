package proxyrt

import (
	"sync"
	"testing"
	"time"
)

type fakePublisher struct {
	events []string
	last   any
}

func (f *fakePublisher) Publish(eventType string, data any) {
	f.events = append(f.events, eventType)
	f.last = data
}

func fixedNow() time.Time { return time.Unix(1700000000, 0).UTC() }

// gatedPublisher задерживает ПЕРВУЮ публикацию до команды теста и записывает
// порядок, в котором состояния доехали до шины.
type gatedPublisher struct {
	mu      sync.Mutex
	calls   int
	order   []Phase
	entered chan struct{}
	release chan struct{}
}

func (g *gatedPublisher) Publish(_ string, data any) {
	g.mu.Lock()
	g.calls++
	first := g.calls == 1
	g.mu.Unlock()
	if first {
		close(g.entered)
		<-g.release
	}
	g.mu.Lock()
	g.order = append(g.order, data.(InstanceState).Phase)
	g.mu.Unlock()
}

func TestStateStoreKeepsPublicationOrder(t *testing.T) {
	// Два писателя по одному инстансу — воркер и ручка API, снимающая инстанс, —
	// обязаны разложить события на шине в том же порядке, в каком состояния легли
	// в хранилище. Иначе старое состояние опубликуется после нового, и фронт
	// застрянет на протухшем до следующего изменения.
	pub := &gatedPublisher{entered: make(chan struct{}), release: make(chan struct{})}
	st := NewStateStore(pub, fixedNow)

	first := Result{States: []ResourceState{{ID: "a", Status: StatusDrift}}}
	second := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		st.Update("inst1", first, PhaseWaiting)
	}()
	<-pub.entered // первый писатель внутри публикации, его состояние уже в хранилище

	wg.Add(1)
	go func() {
		defer wg.Done()
		st.Update("inst1", second, PhaseSettled)
	}()

	// Окно, за которое второй писатель обогнал бы первого, будь публикация вне
	// лока порядка.
	time.Sleep(50 * time.Millisecond)
	close(pub.release)
	wg.Wait()

	pub.mu.Lock()
	order := append([]Phase(nil), pub.order...)
	pub.mu.Unlock()

	if len(order) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: %v", len(order), order)
	}
	if order[0] != PhaseWaiting || order[1] != PhaseSettled {
		t.Fatalf("порядок публикаций %v — новое состояние уехало на шину раньше старого", order)
	}
	stored, _ := st.Get("inst1")
	if stored.Phase != order[1] {
		t.Fatalf("в хранилище %q, а последняя публикация %q", stored.Phase, order[1])
	}
}

func TestStateStoreUpdatePublishesOnChange(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)

	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK}},
	}
	got := st.Update("inst1", res, PhaseSettled)

	if got.Phase != PhaseSettled || got.ID != "inst1" {
		t.Fatalf("состояние собрано неверно: %+v", got)
	}
	if len(got.LastPlan) != 1 {
		t.Fatal("последний план обязан быть публичным")
	}
	if !got.UpdatedAt.Equal(fixedNow()) {
		t.Fatalf("UpdatedAt = %v", got.UpdatedAt)
	}
	if len(pub.events) != 1 || pub.events[0] != EventInstanceState {
		t.Fatalf("публикации %v", pub.events)
	}
}

func TestStateStoreDoesNotRepublishIdenticalState(t *testing.T) {
	// Реконсиляция идёт по каждому событию; одинаковое состояние не должно
	// заливать шину и фронт.
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)
	res := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	st.Update("inst1", res, PhaseSettled)
	st.Update("inst1", res, PhaseSettled)

	if len(pub.events) != 1 {
		t.Fatalf("публикаций %d, ожидали 1 — повтор не публикуется", len(pub.events))
	}
}

func TestStateStorePublishesWhenPhaseChanges(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)
	res := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	st.Update("inst1", res, PhaseWaiting)
	st.Update("inst1", res, PhaseSettled)

	if len(pub.events) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: фаза изменилась", len(pub.events))
	}
}

func TestStateStorePublishesWhenStepReasonChanges(t *testing.T) {
	// StepKey кодирует только Resource/Op/Args — этого хватает, чтобы понять
	// «шаг уже применяли», но не хватает для вопроса «изменилось ли то, что
	// видит пользователь»: причина шага показывается ему.
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)

	first := Result{Steps: []Step{{Resource: "a", Op: "create", Reason: "нужно создать"}}}
	second := Result{Steps: []Step{{Resource: "a", Op: "create", Reason: "восстановление после сноса"}}}

	st.Update("inst1", first, PhaseWaiting)
	st.Update("inst1", second, PhaseWaiting)

	if len(pub.events) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: причина шага изменилась", len(pub.events))
	}
}

func TestStateStoreHandsOutCopies(t *testing.T) {
	// Мьютекс защищает карту, а не содержимое слайсов. Правка выданного слайса
	// на месте — например, сортировка ресурсов в ручке API — не должна доезжать
	// до хранилища: это порча состояния и гонка, невидимая для -race в пакете.
	// Шаг несёт непустую карту: копия одного слайса оставила бы Args общей, и
	// правка аргумента прошла бы в хранилище сквозь «копию». Ресурс несёт
	// непустую Attrs по той же причине — карта в ResourceState такая же общая.
	st := NewStateStore(&fakePublisher{}, fixedNow)
	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Args: map[string]string{"address": "10.70.0.5"}, Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK, Attrs: map[string]string{"foreign-acl": "OpkgTun17:GUEST_ACL"}}},
	}
	st.Update("inst1", res, PhaseWaiting)

	got, _ := st.Get("inst1")
	got.Resources[0].Status = StatusFailed
	got.Resources[0].Attrs["foreign-acl"] = "OpkgTun17:ПОДМЕНА"
	got.Resources[0].Attrs["добавленный"] = "мусор"
	got.LastPlan[0].Op = "destroy"
	got.LastPlan[0].Args["address"] = "10.70.0.99"
	got.LastPlan[0].Args["добавленный"] = "мусор"

	again, _ := st.Get("inst1")
	if again.Resources[0].Status != StatusOK {
		t.Fatalf("правка выданного слайса ресурсов дошла до хранилища: %+v", again.Resources[0])
	}
	if again.Resources[0].Attrs["foreign-acl"] != "OpkgTun17:GUEST_ACL" || len(again.Resources[0].Attrs) != 1 {
		t.Fatalf("правка карты ресурса дошла до хранилища: %+v", again.Resources[0].Attrs)
	}
	if again.LastPlan[0].Op != "create" {
		t.Fatalf("правка выданного плана дошла до хранилища: %+v", again.LastPlan[0])
	}
	if again.LastPlan[0].Args["address"] != "10.70.0.5" {
		t.Fatalf("правка аргумента дошла до хранилища: %+v", again.LastPlan[0].Args)
	}
	if len(again.LastPlan[0].Args) != 1 {
		t.Fatalf("в хранилище прибавился чужой аргумент: %+v", again.LastPlan[0].Args)
	}

	list := st.List()
	list[0].Resources[0].Status = StatusFailed
	list[0].Resources[0].Attrs["foreign-acl"] = "OpkgTun17:ПОДМЕНА"
	list[0].LastPlan[0].Args["address"] = "10.70.0.77"
	third, _ := st.Get("inst1")
	if third.Resources[0].Status != StatusOK {
		t.Fatalf("правка списка дошла до хранилища: %+v", third.Resources[0])
	}
	if third.Resources[0].Attrs["foreign-acl"] != "OpkgTun17:GUEST_ACL" {
		t.Fatalf("правка карты ресурса через List дошла до хранилища: %+v", third.Resources[0].Attrs)
	}
	if third.LastPlan[0].Args["address"] != "10.70.0.5" {
		t.Fatalf("правка аргумента через List дошла до хранилища: %+v", third.LastPlan[0].Args)
	}
}

func TestStateStoreUpdateHandsOutCopy(t *testing.T) {
	// Возвращаемое из Update — та же выдача наружу, что Get и List: ручка apply
	// из плана 5 отдаст его вызывающему. Правка на месте не должна доезжать до
	// хранилища, и цена здесь выше, чем гонка: испорченное хранимое состояние
	// совпадёт со следующим НАСТОЯЩИМ прогоном, sameState подавит публикацию, и
	// фронт застрянет на протухшей картине навсегда.
	st := NewStateStore(&fakePublisher{}, fixedNow)
	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Args: map[string]string{"address": "10.70.0.5"}, Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK}},
	}
	got := st.Update("inst1", res, PhaseWaiting)

	got.Resources[0].Status = StatusFailed
	got.LastPlan[0].Op = "destroy"
	got.LastPlan[0].Args["address"] = "10.70.0.99"

	stored, _ := st.Get("inst1")
	if stored.Resources[0].Status != StatusOK {
		t.Fatalf("правка ресурсов из возврата Update дошла до хранилища: %+v", stored.Resources[0])
	}
	if stored.LastPlan[0].Op != "create" {
		t.Fatalf("правка плана из возврата Update дошла до хранилища: %+v", stored.LastPlan[0])
	}
	if stored.LastPlan[0].Args["address"] != "10.70.0.5" {
		t.Fatalf("правка аргумента из возврата Update дошла до хранилища: %+v", stored.LastPlan[0].Args)
	}
}

// TestStateStorePublishesOnAnyPublicChange закрывает оставшиеся ветки
// sameState. Предикат несёт ограничение «публикация только при изменении»:
// выпавшая ветка означает изменение публичного состояния, которое фронт не
// увидит до следующего непохожего прогона.
func TestStateStorePublishesOnAnyPublicChange(t *testing.T) {
	base := Result{
		Steps:  []Step{{Resource: "a", Op: "set", Args: map[string]string{"address": "10.70.0.5"}, Reason: "расхождение"}},
		States: []ResourceState{{ID: "a", Status: StatusFailed, Error: "политика не найдена"}},
	}

	cases := []struct {
		name           string
		intent         Intent
		res            Result
		phase          Phase
		secondIntent   Intent
		secondRes      Result
		secondPhase    Phase
		whyMustPublish string
	}{
		{
			name: "сменилось намерение", intent: IntentEnabled, res: base, phase: PhaseWaiting,
			secondIntent: IntentDeleted, secondRes: base, secondPhase: PhaseWaiting,
			// Фаза здесь задана тестом и совпадает намеренно: отличить
			// enabled от deleted может только сравнение намерения.
			whyMustPublish: "enabled → deleted при совпавшей фазе",
		},
		{
			name: "сменился текст отказа ресурса", intent: IntentEnabled, res: base, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusFailed, Error: "интерфейс не найден"}},
			},
			whyMustPublish: "та же фаза, другая причина отказа",
		},
		{
			name: "сменились аргументы шага", intent: IntentEnabled, res: base, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  []Step{{Resource: "a", Op: "set", Args: map[string]string{"address": "10.70.0.6"}, Reason: "расхождение"}},
				States: base.States,
			},
			whyMustPublish: "та же причина шага, другой адрес",
		},
		{
			// Статус — самое видимое, что есть у ресурса, и попольное
			// сравнение обязано его нести: без ветки Status «ok» и «failed»
			// при том же тексте наблюдения фронт не различит.
			name: "сменился Status ресурса", intent: IntentEnabled, res: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusOK, Detail: "готово"}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusDrift, Detail: "готово"}},
			},
			whyMustPublish: "тот же текст наблюдения, другой статус",
		},
		{
			// Ресурс подменился на другой при совпавшем статусе: длины равны,
			// и без ветки ID подмена прошла бы молча. Это не выдумка ради
			// ветки — состав ресурсов роли зависит от конфига (у сервера
			// половины появляются и исчезают), и на позиции i вправо
			// приезжает уже другой ресурс.
			name: "сменился ID ресурса", intent: IntentEnabled, res: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "ndms_access", Status: StatusOK}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "permit_absent", Status: StatusOK}},
			},
			whyMustPublish: "тот же статус, другой ресурс на той же позиции",
		},
		{
			// ResourceState сличается ПОПОЛЬНО (в нём карта Attrs, и `==` по
			// структуре не компилируется). Подслучай держит Detail: поле,
			// забытое в перечислении, молча потеряет текст наблюдения.
			name: "сменился Detail ресурса", intent: IntentEnabled, res: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusOK, Detail: "oper up"}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusOK, Detail: "oper down"}},
			},
			whyMustPublish: "тот же статус, другой текст наблюдения",
		},
		{
			// Attrs — карта, и сравнивать её нужно maps.Equal: `x.Attrs !=
			// y.Attrs` не компилируется, а пропуск поля прячет от пользователя
			// смену чужих привязок ACL (ndms_access: foreign-acl).
			name: "сменился Attrs ресурса", intent: IntentEnabled, res: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusOK, Attrs: map[string]string{"foreign-acl": "OpkgTun17:GUEST_ACL"}}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  base.Steps,
				States: []ResourceState{{ID: "a", Status: StatusOK, Attrs: map[string]string{"foreign-acl": "OpkgTun17:OTHER_ACL"}}},
			},
			whyMustPublish: "тот же статус, другие чужие привязки ACL",
		},
		{
			// Список ресурсов вырос. Сравнение идёт циклом по ПРЕДЫДУЩЕМУ
			// состоянию, поэтому без проверки длин общий префикс совпадёт, а
			// новый хвост никто не осмотрит: изменение пропадёт молча, паники
			// не будет.
			name: "ресурсов стало больше", intent: IntentEnabled, res: Result{
				States: []ResourceState{{ID: "a", Status: StatusOK}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				States: []ResourceState{{ID: "a", Status: StatusOK}, {ID: "b", Status: StatusDrift}},
			},
			whyMustPublish: "первый ресурс не изменился, добавился второй",
		},
		{
			// То же для плана: прибавился шаг в хвост.
			name: "в плане прибавился шаг", intent: IntentEnabled, res: Result{
				Steps:  []Step{{Resource: "a", Op: "create", Reason: "нужно создать"}},
				States: base.States,
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps: []Step{
					{Resource: "a", Op: "create", Reason: "нужно создать"},
					{Resource: "b", Op: "up", Reason: "интерфейс не поднят"},
				},
				States: base.States,
			},
			whyMustPublish: "первый шаг не изменился, добавился второй",
		},
		{
			// Обратное направление, и оно опаснее роста: циклы идут по более
			// длинному ПРЕДЫДУЩЕМУ состоянию, поэтому проверка длин здесь не
			// только ловит изменение, но и держит границу слайса. Условие,
			// ловящее лишь рост, дало бы index out of range прямо в Update.
			name: "ресурсов стало меньше", intent: IntentEnabled, res: Result{
				States: []ResourceState{{ID: "a", Status: StatusOK}, {ID: "b", Status: StatusDrift}},
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				States: []ResourceState{{ID: "a", Status: StatusOK}},
			},
			whyMustPublish: "первый ресурс не изменился, второй исчез",
		},
		{
			// То же для плана, и это штатный путь: «есть шаги» переходит в
			// «пусто» при каждом уходе инстанса в settled.
			name: "в плане шагов стало меньше", intent: IntentEnabled, res: Result{
				Steps: []Step{
					{Resource: "a", Op: "create", Reason: "нужно создать"},
					{Resource: "b", Op: "up", Reason: "интерфейс не поднят"},
				},
				States: base.States,
			}, phase: PhaseWaiting,
			secondIntent: IntentEnabled, secondPhase: PhaseWaiting,
			secondRes: Result{
				Steps:  []Step{{Resource: "a", Op: "create", Reason: "нужно создать"}},
				States: base.States,
			},
			whyMustPublish: "первый шаг не изменился, второй исчез",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub := &fakePublisher{}
			st := NewStateStore(pub, fixedNow)

			first, second := c.res, c.secondRes
			first.Intent, second.Intent = c.intent, c.secondIntent
			st.Update("inst1", first, c.phase)
			st.Update("inst1", second, c.secondPhase)

			if len(pub.events) != 2 {
				t.Fatalf("публикаций %d, ожидали 2: %s", len(pub.events), c.whyMustPublish)
			}
		})
	}
}

func TestStateStoreGetAndList(t *testing.T) {
	st := NewStateStore(&fakePublisher{}, fixedNow)
	st.Update("b", Result{Intent: IntentEnabled}, PhaseSettled)
	st.Update("a", Result{Intent: IntentDisabled}, PhaseDisabled)

	if _, ok := st.Get("a"); !ok {
		t.Fatal("инстанс a не найден")
	}
	list := st.List()
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("список не отсортирован по id: %+v", list)
	}
}

// В состояние — и, значит, на шину — уходят только ПОЛЬЗОВАТЕЛЬСКИЕ атрибуты
// наблюдения (Public). Служебные Attrs туда не копируются: в них лежат
// величины вроде uptime_s, растущие между прогонами, и публикация по ним шла
// бы на каждый будильник инстанса, а не на настоящее изменение.
//
// Состояния здесь строит настоящий Plan из наблюдений — иначе тест проверял бы
// собственную сборку ResourceState, а не ту, что стоит в движке.
func TestStateStorePublishesPublicAttrsOnly(t *testing.T) {
	stateFor := func(obs Observation) Result {
		r := &fakeResource{id: "a", obs: obs}
		steps, states := Plan([]Resource{r}, observeAllForTest(r))
		return Result{Intent: IntentEnabled, Steps: steps, States: states}
	}
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)

	st.Update("inst1", stateFor(Observation{Known: true, Exists: true,
		Attrs: map[string]string{"uptime_s": "1"}}), PhaseSettled)
	st.Update("inst1", stateFor(Observation{Known: true, Exists: true,
		Attrs: map[string]string{"uptime_s": "2"}}), PhaseSettled)
	if len(pub.events) != 1 {
		t.Fatalf("публикаций %d, ожидали 1: служебный uptime_s не повод показывать новое состояние", len(pub.events))
	}
	// А смена показываемого атрибута публикацию обязана дать.
	st.Update("inst1", stateFor(Observation{Known: true, Exists: true,
		Attrs:  map[string]string{"uptime_s": "3"},
		Public: map[string]string{"foreign-acl": "OpkgTun17:GUEST_ACL"}}), PhaseSettled)
	if len(pub.events) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: сменились чужие привязки ACL", len(pub.events))
	}
	got := pub.last.(InstanceState).Resources[0].Attrs
	if got["foreign-acl"] != "OpkgTun17:GUEST_ACL" || got["uptime_s"] != "" {
		t.Fatalf("наружу ушли не те атрибуты: %v", got)
	}
}
