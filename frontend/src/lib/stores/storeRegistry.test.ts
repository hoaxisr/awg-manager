import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LISTENER_COALESCE_MS, invalidateAll, invalidateResource, onResourceInvalidated } from './storeRegistry';

describe('onResourceInvalidated', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it('склеивает пачку подсказок в одно перечитывание с последней причиной', () => {
		const fn = vi.fn();
		const unsub = onResourceInvalidated('firc', fn);
		invalidateResource('firc', 'a');
		invalidateResource('firc', 'b');
		invalidateResource('firc', 'c');
		expect(fn).not.toHaveBeenCalled();
		vi.advanceTimersByTime(LISTENER_COALESCE_MS);
		expect(fn).toHaveBeenCalledTimes(1);
		expect(fn).toHaveBeenCalledWith('c');

		invalidateResource('firc', 'd');
		vi.advanceTimersByTime(LISTENER_COALESCE_MS);
		expect(fn).toHaveBeenCalledTimes(2);
		unsub();
	});

	it('не зовёт отписавшегося и чужой ключ', () => {
		const fn = vi.fn();
		const unsub = onResourceInvalidated('uplink', fn);
		invalidateResource('firc', 'x');
		vi.advanceTimersByTime(LISTENER_COALESCE_MS);
		expect(fn).not.toHaveBeenCalled();
		unsub();
		invalidateResource('uplink', 'y');
		vi.advanceTimersByTime(LISTENER_COALESCE_MS);
		expect(fn).not.toHaveBeenCalled();
	});

	it('invalidateAll доходит до подписчиков-функций', () => {
		const fn = vi.fn();
		const unsub = onResourceInvalidated('servers.ingress', fn);
		invalidateAll();
		vi.advanceTimersByTime(LISTENER_COALESCE_MS);
		expect(fn).toHaveBeenCalledTimes(1);
		unsub();
	});
});
