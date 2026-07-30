import { describe, expect, it } from 'vitest';
import { isDnsChainShadowed, isManagedDnsChainRule } from './dnsChainManaged';

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

describe('isDnsChainShadowed', () => {
	const chain = [
		{ action: 'evaluate' as const, server: 'dns-direct', tag: 'awgm-dns-rd' },
		{ action: 'respond' as const, match_response: 'awgm-dns-rd', server: 'dns-tunnel' },
	];

	it('catch-all выше цепочки — пресет мёртв', () => {
		expect(isDnsChainShadowed([{ server: 'dns-direct' }, ...chain])).toBe(true);
	});

	it('catch-all ниже цепочки — пресет работает', () => {
		expect(isDnsChainShadowed([...chain, { server: 'dns-direct' }])).toBe(false);
	});

	it('без catch-all перекрытия нет', () => {
		expect(isDnsChainShadowed([{ domain: ['a.com'], server: 'dns-direct' }, ...chain])).toBe(false);
	});

	it('перекрыты только пользовательские правила — цепочка ни при чём', () => {
		expect(
			isDnsChainShadowed([{ server: 'dns-direct' }, { domain: ['a.com'], server: 'dns-tunnel' }]),
		).toBe(false);
	});
});
