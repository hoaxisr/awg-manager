import { goto } from '$app/navigation';

/**
 * `?edit={id}` — кросс-маршрутный контракт поиска по правилам: страница
 * поиска уводит `goto('/router/ndms?edit=r1')`, целевая страница поднимает
 * этим модалку правила (пропы `editRuleId`/`editRuleCounter` вкладок).
 *
 * Параметр вычищается из URL сразу после чтения: иначе повторный клик по
 * тому же результату дал бы тот же адрес, а `goto` на текущий URL не
 * навигирует — правило не открылось бы второй раз. Счётчик нужен по той же
 * причине внутри вкладки: один и тот же `id` подряд не меняет проп.
 *
 * @param currentUrl геттер реактивного URL страницы (`() => $page.url`)
 */
export function createEditParam(currentUrl: () => URL) {
	let id = $state('');
	let counter = $state(0);
	// Обычный let: чтение счётчика внутри эффекта сделало бы его же
	// зависимостью и зациклило бы эффект.
	let bumps = 0;

	$effect(() => {
		const url = currentUrl();
		const next = url.searchParams.get('edit');
		if (!next) return;

		id = next;
		counter = ++bumps;

		const clean = new URL(url);
		clean.searchParams.delete('edit');
		void goto(`${clean.pathname}${clean.search}`, {
			replaceState: true,
			keepFocus: true,
			noScroll: true,
		});
	});

	return {
		get id() {
			return id;
		},
		get counter() {
			return counter;
		},
	};
}
