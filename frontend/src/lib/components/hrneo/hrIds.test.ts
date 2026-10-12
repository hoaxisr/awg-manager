import { describe, it, expect } from 'vitest';
import type { DnsRoute } from '$lib/types';
import {
	parseHrId,
	hrPosKey,
	withRevision,
	hrRevisionOf,
	isHrRulesChanged,
	findSameHrGroup,
	buildHrTargets,
} from './hrIds';

const r = (id: string, name = '', manualDomains: string[] = ['a.com']): DnsRoute =>
	({ id, name, manualDomains, backend: 'hydraroute' }) as DnsRoute;

describe('hrIds', () => {
	it('разбирает цель с двоеточием', () => {
		expect(parseHrId('hr:abc:x:y:2')).toEqual({ revision: 'abc', target: 'x:y', index: 2 });
		expect(parseHrId('hr:Youtube')).toBeNull();
		expect(parseHrId('hr:a:b:')).toBeNull();
		expect(parseHrId('42')).toBeNull();
	});

	it('позиционный ключ не зависит от ревизии', () => {
		expect(hrPosKey('hr:aaa:P:1')).toBe(hrPosKey('hr:bbb:P:1'));
		expect(withRevision('hr:aaa:P:1', 'bbb')).toBe('hr:bbb:P:1');
	});

	it('ревизия списка — из первой HR-группы', () => {
		expect(hrRevisionOf([r('5'), r('hr:abc:P:0')])).toBe('abc');
		expect(hrRevisionOf([r('5')])).toBeNull();
	});

	it('узнаёт 409 HR_RULES_CHANGED', () => {
		const e = Object.assign(new Error('x'), { status: 409, body: { code: 'HR_RULES_CHANGED' } });
		expect(isHrRulesChanged(e)).toBe(true);
		expect(isHrRulesChanged(new Error('x'))).toBe(false);
	});

	it('та же группа — та же позиция, имя и содержимое', () => {
		const old = r('hr:aaa:P:1', 'X', ['a.com']);
		expect(findSameHrGroup(old, [r('hr:bbb:P:0'), r('hr:bbb:P:1', 'X', ['a.com'])])?.id).toBe('hr:bbb:P:1');
		expect(findSameHrGroup(old, [r('hr:bbb:P:1', 'X', ['b.com'])])).toBeNull();
		expect(findSameHrGroup(old, [r('hr:bbb:P:1', 'Y', ['a.com'])])).toBeNull();
	});

	it('пустые цели из PolicyOrder добавляются, порядок по PolicyOrder', () => {
		const out = buildHrTargets(
			[{ name: 'B', kind: 'policy', ruleCount: 2 }],
			['Empty', 'B'],
		);
		expect(out.map((t) => [t.name, t.ruleCount, !!t.empty])).toEqual([
			['Empty', 0, true],
			['B', 2, false],
		]);
	});
});
