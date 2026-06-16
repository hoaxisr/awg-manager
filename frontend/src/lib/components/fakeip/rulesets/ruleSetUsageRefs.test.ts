import { describe, it, expect } from 'vitest';
import { computeRuleSetUsageRefs } from './ruleSetUsageRefs';

describe('computeRuleSetUsageRefs', () => {
	it('собирает 1-based номера DNS- и route-правил по тегу', () => {
		const dns = [{ rule_set: ['a'] }, { rule_set: ['b', 'a'] }, {}];
		const route = [{ rule_set: ['a'] }, {}, { rule_set: ['b'] }];
		const m = computeRuleSetUsageRefs(dns, route);

		expect(m.get('a')).toEqual({ dns: [1, 2], route: [1] });
		expect(m.get('b')).toEqual({ dns: [2], route: [3] });
	});

	it('игнорирует правила без rule_set', () => {
		const m = computeRuleSetUsageRefs([{}, { rule_set: [] }], [{}]);
		expect(m.size).toBe(0);
	});

	it('не дублирует номер при повторе тега в одном правиле', () => {
		const m = computeRuleSetUsageRefs([{ rule_set: ['a', 'a'] }], []);
		expect(m.get('a')).toEqual({ dns: [1], route: [] });
	});

	it('нормализует -srs-компаньон к базовому inline-тегу', () => {
		// displayRuleSetTag сводит «mydomains-srs» → «mydomains».
		const m = computeRuleSetUsageRefs([{ rule_set: ['mydomains-srs'] }], []);
		expect(m.get('mydomains')).toEqual({ dns: [1], route: [] });
		expect(m.has('mydomains-srs')).toBe(false);
	});
});
