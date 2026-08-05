import { describe, it, expect } from 'vitest';
import { mergedPrefix } from './mergedPrefix';

describe('mergedPrefix', () => {
	it('отдаёт длину системного префикса режима как разницу длин', () => {
		// tproxy со сниффером: sniff + hijack-dns + ip_is_private + route-options
		// перед четырьмя пользовательскими правилами.
		expect(mergedPrefix(8, 4)).toBe(4);
		expect(mergedPrefix(5, 4)).toBe(1);
	});

	it('на холодном сторе молчит, а не объявляет весь трейс системным', () => {
		// Список ещё не загружен. Без гейта сноска сказала бы «7 системных
		// строк» и номер #07 — при том, что системных строк там три.
		expect(mergedPrefix(7, 0)).toBe(0);
	});

	it('не уходит в минус, когда список опередил трассу', () => {
		// Правило добавлено после прогона инспектора: страница уже длиннее.
		expect(mergedPrefix(4, 6)).toBe(0);
	});

	it('без системного префикса нумерация совпадает', () => {
		expect(mergedPrefix(4, 4)).toBe(0);
	});
});
