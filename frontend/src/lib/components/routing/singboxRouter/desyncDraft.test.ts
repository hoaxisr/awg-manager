import { describe, expect, it } from 'vitest';
import { desyncDraftFrom, desyncFieldsFrom, shortStrategy } from './desyncDraft';

describe('desyncDraft', () => {
	it('new outbound defaults to preset auto only', () => {
		expect(desyncFieldsFrom(desyncDraftFrom())).toEqual({ preset: 'auto' });
	});

	it('round-trips fields, keeps explicit auto_tune false, drops empties', () => {
		const src = {
			preset: 'fake_split',
			fake_sni: 'example.org',
			fake_sni_pool: ['a.ru', 'b.ru'],
			ttl: 4,
			fooling: 'badseq',
			auto_tune: false,
			adaptive: { max_attempts: 3, ttl: '24h' },
			strategy: 'seqovl=1',
		};
		expect(desyncFieldsFrom(desyncDraftFrom(src))).toEqual(src);
	});

	it('rejects non-integer numbers', () => {
		const d = desyncDraftFrom();
		d.ttl = '3.5';
		expect(() => desyncFieldsFrom(d)).toThrow('ttl');
	});
});

describe('shortStrategy', () => {
	it('укорачивает хеш и не трогает прочее', () => {
		expect(shortStrategy('multi_split:0123456789abcdef0123456789abcdef')).toBe('multi_split·012345');
		expect(shortStrategy('split')).toBe('split');
	});
});
