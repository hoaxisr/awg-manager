package proxyrt

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeResource — ресурс, полностью управляемый тестом.
type fakeResource struct {
	id     ResourceID
	obs    Observation
	obsErr error
	steps  []Step
}

func (f *fakeResource) ID() ResourceID { return f.id }

func (f *fakeResource) Observe(context.Context) (Observation, error) {
	return f.obs, f.obsErr
}

func (f *fakeResource) Plan(Observation) []Step { return f.steps }

func (f *fakeResource) Apply(context.Context, Step) error { return nil }

func (f *fakeResource) RecheckAfter() time.Duration { return 0 }

func step(id ResourceID, op string) Step {
	return Step{Resource: id, Op: op, Reason: "тест"}
}

func observeAllForTest(rs ...Resource) Observations {
	obs := NewObservations()
	for _, r := range rs {
		o, err := r.Observe(context.Background())
		obs.Put(r.ID(), o, err)
	}
	return obs
}

func TestObservationsGetTellsWhenObservationIsUsable(t *testing.T) {
	// Наблюдение читают роли: желаемое значение одного ресурса вычисляется из
	// наблюдения другого. Ответ обязан различать «вот факт» и «опираться нельзя»,
	// иначе роль примет решение по слепому наблюдению.
	obs := NewObservations()
	obs.Put("known", Observation{Known: true, Detail: "up"}, nil)
	obs.Put("broken", Observation{}, errors.New("RCI недоступен"))
	obs.Put("blind", Observation{Known: false}, nil)
	obs.Put("failed", Observation{Known: true}, nil)
	obs.MarkFailed("failed", "политика не найдена")

	cases := []struct {
		id     ResourceID
		usable bool
		why    string
	}{
		{"known", true, "наблюдение получено"},
		{"broken", false, "наблюдатель вернул ошибку"},
		{"blind", false, "наблюдатель вернул «не смотрел»"},
		{"failed", false, "применение к ресурсу отказало"},
		{"нет-такого", false, "ресурс не наблюдался в этом проходе"},
	}
	for _, c := range cases {
		if _, ok := obs.Get(c.id); ok != c.usable {
			t.Fatalf("%s: пригодность %v, ожидали %v (%s)", c.id, ok, c.usable, c.why)
		}
	}
}

func TestPlanEmptyWhenObservedAndSatisfied(t *testing.T) {
	a := &fakeResource{id: "a", obs: Observation{Known: true, Exists: true}}
	b := &fakeResource{id: "b", obs: Observation{Known: true, Exists: true}}

	steps, states := Plan([]Resource{a, b}, observeAllForTest(a, b))

	if len(steps) != 0 {
		t.Fatalf("ожидали пустой план, получили %v", steps)
	}
	for _, s := range states {
		if s.Status != StatusOK {
			t.Fatalf("ресурс %s: статус %q, ожидали ok", s.ID, s.Status)
		}
	}
}

func TestPlanCollectsStepsInDeclarationOrder(t *testing.T) {
	a := &fakeResource{id: "a", obs: Observation{Known: true}, steps: []Step{step("a", "create")}}
	b := &fakeResource{id: "b", obs: Observation{Known: true}, steps: []Step{step("b", "up")}}

	steps, _ := Plan([]Resource{a, b}, observeAllForTest(a, b))

	if len(steps) != 2 || steps[0].Resource != "a" || steps[1].Resource != "b" {
		t.Fatalf("порядок шагов нарушен: %v", steps)
	}
}

func TestPlanObserveErrorIsUnknownAndBlocksTail(t *testing.T) {
	a := &fakeResource{id: "a", obsErr: errors.New("rci недоступен"), steps: []Step{step("a", "create")}}
	b := &fakeResource{id: "b", obs: Observation{Known: true}, steps: []Step{step("b", "up")}}

	steps, states := Plan([]Resource{a, b}, observeAllForTest(a, b))

	if len(steps) != 0 {
		t.Fatalf("при unknown шагов быть не должно, получили %v", steps)
	}
	if states[0].Status != StatusUnknown || states[0].Error == "" {
		t.Fatalf("a: %+v, ожидали unknown с причиной", states[0])
	}
	if states[1].Status != StatusBlocked {
		t.Fatalf("b: статус %q, ожидали blocked", states[1].Status)
	}
}

func TestPlanNotKnownIsUnknownEvenWithoutError(t *testing.T) {
	// Наблюдатель вернул результат, но признался, что не смотрел. Это unknown,
	// а не «ресурса нет»: иначе слепое наблюдение породило бы create.
	a := &fakeResource{id: "a", obs: Observation{Known: false}, steps: []Step{step("a", "create")}}

	steps, states := Plan([]Resource{a}, observeAllForTest(a))

	if len(steps) != 0 {
		t.Fatalf("Known=false обязан подавлять шаги, получили %v", steps)
	}
	if states[0].Status != StatusUnknown {
		t.Fatalf("статус %q, ожидали unknown", states[0].Status)
	}
}

func TestPlanNeverObservedResourceIsUnknown(t *testing.T) {
	// Ресурс появился во второй сборке декларации и никем не наблюдался.
	// В карте наблюдений его нет — планировать по нулевому наблюдению нельзя.
	a := &fakeResource{id: "a", obs: Observation{Known: true}}
	fresh := &fakeResource{id: "fresh", obs: Observation{Known: true}, steps: []Step{step("fresh", "create")}}

	steps, states := Plan([]Resource{a, fresh}, observeAllForTest(a))

	if len(steps) != 0 {
		t.Fatalf("ненаблюдавшийся ресурс не должен давать шагов, получили %v", steps)
	}
	if states[1].Status != StatusUnknown {
		t.Fatalf("fresh: статус %q, ожидали unknown", states[1].Status)
	}
}

