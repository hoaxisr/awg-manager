import { describe, it, expect } from 'vitest';
import type { ASCParams } from '$lib/types';
import { asc3ConfLines, headerProtectionKey } from './asc3Conf';

const asc = (o: Record<string, unknown>) => o as unknown as ASCParams;

describe('asc3ConfLines', () => {
	// ASC сервера 3.1 в форме NDMS (F685): rc отдаёт числа, HP — строкой.
	it('пишет заданные параметры 3.x грамматикой .conf', () => {
		const p = asc({
			jc: 0, 'header-protection-key': 'K=',
			'rekey-after-time-start': 120, 'rekey-after-time-end': 120,
			'content-padding-addition-start': 0, 'content-padding-addition-end': 80,
			'keepalive-timeout-start': 0, 'keepalive-timeout-end': 0,
			'random-trailers': 1, 'disable-cookies': 0,
		});
		expect(asc3ConfLines(p)).toEqual([
			'HeaderProtectionKey = K=',
			'ContentPaddingAddition = 0-80',
			'RekeyAfterTime = 120',
			'RandomTrailers = on',
		]);
		expect(headerProtectionKey(p)).toBe('K=');
	});

	it('принимает числа строками', () => {
		expect(asc3ConfLines(asc({ 'random-trailers': '1', 'disable-cookies': '1' }))).toEqual([
			'RandomTrailers = on',
			'DisableCookies = on',
		]);
	});

	it('у ASC 2.0 и без ASC — ни строки', () => {
		expect(asc3ConfLines(asc({ jc: 4, s1: 16, h1: '1' }))).toEqual([]);
		expect(asc3ConfLines(null)).toEqual([]);
		expect(headerProtectionKey(null)).toBe('');
	});
});
