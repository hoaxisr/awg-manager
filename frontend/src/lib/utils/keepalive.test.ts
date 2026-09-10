import { describe, it, expect } from 'vitest';
import { effectiveKeepalive } from './keepalive';

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