func TestPlanNotKnownBlocksTail(t *testing.T) {
	a := &fakeResource{id: "a", obs: Observation{Known: false}, steps: []Step{step("a", "create")}}
	b := &fakeResource{id: "b", obs: Observation{Known: true}, steps: []Step{step("b", "up")}}

	steps, states := Plan([]Resource{a, b}, observeAllForTest(a, b))

	if len(steps) != 0 {
		t.Fatalf("шагов быть не должно, получили %v", steps)
	}
	if states[0].Status != StatusUnknown || states[0].Error == "" {
		t.Fatalf("a: %+v, ожидали unknown с причиной", states[0])
	}
	if states[1].Status != StatusBlocked {
		t.Fatalf("b: статус %q, ожидали blocked", states[1].Status)
	}
}

func TestPlanNeverObservedBlocksTail(t *testing.T) {
	// Первый ресурс не наблюдался вовсе, второй наблюдался и требует шаг.
	a := &fakeResource{id: "a", obs: Observation{Known: true}, steps: []Step{step("a", "create")}}
	b := &fakeResource{id: "b", obs: Observation{Known: true}, steps: []Step{step("b", "up")}}

	steps, states := Plan([]Resource{a, b}, observeAllForTest(b)) // наблюдали только b

	if len(steps) != 0 {
		t.Fatalf("шагов быть не должно, получили %v", steps)
	}
	if states[0].Status != StatusUnknown || states[0].Error == "" {
		t.Fatalf("a: %+v, ожидали unknown с причиной", states[0])
	}
	if states[1].Status != StatusBlocked {
		t.Fatalf("b: статус %q, ожидали blocked", states[1].Status)
	}
}

func TestPlanFailedResourceBlocksTail(t *testing.T) {
	a := &fakeResource{id: "a", obs: Observation{Known: true}}
	b := &fakeResource{id: "b", obs: Observation{Known: true}, steps: []Step{step("b", "up")}}
	obs := observeAllForTest(a, b)
	obs.MarkFailed("a", "политика не найдена")

	steps, states := Plan([]Resource{a, b}, obs)

	if len(steps) != 0 {
		t.Fatalf("после failed шагов быть не должно, получили %v", steps)
	}
	if states[0].Status != StatusFailed || states[0].Error == "" {
		t.Fatalf("a: %+v, ожидали failed с причиной", states[0])
	}
	if states[1].Status != StatusBlocked {
		t.Fatalf("b: статус %q, ожидали blocked", states[1].Status)
	}
}

func TestStepKeyDistinguishesArgs(t *testing.T) {
	// Ключ шага решает, применяли ли мы уже этот шаг в прогоне. Аргументы
	// обязаны в него входить: «поставить адрес X» и «поставить адрес Y» —
	// разные шаги.
	a := Step{Resource: "r", Op: "set", Args: map[string]string{"v": "1"}}
	b := Step{Resource: "r", Op: "set", Args: map[string]string{"v": "2"}}
	c := Step{Resource: "r", Op: "set", Args: map[string]string{"v": "1"}}

	if StepKey(a) == StepKey(b) {
		t.Fatal("шаги с разными аргументами обязаны различаться")
	}
	if StepKey(a) != StepKey(c) {
		t.Fatal("одинаковые шаги обязаны совпадать по ключу")
	}
}

func TestStepKeyStableAcrossMapOrder(t *testing.T) {
	a := Step{Resource: "r", Op: "set", Args: map[string]string{"x": "1", "y": "2"}}
	b := Step{Resource: "r", Op: "set", Args: map[string]string{"y": "2", "x": "1"}}

	// Значения захватываем: повторный вызов StepKey в сообщении заново обошёл
	// бы карту и мог напечатать две одинаковые строки на реальном падении.
	for i := 0; i < 50; i++ {
		ka, kb := StepKey(a), StepKey(b)
		if ka != kb {
			t.Fatalf("ключ зависит от порядка обхода карты: %q против %q", ka, kb)
		}
	}
}

func TestStepKeyIsInjectiveWithSeparatorsInData(t *testing.T) {
	// Имена политик доступа приходят от пользователя и могут содержать
	// разделители ключа. Разные шаги обязаны остаться разными.
	//
	// Пара подобрана так, чтобы сырая склейка дала одну строку: подделанный
	// ключ обязан сортироваться ПОСЛЕ настоящего (zone > policy), иначе
	// сортировка ключей развела бы шаги сама и подделка не сработала бы.
	a := Step{Resource: "r", Op: "permit", Args: map[string]string{"policy": "home|zone=lan"}}
	b := Step{Resource: "r", Op: "permit", Args: map[string]string{"policy": "home", "zone": "lan"}}

	if StepKey(a) == StepKey(b) {
		t.Fatalf("ключи склеились: %q", StepKey(a))
	}
}

func TestStepKeyIsInjectiveWithSeparatorsInOp(t *testing.T) {
	// Тот же трюк уровнем выше: разделитель внутри Op не должен притворяться
	// границей между Op и первым аргументом.
	a := Step{Resource: "r", Op: "permit|policy=lan"}
	b := Step{Resource: "r", Op: "permit", Args: map[string]string{"policy": "lan"}}

	if StepKey(a) == StepKey(b) {
		t.Fatalf("ключи склеились: %q", StepKey(a))
	}
}
