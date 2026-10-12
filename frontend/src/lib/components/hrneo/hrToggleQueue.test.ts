import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { DnsRoute } from '$lib/types';
import { createHrToggleQueue } from './hrToggleQueue';

const list = (rev: string): DnsRoute[] =>
	[{ id: `hr:${rev}:P:0` }, { id: `hr:${rev}:P:1` }] as DnsRoute[];

describe('createHrToggleQueue', () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => vi.useRealTimers());

	it('два переключения в окне debounce уходят по очереди со свежей ревизией', async () => {
		const calls: string[] = [];
		let rev = 1;
		const setEnabled = vi.fn(async (id: string) => {
			calls.push(id);
			rev += 1;
			return list(`r${rev}`);
		});
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError: () => {}, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		q.toggle('hr:r1:P:1', false);
		await vi.advanceTimersByTimeAsync(600);
		expect(calls).toEqual(['hr:r1:P:0', 'hr:r2:P:1']);
	});

	it('повторное переключение той же группы — один запрос с последним значением', async () => {
		const setEnabled = vi.fn(async () => list('r2'));
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError: () => {}, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		q.toggle('hr:r1:P:0', true);
		await vi.advanceTimersByTimeAsync(600);
		expect(setEnabled).toHaveBeenCalledTimes(1);
		expect(setEnabled).toHaveBeenCalledWith('hr:r1:P:0', true);
	});

	it('переключение во время запроса не теряется', async () => {
		let release!: () => void;
		const calls: Array<[string, boolean]> = [];
		const setEnabled = vi.fn(async (id: string, enabled: boolean) => {
			calls.push([id, enabled]);
			if (calls.length === 1) await new Promise<void>((r) => (release = r));
			return list(`r${calls.length + 1}`);
		});
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError: () => {}, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		await vi.advanceTimersByTimeAsync(600); // первый запрос висит
		q.toggle('hr:r1:P:0', true); // передумали, пока шёл запрос
		await vi.advanceTimersByTimeAsync(600);
		release();
		await vi.advanceTimersByTimeAsync(600);
		expect(calls.map((c) => c[1])).toEqual([false, true]);
		expect(q.pending()).toEqual({});
	});

	it('третья группа с ревизией из ответа на первый запрос уходит с последней ревизией', async () => {
		let release!: () => void;
		const calls: string[] = [];
		const setEnabled = vi.fn(async (id: string) => {
			calls.push(id);
			if (calls.length === 2) await new Promise<void>((r) => (release = r));
			return list(`r${calls.length + 1}`);
		});
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError: () => {}, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		q.toggle('hr:r1:P:1', false);
		await vi.advanceTimersByTimeAsync(600); // A ушёл (→ r2), B висит
		q.toggle('hr:r2:P:2', false); // C взят из списка, обновлённого после A
		release(); // B → r3
		await vi.advanceTimersByTimeAsync(600);
		expect(calls).toEqual(['hr:r1:P:0', 'hr:r2:P:1', 'hr:r3:P:2']);
	});

	it('ошибка обрывает очередь и сбрасывает оптимистичное состояние', async () => {
		const onError = vi.fn();
		const setEnabled = vi.fn(async () => {
			throw new Error('409');
		});
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		q.toggle('hr:r1:P:1', false);
		expect(Object.keys(q.pending())).toHaveLength(2);
		await vi.advanceTimersByTimeAsync(600);
		expect(setEnabled).toHaveBeenCalledTimes(1);
		expect(onError).toHaveBeenCalledTimes(1);
		expect(q.pending()).toEqual({});
	});

	it('после dispose идущий сброс не зовёт onApplied/onError и не шлёт следующие', async () => {
		let release!: () => void;
		const onApplied = vi.fn();
		const onError = vi.fn();
		const setEnabled = vi.fn(async () => {
			await new Promise<void>((r) => (release = r));
			return list('r2');
		});
		const q = createHrToggleQueue({ setEnabled, onApplied, onError, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		q.toggle('hr:r1:P:1', false);
		await vi.advanceTimersByTimeAsync(600); // первый запрос висит
		q.dispose(); // вкладку закрыли
		release();
		await vi.advanceTimersByTimeAsync(600);
		expect(setEnabled).toHaveBeenCalledTimes(1);
		expect(onApplied).not.toHaveBeenCalled();
		expect(onError).not.toHaveBeenCalled();
	});

	it('после dispose ошибка идущего запроса не доходит до onError', async () => {
		let fail!: () => void;
		const onError = vi.fn();
		const setEnabled = vi.fn(
			() => new Promise<DnsRoute[]>((_, reject) => (fail = () => reject(new Error('409')))),
		);
		const q = createHrToggleQueue({ setEnabled, onApplied: () => {}, onError, debounceMs: 600 });
		q.toggle('hr:r1:P:0', false);
		await vi.advanceTimersByTimeAsync(600);
		q.dispose();
		fail();
		await vi.advanceTimersByTimeAsync(600);
		expect(onError).not.toHaveBeenCalled();
	});
});
