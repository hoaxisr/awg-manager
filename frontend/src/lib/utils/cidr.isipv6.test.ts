import { describe, it, expect } from 'vitest';
import { isIPv6 } from './cidr';

describe('isIPv6', () => {
	it('принимает литеральные адреса', () => {
		for (const v of ['::1', '::', '2001:4860:4860::8888', 'fe80::1', 'fd00:0:0:0:0:0:0:1']) {
			expect(isIPv6(v), v).toBe(true);
		}
	});

	it('отвергает то, что принимала наивная проверка по двоеточию', () => {
		for (const v of ['8.8.8.8:53', 'a:b', 'http://evil:80', ':', '1:2:3', 'gggg::1', '1::2::3']) {
			expect(isIPv6(v), v).toBe(false);
		}
	});
});
