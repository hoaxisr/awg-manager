package dnsroute

import (
	"fmt"
	"regexp"
	"strconv"

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

// packGroupRE — имя пакета списка: слаг и номер _pN (buildGroupName).
var packGroupRE = regexp.MustCompile(`^(.+)_p([0-9]+)$`)

// packLess — порядок обхода групп: пакеты одного списка по номеру, остальное
// по имени.
func packLess(a, b string) bool {
	sa, na, oka := packOf(a)
	sb, nb, okb := packOf(b)
	if oka && okb && sa == sb {
		return na < nb
	}
	return a < b
}

func packOf(group string) (slug string, n int, ok bool) {
	m := packGroupRE.FindStringSubmatch(group)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	return m[1], n, err == nil
}

// planPackMoves ставит строки нового пакета списка рядом с его же пакетами:
// сразу за последней строкой пакета с меньшим номером, а без такого — перед
// первым пакетом с большим. Новая строка NDMS уходит в хвост, а первая
// совпавшая группа выигрывает: пакет, выросший у списка при обновлении
// подписки, иначе проигрывал бы пересечения всем правилам ниже своих (#1107).
// Зовётся только для нового пакета списка, у которого на роутере уже есть
// строки (rciRouteRebuild.nearSiblings).
//
// ordered — весь список в порядке роутера. Строки пакета обязаны стоять ниже
// якоря (запись новой группы — в хвост): тогда перенос строки i на anchor+i не
// сдвигает следующие строки пакета, и позиции берутся из ordered как есть.
// Строка выше якоря (порядок правили руками) — не трогаем.
func planPackMoves(ordered []ndms.DNSRouteRule, group string) []command.DNSRouteMove {
	slug, n, ok := packOf(group)
	if !ok {
		return nil
	}
	var pos []int
	var rest []ndms.DNSRouteRule
	for i, r := range ordered {
		if r.Group == group {
			pos = append(pos, i)
		} else {
			rest = append(rest, r)
		}
	}
	anchor := -1
	for i, r := range rest {
		if s, k, ok := packOf(r.Group); ok && s == slug && k < n {
			anchor = i + 1
		}
	}
	if anchor < 0 {
		for i, r := range rest {
			if s, k, ok := packOf(r.Group); ok && s == slug && k > n {
				anchor = i
				break
			}
		}
	}
	if anchor < 0 || len(pos) == 0 || pos[0] < anchor {
		return nil
	}
	var moves []command.DNSRouteMove
	for i, p := range pos {
		if want := anchor + i; p != want {
			moves = append(moves, command.DNSRouteMove{Index: ordered[p].Index, Position: want})
		}
	}
	return moves
}
