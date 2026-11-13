import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AutolistStatus } from '$lib/types';

const mocks = vi.hoisted(() => ({ getStatus: vi.fn() }));

vi.mock('$lib/api/client', () => ({
	api: {
		getAutolistStatus: mocks.getStatus,
	},
}));

import { invalidateResource } from './storeRegistry';
import { settings } from './settings';
import { autolistStatus, autolistStatusIfExpert } from './autolist';

function status(running: boolean): AutolistStatus {
	return {
		running,
		installed: true,
		enabled: true,
		captureActive: running,
		capturePaused: false,
		activeChecks: 0,
		queuedChecks: 0,
		addsUsedHour: 0,
		addsLimit: 50,
		candidates: [],
		history: [],
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

describe('Autolist status store', () => {
	it('fetches on subscribe and keeps polling without SSE invalidations', async () => {
		autolistStatus.invalidate();
		const unsubscribe = autolistStatus.subscribe(() => {});
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);
		expect(autolistStatus.peek().data).toEqual(status(false));

		await vi.advanceTimersByTimeAsync(90_000);
		expect(mocks.getStatus.mock.calls.length).toBeGreaterThanOrEqual(2);

		unsubscribe();
	});

	it('refetches after invalidate() while subscribed', async () => {
		autolistStatus.invalidate();
		const unsubscribe = autolistStatus.subscribe(() => {});
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);

		mocks.getStatus.mockResolvedValueOnce(status(true));
		invalidateResource('routing.autolistStatus');
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(2);
		expect(autolistStatus.peek().data?.running).toBe(true);

		unsubscribe();
	});
});

describe('Autolist status for the routing page', () => {
	it('does not request status below expert and starts once the level is expert', async () => {
		autolistStatus.invalidate();
		settings.set({ usageLevel: 'advanced' } as never);
		const seen: unknown[] = [];
		const unsubscribe = autolistStatusIfExpert.subscribe((v) => seen.push(v));
		await flushPromises();
		expect(mocks.getStatus).not.toHaveBeenCalled();
		expect(seen.at(-1)).toBeNull();

		settings.set({ usageLevel: 'expert' } as never);
		await flushPromises();
		expect(mocks.getStatus).toHaveBeenCalledTimes(1);
		unsubscribe();
		settings.set(null);
	});
});
