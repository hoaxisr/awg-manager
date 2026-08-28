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

// cancelingResource уважает контекст: его Apply отменяет переданный контекст и
// возвращает причину отмены — так ведёт себя ресурс при выключении демона.
type cancelingResource struct {
	id     ResourceID
	cancel context.CancelFunc
}

func (c *cancelingResource) ID() ResourceID { return c.id }
func (c *cancelingResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true}, nil
}
func (c *cancelingResource) Plan(Observation) []Step {
	return []Step{{Resource: c.id, Op: "create", Reason: "нужно создать"}}
}
func (c *cancelingResource) Apply(ctx context.Context, _ Step) error {
	c.cancel()
	return ctx.Err()
}
func (c *cancelingResource) RecheckAfter() time.Duration { return 0 }

type staticRole struct{ res []Resource }

func (s staticRole) Resources(Intent, any, Observations) []Resource { return s.res }

// unstableRole нарушает контракт: второй ресурс появляется только во втором
// вызове Resources, когда наблюдения уже собраны.
type unstableRole struct {
	first  Resource
	second Resource
}

func (u unstableRole) Resources(_ Intent, _ any, obs Observations) []Resource {
	if _, ok := obs.Get(u.first.ID()); ok {
		return []Resource{u.first, u.second}
	}
	return []Resource{u.first}
}

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

