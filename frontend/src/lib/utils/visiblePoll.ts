/**
 * Опрос, который спит в фоновой вкладке и догоняет при возврате.
 *
 * Обе половины обязательны. Пропуск при `hidden` без догона — это то, что
 * пользователь видит как устаревшие данные: вернулся на вкладку и до целого
 * периода смотрит на прошлое. Ровно этот полный паттерн уже живёт в
 * `lib/stores/polling.ts` (пропуск + обработчик `visibilitychange`); здесь он
 * вынут для опросов, которые стором не являются.
 *
 * Возвращает функцию остановки — её отдают из `$effect`/`onMount`.
 */
export function startVisiblePoll(tick: () => void | Promise<void>, intervalMs: number): () => void {
	const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden';
	const run = () => {
		if (hidden()) return;
		void tick();
	};

	run();
	const timer = setInterval(run, intervalMs);

	let onVisible: (() => void) | null = null;
	if (typeof document !== 'undefined') {
		onVisible = () => {
			if (document.visibilityState === 'visible') void tick();
		};
		document.addEventListener('visibilitychange', onVisible);
	}

	return () => {
		clearInterval(timer);
		if (onVisible && typeof document !== 'undefined') {
			document.removeEventListener('visibilitychange', onVisible);
		}
	};
}
