import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { applyEngineSettings, engineSaveState, resetEngineSaveState } from './engineSettings';
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
		resetEngineSaveState();
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

// E08 инвентаря волны: на странице больше десятка полей с автосохранением и ни
// одной кнопки «Сохранить». Без индикатора пользователь не знает, доехала ли
// правка; тост показывается только на ошибке.
describe('engineSaveState', () => {
	beforeEach(() => {
		notifications.clearAll();
		resetEngineSaveState();
		getSettings.mockClear().mockResolvedValue({ routingMode: 'tproxy' } as SingboxRouterSettings);
		putSettings.mockClear().mockResolvedValue({});
	});

	// Шторка рисовала «✓ Сохранено» сразу на открытии — утверждение о
	// сохранении, которого не было.
	it('до первого сохранения молчит', () => {
		expect(get(engineSaveState)).toBe('idle');
	});

	it('во время сохранения — saving, после успеха — saved', async () => {
		let release: () => void = () => {};
		putSettings.mockImplementation(
			() => new Promise<object>((resolve) => (release = () => resolve({}))),
		);
		const p = applyEngineSettings({ snifferEnabled: false });
		// Ждём, пока обёртка дойдёт до PUT: GET перед ним тоже асинхронный.
		await vi.waitFor(() => expect(putSettings).toHaveBeenCalled());
		expect(get(engineSaveState)).toBe('saving');
		release();
		await p;
		expect(get(engineSaveState)).toBe('saved');
	});

	it('провал сохранения оставляет error', async () => {
		putSettings.mockRejectedValue(new Error('boom'));
		await applyEngineSettings({ snifferEnabled: false });
		expect(get(engineSaveState)).toBe('error');
	});

	// Параллельные сохранения: очередь QoS и blur соседнего поля идут внахлёст.
	// «Сохранено» обязано наступить по последнему, иначе быстрый успех гасит
	// индикатор ещё идущего сохранения.
	it('saving держится, пока не закончилось последнее сохранение', async () => {
		const releases: Array<() => void> = [];
		putSettings.mockImplementation(
			() => new Promise<object>((resolve) => releases.push(() => resolve({}))),
		);
		const a = applyEngineSettings({ snifferEnabled: false });
		const b = applyEngineSettings({ udpTimeout: '10m0s' });
		await vi.waitFor(() => expect(releases.length).toBe(2));
		releases[0]();
		await a;
		expect(get(engineSaveState)).toBe('saving');
		releases[1]();
		await b;
		expect(get(engineSaveState)).toBe('saved');
	});

	// Ошибка внутри пачки липкая: успех соседнего поля не должен стирать
	// единственный признак того, что что-то не доехало.
	it('успех соседа не стирает ошибку той же пачки', async () => {
		const calls: Array<{ ok: () => void; fail: () => void }> = [];
		putSettings.mockImplementation(
			() =>
				new Promise<object>((resolve, reject) => {
					calls.push({ ok: () => resolve({}), fail: () => reject(new Error('boom')) });
				}),
		);
		const a = applyEngineSettings({ snifferEnabled: false });
		const b = applyEngineSettings({ udpTimeout: '10m0s' });
		await vi.waitFor(() => expect(calls.length).toBe(2));
		calls[0].fail();
		await a;
		calls[1].ok();
		await b;
		expect(get(engineSaveState)).toBe('error');
	});

	// Новая пачка начинается с чистого листа: иначе индикатор навсегда застревал
	// бы в «Ошибка сохранения» после одного неудачного сохранения.
	it('следующее удачное сохранение снимает ошибку', async () => {
		putSettings.mockRejectedValueOnce(new Error('boom'));
		await applyEngineSettings({ snifferEnabled: false });
		expect(get(engineSaveState)).toBe('error');
		await applyEngineSettings({ snifferEnabled: true });
		expect(get(engineSaveState)).toBe('saved');
	});
});
