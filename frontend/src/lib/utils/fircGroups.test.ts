import { describe, expect, it } from 'vitest';
import type { FircGroup, FircGroupCheck, FircProxyGroup } from '$lib/types/firc';
import {
	applyProxyGroupToGroup,
	buildGroupCheckFromProxyGroup,
	groupLinksOf,
	groupFormFrom,
	groupFromForm,
	inferDraftProxyGroupCheck,
	managingProxyGroup,
	matchingProxyGroup,
	normalizeGroup,
	normalizeProxyGroupForSave,
	parseImportedGroups,
	proxyGroupsDraft,
	proxyGroupsDraftError,
	savePayload,
	serializeForDiff,
	setPrimaryIface,
	toggleBackupIface,
	withGroupLinks,
} from './fircGroups';

describe('normalizeGroup', () => {
	const base: FircGroup = { id: 'g1', name: 'CF', interface: 'nwg0', enable: true, rules: [] };

	it('keeps the cloudflare flag the daemon sent', () => {
		expect(normalizeGroup({ ...base, cloudflare: true }).cloudflare).toBe(true);
		expect('cloudflare' in normalizeGroup(base)).toBe(false);
	});
});

describe('proxy group check helpers', () => {
	it('prefers proxy group with non-null check when duplicate interface chains exist', () => {
		const groups: FircProxyGroup[] = [
			{ id: 'pg_legacy', name: 'Legacy', interfaces: ['qmi_br0', 'tunvless2'] },
			{
				id: 'pg_active',
				name: 'Configured',
				interfaces: ['qmi_br0', 'tunvless2'],
				check: { mode: 'url', url: 'https://example.test/204' },
			},
		];
		const matched = matchingProxyGroup(groups, { interface: ['qmi_br0', 'tunvless2'] });
		expect(matched?.id).toBe('pg_active');
	});

	it('rejects only newly created duplicate chains', () => {
		const first: FircProxyGroup = { id: 'pg1', name: 'First', interfaces: ['qmi_br0', 'tunvless2'] };
		const second: FircProxyGroup = { id: 'pg2', name: 'Second', interfaces: ['qmi_br0', 'tunvless2'] };
		const err = proxyGroupsDraftError([first, second], [first]);
		expect(err).toContain('First');
		expect(err).toContain('Second');
		// Одинаковые цепочки лежали и раньше — переименование одной из них проходит.
		expect(proxyGroupsDraftError([first, { ...second, name: 'Renamed' }], [first, second])).toBeNull();
		// Третья такая же — новый дубль.
		expect(proxyGroupsDraftError([first, second, { ...second, id: 'pg3' }], [first, second])).not.toBeNull();
		// Дублем стала правка цепочки существующей прокси-группы.
		expect(
			proxyGroupsDraftError([first, second], [first, { ...second, interfaces: ['eth3'] }]),
		).not.toBeNull();
	});

	it('infers, builds, and normalizes proxy group checks cleanly', () => {
		const inferred = inferDraftProxyGroupCheck(
			{ id: 'pg1', name: 'Main', interfaces: ['eth3', 'tunvless0'] },
			[
				{
					id: 'g1',
					name: 'Group1',
					interface: ['eth3', 'tunvless0'],
					enable: true,
					check: { mode: 'url', url: 'https://probe.example/204' },
					rules: [],
				},
			],
		);
		expect(inferred).toEqual({ mode: 'url', url: 'https://probe.example/204' });

		expect(
			buildGroupCheckFromProxyGroup({
				id: 'pg2',
				name: 'KD',
				interfaces: ['eth3', 'tunvless0'],
				check: { mode: 'keendns' },
			}),
		).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });

		expect(
			normalizeProxyGroupForSave({
				id: 'pg3',
				name: ' Single ',
				interfaces: ['eth3'],
				check: { mode: 'url', url: 'https://ignored.example' },
			}),
		).toEqual({
			id: 'pg3',
			name: 'Single',
			interfaces: ['eth3'],
			check: { mode: 'none' },
		});
	});

	it('keeps manual multi-iface chain check editable and delegates PG-backed chain check in groupFromForm', () => {
		const manualCur: FircGroup = {
			id: 'g_manual',
			name: 'Manual',
			interface: ['tunvless0', 'tunvless1'],
			enable: true,
			check: { mode: 'url', url: 'https://old.example/204' },
			rules: [],
		};
		const form = groupFormFrom(manualCur);
		form.checkMode = 'url';
		form.checkUrl = 'https://custom.example/204';

		const savedManual = groupFromForm(form, manualCur, 1, [
			{ id: 'pg_other', name: 'Other', interfaces: ['eth3', 'tunvless2'], check: { mode: 'none' } },
		]);
		expect(savedManual.check).toEqual({ mode: 'url', url: 'https://custom.example/204' });

		const managed: FircProxyGroup[] = [
			{
				id: 'pg_managed',
				name: 'Managed',
				interfaces: ['tunvless0', 'tunvless1'],
				check: { mode: 'keendns' },
			},
		];
		// Одной общей цепочки мало: проверку даёт только связь.
		expect(groupFromForm(form, manualCur, 1, managed).check).toEqual({
			mode: 'url',
			url: 'https://custom.example/204',
		});
		const linkedCur = { ...manualCur, proxyGroupId: 'pg_managed' };
		const savedManaged = groupFromForm(form, linkedCur, 1, managed);
		expect(savedManaged.check).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });
		expect(savedManaged.proxyGroupId).toBe('pg_managed');

		const moved = groupFromForm({ ...form, iface: 'tunvless1' }, linkedCur, 1, managed);
		expect(moved.interface).toEqual(['tunvless1', 'tunvless0']);
		expect(moved.proxyGroupId).toBeUndefined();
		expect(moved.check).toEqual({ mode: 'url', url: 'https://custom.example/204' });
	});

	const CHAIN = ['eth3', 'tunvless0'];
	const FULL: FircGroupCheck = {
		mode: 'url',
		url: 'https://probe.example/204',
		intervalSec: 30,
		timeoutMs: 4000,
		failThreshold: 4,
		recoverThreshold: 5,
	};
	const group = (id: string, check?: FircGroupCheck, iface: string | string[] = CHAIN): FircGroup => ({
		id,
		name: id,
		interface: iface,
		enable: true,
		rules: [],
		...(check ? { check } : {}),
	});
	const pg = (check?: FircGroupCheck, interfaces = CHAIN): FircProxyGroup => ({
		id: 'pg',
		name: 'PG',
		interfaces,
		...(check ? { check } : {}),
	});

	it('leaves a check-less proxy group without check when its chain has no groups', () => {
		expect(proxyGroupsDraft([pg()], [])[0].check).toBeUndefined();
		expect(proxyGroupsDraft([pg()], [group('g1', FULL, ['eth3', 'tunvless9'])])[0].check).toBeUndefined();
		expect('check' in normalizeProxyGroupForSave(proxyGroupsDraft([pg()], [])[0])).toBe(false);
	});

	it('infers the check only when every group on the chain agrees', () => {
		expect(inferDraftProxyGroupCheck(pg(), [group('g1', FULL), group('g2', { ...FULL })])).toEqual(FULL);
		expect(inferDraftProxyGroupCheck(pg(), [group('g1', FULL), group('g2', { mode: 'none' })])).toBeUndefined();
		expect(inferDraftProxyGroupCheck(pg(), [group('g1', FULL), group('g2')])).toBeUndefined();
		expect(
			inferDraftProxyGroupCheck(pg(), [group('g1', FULL), group('g2', { ...FULL, failThreshold: 1 })]),
		).toBeUndefined();
		// fircd отдаёт нули там, где сохранённый файл молчит.
		expect(
			inferDraftProxyGroupCheck(pg(), [
				group('g1', { mode: 'url', url: '', intervalSec: 0, timeoutMs: 0, failThreshold: 0, recoverThreshold: 0 }),
				group('g2', { mode: 'url' }),
			]),
		).toMatchObject({ mode: 'url', url: '' });
		// Выключено у всех — остаётся выключенным.
		expect(inferDraftProxyGroupCheck(pg(), [group('g1'), group('g2', { mode: 'none' })])?.mode).toBe('none');
	});

	it('uses the KeenDNS group only to confirm agreement', () => {
		const kd = group('c100d001', { mode: 'keendns', timeoutMs: 5000 });
		expect(inferDraftProxyGroupCheck(pg(), [kd])).toBeUndefined();
		expect(inferDraftProxyGroupCheck(pg(), [kd, group('g1', FULL)])).toBeUndefined();
		expect(inferDraftProxyGroupCheck(pg(), [kd, group('g1', { mode: 'keendns' })])?.mode).toBe('keendns');
	});

	it('carries every check field through draft, save and group', () => {
		expect(inferDraftProxyGroupCheck(pg(FULL), [])).toEqual(FULL);
		expect(normalizeProxyGroupForSave(pg(FULL)).check).toEqual(FULL);
		expect(buildGroupCheckFromProxyGroup(pg(FULL))).toEqual(FULL);
	});

	it('does not manage groups until the proxy group has a check', () => {
		const cur = {
			...group('g1', { mode: 'url', url: 'https://old.example/204' }),
			proxyGroupId: 'pg',
		};
		expect(managingProxyGroup([pg()], cur)).toBeUndefined();
		expect(managingProxyGroup([pg(FULL)], cur)?.id).toBe('pg');
		expect(managingProxyGroup([pg(FULL)], { ...cur, proxyGroupId: undefined })).toBeUndefined();
		expect(managingProxyGroup([pg(FULL, ['eth3', 'tunvless9'])], cur)).toBeUndefined();
		const form = groupFormFrom(cur);
		form.checkUrl = 'https://new.example/204';
		expect(groupFromForm(form, cur, 1, [pg()]).check).toEqual({ mode: 'url', url: 'https://new.example/204' });
	});

	it('links a group to the picked proxy group and drops the link on a manual chain edit', () => {
		const linked = () => {
			const g = group('g1', undefined, 'eth3');
			applyProxyGroupToGroup(g, pg(FULL));
			return g;
		};
		expect(linked().proxyGroupId).toBe('pg');
		// Показ: связанная прокси-группа важнее первой с той же цепочкой.
		expect(matchingProxyGroup([{ ...pg(FULL), id: 'twin' }, pg()], linked())?.id).toBe('pg');

		const same = linked();
		setPrimaryIface(same, 'eth3');
		expect(same.proxyGroupId).toBe('pg');

		for (const edit of [
			(g: FircGroup) => setPrimaryIface(g, 'tunvless0'),
			(g: FircGroup) => setPrimaryIface(g, 'blackhole'),
			(g: FircGroup) => toggleBackupIface(g, 'tunvless0'),
			(g: FircGroup) => toggleBackupIface(g, 'tunvless7'),
		]) {
			const g = linked();
			edit(g);
			expect(g.proxyGroupId).toBeUndefined();
		}
		const single = { ...group('g1', undefined, 'eth3'), proxyGroupId: 'one' };
		setPrimaryIface(single, 'tunvless0');
		expect(single.proxyGroupId).toBeUndefined();
		const blocked = { ...group('g1', undefined, 'block'), proxyGroupId: 'one' };
		toggleBackupIface(blocked, 'eth3');
		expect(blocked.proxyGroupId).toBeUndefined();
	});

	it('keeps links beside the groups, not in what goes to the daemon', () => {
		const groups = withGroupLinks([group('g1'), group('g2')], { g1: 'pg', gone: 'pg' });
		expect(groupLinksOf(groups)).toEqual({ g1: 'pg' });
		expect(groups.map(normalizeGroup).map((g) => g.proxyGroupId)).toEqual(['pg', undefined]);
		expect(JSON.stringify(savePayload(groups))).not.toContain('proxyGroupId');
		// Ext-конфиг не прочитан — связи черновика не теряются.
		expect(groupLinksOf(withGroupLinks(groups, null))).toEqual({ g1: 'pg' });
		expect(serializeForDiff(groups)).not.toBe(serializeForDiff(withGroupLinks(groups, {})));
		expect(parseImportedGroups(JSON.stringify({ groups }))[0].proxyGroupId).toBeUndefined();
	});

	it('never turns on a check disabled on an existing chain', () => {
		const off = () => group('g1', { mode: 'none', url: '' }, ['eth3', 'tunvless0', 'tunvless1']);

		const reordered = off();
		setPrimaryIface(reordered, 'tunvless0');
		expect(reordered.check?.mode).toBe('none');

		const shortened = off();
		toggleBackupIface(shortened, 'tunvless1');
		expect(shortened.check?.mode).toBe('none');

		const extended = off();
		toggleBackupIface(extended, 'tunvless2');
		expect(extended.check?.mode).toBe('none');

		const applied = off();
		applyProxyGroupToGroup(applied, pg());
		expect(applied.check?.mode).toBe('none');

		// Новая цепочка из одиночного интерфейса получает умолчание и не теряет порогов.
		const grown = group('g2', { ...FULL, mode: 'none' }, 'eth3');
		toggleBackupIface(grown, 'tunvless0');
		expect(grown.check).toEqual(FULL);
	});
});
