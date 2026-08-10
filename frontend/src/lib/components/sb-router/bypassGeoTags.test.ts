import { describe, it, expect } from 'vitest';
import { aggregateGeoIPTags, sumSelectedTags, BYPASS_SET_MAX_ELEM } from './bypassGeoTags';

describe('aggregateGeoIPTags', () => {
	it('суммирует одноимённые теги разных файлов и нормализует регистр', () => {
		const out = aggregateGeoIPTags([
			[
				{ name: 'RU', count: 10 },
				{ name: 'us', count: 3 },
			],
			[{ name: 'ru', count: 5 }],
		]);
		expect(out).toEqual([
			{ name: 'ru', count: 15 },
			{ name: 'us', count: 3 },
		]);
	});

	it('пропускает пустые имена', () => {
		expect(aggregateGeoIPTags([[{ name: '  ', count: 7 }]])).toEqual([]);
	});
});

describe('sumSelectedTags', () => {
	const options = [
		{ name: 'ru', count: 100 },
		{ name: 'cn', count: 200 },
	];

	it('складывает счётчики выбранных тегов', () => {
		expect(sumSelectedTags(['ru', 'CN'], options)).toBe(300);
	});

	it('неизвестный тег считается нулём', () => {
		expect(sumSelectedTags(['ru', 'zz'], options)).toBe(100);
	});

	it('предел набора — 262144', () => {
		expect(BYPASS_SET_MAX_ELEM).toBe(262144);
	});
});
