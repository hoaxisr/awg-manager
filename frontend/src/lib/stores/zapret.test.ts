import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ZapretStatus } from '$lib/types';

const mocks = vi.hoisted(() => ({ getStatus: vi.fn(), control: vi.fn(), success: vi.fn(), error: vi.fn() }));

vi.mock('$lib/api/client', () => ({
	api: {
		getZapretStatus: mocks.getStatus,
		controlZapret: mocks.control,
	},
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: mocks.success, error: mocks.error },
}));

import { invalidateResource } from './storeRegistry';
import { invalidateAllRouting } from './routing';
import { settings } from './settings';
import { zapretStatus, zapretStatusIfExpert } from './zapret';
import type { Settings } from '$lib/types';
import { runZapretControl } from '$lib/components/zapret/control';

function status(installed: boolean, running = false): ZapretStatus {
	return {
		installed,
		running,
		processState: installed ? (running ? 'running' : 'stopped') : 'not_installed',
		strategyCount: 0,
		hostlistCount: 0,
		hasUserHostlist: false,
	};
}

async function flushPromises() {
	await Promise.resolve();
	await Promise.resolve();
}

beforeEach(() => {
	vi.useFakeTimers();
	vi.clearAllMocks();
	mocks.getStatus.mockResolvedValue(status(false));
});

afterEach(() => {
	vi.useRealTimers();
});

describe('Zapret status store', () => {
	it('fetches on subscribe and refreshes on SSE invalidation without a polling timer', async () => {
		const unsubscribe = zapretStatus.subscribe(() => {});
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);
		expect(zapretStatus.peek().data).toEqual(status(false));

		await vi.advanceTimersByTimeAsync(90_000);
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);

		const installed = status(true, true);
		mocks.getStatus.mockResolvedValueOnce(installed);
		invalidateResource('routing.zapretStatus');
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(2);
		expect(zapretStatus.peek().data).toEqual(installed);

		// Кнопка «Обновить» в Маршрутизации перечитывает и статус Zapret.
		invalidateAllRouting();
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(3);
		unsubscribe();
	});
});

describe('Zapret control action', () => {
	it('updates the shared store and reports the action result', async () => {
		const fresh = status(true, true);
		mocks.control.mockResolvedValue(fresh);

		await expect(runZapretControl('start', '/opt/zapret22')).resolves.toEqual(fresh);
		expect(mocks.control).toHaveBeenCalledWith('start', '/opt/zapret22');
		expect(zapretStatus.peek().data).toEqual(fresh);
		expect(mocks.success).toHaveBeenCalledOnce();
	});

	it('invalidates cached status and reports a control failure', async () => {
		mocks.control.mockRejectedValue(new Error('control failed'));

		await expect(runZapretControl('stop', '/opt/zapret22')).resolves.toBeNull();
		expect(mocks.error).toHaveBeenCalledWith('control failed');
		expect(zapretStatus.peek().lastFetchedAt).toBe(0);
	});
});

describe('Zapret status for shared pages', () => {
	it('does not request status below expert and starts once the level is expert', async () => {
		zapretStatus.invalidate();
		settings.set({ usageLevel: 'advanced' } as Settings);
		const seen: unknown[] = [];
		const unsubscribe = zapretStatusIfExpert.subscribe((v) => seen.push(v));
		await flushPromises();
		expect(mocks.getStatus).not.toHaveBeenCalled();
		expect(seen.at(-1)).toBeNull();

		settings.set({ usageLevel: 'expert' } as Settings);
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);
		unsubscribe();
		settings.set(null);
	});
});
