/** Pill / WizardStep — guided wizard: блокируем только будущие шаги, пройденные остаются редактируемыми. */

/** Шаг заблокирован, если предыдущие ещё не выполнены. */
export function wizardStepLocked(prevDone: boolean, isFirst = false): boolean {
	if (isFirst) return false;
	return !prevDone;
}

/** Подсветка текущего (первого незавершённого) шага. */
export function wizardStepHighlighted(
	prevDone: boolean,
	selfDone: boolean,
	isFirst = false
): boolean {
	if (isFirst) return !selfDone;
	return prevDone && !selfDone;
}

/**
 * После завершения настройки: конфиг-шаги (1..configStepsCount) заблокированы,
 * пока пользователь не включит «Изменить». Последний шаг (запуск / логи / ссылка) всегда доступен.
 */
export function wizardStepLockedAfterComplete(
	stepNum: number,
	configStepsCount: number,
	wizardComplete: boolean,
	editUnlocked: boolean,
	prevDone: boolean,
	isFirst = false
): boolean {
	if (wizardComplete && !editUnlocked && stepNum <= configStepsCount) {
		return true;
	}
	return wizardStepLocked(prevDone, isFirst);
}

export function wizardStepHighlightedAfterComplete(
	stepNum: number,
	lastStepNum: number,
	prevDone: boolean,
	selfDone: boolean,
	isFirst: boolean,
	wizardComplete: boolean,
	editUnlocked: boolean
): boolean {
	if (wizardComplete && !editUnlocked) {
		return stepNum === lastStepNum;
	}
	return wizardStepHighlighted(prevDone, selfDone, isFirst);
}
