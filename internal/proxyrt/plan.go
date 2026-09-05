package proxyrt

import (
	"sort"
	"strconv"
	"strings"
)

// Plan строит план и состояния ресурсов. Чистая функция: ввода-вывода нет,
// наблюдения приходят готовыми.
//
// Список ресурсов роли — цепочка. Первый ресурс, чьё наблюдение недоступно или
// чьё применение отказало, обрывает её: всё, что объявлено ниже, помечается
// blocked и шагов не даёт. Это цена отказа от графа зависимостей — порядок в
// декларации обязан идти от предпосылки к следствию.
func Plan(res []Resource, obs Observations) ([]Step, []ResourceState) {
	steps := make([]Step, 0, len(res))
	states := make([]ResourceState, 0, len(res))
	blockedBy := ResourceID("")

	for _, r := range res {
		id := r.ID()
		if blockedBy != "" {
			states = append(states, ResourceState{
				ID:     id,
				Status: StatusBlocked,
				Detail: "ожидает " + string(blockedBy),
			})
			continue
		}

		rec := obs.m[id]
		switch {
		case rec.failed != "":
			states = append(states, ResourceState{ID: id, Status: StatusFailed, Error: rec.failed})
			blockedBy = id
			continue
		case rec.err != nil:
			states = append(states, ResourceState{ID: id, Status: StatusUnknown, Error: rec.err.Error()})
			blockedBy = id
			continue
		case !rec.seen:
			// Ресурс появился в декларации после обхода наблюдателей.
			states = append(states, ResourceState{ID: id, Status: StatusUnknown, Error: "не наблюдался в этом проходе"})
			blockedBy = id
			continue
		case !rec.obs.Known:
			states = append(states, ResourceState{ID: id, Status: StatusUnknown, Error: "наблюдение недоступно"})
			blockedBy = id
			continue
		}

		st := r.Plan(rec.obs)
		if len(st) == 0 {
			states = append(states, ResourceState{ID: id, Status: StatusOK, Detail: rec.obs.Detail, Attrs: rec.obs.Attrs})
			continue
		}
		steps = append(steps, st...)
		states = append(states, ResourceState{ID: id, Status: StatusDrift, Detail: rec.obs.Detail, Attrs: rec.obs.Attrs})
	}
	return steps, states
}

// StepKey — устойчивый ключ шага. По нему цикл понимает, применял ли он уже
// этот шаг в текущем прогоне. Аргументы входят в ключ: «поставить адрес X» и
// «поставить адрес Y» — разные шаги, и второй обязан примениться.
//
// Кодирование самоограничивающее: все переменные части проходят через
// strconv.Quote. В аргументы попадают имена политик доступа, то есть
// произвольный пользовательский текст, и сырая склейка позволяла разделителям
// внутри данных притвориться разделителями структуры. Цена коллизии высока:
// два разных шага сочлись бы одним, и второй цикл молча пропустил бы как
// «уже применённый» — запрошенное изменение тихо не выполнилось бы.
func StepKey(s Step) string {
	var b strings.Builder
	b.WriteString(strconv.Quote(string(s.Resource)))
	b.WriteByte('|')
	b.WriteString(strconv.Quote(s.Op))
	if len(s.Args) == 0 {
		return b.String()
	}
	keys := make([]string, 0, len(s.Args))
	for k := range s.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(strconv.Quote(k))
		b.WriteByte('=')
		b.WriteString(strconv.Quote(s.Args[k]))
	}
	return b.String()
}
