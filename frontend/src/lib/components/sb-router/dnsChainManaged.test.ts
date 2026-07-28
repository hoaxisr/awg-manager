import { describe, expect, it } from 'vitest';
import { isManagedDnsChainRule } from './dnsChainManaged';

describe('isManagedDnsChainRule', () => {
	it('признаёт evaluate-правило пресета по префиксу тега', () => {
		expect(isManagedDnsChainRule({ action: 'evaluate', server: 'd', tag: 'awgm-dns-rd' })).toBe(true);
	});

	it('признаёт правило пресета по ссылке match_response', () => {
		expect(
			isManagedDnsChainRule({ action: 'respond', match_response: 'awgm-dns-ap' }),
		).toBe(true);
	});

	it('пользовательские правила с тем же механизмом не managed', () => {
		expect(isManagedDnsChainRule({ action: 'evaluate', server: 'd', tag: 'rd' })).toBe(false);
		expect(isManagedDnsChainRule({ action: 'respond', match_response: 'rd' })).toBe(false);
		expect(isManagedDnsChainRule({ action: 'respond', match_response: true })).toBe(false);
		expect(isManagedDnsChainRule({ domain: ['a.com'], server: 'd' })).toBe(false);
	});
});
