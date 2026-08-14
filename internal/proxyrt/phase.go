package proxyrt

// DerivePhase выводит фазу инстанса. Фаза нигде не хранится: второго источника
// правды о состоянии не заводим.
//
// Порядок проверок важен. `disabled` — намерение, оно перекрывает всё.
// Состояние ресурсов важнее приговора цикла: `failed` объясняет, ПОЧЕМУ проходы
// упёрлись в потолок, и потому идёт выше `ceiling`. `failed` важнее `unknown`:
// объяснимый отказ информативнее, чем «не посмотрели». `blocked` читается как
// `unknown` — обе означают «не сделано», и ни одна не даёт права на settled.
// Отмена контекста намеренно НЕ становится затыком — иначе каждое выключение
// демона публиковало бы stuck.
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
	if pending {
		return PhaseWaiting
	}
	if stop == StopCeiling {
		return PhaseStuck
	}
	if !planEmpty || stop == StopAwaiting || stop == StopCanceled {
		return PhaseWaiting
	}
	return PhaseSettled
}
