import { describe, it, expect } from 'vitest';
import { MIHOMO_SUPPORTED_RULE_TYPES, ALL_MIHOMO_RULE_TYPES } from './mihomoRuleTypes.generated';

describe('Mihomo Rule Types Generated Parity', () => {
	it('contains exactly 36 supported rule types', () => {
		expect(ALL_MIHOMO_RULE_TYPES).toHaveLength(36);
		const allFromGroups = MIHOMO_SUPPORTED_RULE_TYPES.flatMap((g) => g.items);
		expect(allFromGroups).toHaveLength(36);
		const uniqueTypes = new Set(allFromGroups);
		expect(uniqueTypes.size).toBe(36);
	});

	it('strictly excludes SUB-RULE', () => {
		expect(ALL_MIHOMO_RULE_TYPES).not.toContain('SUB-RULE');
		const allFromGroups = MIHOMO_SUPPORTED_RULE_TYPES.flatMap((g) => g.items);
		expect(allFromGroups).not.toContain('SUB-RULE');
	});

	it('contains exactly 5 categories with correct distribution', () => {
		expect(MIHOMO_SUPPORTED_RULE_TYPES).toHaveLength(5);
		const categoryLabels = MIHOMO_SUPPORTED_RULE_TYPES.map((g) => g.label);
		expect(categoryLabels).toEqual([
			'Домены',
			'IP и сети',
			'Порты и входы',
			'Процесс',
			'Составные'
		]);

		const categoryCounts: Record<string, number> = {};
		for (const g of MIHOMO_SUPPORTED_RULE_TYPES) {
			categoryCounts[g.label] = g.items.length;
		}

		expect(categoryCounts).toEqual({
			'Домены': 6,
			'IP и сети': 9,
			'Порты и входы': 9,
			'Процесс': 7,
			'Составные': 5
		});
	});

	it('contains expected essential rules in categories', () => {
		const domains = MIHOMO_SUPPORTED_RULE_TYPES.find((g) => g.label === 'Домены')?.items ?? [];
		expect(domains).toContain('DOMAIN');
		expect(domains).toContain('DOMAIN-SUFFIX');
		expect(domains).toContain('GEOSITE');

		const ips = MIHOMO_SUPPORTED_RULE_TYPES.find((g) => g.label === 'IP и сети')?.items ?? [];
		expect(ips).toContain('IP-CIDR');
		expect(ips).toContain('GEOIP');

		const composites = MIHOMO_SUPPORTED_RULE_TYPES.find((g) => g.label === 'Составные')?.items ?? [];
		expect(composites).toEqual(['RULE-SET', 'AND', 'OR', 'NOT', 'MATCH']);
	});
});
