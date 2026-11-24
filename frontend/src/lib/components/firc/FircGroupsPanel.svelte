<script lang="ts">
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { onMount, onDestroy, untrack } from 'svelte';
	import { get } from 'svelte/store';
	import { api } from '$lib/api/client';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		reloadFircAll,
		fircSyncNonce,
		fircInterfaceProbeResults,
		fircProbeResults,
	} from '$lib/stores/firc';
	import { fircMigrationAppliedNonce } from '$lib/stores/fircMigrationModal';
	import { Button, Modal, ConfirmModal } from '$lib/components/ui';
	import { RotateCcw } from 'lucide-svelte';
	import FircGroupDevicesModal from './FircGroupDevicesModal.svelte';
	import FircBulkGroupsBar from './FircBulkGroupsBar.svelte';
	import FircBulkRulesEditorModal from './FircBulkRulesEditorModal.svelte';
	import FircGroupsToolbar from './FircGroupsToolbar.svelte';
	import FircFallbackPanel from './FircFallbackPanel.svelte';
	import FircGroupCompactCard from './FircGroupCompactCard.svelte';
	import FircGroupRow from './FircGroupRow.svelte';
	import FircGroupRulesSection from './FircGroupRulesSection.svelte';
	import FircGroupSettingsModal from './FircGroupSettingsModal.svelte';
	import FircImportRulesModal from './FircImportRulesModal.svelte';
	import FircProxyGroupsModal from './FircProxyGroupsModal.svelte';
	import { createFircProbes } from './fircProbes.svelte';
	import { createFircListPages } from './fircListPages.svelte';
	import { createFircGroupForm } from './fircGroupForm.svelte';
	import { createFircGroupSelection } from './fircGroupSelection.svelte';
	import {
		applyBulkGroupAction,
		applyBulkRuleAction,
		type BulkGroupAction,
		type BulkRuleAction,
	} from '$lib/utils/fircBulkRules';
	import { ServiceCatalogModal } from '$lib/components/dnsroutes';
	import { presetCatalog, loadPresetCatalog } from '$lib/stores/presets';
	import { singboxRouterCatalogPresetFilter } from '$lib/utils/catalog-preset';
	import type { CatalogPreset } from '$lib/types';
	import {
		saveFircGroupsDraft,
		loadFircGroupsDraft,
		clearFircGroupsDraft,
	} from '$lib/stores/fircDraft';
	import { friendlyIfaceLabel } from '$lib/utils/fircFallback';
	import {
		MANAGED_SERVER_SUBNETS,
		MAX_GROUP_IFACES,
		allFallbackInterfaceIds,
		buildAvailableInterfaces,
		buildCatalogTunnelOptions,
		buildGroupCheckFromProxyGroup,
		domainCopyNotice,
		emptyExtConfig,
		emptyGroupForm,
		existingCatalogNames,
		existingCatalogRuleSetTags,
		filterGroups,
		groupAccentClass,
		groupFormFrom,
		groupFromForm,
		groupServiceKey,
		groupsFromPresets,
		isSpecialIface,
		matchingProxyGroup,
		normalizeExtConfig,
		normalizeGroup,
		normalizeIfaceList,
		normalizeProxyGroupForSave,
		parseImportedGroups,
		parseRulesText,
		presetSrsUrl,
		proxyGroupsDraft,
		proxyGroupsDraftError,
		randomHexId,
		savePayload,
		serializeForDiff,
		type FircGroupsRuntime,
	} from '$lib/utils/fircGroups';
	import {
		extractFircGroupDomainLines,
		type FircDeviceSelector,
		type FircGroup,
		type FircGroupCheck,
		type FircGroupLive,
		type FircHost,
		type FircInterfaceOption,
		type FircPolicy,
		type FircRule,
		type FircSystemResolver,
		type FircTunnelState,
		type FircExtConfig,
		type FircProxyGroup,
	} from '$lib/types/firc';

	let groups = $state<FircGroup[]>([]);
	let groupView = $state<'grid' | 'list'>('grid');
	function setGroupView(value: 'grid' | 'list') {
		groupView = value;
		try { localStorage.setItem('awgm.firc.groupView', value); } catch { /* Storage may be unavailable. */ }
	}
	let baselineGroups = $state<FircGroup[]>([]);
	let baselineJson = $state('[]');
	let draftedAt = $state<string | null>(null);
	let confirmDiscardOpen = $state(false);
	let liveMap = $state<Record<string, FircGroupLive>>({});
	let netfilterState = $state<{ dirty: boolean; since?: number; error?: string } | undefined>(
		undefined,
	);
	let interfaces = $state<FircInterfaceOption[]>([]);
	let tunnelsState = $state<FircTunnelState[]>([]);
	let hosts = $state<FircHost[]>([]);
	let policies = $state<FircPolicy[]>([]);
	let resolvers = $state<FircSystemResolver[]>([]);
	let extConfig = $state<FircExtConfig>(emptyExtConfig());
	let proxyGroupsModalOpen = $state(false);
	let proxyGroupRefresh = 0;

	async function refreshProxyGroups() {
		const request = ++proxyGroupRefresh;
		try {
			const saved = await api.fircGetExt();
			if (request !== proxyGroupRefresh) return false;
			extConfig = saved;
			return true;
		} catch (error) {
			if (request === proxyGroupRefresh) {
				notifications.error(error instanceof Error ? error.message : String(error));
			}
			return false;
		}
	}
	let pgDraft = $state<FircProxyGroup[]>([]);
	let pgSaving = $state(false);

	let loading = $state(true);
	let saving = $state(false);
	let syncingGroup = $state<Record<string, boolean>>({});
	const probes = createFircProbes(
		(id) => friendlyIfaceLabel(id, interfaces, tunnelsState),
		() => {
			void pollLiveState();
		},
	);
	const probeFallbackInterfaces = probes.probe;
	let searchQuery = $state('');
	let expandedGroups = $state<Record<string, boolean>>({});
	let rulePageMap = $state<Record<string, number>>({});
	const listPages = createFircListPages();
	let openPickerGroupId = $state<string | null>(null);
	let showOthersMap = $state<Record<string, boolean>>({});

	let stopLivePoll: (() => void) | null = null;
	let lastHandledMigrationNonce = get(fircMigrationAppliedNonce);
	let lastHandledSyncNonce = get(fircSyncNonce);

	let rt = $derived<FircGroupsRuntime>({
		groups,
		liveMap,
		tunnelsState,
		interfaces,
		resolvers,
		proxyGroups: extConfig.proxyGroups,
		ifaceProbes: $fircInterfaceProbeResults,
		tunnelProbes: $fircProbeResults,
	});

	$effect(() => {
		const migNonce = $fircMigrationAppliedNonce;
		const syncNonce = $fircSyncNonce;
		if (migNonce === lastHandledMigrationNonce && syncNonce === lastHandledSyncNonce) return;
		lastHandledMigrationNonce = migNonce;
		lastHandledSyncNonce = syncNonce;
		untrack(() => {
			if (!loading && !saving && !serviceCatalogSubmitting && !isDirty) {
				void loadAll();
			}
		});
	});

	let isDirty = $derived(serializeForDiff(groups) !== baselineJson || listPages.hasEdits);

	$effect(() => {
		if (loading) return;
		if (isDirty) {
			draftedAt = saveFircGroupsDraft(groups, baselineJson, draftedAt);
		} else {
			draftedAt = null;
			clearFircGroupsDraft();
		}
	});

	function handleBeforeUnload(e: BeforeUnloadEvent) {
		if (isDirty) {
			e.preventDefault();
			e.returnValue = '';
		}
	}

	/** Сохранённые группы становятся базой сравнения, черновик сбрасывается. */
	function acceptSavedGroups(normalized: FircGroup[]) {
		groups = normalized;
		baselineGroups = normalized.map(normalizeGroup);
		baselineJson = serializeForDiff(normalized);
		draftedAt = null;
		clearFircGroupsDraft();
	}

	function resetAllChanges() {
		groups = baselineGroups.map(normalizeGroup);
		listPages.clearEdits();
		draftedAt = null;
		clearFircGroupsDraft();
		confirmDiscardOpen = false;
		notifications.info(m.firc_groups_toast_discarded());
	}

	async function loadAll() {
		loading = true;
		try {
			const [groupsRes, stateRes, ifacesRes, tunStateRes, hostsRes, policiesRes, resolversRes, extRes] =
				await Promise.all([
					api.fircGetGroups(),
					api.fircGetGroupsState().catch(() => ({ groups: {} })),
					api.fircGetInterfaces().catch(() => []),
					api.fircGetTunnelsState().catch(() => []),
					api.fircGetHosts().catch(() => []),
					api.fircGetPolicies().catch(() => []),
					api.fircGetResolvers().catch(() => []),
					api.fircGetExt().catch(() => null),
				]);
			const normalized = groupsRes.map(normalizeGroup);
			baselineGroups = normalized.map(normalizeGroup);
			baselineJson = serializeForDiff(normalized);

			const existingDraft = loadFircGroupsDraft();
			if (
				existingDraft &&
				Array.isArray(existingDraft.groups) &&
				serializeForDiff(existingDraft.groups.map(normalizeGroup)) !== baselineJson
			) {
				groups = existingDraft.groups.map(normalizeGroup);
				draftedAt = existingDraft.draftedAt || new Date().toISOString();
			} else {
				groups = normalized;
				draftedAt = null;
				clearFircGroupsDraft();
			}
			liveMap = stateRes.groups ?? {};
			netfilterState = 'netfilter' in stateRes ? stateRes.netfilter : undefined;
			interfaces = ifacesRes;
			tunnelsState = tunStateRes;
			hosts = hostsRes;
			policies = policiesRes;
			resolvers = resolversRes;
			if (extRes) extConfig = normalizeExtConfig(extRes);

			const nowSec = Math.floor(Date.now() / 1000);
			const existingProbes = get(fircInterfaceProbeResults);
			const staleIfaces = allFallbackInterfaceIds(extConfig.proxyGroups, groups).filter((id) => {
				const prev = existingProbes[id];
				return !prev?.checkedAt || nowSec - prev.checkedAt > 60;
			});
			if (staleIfaces.length > 0) {
				void probeFallbackInterfaces(staleIfaces, { silent: true });
			}
		} catch (e) {
			notifications.error(m.firc_groups_toast_load_error({ error: (e as Error).message }));
		} finally {
			loading = false;
		}
	}

	async function pollLiveState() {
		try {
			const [stateRes, tunStateRes, ifacesRes] = await Promise.all([
				api.fircGetGroupsState(),
				api.fircGetTunnelsState().catch(() => null),
				api.fircGetInterfaces().catch(() => null),
			]);
			liveMap = stateRes.groups ?? {};
			netfilterState = 'netfilter' in stateRes ? stateRes.netfilter : undefined;
			if (tunStateRes) tunnelsState = tunStateRes;
			if (ifacesRes) interfaces = ifacesRes;
		} catch {
			// Ignore transient poll errors
		}
	}

	function handleWindowClick(e: MouseEvent) {
		if (!openPickerGroupId) return;
		const target = e.target as HTMLElement | null;
		if (target && !target.closest('.iface-picker-root')) {
			openPickerGroupId = null;
		}
	}

	onMount(() => {
		try {
			const savedView = localStorage.getItem('awgm.firc.groupView');
			if (savedView === 'grid' || savedView === 'list') groupView = savedView;
		} catch { /* Keep the default view. */ }
		void loadAll();
		void loadPresetCatalog();
		// Живое состояние демона (активный выход группы, состояние туннелей,
		// интерфейсы) меняется внутри fircd без участия панели — события нет,
		// поэтому опрос, но через visiblePoll: спит в фоне, не накладывается.
		stopLivePoll = startVisiblePoll(pollLiveState, 4000);
		if (typeof window !== 'undefined') {
			window.addEventListener('click', handleWindowClick);
		}
	});

	onDestroy(() => {
		probes.destroy();
		stopLivePoll?.();
		if (typeof window !== 'undefined') {
			window.removeEventListener('click', handleWindowClick);
		}
	});

	async function openProxyGroupsModal() {
		openPickerGroupId = null;
		if (!(await refreshProxyGroups())) return;
		pgDraft = proxyGroupsDraft(extConfig.proxyGroups, groups);
		proxyGroupsModalOpen = true;
	}

	async function saveProxyGroupsModal() {
		const draftError = proxyGroupsDraftError(pgDraft);
		if (draftError) {
			notifications.error(draftError);
			return;
		}
		pgSaving = true;
		try {
			const nextExt: FircExtConfig = {
				...extConfig,
				proxyGroups: pgDraft.map(normalizeProxyGroupForSave),
			};
			const saved = await api.fircSaveExt(nextExt);
			extConfig = saved;
			proxyGroupRefresh++;
			proxyGroupsModalOpen = false;
			await loadAll();
			notifications.success(m.firc_groups_pg_saved());
		} catch (e) {
			notifications.error(m.firc_groups_pg_save_error({ error: (e as Error).message }));
		} finally {
			pgSaving = false;
		}
	}

	let catalogTunnelOptions = $derived(buildCatalogTunnelOptions(interfaces));
	let filteredGroups = $derived(filterGroups(groups, searchQuery));
	let anyExpanded = $derived(groups.some((g) => expandedGroups[g.id]));

	function toggleExpandAll() {
		const nextState = !anyExpanded;
		const next: Record<string, boolean> = {};
		for (const g of groups) {
			next[g.id] = nextState;
			if (nextState && g.list && !listPages.pages[g.id]) {
				void listPages.load(g.id, 0, searchQuery);
			}
		}
		expandedGroups = next;
	}

	function toggleGroupExpand(group: FircGroup) {
		const next = !expandedGroups[group.id];
		expandedGroups = { ...expandedGroups, [group.id]: next };
		if (next && group.list && !listPages.pages[group.id]) {
			void listPages.load(group.id, 0, searchQuery);
		}
	}

	function openGroupRules(group: FircGroup) {
		setGroupView('list');
		expandedGroups = { ...expandedGroups, [group.id]: true };
		if (group.list && !listPages.pages[group.id]) void listPages.load(group.id, 0, searchQuery);
	}

	function moveGroup(index: number, delta: -1 | 1) {
		const target = index + delta;
		if (target < 0 || target >= groups.length) return;
		const copy = [...groups];
		const [item] = copy.splice(index, 1);
		copy.splice(target, 0, item);
		groups = copy;
	}

	function deleteGroup(index: number) {
		const g = groups[index];
		if (!g) return;
		groups = groups.filter((_, i) => i !== index);
	}

	function addRuleToGroup(index: number) {
		const g = groups[index];
		if (!g) return;
		const newRule: FircRule = {
			id: randomHexId(),
			type: 'namespace',
			rule: '',
			enable: true,
		};
		groups[index].rules = [newRule, ...groups[index].rules];
		expandedGroups = { ...expandedGroups, [g.id]: true };
		rulePageMap = { ...rulePageMap, [g.id]: 1 };
	}

	function deleteRuleFromGroup(groupIndex: number, ruleIndex: number) {
		const g = groups[groupIndex];
		if (!g) return;
		groups[groupIndex].rules = g.rules.filter((_, i) => i !== ruleIndex);
	}

	function sortGroupRules(groupIndex: number) {
		const g = groups[groupIndex];
		if (!g) return;
		groups[groupIndex].rules = [...g.rules].sort((a, b) => a.rule.localeCompare(b.rule));
	}

	async function handleSyncList(group: FircGroup) {
		if (!group.list?.url || syncingGroup[group.id]) return;
		syncingGroup = { ...syncingGroup, [group.id]: true };
		try {
			await api.fircSyncGroupList(group.id, group.list.url);
			notifications.success(m.firc_groups_toast_sync_started({ name: group.name }));
			setTimeout(async () => {
				try {
					const fresh = await api.fircGetGroups();
					const match = fresh.find((x) => x.id === group.id);
					if (match?.list) {
						const idx = groups.findIndex((x) => x.id === group.id);
						if (idx >= 0 && groups[idx].list) {
							groups[idx].list!.rulesTotal = match.list.rulesTotal;
							groups[idx].list!.lastUpdate = match.list.lastUpdate;
							groups[idx].list!.sync = match.list.sync;
						}
					}
					if (expandedGroups[group.id]) {
						await listPages.load(group.id, 0, searchQuery);
					}
				} finally {
					syncingGroup = { ...syncingGroup, [group.id]: false };
				}
			}, 1800);
		} catch (e) {
			syncingGroup = { ...syncingGroup, [group.id]: false };
			notifications.error(m.firc_groups_toast_sync_error({ error: (e as Error).message }));
		}
	}

	async function saveAllChanges() {
		if (saving || !isDirty) return;
		for (const g of groups) {
			const emptyRule = g.rules.find((r) => !r.rule.trim());
			if (emptyRule) {
				notifications.error(m.firc_groups_toast_empty_rule({ name: g.name || m.firc_groups_unnamed() }));
				expandedGroups = { ...expandedGroups, [g.id]: true };
				return;
			}
		}
		saving = true;
		try {
			// Save any list rule edits first
			for (const [groupId, page] of Object.entries(listPages.pages)) {
				const editEntries = Object.entries(page.edits).map(([id, ed]) => ({
					id,
					...ed,
				}));
				if (editEntries.length > 0) {
					await api.fircUpdateGroupListRules(groupId, editEntries, false);
				}
			}

			const payload = savePayload(groups);
			const res = await api.fircSaveGroups(payload, true);
			acceptSavedGroups((res?.groups ?? payload).map(normalizeGroup));
			listPages.clearEdits();

			await Promise.all([pollLiveState(), reloadFircAll()]);
			lastHandledSyncNonce = get(fircSyncNonce);
			notifications.success(m.firc_groups_toast_saved());
		} catch (e) {
			notifications.error(m.firc_groups_toast_save_error({ error: (e as Error).message }));
		} finally {
			saving = false;
		}
	}

	function exportConfig() {
		const blob = new Blob([JSON.stringify({ groups }, null, 2)], { type: 'application/json' });
		const link = document.createElement('a');
		link.href = URL.createObjectURL(blob);
		link.download = 'config.firc';
		document.body.appendChild(link);
		link.click();
		document.body.removeChild(link);
	}

	async function copySelectedGroupDomains(groupIds?: string[]) {
		const targetIds =
			groupIds && groupIds.length > 0
				? groupIds
				: filteredGroups.map(({ group }) => group.id);
		const res = extractFircGroupDomainLines(groups, targetIds);
		const copied = res.lines.length > 0 && (await copyToClipboard(res.lines.join('\n')));
		const notice = domainCopyNotice(res, copied);
		notifications[notice.kind](notice.text);
	}

	function handleImportFileChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = () => {
			try {
				const normalized = parseImportedGroups(String(reader.result));
				if (normalized.length === 0) {
					notifications.error(m.firc_groups_import_empty());
					return;
				}
				groups = [...groups, ...normalized];
				notifications.success(m.firc_groups_import_done({ count: normalized.length }));
			} catch {
				notifications.error(m.firc_groups_import_invalid());
			}
		};
		reader.readAsText(file);
		input.value = '';
	}

	// ── Modal 1: Create / Edit Group Settings (List URL + Catalog + DNS) ──
	const groupForm = createFircGroupForm();

	// Список зависит от выбранного в форме выхода: служебный интерфейс виден, только пока он выбран.
	let availableInterfaces = $derived(buildAvailableInterfaces(interfaces, groupForm.form.iface));

	let gmEditGroup = $derived(
		groupForm.mode === 'edit' && groupForm.index !== null ? groups[groupForm.index] : undefined,
	);
	let gmHasChain = $derived(
		Boolean(gmEditGroup) &&
			normalizeIfaceList(gmEditGroup?.interface ?? '').length >= 2 &&
			!isSpecialIface(groupForm.form.iface),
	);
	let gmManagedProxyGroup = $derived(
		gmHasChain && gmEditGroup ? matchingProxyGroup(extConfig.proxyGroups, gmEditGroup) : undefined,
	);
	let gmShowCheck = $derived(gmHasChain && !gmManagedProxyGroup);

	function openCreateGroupModal() {
		groupForm.openWith('create', null, emptyGroupForm(availableInterfaces[0]?.value ?? 'block'));
	}

	function openEditGroupModal(index: number) {
		const g = groups[index];
		if (!g) return;
		groupForm.openWith('edit', index, groupFormFrom(g));
	}

	function submitGroupModal() {
		const idx = groupForm.index;
		const cur = groupForm.mode === 'edit' && idx !== null ? groups[idx] : undefined;
		const next = groupFromForm(groupForm.form, cur, groups.length, extConfig.proxyGroups);
		if (cur && idx !== null) {
			groups[idx] = next;
		} else {
			groups = [...groups, next];
		}
		groupForm.close();
	}

	// ── Modal 2: Devices Filter Modal ──
	let devicesModalOpen = $state(false);
	let devicesModalIndex = $state<number | null>(null);
	let devicesModalBulk = $state(false);

	let devicesModalGroup = $derived(
		devicesModalIndex !== null ? (groups[devicesModalIndex] ?? null) : null,
	);
	let devicesModalInitial = $derived.by<FircDeviceSelector | undefined>(() => {
		const g = devicesModalGroup;
		if (!g) return undefined;
		return {
			allow: (g.devices?.allow ?? []).filter((x) => !MANAGED_SERVER_SUBNETS.has(x)),
			deny: [...(g.devices?.deny ?? [])],
		};
	});

	function openDevicesModal(index: number) {
		if (!groups[index]) return;
		devicesModalBulk = false;
		devicesModalIndex = index;
		devicesModalOpen = true;
	}

	function openBulkDevicesModal() {
		if (selection.groupIds.size === 0) return;
		devicesModalBulk = true;
		devicesModalIndex = null;
		devicesModalOpen = true;
	}

	function applyDevicesModal(selector: FircDeviceSelector) {
		if (devicesModalBulk) {
			runBulkGroupAction({ kind: 'setDevices', devices: selector });
			devicesModalBulk = false;
			devicesModalOpen = false;
			return;
		}
		if (devicesModalIndex === null || !groups[devicesModalIndex]) return;
		groups[devicesModalIndex].devices = selector;
		devicesModalOpen = false;
	}

	// ── Bulk Selection & Batch Editing ──
	const selection = createFircGroupSelection();
	let confirmBulkDeleteGroupsOpen = $state(false);
	let bulkRulesEditorOpen = $state(false);
	let bulkRulesEditorGroupIndex = $state<number | null>(null);
	let bulkRulesEditorGroup = $derived(
		bulkRulesEditorGroupIndex !== null ? (groups[bulkRulesEditorGroupIndex] ?? null) : null,
	);

	function toggleGroupSelectMode() {
		selection.toggleMode();
		if (!selection.selectMode) confirmBulkDeleteGroupsOpen = false;
	}

	function runBulkGroupAction(action: BulkGroupAction) {
		const res = applyBulkGroupAction(groups, selection.groupIds, action);
		if (res.affected === 0) return;
		groups = res.groups;
		if (action.kind === 'delete') {
			const remainingIds = new Set(res.groups.map((g) => g.id));
			selection.setGroups(Array.from(selection.groupIds).filter((id) => remainingIds.has(id)));
			confirmBulkDeleteGroupsOpen = false;
			notifications.success(m.firc_bulk_groups_deleted_toast({ count: res.affected }));
		} else {
			notifications.success(m.firc_bulk_groups_updated_toast({ count: res.affected }));
		}
	}

	function runBulkRuleAction(groupIndex: number, action: BulkRuleAction) {
		const src = groups[groupIndex];
		if (!src) return;
		const sel = selection.rules(src.id);
		const targetGroup =
			action.kind === 'move' ? groups.find((g) => g.id === action.targetGroupId) : undefined;
		const res = applyBulkRuleAction(groups, groupIndex, sel, action);
		if (res.affected === 0) return;
		groups = res.groups;
		if (action.kind === 'move') {
			selection.setRules(src.id, []);
			expandedGroups = { ...expandedGroups, [action.targetGroupId]: true };
			notifications.success(
				m.firc_bulk_rules_moved_toast({
					target: targetGroup?.name ?? '',
					count: res.affected,
				}),
			);
		} else if (action.kind === 'delete') {
			selection.setRules(src.id, []);
			notifications.success(m.firc_bulk_rules_deleted_toast({ count: res.affected }));
		}
	}

	function openBulkRulesEditor(index: number) {
		if (!groups[index]) return;
		bulkRulesEditorGroupIndex = index;
		bulkRulesEditorOpen = true;
	}

	function applyBulkRulesEditor(nextRules: FircRule[]) {
		const idx = bulkRulesEditorGroupIndex;
		if (idx === null || !groups[idx]) return;
		const groupId = groups[idx].id;
		groups = groups.map((g, i) => (i === idx ? { ...g, rules: nextRules } : g));
		expandedGroups = { ...expandedGroups, [groupId]: true };
		selection.setRules(groupId, []);
		bulkRulesEditorOpen = false;
		notifications.success(m.firc_bulk_rules_editor_applied_toast({ count: nextRules.length }));
	}

	// ── Modal 3: Bulk Import Rules Modal ──
	let importRulesModalOpen = $state(false);
	let importRulesGroupIndex = $state<number | null>(null);
	let importRulesText = $state('');

	function openImportRulesModal(index: number) {
		importRulesGroupIndex = index;
		importRulesText = '';
		importRulesModalOpen = true;
	}

	function submitImportRules() {
		if (importRulesGroupIndex === null || !groups[importRulesGroupIndex]) return;
		const newRules = parseRulesText(importRulesText);
		if (newRules.length === 0) {
			importRulesModalOpen = false;
			return;
		}
		const g = groups[importRulesGroupIndex];
		groups[importRulesGroupIndex].rules = [...newRules, ...g.rules];
		expandedGroups = { ...expandedGroups, [g.id]: true };
		importRulesModalOpen = false;
		notifications.success(m.firc_groups_rules_imported({ count: newRules.length }));
	}

	// ── Modal 4: Service Catalog (Sing-box style .srs catalog) ──
	let serviceCatalogOpen = $state(false);
	let serviceCatalogMode = $state<'batch' | 'single'>('batch');
	let serviceCatalogSubmitting = $state(false);

	function openServiceCatalog(mode: 'batch' | 'single') {
		serviceCatalogMode = mode;
		serviceCatalogOpen = true;
	}

	function handleSingleCatalogPick(presets: CatalogPreset[]) {
		const picked = presets[0];
		if (!picked) {
			serviceCatalogOpen = false;
			return;
		}
		groupForm.applyCatalogPick(picked.name, presetSrsUrl(picked));
		serviceCatalogOpen = false;
	}

	async function handleBatchCatalogCreate(presets: CatalogPreset[], targetIface: string) {
		if (presets.length === 0 || serviceCatalogSubmitting) return;
		serviceCatalogSubmitting = true;
		try {
			const createdGroups = groupsFromPresets(
				presets,
				targetIface || availableInterfaces[0]?.value || 'block',
			);
			if (createdGroups.length === 0) {
				serviceCatalogOpen = false;
				return;
			}

			const payload = savePayload([...groups, ...createdGroups]);
			const res = await api.fircSaveGroups(payload, true);
			const normalized = (res?.groups ?? payload).map(normalizeGroup);
			acceptSavedGroups(normalized);
			serviceCatalogOpen = false;

			notifications.success(m.firc_groups_catalog_created({ count: createdGroups.length }));

			// Trigger immediate .srs sync for each newly created group
			for (const cg of createdGroups) {
				const savedMatch =
					normalized.find((x) => x.id === cg.id) ??
					normalized.find((x) => x.list?.url === cg.list?.url && x.name === cg.name);
				if (savedMatch?.list?.url) {
					void handleSyncList(savedMatch);
				}
			}
			await Promise.all([pollLiveState(), reloadFircAll()]);
			lastHandledSyncNonce = get(fircSyncNonce);
		} catch (e) {
			notifications.error(m.firc_groups_catalog_error({ error: (e as Error).message }));
		} finally {
			serviceCatalogSubmitting = false;
		}
	}
