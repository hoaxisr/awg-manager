import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { startVisiblePoll } from './visiblePoll';

function setVisibility(state: 'visible' | 'hidden') {
	Object.defineProperty(document, 'visibilityState', { value: state, configurable: true });
	document.dispatchEvent(new Event('visibilitychange'));
}

describe('startVisiblePoll', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
	});
	afterEach(() => vi.useRealTimers());

	it('опрашивает, пока вкладка видима', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 1000);
		expect(tick).toHaveBeenCalledTimes(1); // стартовый

		await vi.advanceTimersByTimeAsync(2000);
		expect(tick).toHaveBeenCalledTimes(3);
		stop();
	});

	// Половина паттерна: в фоне не спрашиваем.
	it('молчит в фоновой вкладке', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 1000);
		tick.mockClear();

		setVisibility('hidden');
		tick.mockClear(); // событие видимости при hidden ничего не делает
		await vi.advanceTimersByTimeAsync(5000);
		expect(tick).toHaveBeenCalledTimes(0);
		stop();
	});

	// Вторая половина, без которой пользователь возвращается к устаревшим данным.
	it('догоняет при возврате на вкладку', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 10_000);
		tick.mockClear();

		setVisibility('hidden');
		await vi.advanceTimersByTimeAsync(5000);
		expect(tick).toHaveBeenCalledTimes(0);

		setVisibility('visible');
		expect(tick).toHaveBeenCalledTimes(1);
		stop();
	});

	it('останавливается и снимает обработчик', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 1000);
		stop();
		tick.mockClear();

		await vi.advanceTimersByTimeAsync(5000);
		setVisibility('visible');
		expect(tick).toHaveBeenCalledTimes(0);
	});
});
