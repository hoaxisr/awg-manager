import { describe, expect, it } from 'vitest';
import type { FircGroup, FircListRule, FircRule } from '$lib/types/firc';
import {
	admitsEveryDeviceOf,
	analyzeFircImportText,
	computeFircDuplicates,
	detectFircRuleType,
	fircListRuleOverlapId,
	fircOwnRuleOverlapId,
	fircRuleKey,
	formatFircOverlapTooltip,
	normalizeFircListUrl,
	selectFircImportEntries,
} from './fircOverlap';

function makeRule(
	id: string,
	rule = 'deepmind.com',
	overrides: Partial<FircRule> = {},
): FircRule {
	return {
		id,
		type: 'namespace',
		rule,
		enable: true,
		...overrides,
	};
}

function makeGroup(
	id: string,
	overrides: Partial<FircGroup> = {},
): FircGroup {
	return {
		id,
		name: `Group ${id}`,
		interface: 'nwg0',
		enable: true,
		devices: { allow: [], deny: [] },
		rules: [makeRule(`r-${id}`)],
		...overrides,
	};
}

describe('fircRuleKey, detectFircRuleType, and normalizeFircListUrl', () => {
	it('normalizes domain case, whitespace, and legacy wildcard types', () => {
		expect(fircRuleKey(makeRule('1', '  DeepMind.COM  '))).toBe('namespace:deepmind.com::');
		expect(fircRuleKey(makeRule('2', 'deepmind.com'))).toBe('namespace:deepmind.com::');
		expect(fircRuleKey(makeRule('3', '*.Google.com', { type: 'wild' }))).toBe(
			fircRuleKey(makeRule('4', '*.google.com', { type: 'wildcard' })),
		);
		expect(fircRuleKey(makeRule('5', '*.Google.com', { type: 'DotWild' }))).toBe(
			fircRuleKey(makeRule('4', '*.google.com', { type: 'wildcard' })),
		);
	});

	it('preserves regex case while ignoring empty/whitespace rules', () => {
		expect(fircRuleKey(makeRule('1', '^Foo\\D+$', { type: 'regex' }))).not.toBe(
			fircRuleKey(makeRule('2', '^foo\\d+$', { type: 'regex' })),
		);
		expect(fircRuleKey(makeRule('3', '   '))).toBe('');
	});

	it('includes proto and normalized ports for subnet rules', () => {
		const a = fircRuleKey(makeRule('1', '10.0.0.0/8', { type: 'subnet', proto: 'tcp', ports: '80, 443' }));
		const b = fircRuleKey(makeRule('2', '10.0.0.0/8', { type: 'subnet', proto: 'tcp', ports: '80,443' }));
		const c = fircRuleKey(makeRule('3', '10.0.0.0/8', { type: 'subnet', proto: 'udp', ports: '80,443' }));
		expect(a).toBe(b);
		expect(a).not.toBe(c);
	});

	it('auto-detects rule types including regex patterns containing asterisks', () => {
		expect(detectFircRuleType('deepmind.com')).toBe('namespace');
		expect(detectFircRuleType('*.openai.com')).toBe('wildcard');
		expect(detectFircRuleType('149.154.160.0/20')).toBe('subnet');
		expect(detectFircRuleType('2001:db8::/32')).toBe('subnet6');
		expect(detectFircRuleType('^api\\.google\\.com$')).toBe('regex');
		expect(detectFircRuleType('^.*\\.google\\.com$')).toBe('regex');
		expect(detectFircRuleType('^api-[0-9]*\\.openai\\.com$')).toBe('regex');
		expect(detectFircRuleType('foo\\d*\\.bar')).toBe('regex');
	});

	it('normalizes URL scheme/host case while preserving path case', () => {
		expect(normalizeFircListUrl('  HTTPS://GitHub.COM/Org/Repo/List.srs  ')).toBe(
			'https://github.com/Org/Repo/List.srs',
		);
		expect(normalizeFircListUrl('https://github.com/Org/Repo/list.srs')).not.toBe(
			normalizeFircListUrl('https://github.com/Org/Repo/List.srs'),
		);
		expect(normalizeFircListUrl('   ')).toBe('');
	});
});

