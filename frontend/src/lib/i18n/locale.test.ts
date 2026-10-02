import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import { flushSync } from 'svelte';

const STORAGE_KEY = 'awg-manager-locale';

function setBrowserLanguages(languages: string[]): void {
	Object.defineProperty(navigator, 'languages', { value: languages, configurable: true });
}

/** Свежий экземпляр модуля: стартовая локаль определяется при импорте. */
async function loadI18n() {
	vi.resetModules();
	return import('./index');
}

// Скомпилированный словарь Paraglide весит несколько МБ: первая трансформация
// на холодном кеше vitest дольше 5-секундного лимита теста. Прогреваем один
// раз заранее — loadI18n() после vi.resetModules() берёт её из кеша.
beforeAll(async () => {
	await import('./index');
}, 60_000);

describe('locale', () => {
	beforeEach(() => {
		localStorage.clear();
	});

	it('по умолчанию русский', async () => {
		const { locale, m } = await loadI18n();
		expect(locale.current).toBe('ru');
		expect(m.common_cancel()).toBe('Отмена');
	});

	it('язык браузера не учитывается — только ручной выбор', async () => {
		setBrowserLanguages(['en-US', 'en']);
		const { locale, m } = await loadI18n();
		expect(locale.current).toBe('ru');
		expect(m.common_cancel()).toBe('Отмена');
		expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
	});

	it('берёт сохранённый ручной выбор', async () => {
		localStorage.setItem(STORAGE_KEY, 'en');
		const { locale, m } = await loadI18n();
		expect(locale.current).toBe('en');
		expect(m.common_cancel()).toBe('Cancel');
	});

	it('мусор в localStorage игнорируется', async () => {
		localStorage.setItem(STORAGE_KEY, 'klingon');
		const { locale } = await loadI18n();
		expect(locale.current).toBe('ru');
	});

	it('set() переключает сообщения без перезагрузки и запоминает выбор', async () => {
		const { locale, m } = await loadI18n();
		const href = window.location.href;

		locale.set('en');
		flushSync();

		expect(locale.current).toBe('en');
		expect(m.common_cancel()).toBe('Cancel');
		expect(localStorage.getItem(STORAGE_KEY)).toBe('en');
		expect(window.location.href).toBe(href);

		const again = await loadI18n();
		expect(again.locale.current).toBe('en');
	});

	it('set() с неизвестной локалью — no-op', async () => {
		const { locale } = await loadI18n();
		// @ts-expect-error проверяем защиту от значений вне списка
		locale.set('de');
		expect(locale.current).toBe('ru');
		expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
	});

	it('сломанный localStorage не роняет определение и смену языка', async () => {
		const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new Error('SecurityError');
		});
		const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('QuotaExceededError');
		});
		try {
			const { locale, m } = await loadI18n();
			expect(locale.current).toBe('ru');
			locale.set('en');
			expect(m.common_cancel()).toBe('Cancel');
		} finally {
			getItem.mockRestore();
			setItem.mockRestore();
		}
	});
});
