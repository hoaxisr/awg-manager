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
