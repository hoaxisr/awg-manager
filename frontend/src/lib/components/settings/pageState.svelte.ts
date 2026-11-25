import { getContext, setContext } from 'svelte';

/**
 * Состояние вкладок настроек, живущее дольше самих вкладок: смонтирована
 * только активная, и без этого переключение теряло бы несохранённый ввод и
 * признак идущей операции. Срок жизни — страница: ушёл с неё — забыто.
 *
 * Вне страницы (тесты, одиночный рендер) контекста нет — состояние обычное,
 * на один компонент.
 */
const KEY = Symbol('settings-page-state');
type Store = Map<string, unknown>;

/** Завести хранилище; вызывается один раз, страницей настроек. */
export function providePageState(): void {
	setContext<Store>(KEY, new Map());
}

/** Реактивный объект под ключом: первый вызов создаёт, следующие возвращают его же. */
export function pageState<T extends object>(key: string, init: () => T): T {
	const store = getContext<Store | undefined>(KEY);
	const existing = store?.get(key) as T | undefined;
	if (existing) return existing;
	const created = $state(init());
	store?.set(key, created);
	return created;
}

export interface Draft<T> {
	value: T;
	readonly dirty: boolean;
	reset(): void;
}

/**
 * Черновик поля поверх сохранённого значения: пока пользователь не правил,
 * показывает сохранённое, после правки — введённое, пока его не сохранят
 * (значения сошлись) или не сбросят.
 */
export function pageDraft<T>(
	key: string,
	saved: () => T,
	equal: (a: T, b: T) => boolean = Object.is,
): Draft<T> {
	const box = pageState<{ edit: { value: T } | null }>(`draft:${key}`, () => ({ edit: null }));
	const dirty = $derived(box.edit !== null && !equal(box.edit.value, saved()));
	$effect(() => {
		if (box.edit !== null && !dirty) box.edit = null;
	});
	return {
		get value() {
			return box.edit ? box.edit.value : saved();
		},
		set value(value: T) {
			box.edit = { value };
		},
		get dirty() {
			return dirty;
		},
		reset() {
			box.edit = null;
		},
	};
}
