package proxyrt

import (
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

func TestStateStoreUpdatePublishesOnChange(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)

	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK}},
	}
	got := st.Update("inst1", IntentEnabled, res, PhaseSettled)

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

	st.Update("inst1", IntentEnabled, res, PhaseSettled)
	st.Update("inst1", IntentEnabled, res, PhaseSettled)

	if len(pub.events) != 1 {
		t.Fatalf("публикаций %d, ожидали 1 — повтор не публикуется", len(pub.events))
	}
}

func TestStateStorePublishesWhenPhaseChanges(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)
	res := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	st.Update("inst1", IntentEnabled, res, PhaseWaiting)
	st.Update("inst1", IntentEnabled, res, PhaseSettled)

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

	st.Update("inst1", IntentEnabled, first, PhaseWaiting)
	st.Update("inst1", IntentEnabled, second, PhaseWaiting)

	if len(pub.events) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: причина шага изменилась", len(pub.events))
	}
}

func TestStateStoreHandsOutCopies(t *testing.T) {
	// Мьютекс защищает карту, а не содержимое слайсов. Правка выданного слайса
	// на месте — например, сортировка ресурсов в ручке API — не должна доезжать
	// до хранилища: это порча состояния и гонка, невидимая для -race в пакете.
	// Шаг несёт непустую карту: копия одного слайса оставила бы Args общей, и
	// правка аргумента прошла бы в хранилище сквозь «копию».
	st := NewStateStore(&fakePublisher{}, fixedNow)
	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Args: map[string]string{"address": "10.70.0.5"}, Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK}},
	}
	st.Update("inst1", IntentEnabled, res, PhaseWaiting)

	got, _ := st.Get("inst1")
	got.Resources[0].Status = StatusFailed
	got.LastPlan[0].Op = "destroy"
	got.LastPlan[0].Args["address"] = "10.70.0.99"
	got.LastPlan[0].Args["добавленный"] = "мусор"

	again, _ := st.Get("inst1")
	if again.Resources[0].Status != StatusOK {
		t.Fatalf("правка выданного слайса ресурсов дошла до хранилища: %+v", again.Resources[0])
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
	list[0].LastPlan[0].Args["address"] = "10.70.0.77"
	third, _ := st.Get("inst1")
	if third.Resources[0].Status != StatusOK {
		t.Fatalf("правка списка дошла до хранилища: %+v", third.Resources[0])
	}
	if third.LastPlan[0].Args["address"] != "10.70.0.5" {
		t.Fatalf("правка аргумента через List дошла до хранилища: %+v", third.LastPlan[0].Args)
	}
}

// TestStateStorePublishesOnAnyPublicChange закрывает оставшиеся ветки
// sameState. Предикат несёт ограничение «публикация только при изменении»:
// выпавшая ветка означает изменение публичного состояния, которое фронт не
// увидит до следующего непохожего прогона.
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
	got := st.Update("inst1", IntentEnabled, res, PhaseWaiting)

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
			// У DerivePhase ветки для deleted нет, поэтому фаза совпадает:
			// отличить enabled от deleted может только сравнение намерения.
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
			// Detail сравнивается только потому, что ResourceState сличается
			// целой структурой. Подслучай держит это свойство: попольное
			// сравнение без Detail молча потеряет текст наблюдения.
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

			st.Update("inst1", c.intent, c.res, c.phase)
			st.Update("inst1", c.secondIntent, c.secondRes, c.secondPhase)

			if len(pub.events) != 2 {
				t.Fatalf("публикаций %d, ожидали 2: %s", len(pub.events), c.whyMustPublish)
			}
		})
	}
}

func TestStateStoreGetAndList(t *testing.T) {
	st := NewStateStore(&fakePublisher{}, fixedNow)
	st.Update("b", IntentEnabled, Result{}, PhaseSettled)
	st.Update("a", IntentDisabled, Result{}, PhaseDisabled)

	if _, ok := st.Get("a"); !ok {
		t.Fatal("инстанс a не найден")
	}
	list := st.List()
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("список не отсортирован по id: %+v", list)
	}
}
