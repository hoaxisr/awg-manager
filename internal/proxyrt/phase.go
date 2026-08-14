package proxyrt

// DerivePhase выводит фазу инстанса. Фаза нигде не хранится: второго источника
// правды о состоянии не заводим.
//
// Порядок проверок важен. `disabled` — намерение, оно перекрывает всё.
// Потолок проходов — приговор цикла. `failed` важнее `unknown`: объяснимый
// отказ информативнее, чем «не посмотрели». Отмена контекста намеренно НЕ
// становится затыком — иначе каждое выключение демона публиковало бы stuck.
func DerivePhase(intent Intent, res []ResourceState, planEmpty bool, stop StopReason) Phase {
	if intent == IntentDisabled {
		return PhaseDisabled
	}
	if stop == StopCeiling {
		return PhaseStuck
	}
	for _, r := range res {
		if r.Status == StatusFailed {
			return PhaseFailed
		}
	}
	for _, r := range res {
		if r.Status == StatusUnknown {
			return PhaseWaiting
		}
	}
	if !planEmpty || stop == StopAwaiting || stop == StopCanceled {
		return PhaseWaiting
	}
	return PhaseSettled
}
