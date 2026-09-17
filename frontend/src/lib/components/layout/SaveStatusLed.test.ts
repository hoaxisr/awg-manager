import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

vi.mock('$lib/api/client', () => ({ api: {} }));

import SaveStatusLed from './SaveStatusLed.svelte';
import { saveStatus } from '$lib/stores/saveStatus';
import { invalidateResource } from '$lib/stores/storeRegistry';

describe('SaveStatusLed', () => {
	// Ключ `saveStatus` публикует SaveCoordinator на каждом переходе, но стор
	// регистрируется только при ИМПОРТЕ модуля. Пока индикатора не было, стор
	// не импортировал никто — registerStore не выполнялся, и событие уходило
	// в никуда (F358).
	//
	// ГЛАВНЫЙ сторож F358: стор обязан импортировать САМО ПРИЛОЖЕНИЕ, а не
	// только этот тест. Импорт из теста регистрирует стор и делает любую
	// проверку поведения холостой — проверено мутацией «убрать индикатор из
	// шапки»: она проходила зелёной.
	it('стор импортирует компонент приложения, а не только тест', async () => {
		const files = import.meta.glob('/src/lib/components/**/*.svelte', {
			query: '?raw',
			import: 'default',
			eager: true,
		}) as Record<string, string>;
		const importers = Object.entries(files)
			.filter(([, src]) => /from ['"][^'"]*stores\/saveStatus['"]/.test(src))
			.map(([path]) => path);
		expect(
			importers,
			'ни один компонент не импортирует stores/saveStatus — событие SaveCoordinator уйдёт в никуда (F358)',
		).not.toHaveLength(0);
	});

	// Плюс поведение: подсказка обязана дойти до стора и вызвать выборку.
	it('подсказка инвалидации доходит до стора', async () => {
		const spy = vi.fn(async () => ({
			ok: true,
			json: async () => ({ data: { state: 'idle', pendingCount: 0 } }),
		}));
		const prev = globalThis.fetch;
		globalThis.fetch = spy as never;
		const unsub = saveStatus.subscribe(() => {});
		try {
			await new Promise((r) => setTimeout(r, 0));
			spy.mockClear();
			invalidateResource('saveStatus');
			await new Promise((r) => setTimeout(r, 10));
			expect(spy).toHaveBeenCalled();
		} finally {
			unsub();
			globalThis.fetch = prev;
		}
	});

	// «Всё сохранено» — состояние покоя: постоянно горящая точка в шапке
	// сообщала бы ни о чём.
	it('в состоянии idle ничего не рисует', () => {
		saveStatus.applyMutationResponse({ state: 'idle', pendingCount: 0 } as never);
		const { container } = render(SaveStatusLed);
		expect(container.querySelector('.save-led')).toBeNull();
	});

	it('при несохранённых правках показывает точку с подсказкой', async () => {
		saveStatus.applyMutationResponse({ state: 'pending', pendingCount: 3 } as never);
		const { container } = render(SaveStatusLed);
		await new Promise((r) => setTimeout(r, 0));
		const led = container.querySelector('.save-led');
		expect(led).not.toBeNull();
		expect(led?.getAttribute('title')).toContain('не сохранена');
	});

	it('исчерпанные повторы показывает как ошибку', async () => {
		saveStatus.applyMutationResponse({
			state: 'failed',
			pendingCount: 1,
			lastError: 'boom',
		} as never);
		const { container } = render(SaveStatusLed);
		await new Promise((r) => setTimeout(r, 0));
		const led = container.querySelector('.save-led');
		expect(led?.getAttribute('title')).toContain('пропадут при перезагрузке');
		expect(led?.getAttribute('title')).toContain('boom');
	});
});
