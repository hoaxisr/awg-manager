import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { applyEngineSettings } from './engineSettings';
import { notifications } from '$lib/stores/notifications';
import type { SingboxRouterSettings } from '$lib/types';

const { getSettings, putSettings, status } = vi.hoisted(() => ({
	getSettings: vi.fn(async () => ({ routingMode: 'tproxy' }) as SingboxRouterSettings),
	putSettings: vi.fn(async (_next: SingboxRouterSettings) => ({})),
	status: vi.fn(async () => ({ enabled: true, active: true })),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterGetSettings: getSettings,
		singboxRouterPutSettings: putSettings,
		singboxRouterStatus: status,
	},
	ApiGatewayError: class ApiGatewayError extends Error {},
}));

describe('applyEngineSettings', () => {
	beforeEach(() => {
		notifications.clearAll();
		getSettings.mockClear().mockResolvedValue({ routingMode: 'tproxy' } as SingboxRouterSettings);
		putSettings.mockClear().mockResolvedValue({});
	});

	function errors(): string[] {
		return get(notifications)
			.filter((n) => n.type === 'error')
			.map((n) => n.message);
	}

	// База для merge — свежий GET, а не стор: PUT уносит полный объект, и эхо
	// устаревшего стора молча откатывало бы чужие правки.
	it('мержит патч поверх свежих настроек с сервера', async () => {
		getSettings.mockResolvedValue({
			routingMode: 'tproxy',
			bypassExtraPorts: '123 UDP',
		} as SingboxRouterSettings);
		expect(await applyEngineSettings({ selectiveBypass: true })).toBe(true);
		expect(putSettings).toHaveBeenCalledWith({
			routingMode: 'tproxy',
			bypassExtraPorts: '123 UDP',
			selectiveBypass: true,
		});
		expect(errors()).toEqual([]);
	});

	// Падение PUT (409 «применяется конфигурация», обрыв сети) НЕ должно быть
	// молчаливым: чип или тумблер откатится сам, и без тоста пользователь
	// решит, что просто не попал по кнопке.
	it('провал сохранения объясняется тостом с текстом ошибки', async () => {
		putSettings.mockRejectedValue(new Error('операция уже выполняется'));
		expect(await applyEngineSettings({ selectiveBypass: true })).toBe(false);
		expect(errors()).toEqual(['Не удалось сохранить: операция уже выполняется']);
	});

	it('провал чтения настроек тоже объясняется тостом', async () => {
		getSettings.mockRejectedValue(new Error('connection reset'));
		expect(await applyEngineSettings({ selectiveBypass: true })).toBe(false);
		expect(errors()).toEqual(['Не удалось сохранить: connection reset']);
		expect(putSettings).not.toHaveBeenCalled();
	});

	// Не-Error отказ (строка, объект с body.code) не должен превращаться в
	// «[object Object]» без текста.
	it('не-Error отказ приводится к строке', async () => {
		putSettings.mockRejectedValue('OPERATION_IN_PROGRESS');
		expect(await applyEngineSettings({ selectiveBypass: true })).toBe(false);
		expect(errors()).toEqual(['Не удалось сохранить: OPERATION_IN_PROGRESS']);
	});
});
