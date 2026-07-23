import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createSelfReschedulingPoll } from './selfReschedulingPoll';

describe('createSelfReschedulingPoll', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it('start() after stop() is a no-op (disposal-guard, no orphaned timer)', async () => {
		const tick = vi.fn().mockResolvedValue(undefined);
		const poll = createSelfReschedulingPoll(tick, 2000);

		// Component destroyed mid initial load: stop() fires before start().
		poll.stop();
		poll.start();

		await vi.advanceTimersByTimeAsync(10_000);
		expect(tick).not.toHaveBeenCalled();
	});

	it('polls at intervalMs after start(), first tick fires after interval', async () => {
		const tick = vi.fn().mockResolvedValue(undefined);
		const poll = createSelfReschedulingPoll(tick, 2000);

		poll.start();
		expect(tick).toHaveBeenCalledTimes(0); // no immediate tick
		await vi.advanceTimersByTimeAsync(2000);
		expect(tick).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(2000);
		expect(tick).toHaveBeenCalledTimes(2);

		poll.stop();
	});

	it('stop() halts further ticks', async () => {
		const tick = vi.fn().mockResolvedValue(undefined);
		const poll = createSelfReschedulingPoll(tick, 2000);

		poll.start();
		await vi.advanceTimersByTimeAsync(2000);
		expect(tick).toHaveBeenCalledTimes(1);
		poll.stop();
		await vi.advanceTimersByTimeAsync(10_000);
		expect(tick).toHaveBeenCalledTimes(1);
	});
});
