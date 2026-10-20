package dnsroute

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
)

// inPlaceUpdate — правка существующей строки группы по её index.
type inPlaceUpdate struct {
	index string
	op    rciRouteOp
}

// planInPlace раскладывает переход группы от cur (строки в порядке роутера) к
// tgts (цель в порядке приоритета) на сносы, правки и добавления так, чтобы
// строки по возможности правились на месте и сохраняли глобальную позицию
// (#1023). Порядок внутри группы здесь не решается — его наводит planMoves.
//
// Правка по index в содержимое, которое уже есть в списке, создаёт на роутере
// полный дубль без ошибки. Поэтому интерфейс цели, уже стоящий в группе,
// остаётся своей строкой (первое вхождение), а на другие интерфейсы правятся
// только освободившиеся строки — и только на интерфейсы, которых в группе нет.
func planInPlace(cur []ndms.DNSRouteRule, tgts []rciRouteOp) (deleteIndexes []string, updates []inPlaceUpdate, adds []rciRouteOp) {
	wanted := make(map[string]bool, len(tgts))
	for _, t := range tgts {
		wanted[t.Iface] = true
	}
	kept := make(map[string]ndms.DNSRouteRule, len(cur))
	var freed []ndms.DNSRouteRule
	for _, r := range cur {
		if _, dup := kept[r.Interface]; wanted[r.Interface] && !dup {
			kept[r.Interface] = r
			continue
		}
		freed = append(freed, r)
	}

	var rejectEdits []inPlaceUpdate
	var fresh []rciRouteOp
	for _, t := range tgts {
		r, ok := kept[t.Iface]
		if !ok {
			fresh = append(fresh, t)
			continue
		}
		if r.Reject != t.Reject {
			rejectEdits = append(rejectEdits, inPlaceUpdate{index: r.Index, op: t})
		}
	}

	for i, r := range freed {
		if i < len(fresh) {
			updates = append(updates, inPlaceUpdate{index: r.Index, op: fresh[i]})
		} else {
			deleteIndexes = append(deleteIndexes, r.Index)
		}
	}
	if len(fresh) > len(freed) {
		adds = fresh[len(freed):]
	}
	// Смена reject идёт после правок освободившихся строк: повторное вхождение
	// того же интерфейса может как раз нести целевой reject, и правка оставшейся
	// строки раньше, чем дубль уйдёт на другой интерфейс, дала бы полный дубль.
	// Сносы батч применяет первыми, так что от снесённых дублей это не зависит.
	updates = append(updates, rejectEdits...)
	return deleteIndexes, updates, adds
}

// planMoves по выдаче /show/rc после правки (ordered — весь список в порядке
// роутера) строит перестановки, собирающие строки группы подряд от якоря —
// позиции первой строки группы — в порядке tgts, и правки флага disable.
//
// NDMS применяет пачку `rule` последовательно, поэтому список симулируется
// (remove+insert на каждый move): позиция следующей строки считается уже после
// предыдущих перестановок, а move выдаётся только тем строкам, что стоят не на
// месте. Пустой результат означает, что второй POST не нужен.
func planMoves(ordered []ndms.DNSRouteRule, group string, tgts []rciRouteOp) ([]command.DNSRouteMove, []rciRouteDisable, error) {
	anchor, count := -1, 0
	for i, r := range ordered {
		if r.Group != group {
			continue
		}
		if anchor < 0 {
			anchor = i
		}
		count++
	}
	if count > len(tgts) {
		return nil, nil, fmt.Errorf("группа %s: строк %d, целей %d", group, count, len(tgts))
	}

	sim := append([]ndms.DNSRouteRule(nil), ordered...)
	var moves []command.DNSRouteMove
	var disables []rciRouteDisable
	for i, t := range tgts {
		p := -1
		for j, r := range sim {
			if r.Group == group && r.Interface == t.Iface {
				p = j
				break
			}
		}
		if p < 0 {
			return nil, nil, fmt.Errorf("группа %s: нет строки для интерфейса %s", group, t.Iface)
		}
		r := sim[p]
		if want := anchor + i; p != want {
			moves = append(moves, command.DNSRouteMove{Index: r.Index, Position: want})
			sim = append(sim[:p], sim[p+1:]...)
			sim = append(sim[:want], append([]ndms.DNSRouteRule{r}, sim[want:]...)...)
		}
		if r.Disabled != t.Disabled {
			disables = append(disables, rciRouteDisable{Index: r.Index, Disabled: t.Disabled, Group: group, Iface: t.Iface})
		}
	}
	return moves, disables, nil
}
