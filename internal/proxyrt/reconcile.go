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
	var res Result
	applied := make(map[string]bool)

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
		resources := r.role.Resources(intent, r.cfg, obs)
		for _, rs := range resources {
			o, err := rs.Observe(ctx)
			obs.Put(rs.ID(), o, err)
		}
		// Второй вызов: желаемое может зависеть от наблюдений — адрес
		// интерфейса вычисляется из состояния процесса.
		resources = r.role.Resources(intent, r.cfg, obs)
		res.Recheck = minRecheck(resources)

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
		failed := false
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
				failed = true
				break
			}
			applied[StepKey(s)] = true
			if err := rs.Apply(ctx, s); err != nil {
				// Отказ шага не разрушает: помечаем ресурс, хвост цепочки
				// блокируется пересчётом плана, соседи не трогаются.
				obs.MarkFailed(s.Resource, err.Error())
				_, res.States = Plan(resources, obs)
				failed = true
				break
			}
		}
		if failed {
			break
		}
	}
	return res, DerivePhase(intent, res.States, len(res.Steps) == 0, res.Stop)
}

// minRecheck — наименьший ненулевой период подстраховочной сверки.
func minRecheck(res []Resource) time.Duration {
	var min time.Duration
	for _, r := range res {
		d := r.RecheckAfter()
		if d <= 0 {
			continue
		}
		if min == 0 || d < min {
			min = d
		}
	}
	return min
}
