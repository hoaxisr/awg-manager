import { describe, it, expect } from 'vitest';
import { effectiveKeepalive, keepaliveHint } from './keepalive';

// Таблица обязана совпадать с TestKeepaliveEffective
// (internal/storage/keepalive_test.go): подпись в карточке считает то же
// значение, что бэкенд отправит на прошивку.
describe('effectiveKeepalive', () => {
	const table: Array<[string, number | null]> = [
		['25', 25],
		['25-35', 25],
		[' 25 - 35 ', 25],
		['65535', 65535],
		['', null],
		['0', null],
		[' 0 ', null],
		['0-80', null],
		['65536', null],
		['70000-80000', null],
		['abc', null],
		['-5', null],
		['22-', 22],
		// U+FEFF: JS trim() его снимает, Go strings.TrimSpace — нет. Без
		// явного отказа карточка показала бы «применяется 25 с» там, где
		// бэкенд отвергает правку целиком.
		['\ufeff25', null],
		['25\ufeff', null],
	];

	for (const [raw, want] of table) {
		it(`${JSON.stringify(raw)} → ${want}`, () => {
			expect(effectiveKeepalive(raw)).toBe(want);
		});
	}

	// Одиночное значение приходит из API числом (Keepalive.MarshalJSON).
	it('принимает число и пустые значения из API', () => {
		expect(effectiveKeepalive(25)).toBe(25);
		expect(effectiveKeepalive(0)).toBe(null);
		expect(effectiveKeepalive(null)).toBe(null);
		expect(effectiveKeepalive(undefined)).toBe(null);
	});
});

// Подпись «применяется N с» имеет смысл только там, где прошивка получает
// число: NativeWG. Kernel понимает диапазон целиком, и подпись там врала бы.
describe('keepaliveHint', () => {
	const table: Array<[string | null | undefined, string, number | null]> = [
		['nativewg', '25-35', 25],
		['kernel', '25-35', null],
		['nativewg', '25', null],
		['kernel', '25', null],
		[undefined, '25-35', null],
		['', '25-35', null],
		// Диапазон, который прошивке не отправить: показывать нечего.
		['nativewg', '70000-80000', null],
	];

	for (const [backend, raw, want] of table) {
		it(`${JSON.stringify(backend)} + ${JSON.stringify(raw)} → ${want}`, () => {
			expect(keepaliveHint(backend, raw)).toBe(want);
		});
	}
});
