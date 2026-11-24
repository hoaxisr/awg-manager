import { describe, expect, it } from 'vitest';
import type { FircGroup, FircRule } from '$lib/types/firc';
import {
	applyBulkGroupAction,
	applyBulkRuleAction,
	collapseRulesTextToRootDomains,
	detectFircRuleType,
	parseRulesFromText,
	serializeRulesToText,
} from './fircBulkRules';

describe('detectFircRuleType', () => {
	it('detects namespace, wildcard, regex, ipv4 cidr and ipv6 cidr accurately', () => {
		expect(detectFircRuleType('example.com')).toBe('namespace');
		expect(detectFircRuleType('*.openai.com')).toBe('wildcard');
		expect(detectFircRuleType('^api\\.[a-z]+\\.org$')).toBe('regex');
		expect(detectFircRuleType('149.154.160.0/20')).toBe('subnet');
		expect(detectFircRuleType('1.2.3.4')).toBe('subnet');
		expect(detectFircRuleType('2001:db8::/32')).toBe('subnet6');
	});
});

describe('serializeRulesToText & parseRulesFromText', () => {
	it('round-trips rules preserving IDs, autolist metadata, explicit prefixes, proto/ports, [off], and comments', () => {
		const existing: FircRule[] = [
			{ id: 'r1', type: 'namespace', rule: 'youtube.com', enable: true },
			{ id: 'r2', type: 'domain', rule: 'exact.example.org', enable: true, autolist: 'auto-1', autolistByDomain: true },
			{ id: 'r3', type: 'subnet', rule: '149.154.160.0/20', enable: false, proto: 'tcp', ports: '80,443', comment: 'Telegram' },
			{ id: 'r4', type: 'subnet6', rule: '2001:db8::/32', enable: true },
			{ id: 'r5', type: 'namespace', rule: 'old-removed.com', enable: true },
		];

		const text = serializeRulesToText(existing);
		expect(text).toBe(
			[
				'youtube.com',
				'domain:exact.example.org',
				'[off] 149.154.160.0/20 tcp:80,443 # Telegram',
				'2001:db8::/32',
				'old-removed.com',
			].join('\n'),
		);

		const editedText = [
			'# comment line ignored',
			'',
			'youtube.com',
			'domain:exact.example.org',
			'[off] 149.154.160.0/20 tcp:80,443 # Telegram',
			'2001:db8::/32',
			'*.discord.gg # Voice',
		].join('\n');

		let seq = 0;
		const parsed = parseRulesFromText(editedText, existing, () => `new-${++seq}`);

		expect(parsed.kept).toBe(4);
		expect(parsed.added).toBe(1);
		expect(parsed.removed).toBe(1);
		expect(parsed.disabled).toBe(1);
		expect(parsed.rules).toEqual([
			{ id: 'r1', type: 'namespace', rule: 'youtube.com', enable: true },
			{ id: 'r2', type: 'domain', rule: 'exact.example.org', enable: true, autolist: 'auto-1', autolistByDomain: true },
			{ id: 'r3', type: 'subnet', rule: '149.154.160.0/20', enable: false, proto: 'tcp', ports: '80,443', comment: 'Telegram' },
			{ id: 'r4', type: 'subnet6', rule: '2001:db8::/32', enable: true },
			{ id: 'new-1', type: 'wildcard', rule: '*.discord.gg', enable: true, comment: 'Voice' },
		]);
	});
});

function makeGroups(): FircGroup[] {
	return [
		{
			id: 'g1',
			name: 'Group 1',
			interface: 'tunvless0',
			enable: true,
			rules: [
				{ id: 'r1', type: 'subnet', rule: '10.0.0.0/8', enable: true, proto: 'tcp', ports: '443' },
				{ id: 'r2', type: 'namespace', rule: 'example.com', enable: true },
				{ id: 'r3', type: 'wildcard', rule: '*.cdn.net', enable: false },
			],
		},
		{
			id: 'g2',
			name: 'Group 2',
			interface: 'tunvless1',
			enable: true,
			rules: [{ id: 'r4', type: 'namespace', rule: 'keep.org', enable: true }],
		},
	];
}

