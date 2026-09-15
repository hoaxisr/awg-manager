package opkgtun

import (
	"fmt"
	"strings"
)

// namedLimit — сколько чужих держателей называть поимённо. Дальше счётчиком по
// классам: на arm/arm64 занятых бывает под полсотни, и список из полусотни
// строк сообщение только испортит.
const namedLimit = 5

// exhausted — отказ выбора. Неэкспортируем намеренно: программного разбора у
// вызывающих нет ни одного (все три оборачивают и отдают наверх), а
// errors.Is(ErrExhausted) хватает всем. Экспортировать поля ради собственного
// теста значит дать тесту диктовать форму прод-типа.
type exhausted struct {
	self           Holder
	floor, ceiling int
	taken          Taken // ровно то, что видел выбор
	reserved       Taken // чужие незакрытые резервации
	veto           Taken
	conflicts      Conflicts
}

func (p *Pool) exhaustedFor(r Request, taken, early Taken, conflicts Conflicts) error {
	return &exhausted{
		self:      r.self,
		floor:     p.floorFor(r.self),
		ceiling:   p.ceiling,
		taken:     taken,
		reserved:  early,
		veto:      r.veto,
		conflicts: conflicts,
	}
}

func (e *exhausted) Unwrap() error { return ErrExhausted }

// Error называет держателя каждого занятого номера: пул делят четыре
// подсистемы, и совет «освободите номер» без имени того, кто его держит,
// выполнить нечем (#891).
//
// Свои — счётчиком: пользователь видит их в списке туннелей. Чужие — поимённо:
// это единственное, чего в списке нет. Номер, свободный в пуле, но занятый
// ИДЕНТИФИКАТОРОМ, назван отдельно: удаление такой записи освобождает awgN, но
// не номер OpkgTunN, и не сказать этого значит соврать.
func (e *exhausted) Error() string {
	own := 0
	foreign := Taken{}
	for n := e.floor; n <= e.ceiling; n++ {
		h, busy := e.taken[n]
		if !busy {
			continue
		}
		if h.sameKind(e.self) {
			own++
			continue
		}
		foreign[n] = h
	}

	idOnly := Taken{}
	for n, h := range e.veto {
		if n < e.floor || n > e.ceiling {
			continue
		}
		if _, busy := e.taken[n]; !busy {
			idOnly[n] = h
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s в диапазоне %d..%d", ErrExhausted.Error(), e.floor, e.ceiling)
	if len(foreign) > 0 {
		b.WriteString(". Чужие подсистемы: " + listHolders(foreign))
	}
	if len(idOnly) > 0 {
		b.WriteString(". Номер свободен, но занят идентификатор: " + listHolders(idOnly))
	}
	if own > 0 {
		fmt.Fprintf(&b, ". Ваших (%s): %d", e.self.kind, own)
	}
	if len(e.reserved) > 0 {
		b.WriteString(". Заняты незавершённой операцией: " + listHolders(e.reserved))
	}
	if len(e.conflicts) > 0 {
		b.WriteString(". Спорные номера, заявлены дважды: " + e.conflicts.String())
	}
	b.WriteString(". Освободите один номер")
	return b.String()
}

// listHolders — «номер — держатель» по возрастанию, первые namedLimit
// поимённо, остаток счётчиком по классам.
func listHolders(t Taken) string {
	keys := sortedKeys(t)
	parts := make([]string, 0, namedLimit+1)
	for i, n := range keys {
		if i == namedLimit {
			break
		}
		parts = append(parts, fmt.Sprintf("%d — %s", n, t[n]))
	}
	if rest := keys[min(namedLimit, len(keys)):]; len(rest) > 0 {
		byKind := map[kind]int{}
		for _, n := range rest {
			byKind[t[n].kind]++
		}
		counts := make([]string, 0, len(byKind))
		for _, k := range []kind{kindTunnel, kindSystemTunnel, kindProxy, kindRouterMode, kindAnon} {
			if c := byKind[k]; c > 0 {
				counts = append(counts, fmt.Sprintf("%s — %d", k, c))
			}
		}
		parts = append(parts, fmt.Sprintf("и ещё %d (%s)", len(rest), strings.Join(counts, ", ")))
	}
	return strings.Join(parts, ", ")
}
