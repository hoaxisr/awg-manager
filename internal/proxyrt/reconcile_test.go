package proxyrt

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// statefulResource — ресурс с настоящим внутренним состоянием: применение
// действительно меняет то, что потом наблюдается.
type statefulResource struct {
	id      ResourceID
	want    string
	current string
	recheck time.Duration
	applies int
}

func (s *statefulResource) ID() ResourceID { return s.id }

func (s *statefulResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true, Exists: s.current != "", Detail: s.current}, nil
}

func (s *statefulResource) Plan(obs Observation) []Step {
	if obs.Detail == s.want {
		return nil
	}
	return []Step{{Resource: s.id, Op: "set", Args: map[string]string{"value": s.want}, Reason: "расхождение"}}
}

func (s *statefulResource) Apply(_ context.Context, st Step) error {
	s.applies++
	s.current = st.Args["value"]
	return nil
}

func (s *statefulResource) RecheckAfter() time.Duration { return s.recheck }

// asyncResource принимает шаг, но эффект появляется только после внешнего
// события — так ведут себя мутации NDMS.
type asyncResource struct {
	id      ResourceID
	done    bool
	applies int
}

func (a *asyncResource) ID() ResourceID { return a.id }

func (a *asyncResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true, Detail: boolLabel(a.done)}, nil
}

func (a *asyncResource) Plan(obs Observation) []Step {
	if obs.Detail == "done" {
		return nil
	}
	return []Step{{Resource: a.id, Op: "up", Reason: "интерфейс не поднят"}}
}

func (a *asyncResource) Apply(context.Context, Step) error {
	a.applies++
	return nil // эффекта пока нет: его принесёт хук
}

func (a *asyncResource) RecheckAfter() time.Duration { return 0 }

func boolLabel(b bool) string {
	if b {
		return "done"
	}
	return "pending"
}

// failingResource всегда отказывает на применении.
type failingResource struct{ id ResourceID }

func (f *failingResource) ID() ResourceID { return f.id }

func (f *failingResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true}, nil
}

func (f *failingResource) Plan(Observation) []Step {
	return []Step{{Resource: f.id, Op: "create", Reason: "нужно создать"}}
}

func (f *failingResource) Apply(context.Context, Step) error {
	return errors.New("политика не найдена")
}

func (f *failingResource) RecheckAfter() time.Duration { return 0 }

// growingResource каждый проход требует НОВЫЙ шаг — так проверяется потолок.
type growingResource struct {
	id ResourceID
	n  int
}

func (g *growingResource) ID() ResourceID { return g.id }

func (g *growingResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true}, nil
}

func (g *growingResource) Plan(Observation) []Step {
	return []Step{{Resource: g.id, Op: "step", Args: map[string]string{"n": strconv.Itoa(g.n)}, Reason: "следующий"}}
}

func (g *growingResource) Apply(context.Context, Step) error {
	g.n++
	return nil
}

func (g *growingResource) RecheckAfter() time.Duration { return 0 }

// strayRole возвращает ресурс, чей план ссылается на чужой идентификатор.
type strayResource struct{ id ResourceID }

func (s *strayResource) ID() ResourceID { return s.id }
func (s *strayResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true}, nil
}
func (s *strayResource) Plan(Observation) []Step {
	return []Step{{Resource: "нет-такого", Op: "set", Reason: "дефект роли"}}
}
func (s *strayResource) Apply(context.Context, Step) error { return nil }
func (s *strayResource) RecheckAfter() time.Duration       { return 0 }

type staticRole struct{ res []Resource }

func (s staticRole) Resources(Intent, any, Observations) []Resource { return s.res }

func TestReconcileConvergesInTwoPasses(t *testing.T) {
	r := &statefulResource{id: "a", want: "up"}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if res.Stop != StopNone {
		t.Fatalf("стопор %q, ожидали пустой", res.Stop)
	}
	if res.Passes != 2 {
		t.Fatalf("проходов %d, ожидали 2: первый применяет, второй видит пустой план", res.Passes)
	}
	if phase != PhaseSettled {
		t.Fatalf("фаза %q, ожидали settled", phase)
	}
}