describe('applyBulkRuleAction', () => {
	it('enables, disables, changes type (clearing subnet proto/ports), moves, and deletes rules', () => {
		let groups = makeGroups();

		const resDisable = applyBulkRuleAction(groups, 0, new Set(['r1', 'r2']), {
			kind: 'enable',
			enable: false,
		});
		expect(resDisable.affected).toBe(2);
		expect(resDisable.groups[0].rules.map((r) => r.enable)).toEqual([false, false, false]);

		const resType = applyBulkRuleAction(resDisable.groups, 0, new Set(['r1']), {
			kind: 'setType',
			type: 'namespace',
		});
		expect(resType.affected).toBe(1);
		expect(resType.groups[0].rules[0]).toEqual({
			id: 'r1',
			type: 'namespace',
			rule: '10.0.0.0/8',
			enable: false,
		});

		const resMove = applyBulkRuleAction(resType.groups, 0, new Set(['r2']), {
			kind: 'move',
			targetGroupId: 'g2',
		});
		expect(resMove.affected).toBe(1);
		expect(resMove.groups[0].rules.map((r) => r.id)).toEqual(['r1', 'r3']);
		expect(resMove.groups[1].rules.map((r) => r.id)).toEqual(['r2', 'r4']);

		const resDel = applyBulkRuleAction(resMove.groups, 0, new Set(['r1', 'r3']), {
			kind: 'delete',
		});
		expect(resDel.affected).toBe(2);
		expect(resDel.groups[0].rules).toEqual([]);
		groups = resDel.groups;
		expect(groups).toHaveLength(2);
	});
});

describe('applyBulkGroupAction', () => {
	it('toggles enable, sets single/chain interface, clones devices selector, and deletes groups', () => {
		const groups = makeGroups();

		const resDisable = applyBulkGroupAction(groups, new Set(['g1', 'g2']), {
			kind: 'enable',
			enable: false,
		});
		expect(resDisable.affected).toBe(2);
		expect(resDisable.groups.every((g) => !g.enable)).toBe(true);

		const resChain = applyBulkGroupAction(resDisable.groups, new Set(['g1']), {
			kind: 'setInterface',
			iface: ['tunvless0', 'tunvless1'],
		});
		expect(resChain.groups[0].interface).toEqual(['tunvless0', 'tunvless1']);
		expect(resChain.groups[0].check).toEqual({ mode: 'none', url: '' });

		const resChainWithCheck = applyBulkGroupAction(resChain.groups, new Set(['g1', 'g2']), {
			kind: 'setInterface',
			iface: ['qmi_br0', 'tunvless2'],
			check: { mode: 'url', url: 'https://cp.cloudflare.com/generate_204' },
		});
		expect(resChainWithCheck.affected).toBe(2);
		expect(resChainWithCheck.groups[0].check).toEqual({
			mode: 'url',
			url: 'https://cp.cloudflare.com/generate_204',
		});
		expect(resChainWithCheck.groups[1].check).toEqual({
			mode: 'url',
			url: 'https://cp.cloudflare.com/generate_204',
		});
		expect(resChainWithCheck.groups[0].check).not.toBe(resChainWithCheck.groups[1].check);

		const sharedDevices = { allow: ['mac:aa:bb:cc:dd:ee:01'], deny: [] };
		const resDevices = applyBulkGroupAction(resChain.groups, new Set(['g1', 'g2']), {
			kind: 'setDevices',
			devices: sharedDevices,
		});
		expect(resDevices.groups[0].devices).toEqual(sharedDevices);
		expect(resDevices.groups[1].devices).toEqual(sharedDevices);
		expect(resDevices.groups[0].devices).not.toBe(resDevices.groups[1].devices);

		const resDelete = applyBulkGroupAction(resDevices.groups, new Set(['g1']), {
			kind: 'delete',
		});
		expect(resDelete.affected).toBe(1);
		expect(resDelete.groups.map((g) => g.id)).toEqual(['g2']);
	});
});

describe('collapseRulesTextToRootDomains', () => {
	it('collapses subdomains & wildcards, drops domain: when collapsed, deduplicates, and promotes [off]', () => {
		const input = [
			'# header comment',
			'[off] cdn.example.com',
			'api.example.com # keeps enabled promotion',
			'domain:static.vendor.co.uk',
			'domain:exact.org',
			'*.media.youtube.com',
			'*.cache-*.googlevideo.com',
			'149.154.160.0/20 tcp:443 # Telegram',
			'^api\\.[0-9]+\\.claude\\.ai$',
		].join('\n');

		const res = collapseRulesTextToRootDomains(input);
		expect(res.collapsedCount).toBe(4);
		expect(res.deduplicatedCount).toBe(1);
		expect(res.total).toBe(7);
		expect(res.text).toBe(
			[
				'# header comment',
				'example.com # keeps enabled promotion',
				'vendor.co.uk',
				'domain:exact.org',
				'*.youtube.com',
				'*.cache-*.googlevideo.com',
				'149.154.160.0/20 tcp:443 # Telegram',
				'^api\\.[0-9]+\\.claude\\.ai$',
			].join('\n'),
		);
	});
});
