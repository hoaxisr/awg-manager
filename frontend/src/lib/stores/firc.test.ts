import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';

const { status, tunnels, groups, states } = vi.hoisted(() => ({ status: vi.fn(), tunnels: vi.fn(), groups: vi.fn(), states: vi.fn() }));
vi.mock('$lib/api/client', () => ({
	api: { fircGetStatus: status, fircGetTunnels: tunnels, fircGetGroups: groups, fircGetTunnelsState: states },
}));

import { acquireFircStatusWatch, fircForeign, fircStatus, reloadFircAll } from './firc';

const own = { running: true, selected: true } as never;
const foreign = { running: false, selected: true, foreign: { package: 'firc' } } as never;

function setVisible(v: 'visible' | 'hidden') {
	Object.defineProperty(document, 'visibilityState', { value: v, configurable: true });
	document.dispatchEvent(new Event('visibilitychange'));
}

let release: (() => void) | undefined;
beforeEach(() => {
	vi.useFakeTimers();
	vi.clearAllMocks();
	tunnels.mockResolvedValue([]);
	groups.mockResolvedValue([]);
	states.mockResolvedValue([]);
	fircStatus.set(null);
});
afterEach(() => {
	release?.();
	release = undefined;
	vi.useRealTimers();
});

describe('fircForeign', () => {
	it('«не загружено» и «стороннего нет» — разные значения', () => {
		expect(get(fircForeign)).toBeUndefined();
		fircStatus.set(own);
		expect(get(fircForeign)).toBeNull();
		fircStatus.set(foreign);
		expect(get(fircForeign)).toEqual({ package: 'firc' });
	});

	it('статус ставится и при отказе списков: без него раздел так и ждал бы', async () => {
		status.mockResolvedValue(foreign);
		tunnels.mockRejectedValue(new Error('x'));
		await expect(reloadFircAll()).rejects.toThrow('x');
		expect(get(fircForeign)).toEqual({ package: 'firc' });
	});
});

describe('наблюдение за сторонним firc', () => {
	it('в обычной работе таймер не спрашивает, возврат к вкладке — спрашивает', async () => {
		fircStatus.set(own);
		status.mockResolvedValue(own);
		release = acquireFircStatusWatch();
		await vi.advanceTimersByTimeAsync(120_000);
		expect(status).not.toHaveBeenCalled();
		setVisible('hidden');
		setVisible('visible');
		await vi.advanceTimersByTimeAsync(0);
		expect(status).toHaveBeenCalledTimes(1);
		setVisible('visible'); // частые переключения не множат запросы
		await vi.advanceTimersByTimeAsync(0);
		expect(status).toHaveBeenCalledTimes(1);
	});

	it('при стороннем спрашивает редко и замечает его уход — перечитывая всё', async () => {
		fircStatus.set(foreign);
		status.mockResolvedValue(foreign);
		release = acquireFircStatusWatch();
		const second = acquireFircStatusWatch(); // второй держатель своего таймера не заводит
		await vi.advanceTimersByTimeAsync(30_500);
		expect(status.mock.calls.length).toBe(3);
		status.mockResolvedValue(own);
		await vi.advanceTimersByTimeAsync(15_000);
		expect(get(fircForeign)).toBeNull();
		expect(tunnels).toHaveBeenCalled();
		const n = status.mock.calls.length;
		await vi.advanceTimersByTimeAsync(60_000);
		expect(status.mock.calls.length).toBe(n);
		second();
	});

	it('без держателей не спрашивает', async () => {
		status.mockResolvedValue(foreign);
		fircStatus.set(foreign);
		acquireFircStatusWatch()();
		await vi.advanceTimersByTimeAsync(1);
		status.mockClear();
		await vi.advanceTimersByTimeAsync(60_000);
		setVisible('visible');
		await vi.advanceTimersByTimeAsync(0);
		expect(status).not.toHaveBeenCalled();
	});

	it('незагруженный статус дозапрашивает', async () => {
		status.mockRejectedValueOnce(new Error('сеть')).mockResolvedValue(own);
		release = acquireFircStatusWatch();
		await vi.advanceTimersByTimeAsync(16_000);
		expect(get(fircForeign)).toBeNull();
	});
});
