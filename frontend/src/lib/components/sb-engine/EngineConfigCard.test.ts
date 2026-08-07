import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import EngineConfigCard from './EngineConfigCard.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { ConfigSlotInfo, SingboxRouterSettings } from '$lib/types';

const { configSlots, configPreview } = vi.hoisted(() => ({
	configSlots: vi.fn(async () => ({ slots: [] as ConfigSlotInfo[] })),
	configPreview: vi.fn(async () => ({ json: '{}' })),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxConfigSlots: configSlots,
		singboxGetConfigPreview: configPreview,
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

function slot(patch: Partial<ConfigSlotInfo> = {}): ConfigSlotInfo {
	return {
		slot: 'tproxy',
		filename: '20-tproxy.json',
		ownership: 'system',
		enabled: true,
		hasDraft: false,
		size: 1024,
		...patch,
	};
}

describe('EngineConfigCard', () => {
	beforeEach(() => {
		configSlots.mockClear().mockResolvedValue({ slots: [] });
		configPreview.mockClear().mockResolvedValue({ json: '{}' });
		singboxRouter.setSettings(null);
	});

	// ── Кнопки ───────────────────────────────────────────────────────────────

	it('обе кнопки на месте: итоговый JSON и редактор слотов', () => {
		const { container } = render(EngineConfigCard);
		const rows = container.querySelectorAll('.row');
		expect(rows).toHaveLength(2);
		expect(rows[0].querySelector('button')?.textContent?.trim()).toBe('Показать');
		expect(rows[1].querySelector('button')?.textContent?.trim()).toBe('Редактор');
	});

	it('«Показать» открывает шторку итогового конфига', async () => {
		const { getByText, getByRole } = render(EngineConfigCard);
		await fireEvent.click(getByText('Показать'));
		expect(getByRole('dialog', { name: 'Конфиг sing-box' })).toBeTruthy();
		await vi.waitFor(() => expect(configPreview).toHaveBeenCalled());
	});

	it('«Редактор» открывает шторку слотов', async () => {
		const { getByText, getByRole } = render(EngineConfigCard);
		await fireEvent.click(getByText('Редактор'));
		expect(getByRole('dialog', { name: 'Конфигурация sing-box' })).toBeTruthy();
	});

	// Гейт `mode === 'expert'` у кнопки «Редактор» снят спекой (§3): режима
	// больше нет, а от ошибки защищают валидация и черновик. Настройки движка
	// карточке при этом вообще не нужны — она про файлы, а не про поля.
	it('редактор доступен и без загруженных настроек движка', () => {
		const { getByRole } = render(EngineConfigCard);
		expect((getByRole('button', { name: 'Редактор' }) as HTMLButtonElement).disabled).toBe(false);
	});

	it.each(['tproxy', 'policy-tun', 'fakeip-tun'])('в режиме %s карточка та же', (routingMode) => {
		singboxRouter.setSettings({ routingMode } as SingboxRouterSettings);
		const { container } = render(EngineConfigCard);
		expect(container.querySelectorAll('.row')).toHaveLength(2);
	});

	// ── Чипы слотов ──────────────────────────────────────────────────────────

	it('чипы строятся по ответу API, а не по списку в коде', async () => {
		configSlots.mockResolvedValue({
			slots: [
				slot({ slot: 'base', filename: '00-base.json' }),
				slot({ slot: 'tproxy', filename: '20-tproxy.json' }),
				slot({ slot: 'routing', filename: '21-routing.json' }),
			],
		});
		const { container } = render(EngineConfigCard);
		await vi.waitFor(() => expect(container.querySelectorAll('.chip')).toHaveLength(3));
		const names = [...container.querySelectorAll('.chip .chip-name')].map((n) => n.textContent);
		expect(names).toEqual(['00-base', '20-tproxy', '21-routing']);
	});

	it('выключенный слот и слот с черновиком помечены состоянием чипа', async () => {
		configSlots.mockResolvedValue({
			slots: [
				slot({ slot: 'fakeip', filename: '20-fakeip.json', enabled: false }),
				slot({ slot: 'user', filename: '90-user.json', ownership: 'user', hasDraft: true }),
			],
		});
		const { container } = render(EngineConfigCard);
		await vi.waitFor(() => expect(container.querySelectorAll('.chip')).toHaveLength(2));
		const states = [...container.querySelectorAll('.chip')].map((c) => c.getAttribute('data-state'));
		expect(states).toEqual(['off', 'draft']);
	});

	it('слоты запрашиваются ровно один раз на монтаж', async () => {
		render(EngineConfigCard);
		await vi.waitFor(() => expect(configSlots).toHaveBeenCalledTimes(1));
		await new Promise((r) => setTimeout(r, 0));
		expect(configSlots).toHaveBeenCalledTimes(1);
	});

	// В редакторе слот включают, выключают и применяют черновик — устаревшие
	// чипы врали бы ровно про то, что пользователь только что менял.
	it('закрытие редактора обновляет чипы', async () => {
		const { getByText, getByRole, getByLabelText, queryByRole } = render(EngineConfigCard);
		await vi.waitFor(() => expect(configSlots).toHaveBeenCalledTimes(1));
		await fireEvent.click(getByText('Редактор'));
		expect(getByRole('dialog', { name: 'Конфигурация sing-box' })).toBeTruthy();
		const before = configSlots.mock.calls.length;
		await fireEvent.click(getByLabelText('Закрыть'));
		expect(queryByRole('dialog')).toBeNull();
		await vi.waitFor(() => expect(configSlots.mock.calls.length).toBe(before + 1));
	});

	// Подсказка чипа проверена как строка в configSlots.test.ts — здесь ровно
	// то, что тот тест проверить не может: что она доезжает до атрибута.
	it('подсказка чипа попадает в DOM', async () => {
		configSlots.mockResolvedValue({ slots: [slot()] });
		const { container } = render(EngineConfigCard);
		await vi.waitFor(() => expect(container.querySelectorAll('.chip')).toHaveLength(1));
		expect(container.querySelector('.chip')?.getAttribute('title')).toContain('20-tproxy.json');
	});

	// ── «Итоговый конфиг» изнутри шторки слотов ─────────────────────────────

	it('«Итоговый конфиг» из шторки слотов закрывает список и открывает JSON', async () => {
		const { getByText, getByRole, queryByRole } = render(EngineConfigCard);
		await fireEvent.click(getByText('Редактор'));
		await fireEvent.click(getByText('Итоговый конфиг'));
		expect(queryByRole('dialog', { name: 'Конфигурация sing-box' })).toBeNull();
		expect(getByRole('dialog', { name: 'Конфиг sing-box' })).toBeTruthy();
		await vi.waitFor(() => expect(configPreview).toHaveBeenCalled());
	});

	// Тот же инвариант, что и у закрытия крестиком: в редакторе слот включают,
	// выключают и применяют черновик, и путь «К списку → Итоговый конфиг»
	// оставлял бы под шторкой доreload-ные чипы.
	it('«Итоговый конфиг» из шторки слотов обновляет чипы', async () => {
		const { getByText } = render(EngineConfigCard);
		await vi.waitFor(() => expect(configSlots).toHaveBeenCalledTimes(1));
		await fireEvent.click(getByText('Редактор'));
		const before = configSlots.mock.calls.length;
		await fireEvent.click(getByText('Итоговый конфиг'));
		await vi.waitFor(() => expect(configSlots.mock.calls.length).toBe(before + 1));
	});

	// Упавший запрос и пустой ответ — разные состояния: «слотов нет» на месте
	// сетевой ошибки сказало бы, что конфиг пуст, хотя мы про него ничего не знаем.
	it('упавший запрос слотов не роняет кнопки и виден отдельным состоянием', async () => {
		configSlots.mockRejectedValue(new Error('нет связи'));
		const { container } = render(EngineConfigCard);
		await vi.waitFor(() => expect(container.querySelector('[data-slots="failed"]')).toBeTruthy());
		expect(container.querySelectorAll('.row')).toHaveLength(2);
		expect(container.querySelectorAll('.chip')).toHaveLength(0);
	});

	it('пустой ответ — своё состояние, а не «недоступно»', async () => {
		const { container } = render(EngineConfigCard);
		await vi.waitFor(() => expect(container.querySelector('[data-slots="empty"]')).toBeTruthy());
		expect(container.querySelector('[data-slots="failed"]')).toBeNull();
	});
});