</script>

<svelte:window onbeforeunload={handleBeforeUnload} />

<div class="firc-groups-panel">
	{#if isDirty}
		<div class="firc-staging-banner">
			<div class="firc-staging-left">
				<span class="firc-staging-dot" aria-hidden="true"></span>
				<span class="firc-staging-title">
					{m.firc_groups_unsaved_title()}
					<span class="firc-staging-sub">
						· {m.firc_groups_unsaved_sub()}
					</span>
				</span>
			</div>
			<div class="firc-staging-actions">
				<Button
					variant="ghost"
					size="sm"
					disabled={saving}
					onclick={() => (confirmDiscardOpen = true)}
				>
					{#snippet iconBefore()}<RotateCcw size={13} />{/snippet}
					{m.firc_groups_reset()}
				</Button>
				<Button
					variant="primary"
					size="sm"
					disabled={saving}
					loading={saving}
					onclick={saveAllChanges}
				>
					{m.firc_groups_save_changes()}
				</Button>
			</div>
		</div>
	{/if}

	<FircGroupsToolbar
		groupsCount={groups.length}
		netfilterDirty={Boolean(netfilterState?.dirty)}
		bind:searchQuery
		{groupView}
		{anyExpanded}
		proxyGroupsCount={extConfig.proxyGroups.length}
		groupSelectMode={selection.selectMode}
		onsetView={setGroupView}
		ontoggleExpandAll={toggleExpandAll}
		onimportFile={handleImportFileChange}
		onexport={exportConfig}
		oncopyDomains={() => copySelectedGroupDomains()}
		onopenProxyGroups={openProxyGroupsModal}
		onopenCatalog={() => openServiceCatalog('batch')}
		ontoggleSelectMode={toggleGroupSelectMode}
		oncreateGroup={openCreateGroupModal}
	/>

	{#if selection.selectMode}
		<FircBulkGroupsBar
			selectedCount={selection.groupIds.size}
			visibleCount={filteredGroups.length}
			interfaces={availableInterfaces}
			proxyGroups={extConfig.proxyGroups}
			onSelectAllVisible={() => selection.setGroups(filteredGroups.map((entry) => entry.group.id))}
			onDeselectAll={() => selection.setGroups([])}
			onDone={toggleGroupSelectMode}
			onSetInterface={(iface) => {
				let check: FircGroupCheck | undefined;
				if (Array.isArray(iface)) {
					const matched = matchingProxyGroup(extConfig.proxyGroups, { interface: iface });
					check =
						(matched ? buildGroupCheckFromProxyGroup(matched) : undefined) ??
						(iface.length >= 2 ? { mode: 'url', url: '' } : { mode: 'none', url: '' });
				}
				runBulkGroupAction({ kind: 'setInterface', iface, check });
			}}
			onOpenDevices={openBulkDevicesModal}
			onEnable={() => runBulkGroupAction({ kind: 'enable', enable: true })}
			onDisable={() => runBulkGroupAction({ kind: 'enable', enable: false })}
			onCopyDomains={() => copySelectedGroupDomains(Array.from(selection.groupIds))}
			onDelete={() => (confirmBulkDeleteGroupsOpen = true)}
		/>
	{/if}

	<p class="priority-hint">
		{m.firc_groups_priority_hint()}
	</p>

	{#if extConfig.proxyGroups.length > 0}
		<FircFallbackPanel
			{rt}
			probingIfaces={probes.ifaces}
			probingGroups={probes.groups}
			onprobe={probeFallbackInterfaces}
			onconfigure={openProxyGroupsModal}
		/>
	{/if}

	{#if loading}
		<div class="empty-box">{m.firc_groups_loading()}</div>
	{:else if filteredGroups.length === 0}
		<div class="empty-box">
			{#if searchQuery.trim()}
				{m.firc_groups_search_empty({ query: searchQuery })}
			{:else}
				{m.firc_groups_empty()}
			{/if}
		</div>
	{:else}
		<div class="cards-stack" class:compact-grid={groupView === 'grid'}>
			{#each filteredGroups as { group, index } (group.id)}
				{@const orderStr = String(index + 1).padStart(2, '0')}
				{@const isExpanded = Boolean(expandedGroups[group.id])}
				{@const isPickerOpen = openPickerGroupId === group.id}
				{@const selectedRuleSet = selection.rules(group.id)}

				<div
					class="card-wrap {groupAccentClass(liveMap, group)}"
					class:disabled-card={!group.enable}
					class:picker-open-card={isPickerOpen}
					class:bulk-selected-card={selection.selectMode && selection.groupIds.has(group.id)}
				>
					{#if groupView === 'grid'}
						<FircGroupCompactCard
							{group}
							{orderStr}
							{rt}
							probingIfaces={probes.ifaces}
							probingGroups={probes.groups}
							selectMode={selection.selectMode}
							selected={selection.groupIds.has(group.id)}
							ontoggleSelected={() => selection.toggleGroup(group.id)}
							onprobe={probeFallbackInterfaces}
							onopenRules={() => openGroupRules(group)}
							onopenSettings={() => openEditGroupModal(index)}
						/>
					{:else}
						<FircGroupRow
							bind:group={groups[index]}
							{orderStr}
							isFirst={index === 0}
							isLast={index === groups.length - 1}
							{rt}
							serviceKey={groupServiceKey($presetCatalog, group)}
							probingIfaces={probes.ifaces}
							probingGroups={probes.groups}
							selectMode={selection.selectMode}
							selected={selection.groupIds.has(group.id)}
							{isExpanded}
							syncing={Boolean(syncingGroup[group.id])}
							pickerOpen={isPickerOpen}
							showOthers={Boolean(showOthersMap[group.id])}
							ontoggleSelected={() => selection.toggleGroup(group.id)}
							onmove={(delta) => moveGroup(index, delta)}
							ontoggleExpand={() => toggleGroupExpand(group)}
							ontogglePicker={() => {
								openPickerGroupId = isPickerOpen ? null : group.id;
								if (!isPickerOpen) void refreshProxyGroups();
							}}
							onclosePicker={() => (openPickerGroupId = null)}
							ontoggleOthers={() =>
								(showOthersMap = { ...showOthersMap, [group.id]: !showOthersMap[group.id] })}
							onprobe={probeFallbackInterfaces}
							onopenProxyGroups={openProxyGroupsModal}
							onsync={() => handleSyncList(group)}
							onaddRule={() => addRuleToGroup(index)}
							onopenDevices={() => openDevicesModal(index)}
							oncopyDomains={() => copySelectedGroupDomains([group.id])}
							onimportRules={() => openImportRulesModal(index)}
							onopenSettings={() => openEditGroupModal(index)}
							ondelete={() => deleteGroup(index)}
						/>

						{#if isExpanded}
							<FircGroupRulesSection
								bind:group={groups[index]}
								otherGroups={groups
									.filter((og) => og.id !== group.id)
									.map((og) => ({ id: og.id, name: og.name }))}
								currentPage={rulePageMap[group.id] ?? 1}
								listPage={listPages.pages[group.id]}
								syncing={Boolean(syncingGroup[group.id])}
								{selectedRuleSet}
								onsetPage={(page) => (rulePageMap = { ...rulePageMap, [group.id]: page })}
								onsync={() => handleSyncList(group)}
								onsort={() => sortGroupRules(index)}
								onselectAllRules={() => selection.setRules(group.id, group.rules.map((r) => r.id))}
								onclearRules={() => selection.setRules(group.id, [])}
								ontoggleRuleSelected={(ruleId) => selection.toggleRule(group.id, ruleId)}
								onbulk={(action) => runBulkRuleAction(index, action)}
								onaddRule={() => addRuleToGroup(index)}
								ondeleteRule={(ruleIndex) => deleteRuleFromGroup(index, ruleIndex)}
								onopenBulkEditor={() => openBulkRulesEditor(index)}
								onloadListPage={(offset) => listPages.load(group.id, offset, searchQuery)}
								ontoggleListRule={(rule, checked) => listPages.toggleRule(group.id, rule, checked)}
							/>
						{/if}
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<FircGroupSettingsModal
	open={groupForm.open}
	mode={groupForm.mode}
	bind:form={groupForm.form}
	{availableInterfaces}
	showCheck={gmShowCheck}
	managedProxyGroupName={gmManagedProxyGroup?.name ?? ''}
	catalog={groupForm.catalog}
	previewLoading={groupForm.previewLoading}
	previewData={groupForm.previewData}
	previewError={groupForm.previewError}
	onclose={groupForm.close}
	onsubmit={submitGroupModal}
	onpreview={groupForm.preview}
	onensureCatalog={() => void groupForm.ensureCatalog()}
	onopenCatalog={() => openServiceCatalog('single')}
/>

<!-- Modal 2: Devices & Policies Filter -->
<FircGroupDevicesModal
	open={devicesModalOpen}
	groupName={devicesModalBulk
		? m.firc_bulk_groups_devices_title({ count: selection.groupIds.size })
		: devicesModalGroup?.name}
	initialDevices={devicesModalBulk ? { allow: [], deny: [] } : devicesModalInitial}
	{hosts}
	{policies}
	onclose={() => {
		devicesModalBulk = false;
		devicesModalOpen = false;
	}}
	onapply={applyDevicesModal}
/>

<FircImportRulesModal
	open={importRulesModalOpen}
	bind:text={importRulesText}
	onclose={() => (importRulesModalOpen = false)}
	onsubmit={submitImportRules}
/>

<FircProxyGroupsModal
	open={proxyGroupsModalOpen}
	bind:draft={pgDraft}
	{rt}
	probingIfaces={probes.ifaces}
	probingGroups={probes.groups}
	saving={pgSaving}
	onprobe={probeFallbackInterfaces}
	onclose={() => (proxyGroupsModalOpen = false)}
	onsave={saveProxyGroupsModal}
/>

<!-- Modal 5: Service Catalog (.srs) -->
<ServiceCatalogModal
	bind:open={serviceCatalogOpen}
	title={m.firc_groups_catalog_title()}
	presetFilter={singboxRouterCatalogPresetFilter}
	markExisting={serviceCatalogMode === 'batch'}
	existingNames={existingCatalogNames(groups, $presetCatalog)}
	existingRuleSetTags={existingCatalogRuleSetTags(groups, $presetCatalog)}
	footer={serviceCatalogMode === 'batch' ? 'tunnel' : 'none'}
	tunnelOptions={catalogTunnelOptions}
	hideDownloadRouteNote={true}
	multiple={serviceCatalogMode === 'batch'}
	warnLargeDnsLists={false}
	submitting={serviceCatalogSubmitting}
	onclose={() => (serviceCatalogOpen = false)}
	onconfirm={handleSingleCatalogPick}
	oncreate={(presets, tunnelId) => void handleBatchCatalogCreate(presets, tunnelId)}
/>

<!-- Modal 6: Confirm Discard Draft -->
<Modal
	open={confirmDiscardOpen}
	title={m.firc_groups_discard_title()}
	size="sm"
	onclose={() => (confirmDiscardOpen = false)}
>
	<p class="hint" style="font-size: 13px; color: var(--text-secondary);">
		{m.firc_groups_discard_msg()}
	</p>
	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={() => (confirmDiscardOpen = false)}>
				{m.common_cancel()}
			</Button>
			<Button variant="danger" size="sm" onclick={resetAllChanges}>
				{m.firc_groups_discard_confirm()}
			</Button>
		</div>
	{/snippet}
</Modal>

<FircBulkRulesEditorModal
	open={bulkRulesEditorOpen}
	groupName={bulkRulesEditorGroup?.name}
	rules={bulkRulesEditorGroup?.rules ?? []}
	makeId={randomHexId}
	onclose={() => (bulkRulesEditorOpen = false)}
	onapply={applyBulkRulesEditor}
/>

<ConfirmModal
	open={confirmBulkDeleteGroupsOpen}
	title={m.firc_bulk_groups_delete_title()}
	message={m.firc_bulk_groups_delete_msg({ count: selection.groupIds.size })}
	confirmLabel={m.firc_bulk_delete()}
	cancelLabel={m.common_cancel()}
	variant="danger"
	onConfirm={() => runBulkGroupAction({ kind: 'delete' })}
	onClose={() => (confirmBulkDeleteGroupsOpen = false)}
/>

<style>
	.firc-groups-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.priority-hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
	}

	.empty-box {
		padding: 32px 16px;
		text-align: center;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		color: var(--text-muted);
		font-size: 13px;
	}

	.cards-stack.compact-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 260px), 1fr)); gap: .65rem; align-items: stretch; }

	.cards-stack {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.card-wrap {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		transition:
			border-color var(--t-fast),
			background var(--t-fast);
		position: relative;
		overflow: visible;
	}

	.card-wrap.picker-open-card {
		z-index: 45;
	}

	.card-wrap:hover {
		border-color: var(--border-hover);
	}

	.card-wrap.disabled-card {
		opacity: 0.68;
	}

	.card-wrap.bulk-selected-card {
		border-color: var(--color-accent-border);
		box-shadow: 0 0 0 1px var(--color-accent-border);
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.firc-staging-banner {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 10px;
		padding: 10px 14px;
		background: color-mix(in srgb, var(--color-warning) 10%, var(--bg-secondary));
		border: 1px solid color-mix(in srgb, var(--color-warning) 35%, var(--border));
		border-left: 3px solid var(--color-warning);
		border-radius: var(--radius);
		position: sticky;
		top: 56px;
		z-index: 25;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
	}

	.firc-staging-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}

	.firc-staging-dot {
		width: 8px;
		height: 8px;
		border-radius: 999px;
		background: var(--color-warning);
		flex-shrink: 0;
	}

	.firc-staging-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.firc-staging-sub {
		font-weight: 400;
		font-size: 12px;
		color: var(--text-secondary);
	}

	.firc-staging-actions {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-shrink: 0;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}
</style>
