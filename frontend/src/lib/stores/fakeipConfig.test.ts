import { describe, it, expect, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import type {
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterOutbound,
} from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: {
		singboxFakeIPListRules: vi.fn().mockResolvedValue([]),
		singboxFakeIPListRuleSets: vi.fn().mockResolvedValue([]),
		singboxFakeIPListOutbounds: vi.fn().mockResolvedValue([]),
		singboxFakeIPListDNSServers: vi.fn().mockResolvedValue([]),
		singboxFakeIPListDNSRules: vi.fn().mockResolvedValue([]),
		singboxFakeIPGetDNSGlobals: vi.fn().mockResolvedValue({ final: '', strategy: '' as const }),
	},
}));

vi.mock('$lib/stores/awgTags', () => ({
	awgTags: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/stores/subscriptions', () => ({
	subscriptionsStore: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/stores/singbox', () => ({
	singboxTunnels: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/components/routing/singboxRouter/outboundOptions', () => ({
	buildOutboundOptions: vi.fn(() => []),
}));

import { fakeipConfig } from './fakeipConfig';
import { api } from '$lib/api/client';

const MOCK_DNS_SERVERS: SingboxRouterDNSServer[] = [
	{ tag: 'dns-fakeip', type: 'udp', server: '8.8.8.8', server_port: 53 },
	{ tag: 'dns-direct', type: 'udp', server: '77.88.8.8', server_port: 53 },
];

const MOCK_DNS_RULES: SingboxRouterDNSRule[] = [
	{ action: 'route', rule_set: ['geosite-private'], server: 'dns-direct' },
];

const MOCK_RULES: SingboxRouterRule[] = [
	{ action: 'sniff' },
	{ action: 'route', domain_suffix: ['youtube.com'], outbound: 'proxy-eu' },
];

const MOCK_RULE_SETS: SingboxRouterRuleSet[] = [
	{ tag: 'geosite-private', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-private.srs', update_interval: '24h', download_detour: 'direct' },
];

const MOCK_OUTBOUNDS: SingboxRouterOutbound[] = [
	{ type: 'selector', tag: 'proxy-eu', outbounds: ['awg-vpn0'], source: 'router' },
];

const MOCK_DNS_GLOBALS: SingboxRouterDNSGlobals = { final: 'dns-fakeip', strategy: 'prefer_ipv4' };

describe('fakeipConfig store', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(api.singboxFakeIPListRules).mockResolvedValue(MOCK_RULES);
		vi.mocked(api.singboxFakeIPListRuleSets).mockResolvedValue(MOCK_RULE_SETS);
		vi.mocked(api.singboxFakeIPListOutbounds).mockResolvedValue(MOCK_OUTBOUNDS);
		vi.mocked(api.singboxFakeIPListDNSServers).mockResolvedValue(MOCK_DNS_SERVERS);
		vi.mocked(api.singboxFakeIPListDNSRules).mockResolvedValue(MOCK_DNS_RULES);
		vi.mocked(api.singboxFakeIPGetDNSGlobals).mockResolvedValue(MOCK_DNS_GLOBALS);
	});

	it('starts uninitialized', () => {
		expect(get(fakeipConfig.initialized)).toBe(false);
		expect(get(fakeipConfig.loading)).toBe(false);
	});

	it('loadAll populates all substores and sets initialized', async () => {
		await fakeipConfig.loadAll();

		expect(get(fakeipConfig.initialized)).toBe(true);
		expect(get(fakeipConfig.loading)).toBe(false);
		expect(get(fakeipConfig.error)).toBeNull();

		expect(get(fakeipConfig.dnsServers)).toEqual(MOCK_DNS_SERVERS);
		expect(get(fakeipConfig.dnsRules)).toEqual(MOCK_DNS_RULES);
		expect(get(fakeipConfig.dnsGlobals)).toEqual(MOCK_DNS_GLOBALS);
		expect(get(fakeipConfig.outbounds)).toEqual(MOCK_OUTBOUNDS);

		const rules = get(fakeipConfig.rules);
		expect(rules).toHaveLength(MOCK_RULES.length);

		const ruleSets = get(fakeipConfig.ruleSets);
		expect(ruleSets).toHaveLength(MOCK_RULE_SETS.length);
		expect(ruleSets[0].tag).toBe('geosite-private');
	});

	it('loadAll calls fakeip endpoints, not router endpoints', async () => {
		await fakeipConfig.loadAll();

		expect(api.singboxFakeIPListRules).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPListDNSServers).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPListDNSRules).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPGetDNSGlobals).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPListRuleSets).toHaveBeenCalledOnce();
		expect(api.singboxFakeIPListOutbounds).toHaveBeenCalledOnce();
	});

	it('loadAll on API error sets error and still sets initialized', async () => {
		vi.mocked(api.singboxFakeIPListRules).mockRejectedValue(new Error('network error'));

		await fakeipConfig.loadAll();

		expect(get(fakeipConfig.initialized)).toBe(true);
		expect(get(fakeipConfig.error)).toBe('network error');
	});

	it('ruleUiKeys has same length as rules after loadAll', async () => {
		await fakeipConfig.loadAll();

		const rules = get(fakeipConfig.rules);
		const keys = get(fakeipConfig.ruleUiKeys);
		expect(keys).toHaveLength(rules.length);
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

	it('applyOutbounds replaces outbounds store', () => {
		const next: SingboxRouterOutbound[] = [{ type: 'selector', tag: 'new-proxy', outbounds: [], source: 'router' }];
		fakeipConfig.applyOutbounds(next);
		expect(get(fakeipConfig.outbounds)).toEqual(next);
	});

	it('applyRules updates rules and ruleUiKeys', () => {
		const next: SingboxRouterRule[] = [{ action: 'route', domain_suffix: ['discord.com'], outbound: 'proxy-eu' }];
		fakeipConfig.applyRules(next);
		const rules = get(fakeipConfig.rules);
		const keys = get(fakeipConfig.ruleUiKeys);
		expect(rules).toHaveLength(1);
		expect(keys).toHaveLength(1);
	});

	it('applyRuleSets updates ruleSets store', () => {
		const next: SingboxRouterRuleSet[] = [{ tag: 'geosite-test', type: 'remote', format: 'binary', url: 'https://cdn.example.com/test.srs', update_interval: '24h', download_detour: 'direct' }];
		fakeipConfig.applyRuleSets(next);
		expect(get(fakeipConfig.ruleSets)).toHaveLength(1);
		expect(get(fakeipConfig.ruleSets)[0].tag).toBe('geosite-test');
	});
});
