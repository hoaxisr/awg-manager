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

	it('молчит в фоновой вкладке', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 1000);
		tick.mockClear();

		setVisibility('hidden');
		tick.mockClear();
		await vi.advanceTimersByTimeAsync(5000);
		expect(tick).toHaveBeenCalledTimes(0);
		stop();
	});

	it('догоняет при возврате, если период уже прошёл', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 10_000);
		tick.mockClear();

		setVisibility('hidden');
		await vi.advanceTimersByTimeAsync(11_000);
		expect(tick).toHaveBeenCalledTimes(0);

		setVisibility('visible');
		expect(tick).toHaveBeenCalledTimes(1);
		stop();
	});

	// Без этой защиты частое переключение вкладок даёт запрос на КАЖДЫЙ фокус.
	// Для дорогих ручек (полный разбор conntrack) это выходит дороже таймера,
	// который хелпер и заменяет.
	it('не догоняет на быстром переключении вкладок', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 30_000);
		tick.mockClear();

		for (let i = 0; i < 5; i++) {
			setVisibility('hidden');
			await vi.advanceTimersByTimeAsync(500);
			setVisibility('visible');
			await vi.advanceTimersByTimeAsync(500);
		}
		expect(tick).toHaveBeenCalledTimes(0);
		stop();
	});

	// Догон перефазирует интервал: иначе штатный тик приходил бы сразу следом.
	it('перефазирует интервал после догона', async () => {
		const tick = vi.fn();
		const stop = startVisiblePoll(tick, 10_000);
		await vi.advanceTimersByTimeAsync(9_000);
		tick.mockClear();

		setVisibility('hidden');
		await vi.advanceTimersByTimeAsync(2_000); // период истёк
		setVisibility('visible');
		expect(tick).toHaveBeenCalledTimes(1); // догон

		await vi.advanceTimersByTimeAsync(1_000); // прежний тик пришёлся бы сюда
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
