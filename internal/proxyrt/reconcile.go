package proxyrt

import (
	"context"
	"time"
)

// defaultMaxPasses — страховка от неучтённого случая. Основной выход из цикла —
// «новых шагов не осталось», см. Run.
const defaultMaxPasses = 5

type ReconcileOpts struct {
	MaxPasses int
}

// Result — что цикл сделал за вызов.
type Result struct {
	// Intent — намерение, по которому цикл считал фазу. Едет вместе с
	// результатом, чтобы вызывающий не перечитывал его третий раз после прогона
	// и не публиковал пару, которой цикл не считал: намерение к моменту возврата
	// уже могло смениться, а фронт красит инстанс по намерению и объясняет по
	// фазе.
	Intent Intent
	Steps  []Step
	States []ResourceState
	Stop   StopReason
	Passes int
	// Recheck — минимальный ненулевой RecheckAfter среди ресурсов последнего
	// прохода. Воркер по нему заводит таймер подстраховки.
	Recheck time.Duration
}

type Reconciler struct {
	role      Role
	cfg       any
	maxPasses int
}

func NewReconciler(role Role, cfg any, opts ReconcileOpts) *Reconciler {
	maxPasses := opts.MaxPasses
	if maxPasses <= 0 {
		maxPasses = defaultMaxPasses
	}
	return &Reconciler{role: role, cfg: cfg, maxPasses: maxPasses}
}

// Run гоняет проходы до неподвижной точки.
//
// Ключевое правило: внутри одного прогона каждый шаг применяется не более
// одного раза. План повторился, а новых шагов нет — значит применённое ещё не
// дало эффекта. Это waiting с ожиданием события, а не затык: мутации NDMS
// асинхронны, oper up приходит хуком через секунды. Тем же правилом снимается
// горячий цикл, если внешний актор откатывает наши правила — повторяемость
// через события ловит backoff инстанса, а не эвристика внутри цикла.
func (r *Reconciler) Run(ctx context.Context, intent Intent) (Result, Phase) {
	res := Result{Intent: intent}
	applied := make(map[string]bool)
	// Ресурсы последнего прохода: по ним после цикла считается будильник.
	var resources []Resource

	for {
		if err := ctx.Err(); err != nil {
			res.Stop = StopCanceled
			break
		}
		if res.Passes >= r.maxPasses {
			res.Stop = StopCeiling
			break
		}
		res.Passes++

		obs := NewObservations()
		resources = r.role.Resources(intent, r.cfg, obs)
		dup := ResourceID("")
		for _, rs := range resources {
			id := rs.ID()
			if _, twice := obs.m[id]; twice {
				dup = id
				break
			}
			o, err := rs.Observe(ctx)
			obs.Put(id, o, err)
		}
		if dup != "" {
			// Два ресурса с одним идентификатором — дефект роли, и он опаснее
			// постороннего шага: наблюдение одного затирается другим, а шаг
			// одного исполняет ЧУЖОЙ объект, то есть правило уезжает в чужую
			// политику. Прерываем ДО применения: побочных эффектов не будет
			// вовсе, а наружу уедет отказ с причиной, а не здоровое ожидание.
			//
			// Хватает одной проверки на первом списке: состав ресурсов по
			// контракту Role стабилен для пары (intent, cfg).
			res.Steps = nil
			res.States = []ResourceState{{
				ID:     dup,
				Status: StatusFailed,
				Error:  "идентификатор ресурса объявлен в роли дважды",
			}}
			// Будильник тут не нужен: ничего не применялось, а дефект роли сам
			// не рассосётся — незачем гонять прогоны по таймеру.
			resources = nil
			break
		}
		// Второй вызов: желаемое может зависеть от наблюдений — адрес
		// интерфейса вычисляется из состояния процесса.
		resources = r.role.Resources(intent, r.cfg, obs)

		steps, states := Plan(resources, obs)
		res.Steps, res.States = steps, states
		if len(steps) == 0 {
			break
		}

		fresh := steps[:0:0]
		for _, s := range steps {
			if !applied[StepKey(s)] {
				fresh = append(fresh, s)
			}
		}
		if len(fresh) == 0 {
			res.Stop = StopAwaiting
			break
		}

		byID := make(map[ResourceID]Resource, len(resources))
		for _, rs := range resources {
			byID[rs.ID()] = rs
		}
		// abort прерывает весь прогон. Причин две: отказ применения и отмена
		// контекста, и это разные вещи — см. ветку ошибки ниже.
		abort := false
		for _, s := range fresh {
			rs, ok := byID[s.Resource]
			if !ok {
				// План сослался на ресурс, которого нет в декларации: дефект
				// роли. Молча пропустить шаг нельзя — он не применится и нигде
				// не всплывёт.
				res.States = append(res.States, ResourceState{
					ID:     s.Resource,
					Status: StatusFailed,
					Error:  "шаг ссылается на ресурс, отсутствующий в декларации роли",
				})
				abort = true
				break
			}
			applied[StepKey(s)] = true
			if err := rs.Apply(ctx, s); err != nil {
				if ctx.Err() != nil {
					// Ресурс уважает контекст и вернул причину отмены. Это не
					// его отказ, а наше выключение: пометить ресурс failed
					// значило бы оставлять след «отказ» после каждого
					// shutdown с незавершённым применением.
					res.Stop = StopCanceled
					abort = true
					break
				}
				// Отказ шага не разрушает: помечаем ресурс, хвост цепочки
				// блокируется пересчётом плана, соседи не трогаются.
				obs.MarkFailed(s.Resource, err.Error())
				_, res.States = Plan(resources, obs)
				abort = true
				break
			}
		}
		if abort {
			break
		}
	}
	// Будильник считается ПОСЛЕ цикла, по состоянию ресурсов на момент выхода.
	// Ресурс вправе взвести backoff внутри Apply (гейт пригодности бинаря,
	// отказ старта процесса), а отказ Apply рвёт прогон — счёт до применения
	// публиковал бы Recheck=0, и разбудить инстанс в failed было бы некому.
	// Ровно так же выходы по потолку проходов и по StopAwaiting забирают паузу
	// последнего применения, а не предыдущего прохода.
	res.Recheck = minRecheck(resources)

	// При отменённом контексте причина остановки — отмена, чем бы цикл ни
	// завершился до этого: мы выключаемся, и любой другой вывод (ожидание
	// эффекта, исчерпанный потолок) наружу не публикуется.
	//
	// Отмена ловится в любом месте прохода, в том числе внутри наблюдения —
	// самого долгого шага. Тогда ресурс становится unknown, а цикл заключает
	// что-то своё: план пуст — StopNone, план повторился — StopAwaiting. Ни то
	// ни другое не должно доехать до пользователя ложным состоянием.
	if ctx.Err() != nil && res.Stop != StopCanceled {
		res.Stop = StopCanceled
	}
	return res, DerivePhase(intent, res.States, len(res.Steps) == 0, res.Stop)
}

// minRecheck — наименьший ненулевой период подстраховочной сверки.
func minRecheck(res []Resource) time.Duration {
	var best time.Duration
	for _, r := range res {
		d := r.RecheckAfter()
		if d <= 0 {
			continue
		}
		if best == 0 || d < best {
			best = d
		}
	}
	return best
}