describe('admitsEveryDeviceOf', () => {
	it('treats unscoped upper group as covering every lower selector', () => {
		expect(admitsEveryDeviceOf({ allow: [], deny: [] }, { allow: ['192.168.1.5'], deny: [] })).toBe(true);
		expect(admitsEveryDeviceOf(undefined, { allow: [], deny: ['mac:aa:bb'] })).toBe(true);
	});

	it('splits when scoped upper group sits above an unscoped lower group', () => {
		expect(admitsEveryDeviceOf({ allow: ['192.168.1.5'], deny: [] }, { allow: [], deny: [] })).toBe(false);
	});

	it('handles allow subset and deny superset comparisons case-insensitively and order-independently', () => {
		expect(
			admitsEveryDeviceOf(
				{ allow: ['MAC:AA:BB', ' y '], deny: ['z'] },
				{ allow: ['y', 'mac:aa:bb'], deny: ['z', 'w'] },
			),
		).toBe(true);
		expect(
			admitsEveryDeviceOf(
				{ allow: ['x', 'y'], deny: [] },
				{ allow: ['x'], deny: [] },
			),
		).toBe(true);
		expect(
			admitsEveryDeviceOf(
				{ allow: ['x'], deny: [] },
				{ allow: ['x', 'y'], deny: [] },
			),
		).toBe(false);
		expect(
			admitsEveryDeviceOf(
				{ allow: ['x', 'y'], deny: ['z'] },
				{ allow: ['x', 'y'], deny: [] },
			),
		).toBe(false);
	});

	it('ignores AWGM auto-injected managed server subnets in allow selectors', () => {
		expect(
			admitsEveryDeviceOf(
				{ allow: ['192.168.1.5'], deny: [] },
				{ allow: ['192.168.1.5', '10.10.0.0/24', '10.66.0.0/16'], deny: [] },
			),
		).toBe(true);
		expect(
			admitsEveryDeviceOf(
				{ allow: ['10.10.0.0/24'], deny: [] },
				{ allow: ['192.168.1.5'], deny: [] },
			),
		).toBe(true);
	});

	it('handles deny-only upper groups properly', () => {
		expect(admitsEveryDeviceOf({ allow: [], deny: ['x'] }, { allow: [], deny: ['x', 'z'] })).toBe(true);
		expect(admitsEveryDeviceOf({ allow: [], deny: ['x'] }, { allow: [], deny: [] })).toBe(false);
		expect(admitsEveryDeviceOf({ allow: [], deny: ['x'] }, { allow: ['y'], deny: [] })).toBe(false);
		expect(admitsEveryDeviceOf({ allow: [], deny: ['x'] }, { allow: ['y'], deny: ['x'] })).toBe(true);
	});
});