func TestReconcileAwaitsAsyncEffect(t *testing.T) {
	// Здоровый путь: шаг применён, эффект придёт позже. Второй проход видит тот
	// же шаг, новых нет — выходим в waiting, а не крутим цикл и не зовём затык.
	a := &asyncResource{id: "a"}
	rec := NewReconciler(staticRole{res: []Resource{a}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if res.Stop != StopAwaiting {
		t.Fatalf("стопор %q, ожидали awaiting", res.Stop)
	}
	if phase != PhaseWaiting {
		t.Fatalf("фаза %q, ожидали waiting", phase)
	}
	if a.applies != 1 {
		t.Fatalf("применений %d, ожидали 1: шаг не должен применяться дважды за прогон", a.applies)
	}
	if res.Passes != 2 {
		t.Fatalf("проходов %d, ожидали 2", res.Passes)
	}
}

func TestReconcileAsyncEffectSettlesOnNextRun(t *testing.T) {
	// Пришло событие, эффект появился — следующий прогон обязан быть пустым.
	a := &asyncResource{id: "a"}
	rec := NewReconciler(staticRole{res: []Resource{a}}, nil, ReconcileOpts{})
	rec.Run(context.Background(), IntentEnabled)

	a.done = true
	res, phase := rec.Run(context.Background(), IntentEnabled)

	if len(res.Steps) != 0 || phase != PhaseSettled {
		t.Fatalf("после эффекта ожидали пустой план и settled, получили %v / %q", res.Steps, phase)
	}
	if a.applies != 1 {
		t.Fatalf("применений %d, ожидали 1", a.applies)
	}
}

func TestReconcileStopsOnCeiling(t *testing.T) {
	// Ресурс каждый проход требует новый шаг — «применили один раз» его не
	// останавливает, срабатывает именно потолок.
	g := &growingResource{id: "a"}
	rec := NewReconciler(staticRole{res: []Resource{g}}, nil, ReconcileOpts{MaxPasses: 3})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if res.Stop != StopCeiling {
		t.Fatalf("стопор %q, ожидали ceiling", res.Stop)
	}
	if res.Passes != 3 {
		t.Fatalf("проходов %d, ожидали ровно 3", res.Passes)
	}
	if phase != PhaseStuck {
		t.Fatalf("фаза %q, ожидали stuck", phase)
	}
}

func TestReconcileApplyErrorMarksFailedAndBlocksTail(t *testing.T) {
	bad := &failingResource{id: "a"}
	tail := &statefulResource{id: "b", want: "up"}
	rec := NewReconciler(staticRole{res: []Resource{bad, tail}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if phase != PhaseFailed {
		t.Fatalf("фаза %q, ожидали failed", phase)
	}
	if res.States[0].Error == "" {
		t.Fatal("причина отказа обязана доехать до состояния ресурса")
	}
	if res.States[1].Status != StatusBlocked {
		t.Fatalf("хвост цепочки: %q, ожидали blocked", res.States[1].Status)
	}
	if tail.applies != 0 {
		t.Fatal("после отказа предшественника хвост применяться не должен")
	}
}

func TestReconcileCanceledContextIsNotStuck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := NewReconciler(staticRole{res: []Resource{&statefulResource{id: "a", want: "up"}}}, nil, ReconcileOpts{})

	res, phase := rec.Run(ctx, IntentEnabled)

	if res.Stop != StopCanceled {
		t.Fatalf("стопор %q, ожидали canceled", res.Stop)
	}
	if phase == PhaseStuck {
		t.Fatal("отмена контекста не должна публиковаться как затык")
	}
}

func TestReconcileStrayStepIsFailedNotSilent(t *testing.T) {
	rec := NewReconciler(staticRole{res: []Resource{&strayResource{id: "a"}}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if phase != PhaseFailed {
		t.Fatalf("фаза %q, ожидали failed: шаг на несуществующий ресурс не должен теряться", phase)
	}
	var found bool
	for _, st := range res.States {
		if st.ID == "нет-такого" && st.Status == StatusFailed && st.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("состояния не содержат объяснения потерянного шага: %+v", res.States)
	}
}

func TestReconcileReportsMinRecheck(t *testing.T) {
	slow := &statefulResource{id: "a", want: "up", recheck: time.Minute}
	fast := &statefulResource{id: "b", want: "up", recheck: 15 * time.Second}
	none := &statefulResource{id: "c", want: "up"}
	rec := NewReconciler(staticRole{res: []Resource{slow, fast, none}}, nil, ReconcileOpts{})

	res, _ := rec.Run(context.Background(), IntentEnabled)

	if res.Recheck != 15*time.Second {
		t.Fatalf("Recheck = %v, ожидали минимальный ненулевой 15s", res.Recheck)
	}
}
