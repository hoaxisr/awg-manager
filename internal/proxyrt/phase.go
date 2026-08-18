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
	// `deleted` — то же «работать не должно», что и `disabled`: роли объявляют
	// при нём то же пустое желаемое (процесс остановлен, адрес снят, интерфейс
	// опущен), а снос NDMS — работа уборщика по меткам (§4.2), не декларации.
	// Своей фазы у удаления нет намеренно: словарь §8 её не знает, а отличить
	// удаляемый инстанс от выключенного можно по намерению — оно публикуется
	// рядом с фазой (StateStore сравнивает и его). Без этой ветки удаляемый
	// инстанс докладывал бы `settled` — «всё как заказано» — пока уборка ещё
	// не случилась.
	if intent == IntentDisabled || intent == IntentDeleted {
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
