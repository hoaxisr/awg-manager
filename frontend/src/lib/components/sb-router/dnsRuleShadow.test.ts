import { describe, expect, it } from 'vitest';
import {
	isCatchAllDnsRule,
	firstCatchAllDnsRuleIndex,
	computeShadowedDnsRuleIndices,
} from './dnsRuleShadow';
import type { SingboxRouterDNSRule } from '$lib/types';

const suffix = (d: string): SingboxRouterDNSRule => ({ domain_suffix: [d], action: 'route', server: 's' });
const ruleSet = (tag: string): SingboxRouterDNSRule => ({ rule_set: [tag], action: 'route', server: 's' });
const catchAll = (server = 's'): SingboxRouterDNSRule => ({ action: 'route', server });
const catchAllBlock = (): SingboxRouterDNSRule => ({ action: 'reject', method: 'drop' });
const queryTypeOnly = (): SingboxRouterDNSRule => ({ query_type: ['A'], action: 'route', server: 's' });

describe('isCatchAllDnsRule', () => {
	it('is true for a matcher-less route rule', () => {
		expect(isCatchAllDnsRule(catchAll())).toBe(true);
	});

	it('is true for a matcher-less block rule', () => {
		expect(isCatchAllDnsRule(catchAllBlock())).toBe(true);
	});

	it('is false when any domain matcher is present', () => {
		expect(isCatchAllDnsRule(suffix('.youtube.com'))).toBe(false);
		expect(isCatchAllDnsRule(ruleSet('geosite-ru'))).toBe(false);
	});

	it('is false for a query_type-only rule (query_type restricts the query)', () => {
		expect(isCatchAllDnsRule(queryTypeOnly())).toBe(false);
	});

	it('is false for a source_ip_cidr-only rule (matcher backend-side)', () => {
		const sourceOnly: SingboxRouterDNSRule = {
			source_ip_cidr: ['192.168.1.5/32'],
			action: 'route',
			server: 's',
		};
		expect(isCatchAllDnsRule(sourceOnly)).toBe(false);
		expect(computeShadowedDnsRuleIndices([sourceOnly, suffix('a')]).size).toBe(0);
	});

	it('правило только с match_response — не catch-all', () => {
		expect(isCatchAllDnsRule({ match_response: true, action: 'respond' })).toBe(false);
	});
});

describe('firstCatchAllDnsRuleIndex', () => {
	it('returns -1 when there is no catch-all', () => {
		expect(firstCatchAllDnsRuleIndex([suffix('a'), ruleSet('b')])).toBe(-1);
	});

	it('returns the index of the first matcher-less rule', () => {
		expect(firstCatchAllDnsRuleIndex([suffix('a'), catchAll(), suffix('b')])).toBe(1);
	});

	it('returns the first when multiple catch-alls exist', () => {
		expect(firstCatchAllDnsRuleIndex([catchAll('x'), catchAll('y')])).toBe(0);
	});
});

describe('computeShadowedDnsRuleIndices', () => {
	it('is empty when there is no catch-all', () => {
		expect(computeShadowedDnsRuleIndices([suffix('a'), ruleSet('b')]).size).toBe(0);
	});

	it('shadows every index after a catch-all at k', () => {
		const rules = [suffix('a'), catchAll(), suffix('b'), ruleSet('c')];
		const shadowed = computeShadowedDnsRuleIndices(rules);
		expect([...shadowed].sort()).toEqual([2, 3]);
		// The catch-all itself and everything before it stay active.
		expect(shadowed.has(0)).toBe(false);
		expect(shadowed.has(1)).toBe(false);
	});

	it('is empty when the catch-all is the last rule', () => {
		expect(computeShadowedDnsRuleIndices([suffix('a'), catchAll()]).size).toBe(0);
	});

	it('is governed by the FIRST catch-all when several exist', () => {
		const rules = [catchAll('x'), suffix('b'), catchAll('y')];
		expect([...computeShadowedDnsRuleIndices(rules)].sort()).toEqual([1, 2]);
	});

	it('handles an empty list', () => {
		expect(computeShadowedDnsRuleIndices([]).size).toBe(0);
	});

	it('catch-all evaluate не затеняет правила ниже', () => {
		const rules: SingboxRouterDNSRule[] = [
			{ action: 'evaluate', server: 's', tag: 'rd' },
			{ match_response: 'rd', response_rcode: 'NOERROR', action: 'respond' },
		];
		expect(computeShadowedDnsRuleIndices(rules).size).toBe(0);
	});
});
