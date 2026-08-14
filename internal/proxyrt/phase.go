package proxyrt

// DerivePhase выводит фазу инстанса. Фаза нигде не хранится: второго источника
// правды о состоянии не заводим.
//
// Порядок проверок важен. `disabled` — намерение, оно перекрывает всё.
//
// `failed` идёт выше потолка проходов: отказ объясняет, ПОЧЕМУ цикл упёрся в
// потолок, и такая причина информативнее самого факта упора. `unknown` и
// `blocked`, наоборот, идут ниже потолка: они причину не объясняют, и если
// пропустить их вперёд, инстанс с вечно ненаблюдаемым ресурсом навсегда
// застрянет в `waiting`, а сигнал `stuck` — «нужно вмешательство» — не выйдет
// наружу никогда.
//
// Между собой `unknown` и `blocked` равны: обе означают «не сделано» и ни одна
// не даёт права на settled. Отмена контекста намеренно НЕ становится затыком —
// иначе каждое выключение демона публиковало бы stuck.
func DerivePhase(intent Intent, res []ResourceState, planEmpty bool, stop StopReason) Phase {
	if intent == IntentDisabled {
		return PhaseDisabled
	}
	pending := false
	for _, r := range res {
		switch r.Status {
		case StatusFailed:
			return PhaseFailed
		case StatusUnknown, StatusBlocked:
			pending = true
		}
	}
	if stop == StopCeiling {
		return PhaseStuck
	}
	if pending {
		return PhaseWaiting
	}
	if !planEmpty || stop == StopAwaiting || stop == StopCanceled {
		return PhaseWaiting
	}
	return PhaseSettled
}
