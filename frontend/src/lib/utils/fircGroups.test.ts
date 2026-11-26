import { describe, expect, it } from 'vitest';
import type { CatalogPreset } from '$lib/types';
import type { FircGroup, FircGroupCheck, FircInterfaceOption, FircProxyGroup } from '$lib/types/firc';
import {
	applyProxyGroupToGroup,
	buildAvailableInterfaces,
	buildBulkInterfaces,
	buildCatalogTunnelOptions,
	buildGroupCheckFromProxyGroup,
	buildProxyGroupSelectOptions,
	emptyGroupForm,
	getPickerGroups,
	groupLinksOf,
	groupFormFrom,
	groupFromForm,
	groupsFromPresets,
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
	syncGroupsWithProxyGroupChanges,
	syncRestoredDraftWithProxyGroups,
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

		const kdCur: FircGroup = {
			...manualCur,
			check: { mode: 'keendns', timeoutMs: 5000, intervalSec: 45 },
		};
		const kdForm = groupFormFrom(kdCur);
		kdForm.checkMode = 'url';
		kdForm.checkUrl = 'https://probe.example/204';
		expect(groupFromForm(kdForm, kdCur, 1, []).check).toEqual({
			mode: 'url',
			url: 'https://probe.example/204',
			intervalSec: 45,
		});
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

	it('creates and edits groups directly with a Fallback proxy group in groupForm', () => {
		const managedPg = pg(FULL);
		const checklessPg = { id: 'pg_free', name: 'Free', interfaces: ['tunvless0', 'tunvless1'] };
		const singlePg = { id: 'pg_one', name: 'One', interfaces: ['tunvless0'] };
		const pgs: FircProxyGroup[] = [managedPg, checklessPg, singlePg];

		// Создание новой группы сразу на прокси-группу с проверкой.
		const createdManaged = groupFromForm(
			{ ...emptyGroupForm('pg:pg'), name: 'Streaming' },
			undefined,
			0,
			pgs,
		);
		expect(createdManaged.interface).toEqual(CHAIN);
		expect(createdManaged.proxyGroupId).toBe('pg');
		expect(createdManaged.check).toEqual(FULL);

		// Создание новой группы на прокси-группу без своей проверки использует форму.
		const createdFree = groupFromForm(
			{ ...emptyGroupForm('pg:pg_free'), name: 'Custom', checkMode: 'url', checkUrl: 'https://my.example/204' },
			undefined,
			1,
			pgs,
		);
		expect(createdFree.interface).toEqual(['tunvless0', 'tunvless1']);
		expect(createdFree.proxyGroupId).toBe('pg_free');
		expect(createdFree.check).toEqual({ mode: 'url', url: 'https://my.example/204' });

		// Прокси-группа с одним интерфейсом привязывается без check.
		const createdSingle = groupFromForm(emptyGroupForm('pg:pg_one'), undefined, 2, pgs);
		expect(createdSingle.interface).toBe('tunvless0');
		expect(createdSingle.proxyGroupId).toBe('pg_one');
		expect(createdSingle.check).toBeUndefined();

		// groupFormFrom показывает `pg:<id>` для связанной группы и обычный интерфейс для несвязанной.
		expect(groupFormFrom(createdManaged, pgs).iface).toBe('pg:pg');
		expect(groupFormFrom(group('unlinked', FULL, CHAIN), pgs).iface).toBe('eth3');

		// Редактирование: переключение одиночного правила на прокси-группу и обратно на другую.
		const singleCur = group('g_single', undefined, 'tunvless2');
		const switchedToPg = groupFromForm({ ...groupFormFrom(singleCur, pgs), iface: 'pg:pg' }, singleCur, 1, pgs);
		expect(switchedToPg.interface).toEqual(CHAIN);
		expect(switchedToPg.proxyGroupId).toBe('pg');
		expect(switchedToPg.check).toEqual(FULL);

		const switchedToOther = groupFromForm(
			{ ...groupFormFrom(switchedToPg, pgs), iface: 'pg:pg_one' },
			switchedToPg,
			1,
			pgs,
		);
		expect(switchedToOther.interface).toBe('tunvless0');
		expect(switchedToOther.proxyGroupId).toBe('pg_one');
		expect(switchedToOther.check).toEqual({ ...FULL, mode: 'none', url: '' });

		// Явное переключение с Fallback-группы на одиночный интерфейс (в т.ч. на её же primary или другой) отвязывает правило.
		const detachedPrimary = groupFromForm(
			{ ...groupFormFrom(switchedToPg, pgs), iface: 'eth3' },
			switchedToPg,
			1,
			pgs,
		);
		expect(detachedPrimary.interface).toBe('eth3');
		expect(detachedPrimary.proxyGroupId).toBeUndefined();
		expect(detachedPrimary.check?.mode).toBe('none');

		const detachedOther = groupFromForm(
			{ ...groupFormFrom(switchedToPg, pgs), iface: 'tunvless2' },
			switchedToPg,
			1,
			pgs,
		);
		expect(detachedOther.interface).toBe('tunvless2');
		expect(detachedOther.proxyGroupId).toBeUndefined();
		expect(detachedOther.check?.mode).toBe('none');

		// Устаревшее значение `pg:missing` не протекает в interface.
		const staleEdit = groupFromForm({ ...groupFormFrom(singleCur, pgs), iface: 'pg:missing' }, singleCur, 1, pgs);
		expect(staleEdit.interface).toBe('tunvless2');
		expect(staleEdit.proxyGroupId).toBeUndefined();
		expect(groupFromForm(emptyGroupForm('pg:missing'), undefined, 0, pgs).interface).toBe('block');
	});

	it('includes Fallback proxy groups in catalog options and groupsFromPresets', () => {
		const ifaces: FircInterfaceOption[] = [
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
			{ id: 'eth3', name: 'ISP', role: 'wan' },
		];
		const pgs: FircProxyGroup[] = [
			pg(FULL),
			{ id: 'pg_free', name: 'Free', interfaces: ['tunvless0', 'eth3'] },
			{ id: 'pg_empty', name: 'Empty', interfaces: [] },
		];

		const pgOpts = buildProxyGroupSelectOptions(pgs, ifaces);
		expect(pgOpts.map((o) => ({ value: o.value, isChecklessChain: o.isChecklessChain }))).toEqual([
			{ value: 'pg:pg', isChecklessChain: false },
			{ value: 'pg:pg_free', isChecklessChain: true },
		]);

		// Первым (выбор окна каталога по умолчанию) остаётся туннель; прокси-группы —
		// после туннелей и WAN, перед блокировкой.
		const catalogOpts = buildCatalogTunnelOptions(ifaces, pgs);
		expect(catalogOpts.map((o) => o.value)).toEqual([
			'tunvless0',
			'eth3',
			'pg:pg',
			'pg:pg_free',
			'blackhole',
			'block',
		]);

		const preset: CatalogPreset = {
			id: 'yt',
			name: 'YouTube',
			category: 'media',
			engines: { singbox: { ruleSets: [{ tag: 'yt', url: 'https://example.test/yt.srs' }] } },
		} as CatalogPreset;

		const fromManaged = groupsFromPresets([preset], 'pg:pg', pgs);
		expect(fromManaged).toHaveLength(1);
		expect(fromManaged[0].interface).toEqual(CHAIN);
		expect(fromManaged[0].proxyGroupId).toBe('pg');
		expect(fromManaged[0].check).toEqual(FULL);

		const fromFree = groupsFromPresets([preset], 'pg:pg_free', pgs);
		expect(fromFree[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(fromFree[0].proxyGroupId).toBe('pg_free');
		expect(fromFree[0].check).toEqual({ mode: 'url', url: '' });

		// Устаревшее `pg:missing` не протекает в interface.
		expect(groupsFromPresets([preset], 'pg:missing', pgs)).toEqual([]);
	});

	it('propagates proxy group chain edits into drafts without overwriting diverged or unlinked groups', () => {
		const prevPg = pg(FULL);
		const reorderedPg = pg({ ...FULL, url: 'https://new.example/204' }, ['tunvless0', 'eth3']);
		const linked = { ...group('g1', FULL, CHAIN), proxyGroupId: 'pg' };
		const unlinked = group('g2', FULL, CHAIN);
		const diverged = { ...group('g3', FULL, ['eth3', 'tunvless9']), proxyGroupId: 'pg' };

		const synced = syncGroupsWithProxyGroupChanges([linked, unlinked, diverged], [prevPg], [reorderedPg]);
		expect(synced.changed).toBe(true);
		expect(synced.groups[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(synced.groups[0].proxyGroupId).toBe('pg');
		expect(synced.groups[0].check?.url).toBe('https://new.example/204');
		expect(synced.groups[1].interface).toEqual(CHAIN);
		expect(synced.groups[2].interface).toEqual(['eth3', 'tunvless9']);

		// Сужение до одного интерфейса отключает проверку, но сохраняет связь.
		const shrunk = syncGroupsWithProxyGroupChanges(
			synced.groups,
			[reorderedPg],
			[pg({ mode: 'none' }, ['tunvless0'])],
		);
		expect(shrunk.groups[0].interface).toBe('tunvless0');
		expect(shrunk.groups[0].proxyGroupId).toBe('pg');
		expect(shrunk.groups[0].check?.mode).toBe('none');

		// Удаление прокси-группы снимает связь, не трогая интерфейсы.
		const deleted = syncGroupsWithProxyGroupChanges(shrunk.groups, [pg({ mode: 'none' }, ['tunvless0'])], []);
		expect(deleted.groups[0].interface).toBe('tunvless0');
		expect(deleted.groups[0].proxyGroupId).toBeUndefined();

		// Восстановленный черновик: обновляется новая несохранённая группа, группа, уже обновлённая на сервере,
		// и существующая несвязанная группа, которую пользователь привязал к прокси-группе в черновике;
		// старая расходящаяся группа не перетирается.
		const serverGroups = [
			{ ...group('g1', reorderedPg.check, ['tunvless0', 'eth3']), proxyGroupId: 'pg' },
			group('g2', undefined, 'eth3'),
			{ ...group('g3', FULL, ['eth3', 'tunvless9']), proxyGroupId: 'pg' },
		];
		const draftNewlyLinked = { ...group('g2', FULL, CHAIN), proxyGroupId: 'pg' };
		const draftNew = { ...group('g_new', FULL, CHAIN), proxyGroupId: 'pg' };
		const restored = syncRestoredDraftWithProxyGroups(
			[linked, draftNewlyLinked, diverged, draftNew],
			serverGroups,
			[reorderedPg],
		);
		expect(restored[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(restored[1].interface).toEqual(['tunvless0', 'eth3']);
		expect(restored[1].check?.url).toBe('https://new.example/204');
		expect(restored[2].interface).toEqual(['eth3', 'tunvless9']);
		expect(restored[3].interface).toEqual(['tunvless0', 'eth3']);
		expect(restored[3].proxyGroupId).toBe('pg');

		// Спецгруппа KeenDNS (c100d001): при сужении PG до 1 интерфейса связь снимается, как на бэкенде.
		const kdDraft = { ...group('c100d001', FULL, CHAIN), proxyGroupId: 'pg' };
		const kdShrunk = syncGroupsWithProxyGroupChanges([kdDraft], [prevPg], [pg({ mode: 'none' }, ['eth3'])]);
		expect(kdShrunk.groups[0].interface).toBe('eth3');
		expect(kdShrunk.groups[0].proxyGroupId).toBeUndefined();

		const kdRestored = syncRestoredDraftWithProxyGroups(
			[kdDraft],
			[group('c100d001', { mode: 'none' }, 'eth3')],
			[pg({ mode: 'none' }, ['eth3'])],
		);
		expect(kdRestored[0].interface).toBe('eth3');
		expect(kdRestored[0].proxyGroupId).toBeUndefined();

		// Спецгруппа KeenDNS без proxyGroupId (cloudHealthCheck или рост 1 → 2):
		// подхватывает актуальную цепочку, связь и проверку с сервера без фантомного diff.
		const kdCloudDraft = group('c100d001', { mode: 'keendns', timeoutMs: 5000 }, CHAIN);
		const kdCloudServer = group('c100d001', { mode: 'keendns', timeoutMs: 5000 }, ['tunvless0', 'eth3']);
		const kdCloudRestored = syncRestoredDraftWithProxyGroups([kdCloudDraft], [kdCloudServer], [reorderedPg]);
		expect(kdCloudRestored[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(kdCloudRestored[0].proxyGroupId).toBeUndefined();

		const kdSingleDraft = group('c100d001', { mode: 'none' }, 'eth3');
		const kdGrownServer = { ...group('c100d001', FULL, CHAIN), proxyGroupId: 'pg' };
		const kdGrownRestored = syncRestoredDraftWithProxyGroups([kdSingleDraft], [kdGrownServer], [prevPg]);
		expect(kdGrownRestored[0].interface).toEqual(CHAIN);
		expect(kdGrownRestored[0].proxyGroupId).toBe('pg');
		expect(kdGrownRestored[0].check).toEqual(FULL);

		// Global (c100d002): при сужении PG до 1 интерфейса с check:{mode:'none'} связь сохраняется,
		// как в backend specialGroupLinks, а при снятии ссылки на сервере унаследованный check очищается.
		const globalDraft = { ...group('c100d002', FULL, CHAIN), proxyGroupId: 'pg' };
		const globalShrunk = syncGroupsWithProxyGroupChanges(
			[globalDraft],
			[prevPg],
			[pg({ mode: 'none' }, ['eth3'])],
		);
		expect(globalShrunk.groups[0].interface).toBe('eth3');
		expect(globalShrunk.groups[0].proxyGroupId).toBe('pg');

		const globalUnlinkedServer = group('c100d002', { mode: 'none' }, ['tunvless0', 'eth3']);
		const globalRestored = syncRestoredDraftWithProxyGroups([globalDraft], [globalUnlinkedServer], [reorderedPg]);
		expect(globalRestored[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(globalRestored[0].proxyGroupId).toBeUndefined();
		expect(globalRestored[0].check).toEqual({ mode: 'none', url: '' });

		// Смонтированный черновик: спецгруппы KeenDNS (cloudHealthCheck без proxyGroupId) и Global (на PG без check)
		// тоже обновляют порядок туннелей при передаче nextExt.
		const nextSpecialExt = {
			proxyGroups: [reorderedPg],
			globalTarget: 'pg:pg',
			keenDnsCloud: { enabled: true, cloudHealthCheck: true, proxyGroupId: 'pg', interfaces: ['tunvless0', 'eth3'] },
			serverRouting: { enabledServers: [] },
			groupLinks: {},
		};
		const specialSynced = syncGroupsWithProxyGroupChanges(
			[
				group('c100d001', { mode: 'keendns', timeoutMs: 5000 }, CHAIN),
				group('c100d002', { mode: 'none' }, CHAIN),
			],
			[prevPg],
			[reorderedPg],
			nextSpecialExt,
		);
		expect(specialSynced.changed).toBe(true);
		expect(specialSynced.groups[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(specialSynced.groups[1].interface).toEqual(['tunvless0', 'eth3']);

		// KeenDNS с cloudHealthCheck: рост PG 1 -> 2 интерфейса выставляет check:{mode:'keendns',timeoutMs:5000},
		// а внешнее включение cloudHealthCheck на PG-связанной KeenDNS снимает proxyGroupId и включает keendns-проверку.
		const kdGrown = syncGroupsWithProxyGroupChanges(
			[group('c100d001', { mode: 'none' }, 'eth3')],
			[pg({ mode: 'none' }, ['eth3'])],
			[reorderedPg],
			nextSpecialExt,
		);
		expect(kdGrown.changed).toBe(true);
		expect(kdGrown.groups[0].interface).toEqual(['tunvless0', 'eth3']);
		expect(kdGrown.groups[0].proxyGroupId).toBeUndefined();
		expect(kdGrown.groups[0].check).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });

		const kdCloudToggled = syncGroupsWithProxyGroupChanges(
			[{ ...group('c100d001', FULL, ['tunvless0', 'eth3']), proxyGroupId: 'pg' }],
			[reorderedPg],
			[reorderedPg],
			nextSpecialExt,
		);
		expect(kdCloudToggled.changed).toBe(true);
		expect(kdCloudToggled.groups[0].proxyGroupId).toBeUndefined();
		expect(kdCloudToggled.groups[0].check).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });

		// Смена цели спецгрупп Global/KeenDNS на прокси-группу без check или одиночный интерфейс:
		// устаревший proxyGroupId и унаследованный check сбрасываются, чтобы сохранение черновика их не воскрешало.
		const checklessPg2: FircProxyGroup = { id: 'pg2', name: 'PG2', interfaces: ['eth3', 'tunvless9'] };
		const switchedExt = {
			proxyGroups: [reorderedPg, checklessPg2],
			globalTarget: 'pg:pg2',
			keenDnsCloud: { enabled: true, cloudHealthCheck: false, proxyGroupId: 'pg2', interfaces: [] },
			serverRouting: { enabledServers: [] },
			groupLinks: {},
		};
		const specialRetargeted = syncGroupsWithProxyGroupChanges(
			[
				{ ...group('c100d001', FULL, ['tunvless0', 'eth3']), proxyGroupId: 'pg' },
				{ ...group('c100d002', FULL, ['tunvless0', 'eth3']), proxyGroupId: 'pg' },
			],
			[reorderedPg, checklessPg2],
			[reorderedPg, checklessPg2],
			switchedExt,
		);
		expect(specialRetargeted.changed).toBe(true);
		expect(specialRetargeted.groups[0].interface).toEqual(['eth3', 'tunvless9']);
		expect(specialRetargeted.groups[0].proxyGroupId).toBeUndefined();
		expect(specialRetargeted.groups[0].check).toEqual({ mode: 'none', url: '' });
		expect(specialRetargeted.groups[1].interface).toEqual(['eth3', 'tunvless9']);
		expect(specialRetargeted.groups[1].proxyGroupId).toBeUndefined();
		expect(specialRetargeted.groups[1].check).toEqual({ mode: 'none', url: '' });

		// Global переключён на iface:<id>, а у KeenDNS выключен cloudHealthCheck на PG без check.
		const globalIfaceAndCloudOffExt = {
			proxyGroups: [reorderedPg, checklessPg2],
			globalTarget: 'iface:tunvless2',
			keenDnsCloud: { enabled: true, cloudHealthCheck: false, proxyGroupId: 'pg2', interfaces: [] },
			serverRouting: { enabledServers: [] },
			groupLinks: {},
		};
		const specialOff = syncGroupsWithProxyGroupChanges(
			[
				group('c100d001', { mode: 'keendns', url: '', timeoutMs: 5000 }, ['eth3', 'tunvless9']),
				{ ...group('c100d002', FULL, ['tunvless0', 'eth3']), proxyGroupId: 'pg' },
			],
			[reorderedPg, checklessPg2],
			[reorderedPg, checklessPg2],
			globalIfaceAndCloudOffExt,
		);
		expect(specialOff.changed).toBe(true);
		expect(specialOff.groups[0].check).toEqual({ mode: 'none', url: '' });
		expect(specialOff.groups[1].interface).toBe('tunvless2');
		expect(specialOff.groups[1].proxyGroupId).toBeUndefined();
		expect(specialOff.groups[1].check).toEqual({ mode: 'none', url: '' });

		// syncRestoredDraftWithProxyGroups также сбрасывает keendns-проверку при выключении cloudHealthCheck.
		const kdCloudOffRestored = syncRestoredDraftWithProxyGroups(
			[group('c100d001', { mode: 'keendns', url: '', timeoutMs: 5000 }, ['eth3', 'tunvless9'])],
			[group('c100d001', { mode: 'none', url: '' }, ['eth3', 'tunvless9'])],
			[checklessPg2],
		);
		expect(kdCloudOffRestored[0].check).toEqual({ mode: 'none', url: '' });
	});
});

describe('Proxy-интерфейсы t2s* в выборе выхода', () => {
	const interfaces: FircInterfaceOption[] = [
		{ id: 'eth3', name: 'Broadband', role: 'wan' },
		{ id: 't2s10', name: 'LateSocks', role: 'tunnel' },
		{ id: 't2s1', name: 'Proxy1', role: 'tunnel' },
		{ id: 't2s0', ndmsName: 'Proxy0', role: 'tunnel' },
		{ id: 't2s2', name: 'SingboxOwned', role: 'tunnel', awgmSingboxProxy: true },
		{ id: 't2s3', name: 'VOX', role: 'tunnel', awgmSingboxProxy: true },
		{ id: 'nwg0', name: 'Office VPN', role: 'tunnel' },
		{ id: 'tunvless0', name: 'DE VLESS', role: 'tunnel' },
		{ id: 'dns', role: 'other' },
	];
	const group = (iface: string): FircGroup => ({
		id: 'g1',
		name: 'G',
		interface: iface,
		enable: true,
		rules: [],
	});

	it('показывает системные t2s* в детерминированном порядке, а прокси sing-box скрывает если не выбраны', () => {
		const res = getPickerGroups(interfaces, group('nwg0'));
		expect(res.tunnels).toEqual([
			{ id: 'tunvless0', label: 'tunvless0 — DE VLESS', role: 'tunnel' },
			{ id: 'nwg0', label: 'nwg0 — Office VPN', role: 'tunnel' },
			{ id: 't2s0', label: 't2s0 — Proxy0', role: 'tunnel' },
			{ id: 't2s1', label: 't2s1 — Proxy1', role: 'tunnel' },
			{ id: 't2s10', label: 't2s10 — LateSocks', role: 'tunnel' },
		]);
	});

	it('сохраняет системные t2s* в списке после снятия выбора, а sing-box прокси показывает только пока выбран', () => {
		const pickedNative = getPickerGroups(interfaces, group('t2s0')).tunnels.map((t) => t.id);
		const unpickedNative = getPickerGroups(interfaces, group('nwg0')).tunnels.map((t) => t.id);
		expect(pickedNative).toContain('t2s0');
		expect(unpickedNative).toContain('t2s0');

		const pickedSingbox = getPickerGroups(interfaces, group('t2s2')).tunnels.map((t) => t.id);
		const unpickedSingbox = getPickerGroups(interfaces, group('nwg0')).tunnels.map((t) => t.id);
		expect(pickedSingbox).toContain('t2s2');
		expect(unpickedSingbox).not.toContain('t2s2');
		expect(unpickedSingbox).not.toContain('t2s3');
	});

	it('предлагает системные t2s* в форме группы, массовой смене и каталоге в едином порядке', () => {
		const avail = buildAvailableInterfaces(interfaces, 'nwg0');
		expect(avail.map((o) => o.value)).toEqual([
			'tunvless0',
			'nwg0',
			't2s0',
			't2s1',
			't2s10',
			'eth3',
			'blackhole',
			'block',
			'dns',
		]);
		expect(avail.find((o) => o.value === 't2s0')?.label).toBe('t2s0 — Proxy0');
		expect(buildAvailableInterfaces(interfaces, 't2s2').map((o) => o.value)).toContain('t2s2');

		const bulk = buildBulkInterfaces(interfaces, [group('nwg0')]);
		expect(bulk.map((o) => o.value)).toEqual([
			'tunvless0',
			'nwg0',
			't2s0',
			't2s1',
			't2s10',
			'eth3',
			'blackhole',
			'block',
			'dns',
		]);

		const catalog = buildCatalogTunnelOptions(interfaces);
		expect(catalog.map((o) => o.value)).toEqual([
			'tunvless0',
			'nwg0',
			't2s0',
			't2s1',
			't2s10',
			'eth3',
			'blackhole',
			'block',
		]);
		expect(catalog.find((o) => o.value === 't2s0')?.label).toBe('t2s0 — Proxy0');
	});

	it('позволяет снимать выбор с отдельных t2s* в цепочке Xray и возвращать их обратно', () => {
		const xrayIfaces: FircInterfaceOption[] = [
			{ id: 't2s0', name: '!Xray_Nuxt_t2s0', ndmsName: 'Proxy0', role: 'tunnel' },
			{ id: 't2s2', name: '!Xray_OD_PRG_t2s2', ndmsName: 'Proxy2', role: 'tunnel' },
			{ id: 't2s4', name: '!Xray_OD_Hels_t2s4', ndmsName: 'Proxy4', role: 'tunnel' },
		];
		const chainGroup: FircGroup = {
			id: 'g-xray',
			name: 'Xray Chain',
			interface: ['t2s0', 't2s4', 't2s2'],
			enable: true,
			rules: [],
		};

		expect(getPickerGroups(xrayIfaces, chainGroup).tunnels.map((t) => t.id)).toEqual([
			't2s0',
			't2s2',
			't2s4',
		]);

		toggleBackupIface(chainGroup, 't2s2');
		expect(chainGroup.interface).toEqual(['t2s0', 't2s4']);
		expect(getPickerGroups(xrayIfaces, chainGroup).tunnels.map((t) => t.id)).toEqual([
			't2s0',
			't2s2',
			't2s4',
		]);

		toggleBackupIface(chainGroup, 't2s4');
		expect(chainGroup.interface).toBe('t2s0');
		expect(getPickerGroups(xrayIfaces, chainGroup).tunnels.map((t) => t.id)).toEqual([
			't2s0',
			't2s2',
			't2s4',
		]);

		toggleBackupIface(chainGroup, 't2s2');
		expect(chainGroup.interface).toEqual(['t2s0', 't2s2']);
	});
});