describe('computeFircDuplicates', () => {
	it('marks only the later copy as shadowed when covered by an earlier unscoped group', () => {
		const res = computeFircDuplicates([
			makeGroup('a', { name: 'Google AI' }),
			makeGroup('b', { name: 'Antigravity', devices: { allow: ['192.168.1.5'], deny: [] } }),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('a', 'r-a'))).toBeUndefined();
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))).toEqual({
			kind: 'shadowed',
			groupId: 'a',
			groupName: 'Google AI',
			ruleId: fircOwnRuleOverlapId('a', 'r-a'),
			rawRuleId: 'r-a',
			inSameGroup: false,
			isTargetListRule: false,
		});
		expect(res.duplicateGroupIds.has('a')).toBe(false);
		expect(res.duplicateGroupIds.has('b')).toBe(true);
		expect(res.shadowedOwnRuleIdsByGroup.get('b')).toEqual(new Set(['r-b']));
		expect(formatFircOverlapTooltip(res.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))!)).toContain(
			'Google AI',
		);
	});

	it('does not collide or flag upper group when two groups share identical rule.id values', () => {
		const res = computeFircDuplicates([
			makeGroup('a', { name: 'Upper', rules: [makeRule('same-id', 'deepmind.com')] }),
			makeGroup('b', { name: 'Lower', rules: [makeRule('same-id', 'deepmind.com')] }),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('a', 'same-id'))).toBeUndefined();
		expect(res.duplicateGroupIds.has('a')).toBe(false);
		expect(res.shadowedOwnRuleIdsByGroup.get('a')).toBeUndefined();

		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'same-id'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'a',
			ruleId: fircOwnRuleOverlapId('a', 'same-id'),
			rawRuleId: 'same-id',
			inSameGroup: false,
		});
		expect(res.duplicateGroupIds.has('b')).toBe(true);
		expect(res.shadowedOwnRuleIdsByGroup.get('b')).toEqual(new Set(['same-id']));
	});

	it('marks split overlap when scoped upper group does not cover lower unscoped group', () => {
		const res = computeFircDuplicates([
			makeGroup('a', { name: 'TV only', devices: { allow: ['192.168.1.5'], deny: [] } }),
			makeGroup('b', { name: 'All devices', devices: { allow: [], deny: [] } }),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))?.kind).toBe('split');
		expect(res.shadowedOwnRuleIdsByGroup.get('b')).toBeUndefined();
		expect(res.splitCountByGroup.get('b')).toBe(1);
		expect(formatFircOverlapTooltip(res.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))!)).toContain(
			'TV only',
		);
	});

	it('disabled upper group or rule marks nothing, while disabled later copy is still marked', () => {
		const upperDisabledGroup = computeFircDuplicates([
			makeGroup('a', { enable: false }),
			makeGroup('b'),
		]);
		expect(upperDisabledGroup.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))).toBeUndefined();

		const upperDisabledRule = computeFircDuplicates([
			makeGroup('a', { rules: [makeRule('r-a', 'deepmind.com', { enable: false })] }),
			makeGroup('b'),
		]);
		expect(upperDisabledRule.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))).toBeUndefined();

		const lowerDisabledRule = computeFircDuplicates([
			makeGroup('a'),
			makeGroup('b', { rules: [makeRule('r-b', 'deepmind.com', { enable: false })] }),
		]);
		expect(lowerDisabledRule.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))?.kind).toBe('shadowed');

		const lowerDisabledGroup = computeFircDuplicates([
			makeGroup('a'),
			makeGroup('b', { enable: false }),
		]);
		expect(lowerDisabledGroup.ruleOverlaps.get(fircOwnRuleOverlapId('b', 'r-b'))?.kind).toBe('shadowed');
	});

	it('prefers the first earlier shadowing group over an earlier split group', () => {
		const res = computeFircDuplicates([
			makeGroup('a', { name: 'Scoped', devices: { allow: ['x'], deny: [] } }),
			makeGroup('b', { name: 'Global', devices: { allow: [], deny: [] } }),
			makeGroup('c', { name: 'Target', devices: { allow: ['y'], deny: [] } }),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('c', 'r-c'))?.kind).toBe('shadowed');
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('c', 'r-c'))?.groupId).toBe('b');
	});

	it('marks intra-group duplicates as shadowed with inSameGroup=true', () => {
		const res = computeFircDuplicates([
			makeGroup('a', {
				name: 'Antigravity',
				rules: [makeRule('r1', 'deepmind.com'), makeRule('r2', 'DEEPMIND.COM')],
			}),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('a', 'r1'))).toBeUndefined();
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('a', 'r2'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'a',
			ruleId: fircOwnRuleOverlapId('a', 'r1'),
			rawRuleId: 'r1',
			inSameGroup: true,
		});
		expect(res.shadowedOwnRuleIdsByGroup.get('a')).toEqual(new Set(['r2']));
		expect(formatFircOverlapTooltip(res.ruleOverlaps.get(fircOwnRuleOverlapId('a', 'r2'))!)).toContain(
			'этой же группе',
		);
	});

	it('ignores empty and whitespace-only rules', () => {
		const res = computeFircDuplicates([
			makeGroup('a', { rules: [makeRule('r1', ''), makeRule('r2', '   ')] }),
			makeGroup('b', { rules: [makeRule('r3', ''), makeRule('r4', '\t')] }),
		]);
		expect(res.ruleOverlaps.size).toBe(0);
		expect(res.duplicateGroupIds.size).toBe(0);
	});

	it('detects intra-group duplicates inside disabled groups without letting disabled groups shadow later groups', () => {
		const res = computeFircDuplicates([
			makeGroup('disabled', {
				name: 'Disabled Group',
				enable: false,
				rules: [makeRule('d1', 'deepmind.com'), makeRule('d2', 'deepmind.com')],
			}),
			makeGroup('enabled', {
				name: 'Enabled Group',
				enable: true,
				rules: [makeRule('e1', 'deepmind.com')],
			}),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('disabled', 'd1'))).toBeUndefined();
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('disabled', 'd2'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'disabled',
			rawRuleId: 'd1',
			inSameGroup: true,
		});
		expect(res.shadowedOwnRuleIdsByGroup.get('disabled')).toEqual(new Set(['d2']));
		// Disabled group must NOT shadow subsequent enabled group
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('enabled', 'e1'))).toBeUndefined();
		expect(res.duplicateGroupIds.has('enabled')).toBe(false);
	});

	it('classifies intra-group duplicate inside disabled group as shadowed even below an earlier split-scope group', () => {
		const res = computeFircDuplicates([
			makeGroup('scoped-upper', {
				name: 'TV Only',
				devices: { allow: ['192.168.1.50'], deny: [] },
				rules: [makeRule('u1', 'deepmind.com')],
			}),
			makeGroup('disabled-lower', {
				name: 'Disabled Global',
				enable: false,
				devices: { allow: [], deny: [] },
				rules: [makeRule('d1', 'deepmind.com'), makeRule('d2', 'deepmind.com')],
			}),
		]);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('disabled-lower', 'd1'))).toMatchObject({
			kind: 'split',
			groupId: 'scoped-upper',
			inSameGroup: false,
		});
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('disabled-lower', 'd2'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'disabled-lower',
			rawRuleId: 'd1',
			inSameGroup: true,
		});
		expect(res.shadowedOwnRuleIdsByGroup.get('disabled-lower')).toEqual(new Set(['d2']));
	});

	it('marks redundant own rules as shadowed by loaded remote list rules within the same group', () => {
		const g = makeGroup('g1', {
			name: 'AI Combined',
			rules: [makeRule('own-1', 'deepmind.com'), makeRule('own-2', 'disabled-in-list.com')],
		});
		const listRules: Record<string, FircListRule[]> = {
			g1: [
				{ id: 'lr-1', type: 'namespace', rule: 'deepmind.com', enable: true },
				{ id: 'lr-2', type: 'namespace', rule: 'disabled-in-list.com', enable: false },
			],
		};
		const res = computeFircDuplicates([g], listRules);
		expect(res.ruleOverlaps.get(fircListRuleOverlapId('g1', 'lr-1'))).toBeUndefined();
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('g1', 'own-1'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'g1',
			ruleId: fircListRuleOverlapId('g1', 'lr-1'),
			inSameGroup: true,
			isTargetListRule: true,
		});
		expect(res.shadowedOwnRuleIdsByGroup.get('g1')).toEqual(new Set(['own-1']));
		// Disabled remote list rule must NOT shadow manual own rule
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('g1', 'own-2'))).toBeUndefined();
		expect(formatFircOverlapTooltip(res.ruleOverlaps.get(fircOwnRuleOverlapId('g1', 'own-1'))!)).toContain(
			'удалённого списка этой же группы',
		);
	});

	it('detects overlaps across own rules and loaded remote list rules without ID collisions', () => {
		const g1 = makeGroup('g1', { name: 'Remote AI', rules: [] });
		const g2 = makeGroup('g2', {
			name: 'Manual Antigravity',
			rules: [makeRule('shared-id', 'deepmind.com')],
		});
		const listRules: Record<string, FircListRule[]> = {
			g1: [{ id: 'shared-id', type: 'namespace', rule: 'deepmind.com', enable: true }],
			g2: [{ id: 'lr2', type: 'namespace', rule: 'deepmind.com', enable: true }],
		};
		const res = computeFircDuplicates([g1, g2], listRules);
		expect(res.ruleOverlaps.get(fircOwnRuleOverlapId('g2', 'shared-id'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'g1',
			ruleId: fircListRuleOverlapId('g1', 'shared-id'),
			rawRuleId: 'shared-id',
			isTargetListRule: true,
		});
		expect(res.ruleOverlaps.get(fircListRuleOverlapId('g2', 'lr2'))).toMatchObject({
			kind: 'shadowed',
			groupId: 'g1',
		});
		// Only own rules should be eligible for 1-click shadowed removal
		expect(res.shadowedOwnRuleIdsByGroup.get('g2')).toEqual(new Set(['shared-id']));
	});

	it('detects duplicate remote list URLs across groups', () => {
		const g1 = makeGroup('g1', {
			name: 'AI 1',
			rules: [],
			list: { url: 'https://Example.com/lists/AI.srs', interval: 86400 },
		});
		const g2 = makeGroup('g2', {
			name: 'AI 2',
			rules: [],
			list: { url: 'https://example.com/lists/AI.srs', interval: 86400 },
		});
		const res = computeFircDuplicates([g1, g2]);
		expect(res.listUrlOverlaps.get('g1')).toBeUndefined();
		expect(res.listUrlOverlaps.get('g2')).toMatchObject({
			kind: 'shadowed',
			groupId: 'g1',
			groupName: 'AI 1',
		});
		expect(res.duplicateGroupIds.has('g2')).toBe(true);
	});
});