func TestReconcileDuplicateResourceIDIsFailedNotSilent(t *testing.T) {
	// Два ресурса с одним идентификатором — дефект роли опаснее постороннего
	// шага: наблюдение первого затирается вторым, а шаг первого исполняет ЧУЖОЙ
	// объект. На роутере это правило, уехавшее в чужую политику, — и наружу при
	// этом уезжало здоровое «ожидание».
	first := &statefulResource{id: "same", want: "первый", recheck: time.Minute}
	second := &statefulResource{id: "same", want: "второй", recheck: time.Minute}
	rec := NewReconciler(staticRole{res: []Resource{first, second}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if phase != PhaseFailed {
		t.Fatalf("фаза %q, ожидали failed: коллизия идентификаторов обязана быть громкой", phase)
	}
	if first.applies != 0 || second.applies != 0 {
		t.Fatalf("применений первый=%d второй=%d, ожидали 0: до чужого объекта дело доходить не должно",
			first.applies, second.applies)
	}
	var found bool
	for _, st := range res.States {
		if st.ID == "same" && st.Status == StatusFailed && st.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("состояния не называют коллизию идентификаторов: %+v", res.States)
	}
	if res.Recheck != 0 {
		t.Fatalf("Recheck = %v, ожидали 0: дефект роли сам не рассосётся, гонять прогоны по таймеру незачем", res.Recheck)
	}
}

func TestReconcileResultCarriesIntent(t *testing.T) {
	// Фаза считается по намерению, значит намерение обязано ехать наружу вместе с
	// ней. Иначе вызывающий перечитывает намерение ТРЕТИЙ раз, уже после прогона,
	// и публикует пару, которой цикл не считал: {disabled, settled} или
	// {enabled, disabled}. Фронт красит инстанс по намерению, а объясняет по фазе
	// — на экране получилось бы противоречие.
	r := &statefulResource{id: "a", want: "up"}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentDisabled)

	if res.Intent != IntentDisabled {
		t.Fatalf("намерение в результате %q, считали по %q", res.Intent, IntentDisabled)
	}
	if phase != PhaseDisabled {
		t.Fatalf("фаза %q, ожидали disabled", phase)
	}

	res, _ = rec.Run(context.Background(), IntentEnabled)
	if res.Intent != IntentEnabled {
		t.Fatalf("намерение в результате %q, считали по %q", res.Intent, IntentEnabled)
	}
}

func TestReconcileAppliesEachStepOnceOnMixedRole(t *testing.T) {
	// Роль из двух ресурсов: один сходится сразу, второй ждёт внешнего
	// эффекта. На проходе 2 план УЖЕ ИЗМЕНИЛСЯ (остался только async-шаг),
	// но свежих шагов в нём нет — значит выходим, а не применяем второй раз.
	// Реализация, сравнивающая планы целиком, здесь применит async дважды.
	async := &asyncResource{id: "async"}
	sync := &statefulResource{id: "sync", want: "up"}
	rec := NewReconciler(staticRole{res: []Resource{async, sync}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if async.applies != 1 {
		t.Fatalf("async применён %d раз, ожидали ровно 1", async.applies)
	}
	if res.Stop != StopAwaiting {
		t.Fatalf("стопор %q, ожидали awaiting", res.Stop)
	}
	if res.Passes != 2 {
		t.Fatalf("проходов %d, ожидали 2", res.Passes)
	}
	if phase != PhaseWaiting {
		t.Fatalf("фаза %q, ожидали waiting", phase)
	}
}

func TestReconcileUnstableRoleCompositionStallsVisibly(t *testing.T) {
	// Документирует последствие нарушения контракта Role: ресурс, не
	// попавший в список ДО наблюдения, получает unknown с причиной и
	// инстанс остаётся в waiting. Это не желаемое поведение, а зафиксированная
	// цена нарушения — чтобы её нашли по тесту, а не по зависшему роутеру.
	a := &statefulResource{id: "a", want: "up", current: "up"}
	b := &statefulResource{id: "b", want: "up"}
	rec := NewReconciler(unstableRole{first: a, second: b}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if b.applies != 0 {
		t.Fatalf("b применён %d раз — контракт нарушен, но шаг прошёл?", b.applies)
	}
	if phase != PhaseWaiting {
		t.Fatalf("фаза %q, ожидали waiting", phase)
	}
	var sawUnknown bool
	for _, st := range res.States {
		if st.ID == "b" && st.Status == StatusUnknown && st.Error != "" {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("причина, по которой b не наблюдался, не видна: %+v", res.States)
	}
}

func TestReconcileCancelDuringApplyIsNotFailure(t *testing.T) {
	// Выключение демона застаёт применение на середине. Ресурс честно вернул
	// причину отмены — это не его отказ, и следа «failed» остаться не должно.
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelingResource{id: "a", cancel: cancel}
	rec := NewReconciler(staticRole{res: []Resource{r}}, nil, ReconcileOpts{})

	res, phase := rec.Run(ctx, IntentEnabled)

	if res.Stop != StopCanceled {
		t.Fatalf("стопор %q, ожидали canceled", res.Stop)
	}
	if phase == PhaseFailed {
		t.Fatal("выключение демона не должно оставлять след отказа")
	}
	for _, st := range res.States {
		if st.Status == StatusFailed {
			t.Fatalf("ресурс помечен отказавшим при отмене: %+v", st)
		}
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

// backoffResource — образец Proc: паузу до следующей попытки он взводит ВНУТРИ
// Apply (гейт пригодности бинаря, отказ старта), а не в Observe.
type backoffResource struct {
	id      ResourceID
	backoff time.Duration
	fail    bool
	grow    bool // каждое применение рождает НОВЫЙ шаг — так добирается потолок
	n       int
	recheck time.Duration
}

func (b *backoffResource) ID() ResourceID { return b.id }

func (b *backoffResource) Observe(context.Context) (Observation, error) {
	return Observation{Known: true}, nil
}

func (b *backoffResource) Plan(Observation) []Step {
	arg := "0"
	if b.grow {
		arg = strconv.Itoa(b.n)
	}
	return []Step{{Resource: b.id, Op: "start", Args: map[string]string{"n": arg}, Reason: "нужно запустить"}}
}

func (b *backoffResource) Apply(context.Context, Step) error {
	b.n++
	b.recheck = b.backoff * time.Duration(b.n) // каждое применение удлиняет паузу
	if b.fail {
		return errors.New("пин бинаря не обновлён")
	}
	return nil
}

func (b *backoffResource) RecheckAfter() time.Duration { return b.recheck }

func TestReconcileRecheckSurvivesApplyFailure(t *testing.T) {
	// Отказ Apply рвёт прогон, но ресурс уже взвёл backoff внутри Apply. Если
	// считать будильник до применения, наружу уедет Recheck=0 и воркер не
	// заведёт таймер: wdtt-клиент с протухшим пином бинаря останется в failed
	// навсегда — будить его некому, процесса нет, а значит нет и push'ей.
	b := &backoffResource{id: "process", backoff: 5 * time.Second, fail: true}
	rec := NewReconciler(staticRole{res: []Resource{b}}, nil, ReconcileOpts{})

	res, phase := rec.Run(context.Background(), IntentEnabled)

	if phase != PhaseFailed {
		t.Fatalf("фаза %q, ожидали failed", phase)
	}
	if res.Recheck != 5*time.Second {
		t.Fatalf("Recheck = %v, ожидали ровно 5s — backoff, взведённый внутри Apply", res.Recheck)
	}
}

func TestReconcileRecheckSurvivesCeiling(t *testing.T) {
	// Потолок проходов — тоже выход мимо пересчёта: последний проход применил и
	// взвёл паузу, а цикл оборвался сверху.
	b := &backoffResource{id: "process", backoff: 5 * time.Second, grow: true}
	rec := NewReconciler(staticRole{res: []Resource{b}}, nil, ReconcileOpts{MaxPasses: 2})

	res, _ := rec.Run(context.Background(), IntentEnabled)

	if res.Stop != StopCeiling {
		t.Fatalf("стопор %q, ожидали ceiling", res.Stop)
	}
	if res.Recheck != 10*time.Second {
		t.Fatalf("Recheck = %v, ожидали ровно 10s — пауза после ПОСЛЕДНЕГО применения, а не после предпоследнего", res.Recheck)
	}
}

func TestReconcileRecheckSurvivesAwaiting(t *testing.T) {
	// Ожидание эффекта: шаг применён в первом проходе, во втором повторён тем же
	// и отброшен как применённый. Пауза, взведённая тем применением, обязана
	// доехать наружу.
	b := &backoffResource{id: "process", backoff: 5 * time.Second}
	rec := NewReconciler(staticRole{res: []Resource{b}}, nil, ReconcileOpts{})

	res, _ := rec.Run(context.Background(), IntentEnabled)

	if res.Stop != StopAwaiting {
		t.Fatalf("стопор %q, ожидали awaiting", res.Stop)
	}
	if res.Recheck != 5*time.Second {
		t.Fatalf("Recheck = %v, ожидали ровно 5s", res.Recheck)
	}
}
