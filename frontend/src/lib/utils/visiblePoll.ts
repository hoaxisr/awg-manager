/**
 * Опрос, который спит в фоновой вкладке и догоняет при возврате.
 *
 * Обе половины обязательны. Пропуск при `hidden` без догона — это то, что
 * пользователь видит как устаревшие данные: вернулся на вкладку и до целого
 * периода смотрит на прошлое. Полный паттерн уже живёт в
 * `lib/stores/polling.ts`; здесь он вынут для опросов, которые стором не
 * являются.
 *
 * Возвращает функцию остановки — её отдают из `$effect`/`onMount`.
 */
export function startVisiblePoll(tick: () => void | Promise<void>, intervalMs: number): () => void {
	const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden';

	// inflight — та самая защита, что есть у образца в polling.ts. Без неё
	// догон накладывается на идущий запрос.
	let inflight = false;
	let lastRunAt = 0;
	let timer: ReturnType<typeof setInterval> | null = null;

	const run = async () => {
		if (hidden() || inflight) return;
		inflight = true;
		lastRunAt = Date.now();
		try {
			await tick();
		} finally {
			inflight = false;
		}
	};

	const startTimer = () => {
		if (timer !== null) clearInterval(timer);
		timer = setInterval(() => void run(), intervalMs);
	};

	void run();
	startTimer();

	let onVisible: (() => void) | null = null;
	if (typeof document !== 'undefined') {
		onVisible = () => {
			if (document.visibilityState !== 'visible') return;
			// Догоняем, только если период УЖЕ прошёл. Иначе частое
			// переключение вкладок давало бы запрос на каждый фокус — для
			// дорогих ручек (полный разбор conntrack) это выходит дороже
			// таймера, который мы этим хелпером заменяем.
			if (Date.now() - lastRunAt < intervalMs) return;
			void run();
			// Перефазируем: иначе штатный тик может прийти сразу за догоном.
			startTimer();
		};
		document.addEventListener('visibilitychange', onVisible);
	}

	return () => {
		if (timer !== null) clearInterval(timer);
		if (onVisible && typeof document !== 'undefined') {
			document.removeEventListener('visibilitychange', onVisible);
		}
	};
}