describe('analyzeFircImportText and selectFircImportEntries', () => {
	it('classifies new, batch duplicates, same-group duplicates, shadowed, split, and lower-group rules', () => {
		const groups: FircGroup[] = [
			makeGroup('g-upper-global', {
				name: 'AI Global',
				rules: [makeRule('u1', 'deepmind.com')],
			}),
			makeGroup('g-upper-scoped', {
				name: 'TV Only',
				devices: { allow: ['192.168.1.50'], deny: [] },
				rules: [makeRule('u2', 'gemini.google.com')],
			}),
			makeGroup('g-target', {
				name: 'Antigravity',
				rules: [makeRule('t1', 'ai.google.dev')],
			}),
			makeGroup('g-lower', {
				name: 'Lower Group',
				rules: [makeRule('l1', 'labs.google')],
			}),
		];

		const text = [
			'# comment line',
			'deepmind.com',
			'DEEPMIND.COM',
			'ai.google.dev',
			'gemini.google.com',
			'labs.google',
			'jules.google',
			'jules.google',
		].join('\n');

		const analysis = analyzeFircImportText(text, 2, groups);
		expect(analysis.totalValid).toBe(7);
		expect(analysis.otherGroupShadowedCount).toBe(1);
		expect(analysis.batchDuplicateCount).toBe(2);
		expect(analysis.sameGroupDuplicateCount).toBe(1);
		expect(analysis.otherGroupSplitCount).toBe(1);
		expect(analysis.otherGroupLaterCount).toBe(1);
		expect(analysis.newCount).toBe(1);
		expect(analysis.shadowedGroupNames).toEqual(['AI Global']);
		expect(analysis.otherNoticeGroupNames).toEqual(['TV Only', 'Lower Group']);

		const filtered = selectFircImportEntries(analysis, {
			skipBatchAndSameGroup: true,
			skipOtherGroupShadowed: true,
		});
		expect(filtered.selected.map((e) => e.rule)).toEqual([
			'gemini.google.com',
			'labs.google',
			'jules.google',
		]);
		expect(filtered.skippedBatchOrSame).toBe(3);
		expect(filtered.skippedOtherShadowed).toBe(1);

		// Even when skipBatchAndSameGroup=false, duplicate copies of an upper-shadowed rule
		// must still be skipped when skipOtherGroupShadowed=true.
		const skipShadowedOnly = selectFircImportEntries(analysis, {
			skipBatchAndSameGroup: false,
			skipOtherGroupShadowed: true,
		});
		expect(skipShadowedOnly.selected.map((e) => e.rule)).toEqual([
			'ai.google.dev',
			'gemini.google.com',
			'labs.google',
			'jules.google',
			'jules.google',
		]);
		expect(skipShadowedOnly.skippedOtherShadowed).toBe(2);
		expect(skipShadowedOnly.skippedBatchOrSame).toBe(0);

		const allowShadowed = selectFircImportEntries(analysis, {
			skipBatchAndSameGroup: true,
			skipOtherGroupShadowed: false,
		});
		expect(allowShadowed.selected.map((e) => e.rule)).toEqual([
			'deepmind.com',
			'gemini.google.com',
			'labs.google',
			'jules.google',
		]);
	});

	it('respects forcedType, ignores disabled groups, and handles out-of-bounds target index safely', () => {
		const groups: FircGroup[] = [
			makeGroup('g-disabled', {
				name: 'Disabled Upper',
				enable: false,
				rules: [makeRule('d1', 'deepmind.com', { type: 'domain' })],
			}),
			makeGroup('g-upper', {
				name: 'Enabled Upper',
				rules: [makeRule('u1', 'deepmind.com', { type: 'domain' })],
			}),
			makeGroup('g-target', { name: 'Target', rules: [] }),
		];

		// Auto-detect classifies deepmind.com as namespace (does not match upper domain rule)
		const autoRes = analyzeFircImportText('deepmind.com', 2, groups, undefined, 'auto');
		expect(autoRes.newCount).toBe(1);
		expect(autoRes.otherGroupShadowedCount).toBe(0);

		// Forcing type='domain' matches the enabled upper group (not the disabled one)
		const forcedRes = analyzeFircImportText('deepmind.com', 2, groups, undefined, 'domain');
		expect(forcedRes.otherGroupShadowedCount).toBe(1);
		expect(forcedRes.shadowedGroupNames).toEqual(['Enabled Upper']);

		// Out-of-bounds target index treats valid lines as new without throwing
		const oobRes = analyzeFircImportText('example.com', 99, groups);
		expect(oobRes.totalValid).toBe(1);
		expect(oobRes.newCount).toBe(1);
	});

	it('records shadowedGroupNames when a rule is both same-group duplicate and shadowed by upper group', () => {
		const groups: FircGroup[] = [
			makeGroup('g-upper', {
				name: 'Google AI',
				rules: [makeRule('u1', 'deepmind.com')],
			}),
			makeGroup('g-target', {
				name: 'Antigravity',
				rules: [makeRule('t1', 'deepmind.com')],
			}),
		];
		const analysis = analyzeFircImportText('deepmind.com', 1, groups);
		expect(analysis.sameGroupDuplicateCount).toBe(1);
		expect(analysis.shadowedGroupNames).toEqual(['Google AI']);
		expect(analysis.entries[0]).toMatchObject({
			status: 'same_group_duplicate',
			shadowedByUpper: true,
			conflictGroupName: 'Google AI',
		});
		const sel = selectFircImportEntries(analysis, {
			skipBatchAndSameGroup: false,
			skipOtherGroupShadowed: true,
		});
		expect(sel.selected).toHaveLength(0);
		expect(sel.skippedOtherShadowed).toBe(1);
	});

	it('does not treat disabled remote list rules in the target group as same_group_duplicate during import', () => {
		const groups: FircGroup[] = [
			makeGroup('g-target', {
				name: 'AI',
				rules: [],
			}),
		];
		const listRules: Record<string, FircListRule[]> = {
			'g-target': [
				{ id: 'lr-off', type: 'namespace', rule: 'disabled-list.ai', enable: false },
				{ id: 'lr-on', type: 'namespace', rule: 'enabled-list.ai', enable: true },
			],
		};
		const analysis = analyzeFircImportText(
			'disabled-list.ai\nenabled-list.ai',
			0,
			groups,
			listRules,
		);
		expect(analysis.newCount).toBe(1);
		expect(analysis.sameGroupDuplicateCount).toBe(1);
		expect(analysis.entries[0]).toMatchObject({ rule: 'disabled-list.ai', status: 'new' });
		expect(analysis.entries[1]).toMatchObject({
			rule: 'enabled-list.ai',
			status: 'same_group_duplicate',
		});
	});

	it('flags and excludes auto-detected subnet/subnet6 rules when importing into a DNS-only group', () => {
		const groups: FircGroup[] = [
			makeGroup('g-dns', {
				name: 'DoH Only',
				interface: 'dns',
				resolve: { server: 'https://9.9.9.9/dns-query', fallback: true },
				rules: [],
			}),
		];
		const analysis = analyzeFircImportText(
			['example.com', '149.154.160.0/20', '2001:db8::/32', '*.openai.com'].join('\n'),
			0,
			groups,
		);
		expect(analysis.totalValid).toBe(4);
		expect(analysis.newCount).toBe(2);
		expect(analysis.unsupportedDnsOnlySubnetCount).toBe(2);
		expect(analysis.entries.map((e) => [e.rule, e.status])).toEqual([
			['example.com', 'new'],
			['149.154.160.0/20', 'unsupported_dns_only_subnet'],
			['2001:db8::/32', 'unsupported_dns_only_subnet'],
			['*.openai.com', 'new'],
		]);

		const sel = selectFircImportEntries(analysis, {
			skipBatchAndSameGroup: false,
			skipOtherGroupShadowed: false,
		});
		expect(sel.selected.map((e) => e.rule)).toEqual(['example.com', '*.openai.com']);
		expect(sel.skippedUnsupportedSubnet).toBe(2);
	});
});
