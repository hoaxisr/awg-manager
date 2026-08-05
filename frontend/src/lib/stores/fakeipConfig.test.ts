import { describe, it, expect, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import type {
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
} from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: {
		singboxFakeIPListDNSServers: vi.fn().mockResolvedValue([]),
		singboxFakeIPListDNSRules: vi.fn().mockResolvedValue([]),
		singboxFakeIPGetDNSGlobals: vi.fn().mockResolvedValue({ final: '', strategy: '' as const }),
	},
}));

import { fakeipConfig, createFakeipConfigStore } from './fakeipConfig';
import { api } from '$lib/api/client';

const MOCK_DNS_SERVERS: SingboxRouterDNSServer[] = [
	{ tag: 'dns-fakeip', type: 'udp', server: '8.8.8.8', server_port: 53 },
	{ tag: 'dns-direct', type: 'udp', server: '77.88.8.8', server_port: 53 },
];

const MOCK_DNS_RULES: SingboxRouterDNSRule[] = [
	{ action: 'route', rule_set: ['geosite-private'], server: 'dns-direct' },
];

const MOCK_DNS_GLOBALS: SingboxRouterDNSGlobals = { final: 'dns-fakeip', strategy: 'prefer_ipv4' };

describe('fakeipConfig store', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.singboxFakeIPListDNSServers).mockResolvedValue(MOCK_DNS_SERVERS);
		vi.mocked(api.singboxFakeIPListDNSRules).mockResolvedValue(MOCK_DNS_RULES);
		vi.mocked(api.singboxFakeIPGetDNSGlobals).mockResolvedValue(MOCK_DNS_GLOBALS);
	});

	it('starts uninitialized', () => {
		expect(get(fakeipConfig.initialized)).toBe(false);
		expect(get(fakeipConfig.loading)).toBe(false);
	});

	it('loadAll populates DNS substores and sets initialized', async () => {
		await fakeipConfig.loadAll();

		expect(get(fakeipConfig.initialized)).toBe(true);
		expect(get(fakeipConfig.loading)).toBe(false);
		expect(get(fakeipConfig.error)).toBeNull();

		expect(get(fakeipConfig.dnsServers)).toEqual(MOCK_DNS_SERVERS);
		expect(get(fakeipConfig.dnsRules)).toEqual(MOCK_DNS_RULES);
		expect(get(fakeipConfig.dnsGlobals)).toEqual(MOCK_DNS_GLOBALS);
	});

	it('loadAll трогает только DNS-ручки fakeip', async () => {
		await fakeipConfig.loadAll();

		expect(api.singboxFakeIPListDNSServers).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPListDNSRules).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPGetDNSGlobals).toHaveBeenCalledOnce();
	});

	it('стор не отдаёт правила, наборы и outbound-ы — они в общем слоте', () => {
		for (const key of ['rules', 'ruleUiKeys', 'ruleSets', 'outbounds', 'options']) {
			expect(fakeipConfig).not.toHaveProperty(key);
		}
	});

	// Свежий стор, а не синглтон: у синглтона предыдущие тесты уже подняли
	// initialized, и ассерт про false прошёл бы мимо проверяемого поведения.
	it('loadAll на ошибке НЕ помечает стор инициализированным — mount повторит запрос', async () => {
		vi.mocked(api.singboxFakeIPListDNSServers).mockRejectedValue(new Error('network error'));

		const store = createFakeipConfigStore();
		await store.loadAll();

		expect(get(store.initialized)).toBe(false);
		expect(get(store.loading)).toBe(false);
		expect(get(store.error)).toBe('network error');
	});

	it('applyDNSServers replaces dnsServers store', () => {
		const next: SingboxRouterDNSServer[] = [{ tag: 'new-server', type: 'udp', server: '1.1.1.1', server_port: 53 }];
		fakeipConfig.applyDNSServers(next);
		expect(get(fakeipConfig.dnsServers)).toEqual(next);
	});

	it('applyDNSRules replaces dnsRules store', () => {
		const next: SingboxRouterDNSRule[] = [{ action: 'route', rule_set: ['geosite-youtube'], server: 'dns-fakeip' }];
		fakeipConfig.applyDNSRules(next);
		expect(get(fakeipConfig.dnsRules)).toEqual(next);
	});

	it('applyDNSGlobals replaces dnsGlobals store', () => {
		const next: SingboxRouterDNSGlobals = { final: 'dns-direct', strategy: 'prefer_ipv6' };
		fakeipConfig.applyDNSGlobals(next);
		expect(get(fakeipConfig.dnsGlobals)).toEqual(next);
	});
});
