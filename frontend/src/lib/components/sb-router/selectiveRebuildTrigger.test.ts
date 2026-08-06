import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { triggerSelectiveRebuildManual } from './selectiveRebuildTrigger';
import { selectiveBypass } from '$lib/stores/selectiveBypass';
import { notifications } from '$lib/stores/notifications';
import type { SelectiveStatus } from '$lib/types';

const { rebuild } = vi.hoisted(() => ({ rebuild: vi.fn() }));
vi.mock('$lib/api/client', async () => {
	const { ApiGatewayError } = await import('$lib/api/clientCore');
	return { api: { singboxRouterSelectiveRebuild: rebuild }, ApiGatewayError };
});

function status(patch: Partial<SelectiveStatus> = {}): SelectiveStatus {
	return {
		available: true,
		xtSetAvailable: true,
		conntrackAvailable: true,
		installing: false,
		enabled: true,
		entryCount: 0,
		...patch,
	};
}

describe('triggerSelectiveRebuildManual', () => {
	beforeEach(() => {
		rebuild.mockReset();
		notifications.clearAll();
		selectiveBypass.clearModalRequest();
		selectiveBypass.applyStatus(status({ entryCount: 1 }));
	});
	afterEach(() => {
		vi.restoreAllMocks();
	});

	function errors(): string[] {
		return get(notifications)
			.filter((n) => n.type === 'error')
			.map((n) => n.message);
	}

	it('просит открыть окно прогресса и сбрасывает прежний прогресс', async () => {
		selectiveBypass.applyProgress({ phase: 'done', message: 'старое', current: 1, total: 1 });
		rebuild.mockResolvedValue(status({ rebuilding: true }));
		await triggerSelectiveRebuildManual();
		expect(get(selectiveBypass.modalRequested)).toBe(true);
		expect(get(selectiveBypass.progress)).toBeNull();
	});

	it('успех: применяет статус из ответа', async () => {
		rebuild.mockResolvedValue(status({ rebuilding: true, entryCount: 42 }));
		expect(await triggerSelectiveRebuildManual()).toBe('started');
		expect(get(selectiveBypass.status)?.entryCount).toBe(42);
		expect(errors()).toEqual([]);
	});

	// Гонка 202 со SSE. Мгновенно упавшая пересборка публикует терминальный
	// статус РАНЬШЕ, чем разрешится 202; применив тело ответа поверх, мы вернули
	// бы rebuilding: true, и кнопка залипла бы в «Пересборка…».
	it('не затирает более свежий статус, прилетевший по SSE во время запроса', async () => {
		rebuild.mockImplementation(async () => {
			selectiveBypass.applyStatus(status({ rebuilding: false, lastError: 'resolve failed' }));
			return status({ rebuilding: true, entryCount: 7 });
		});
		expect(await triggerSelectiveRebuildManual()).toBe('superseded');
		const after = get(selectiveBypass.status);
		expect(after?.rebuilding).toBe(false);
		expect(after?.lastError).toBe('resolve failed');
		expect(after?.entryCount).not.toBe(7);
	});

	// Шлюз не дождался ответа — пересборка идёт на роутере, прогресс доедет по
	// SSE. Тост здесь был бы ложной тревогой.
	it('ошибка шлюза: без тоста, пересборка считается фоновой', async () => {
		const { ApiGatewayError } = await import('$lib/api/clientCore');
		vi.spyOn(console, 'warn').mockImplementation(() => {});
		rebuild.mockRejectedValue(new ApiGatewayError('gateway timeout', 504));
		expect(await triggerSelectiveRebuildManual()).toBe('background');
		expect(errors()).toEqual([]);
	});

	it('409 OPERATION_IN_PROGRESS: честный тост про применение конфигурации', async () => {
		rebuild.mockRejectedValue({ body: { code: 'OPERATION_IN_PROGRESS' } });
		expect(await triggerSelectiveRebuildManual()).toBe('busy');
		expect(errors()).toEqual([
			'Пересборка недоступна: применяется конфигурация sing-box. Повторите позже.',
		]);
	});

	it('прочая ошибка: тост с текстом ошибки', async () => {
		rebuild.mockRejectedValue(new Error('ipset: no such file'));
		expect(await triggerSelectiveRebuildManual()).toBe('failed');
		expect(errors()).toEqual(['Не удалось пересобрать ipset: ipset: no such file']);
	});

	// Кнопка видна только при включённом селективном перехвате, но решение
	// «жать или не жать» принял человек: молчаливый выход, как у
	// triggerSelectiveRebuildIfEnabled, выглядел бы «нажал и ничего».
	it('не проверяет настройки: запрос уходит даже с пустым стором настроек', async () => {
		rebuild.mockResolvedValue(status({ rebuilding: true }));
		await triggerSelectiveRebuildManual();
		expect(rebuild).toHaveBeenCalledTimes(1);
	});
});
