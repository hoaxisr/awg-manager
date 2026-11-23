<script lang="ts">
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { onMount, onDestroy, untrack } from 'svelte';
	import { get } from 'svelte/store';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		reloadFircAll,
		fircSyncNonce,
		fircInterfaceProbeResults,
		fircProbeResults,
	} from '$lib/stores/firc';
	import { fircMigrationAppliedNonce } from '$lib/stores/fircMigrationModal';
	import { Button, Badge, Toggle, Modal, ConfirmModal, StatusDot, type DropdownOption } from '$lib/components/ui';
	import FircGroupDevicesModal from './FircGroupDevicesModal.svelte';
	import FircBulkGroupsBar from './FircBulkGroupsBar.svelte';
	import FircBulkRulesBar from './FircBulkRulesBar.svelte';
	import FircBulkRulesEditorModal from './FircBulkRulesEditorModal.svelte';
	import {
		applyBulkGroupAction,
		applyBulkRuleAction,
		detectFircRuleType,
		type BulkGroupAction,
		type BulkRuleAction,
	} from '$lib/utils/fircBulkRules';
	import ServiceTile from '$lib/components/sb-router/ServiceTile.svelte';
	import { ServiceCatalogModal } from '$lib/components/dnsroutes';
	import { presetCatalog, loadPresetCatalog } from '$lib/stores/presets';
	import { singboxRouterCatalogPresetFilter } from '$lib/utils/catalog-preset';
	import { searchFircCatalog } from '$lib/utils/fircCatalogSearch';
	import type { CatalogPreset } from '$lib/types';
	import {
		Plus,
		Trash2,
		RefreshCw,
		Settings,
		ChevronDown,
		ChevronUp,
		ArrowUp,
		ArrowDown,
		ArrowRight,
		Upload,
		Download,
		Search,
		Monitor,
		ChevronsDownUp,
		ChevronsUpDown,
		Shield,
		Globe,
		Ban,
		Link2,
		X,
		LayoutGrid,
		RotateCcw,
		Copy,
		Zap,
		Edit3,
		FilePen,
	} from 'lucide-svelte';
	import {
		saveFircGroupsDraft,
		loadFircGroupsDraft,
		clearFircGroupsDraft,
	} from '$lib/stores/fircDraft';
	import {
		friendlyIfaceLabel,
		getEffectiveInterfaceProbe,
		isInternalServerFircInterface,
		isSelectableFircFallbackInterface,
		resolveInterfaceHealth,
		resolveGroupActiveInterface,
		resolveProxyGroupRuntime,
		probeLatencyTone,
		formatProbeLatency,
	} from '$lib/utils/fircFallback';
	import {
		FIRC_RULE_TYPES,
		extractFircGroupDomainLines,
		type FircBlockAnswer,
		type FircCatalog,
		type FircDeviceSelector,
		type FircGroup,
		type FircGroupCheck,
		type FircGroupLive,
		type FircHost,
		type FircInterfaceOption,
		type FircInterfaceProbeResult,
		type FircListPreviewRes,
		type FircListRule,
		type FircPolicy,
		type FircRule,
		type FircRuleType,
		type FircSystemResolver,
		type FircTunnelState,
		type FircExtConfig,
		type FircProxyGroup,
	} from '$lib/types/firc';

	const PAGE_SIZE = 50;
	const INTERVAL_OPTIONS = [
		{ value: 3600, label: 'Раз в час' },
		{ value: 21600, label: 'Раз в 6 часов' },
		{ value: 86400, label: 'Раз в день' },
		{ value: 604800, label: 'Раз в неделю' },
		{ value: 0, label: 'Вручную' },
	];

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
	let extConfig = $state<FircExtConfig>({
		proxyGroups: [],
		keenDnsCloud: { enabled: false, interfaces: [] },
		serverRouting: {
			enabledServers: [],
			awgServer: false,
			wdttServer: false,
			freeTurnServer: false,
			vlessXhttpServer: false,
		},
	});
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
	let probingIfaces = $state<Record<string, boolean>>({});
	let probingGroups = $state<Record<string, boolean>>({});
	let destroyed = false;
	let searchQuery = $state('');
	let expandedGroups = $state<Record<string, boolean>>({});
	let rulePageMap = $state<Record<string, number>>({});

	// List rules state per group
	interface GroupListPageState {
		offset: number;
		total: number;
		matched: number;
		loading: boolean;
		rules: FircListRule[];
		edits: Record<string, { type?: string; enable?: boolean }>;
	}
	let listPages = $state<Record<string, GroupListPageState>>({});

	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let importFileInput = $state<HTMLInputElement | null>(null);
	let lastHandledMigrationNonce = get(fircMigrationAppliedNonce);
	let lastHandledSyncNonce = get(fircSyncNonce);

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

	function randomHexId(): string {
		const bytes = new Uint8Array(4);
		if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
			crypto.getRandomValues(bytes);
		} else {
			for (let i = 0; i < 4; i++) bytes[i] = Math.floor(Math.random() * 256);
		}
		return Array.from(bytes)
			.map((b) => b.toString(16).padStart(2, '0'))
			.join('');
	}

	function normalizeGroup(g: FircGroup): FircGroup {
		return {
			id: g.id || randomHexId(),
			name: g.name ?? '',
			interface: g.interface || 'block',
			enable: g.enable ?? true,
			devices: {
				allow: [...(g.devices?.allow ?? [])],
				deny: [...(g.devices?.deny ?? [])],
			},
			rules: (g.rules ?? []).map((r) => ({
				id: r.id || randomHexId(),
				type: r.type || 'namespace',
				rule: r.rule ?? '',
				enable: r.enable ?? true,
				...(r.proto ? { proto: r.proto } : {}),
				...(r.ports ? { ports: r.ports } : {}),
				...(r.comment ? { comment: r.comment } : {}),
				...(r.autolist ? { autolist: r.autolist } : {}),
				...(r.autolistByDomain ? { autolistByDomain: r.autolistByDomain } : {}),
			})),
			list: g.list
				? {
						url: g.list.url ?? '',
						interval: g.list.interval ?? 86400,
						lastUpdate: g.list.lastUpdate ?? 0,
						rulesTotal: g.list.rulesTotal ?? g.list.rules ?? 0,
						rules: g.list.rules ?? g.list.rulesTotal ?? 0,
						sync: g.list.sync ?? { state: 'idle', error: '', lastCheck: 0 },
					}
				: null,
			resolve: g.resolve
				? { tunnel: g.resolve.tunnel ?? true, server: g.resolve.server ?? '' }
				: { tunnel: true, server: '' },
			...(g.check
				? {
						check: {
							mode: g.check.mode || 'none',
							url: g.check.url ?? '',
							...(g.check.intervalSec ? { intervalSec: g.check.intervalSec } : {}),
							...(g.check.timeoutMs ? { timeoutMs: g.check.timeoutMs } : {}),
							...(g.check.failThreshold ? { failThreshold: g.check.failThreshold } : {}),
							...(g.check.recoverThreshold ? { recoverThreshold: g.check.recoverThreshold } : {}),
						} as FircGroupCheck,
					}
				: {}),
			...(g.resolver ? { resolver: g.resolver } : {}),
			...(g.block ? { block: g.block } : {}),
		};
	}

	function serializeForDiff(list: FircGroup[]): string {
		return JSON.stringify(
			list.map((g) => ({
				id: g.id,
				name: g.name,
				interface: g.interface,
				enable: g.enable,
				devices: g.devices,
				rules: g.rules.map((r) => ({
					id: r.id,
					type: r.type,
					rule: r.rule,
					enable: r.enable,
					proto: r.proto || undefined,
					ports: r.ports || undefined,
					comment: r.comment || undefined,
				})),
				list: g.list ? { url: g.list.url, interval: g.list.interval ?? 86400 } : null,
				resolve: g.resolve ?? { tunnel: true, server: '' },
				check:
					g.check && g.check.mode !== 'none'
						? {
								mode: g.check.mode,
								url: g.check.url ?? '',
								intervalSec: g.check.intervalSec ?? 0,
								timeoutMs: g.check.timeoutMs ?? 0,
								failThreshold: g.check.failThreshold ?? 0,
								recoverThreshold: g.check.recoverThreshold ?? 0,
							}
						: null,
				block: g.interface === 'block' ? (g.block ?? 'null') : undefined,
			})),
		);
	}

	let hasListRuleEdits = $derived(
		Object.values(listPages).some((p) => Object.keys(p.edits).length > 0),
	);
	let isDirty = $derived(
		serializeForDiff(groups) !== baselineJson || hasListRuleEdits,
	);

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

	function resetAllChanges() {
		groups = baselineGroups.map(normalizeGroup);
		const cleanedPages: Record<string, GroupListPageState> = {};
		for (const [k, v] of Object.entries(listPages)) {
			cleanedPages[k] = { ...v, edits: {} };
		}
		listPages = cleanedPages;
		draftedAt = null;
		clearFircGroupsDraft();
		confirmDiscardOpen = false;
		notifications.info('Несохранённые изменения сброшены');
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
			if (extRes) {
				extConfig = {
					proxyGroups: extRes.proxyGroups ?? [],
					globalTarget: extRes.globalTarget,
					keenDnsCloud: extRes.keenDnsCloud ?? { enabled: false, interfaces: [] },
					serverRouting: extRes.serverRouting ?? {
						enabledServers: [],
						awgServer: false,
						wdttServer: false,
						freeTurnServer: false,
						vlessXhttpServer: false,
					},
				};
			}

			const nowSec = Math.floor(Date.now() / 1000);
			const existingProbes = get(fircInterfaceProbeResults);
			const staleIfaces = allFallbackInterfaceIds().filter((id) => {
				const prev = existingProbes[id];
				return !prev?.checkedAt || nowSec - prev.checkedAt > 60;
			});
			if (staleIfaces.length > 0) {
				void probeFallbackInterfaces(staleIfaces, { silent: true });
			}
		} catch (e) {
			notifications.error(`Ошибка загрузки групп FIRC: ${(e as Error).message}`);
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

	function anchorPopover(node: HTMLElement) {
		function updatePosition() {
			const root = node.closest('.iface-picker-root') as HTMLElement | null;
			if (!root || typeof window === 'undefined') return;
			const rect = root.getBoundingClientRect();
			const vh = window.innerHeight;
			const vw = window.innerWidth;
			const spaceBelow = Math.max(0, vh - rect.bottom - 14);
			const spaceAbove = Math.max(0, rect.top - 14);

			if (spaceBelow < 310 && spaceAbove > spaceBelow) {
				node.style.top = 'auto';
				node.style.bottom = 'calc(100% + 5px)';
				node.style.maxHeight = `${Math.min(350, Math.max(180, spaceAbove))}px`;
			} else {
				node.style.top = 'calc(100% + 5px)';
				node.style.bottom = 'auto';
				node.style.maxHeight = `${Math.min(350, Math.max(180, spaceBelow))}px`;
			}

			const popWidth = Math.min(310, Math.max(230, vw - 20));
			node.style.width = `${popWidth}px`;
			let leftOffset = 0;
			if (rect.left + popWidth > vw - 10) {
				leftOffset = vw - 10 - popWidth - rect.left;
			}
			if (rect.left + leftOffset < 10) {
				leftOffset = 10 - rect.left;
			}
			node.style.left = `${leftOffset}px`;
			node.style.right = 'auto';
		}

		updatePosition();
		const raf = requestAnimationFrame(() => {
			updatePosition();
			const primaryRow = node.querySelector('.iface-pop-row.is-primary') as HTMLElement | null;
			if (primaryRow) {
				primaryRow.scrollIntoView({ block: 'nearest' });
			}
		});
		window.addEventListener('resize', updatePosition);
		window.addEventListener('scroll', updatePosition, true);

		return {
			destroy() {
				cancelAnimationFrame(raf);
				window.removeEventListener('resize', updatePosition);
				window.removeEventListener('scroll', updatePosition, true);
			},
		};
	}

	const MAX_GROUP_IFACES = 4;
	let openPickerGroupId = $state<string | null>(null);
	let showOthersMap = $state<Record<string, boolean>>({});

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
		pollTimer = setInterval(() => void pollLiveState(), 4000);
		if (typeof window !== 'undefined') {
			window.addEventListener('click', handleWindowClick);
		}
	});

	onDestroy(() => {
		destroyed = true;
		if (pollTimer) clearInterval(pollTimer);
		if (typeof window !== 'undefined') {
			window.removeEventListener('click', handleWindowClick);
		}
	});

	function normalizeIfaceList(raw: string | string[] | undefined): string[] {
		if (Array.isArray(raw)) return raw.filter((s) => typeof s === 'string' && s.length > 0);
		if (typeof raw === 'string' && raw.length > 0) return [raw];
		return [];
	}

	function denormalizeIfaceList(list: string[]): string | string[] {
		const cleaned = list.filter((s) => s.length > 0);
		if (cleaned.length === 0) return '';
		if (cleaned.length === 1) return cleaned[0];
		return cleaned;
	}

	function isSpecialIface(id: string): boolean {
		return id === 'blackhole' || id === 'block';
	}

	function primaryIface(iface: string | string[]): string {
		if (Array.isArray(iface)) return iface[0] ?? '';
		return iface ?? '';
	}

	function formatIfaceOption(opt: FircInterfaceOption): string {
		const rawDesc = opt.name || opt.ndmsName || '';
		const shortDesc = rawDesc.split(' (')[0].trim();
		return shortDesc && shortDesc !== opt.id ? `${opt.id} — ${shortDesc}` : opt.id;
	}

	function setPrimaryIface(group: FircGroup, id: string) {
		if (isSpecialIface(id)) {
			group.interface = id;
			if (id === 'block' && !group.block) group.block = 'null';
			openPickerGroupId = null;
			return;
		}
		const cur = normalizeIfaceList(group.interface);
		if (cur.length <= 1 || isSpecialIface(cur[0])) {
			group.interface = id;
			openPickerGroupId = null;
			return;
		}
		const rest = cur.filter((x) => x !== id && !isSpecialIface(x));
		const next = [id, ...rest].slice(0, MAX_GROUP_IFACES);
		group.interface = denormalizeIfaceList(next);
	}

	function ensureDefaultGroupCheck(group: FircGroup) {
		const list = normalizeIfaceList(group.interface);
		if (list.length >= 2 && !isSpecialIface(list[0])) {
			if (!group.check || group.check.mode === 'none') {
				group.check = {
					mode: group.id === 'c100d001' ? 'keendns' : 'url',
					url: group.check?.url ?? '',
				};
			}
		} else if (list.length < 2 && group.check && group.check.mode !== 'none') {
			group.check = { ...group.check, mode: 'none' };
		}
	}

	function toggleBackupIface(group: FircGroup, id: string, e: MouseEvent) {
		e.stopPropagation();
		if (isSpecialIface(id)) return;
		const cur = normalizeIfaceList(group.interface);
		const idx = cur.indexOf(id);
		if (idx >= 0) {
			if (cur.length <= 1) return;
			const next = cur.filter((x) => x !== id);
			group.interface = denormalizeIfaceList(next);
			ensureDefaultGroupCheck(group);
		} else {
			if (cur.length === 0 || isSpecialIface(cur[0])) {
				group.interface = id;
				ensureDefaultGroupCheck(group);
				return;
			}
			if (cur.length >= MAX_GROUP_IFACES) return;
			group.interface = denormalizeIfaceList([...cur, id]);
			ensureDefaultGroupCheck(group);
		}
	}

	function ifaceDotState(group: FircGroup, id: string): 'alive' | 'dead' | 'unknown' | null {
		if (isSpecialIface(id)) return null;
		const liveEntry = liveMap[group.id]?.interfaces?.find((x) => x.name === id);
		if (liveEntry) return liveEntry.state;
		const groupLive = liveMap[group.id];
		if (
			groupLive &&
			(groupLive.active === id || (!groupLive.active && primaryIface(group.interface) === id))
		) {
			if (groupLive.live) return 'alive';
			if (
				groupLive.reason === 'waiting_iface' ||
				groupLive.reason === 'no_node' ||
				groupLive.reason === 'error'
			) {
				return 'dead';
			}
		}
		const tunSt = tunnelsState.find((t) => t.device === id);
		if (tunSt) {
			if (tunSt.status === 'up') return 'alive';
			if (tunSt.status === 'starting') return 'unknown';
			return 'dead';
		}
		const sysOpt = interfaces.find((x) => x.id === id);
		if (sysOpt && typeof sysOpt.up === 'boolean') {
			return sysOpt.up ? 'alive' : 'dead';
		}
		return 'unknown';
	}

	function ifaceDotTooltip(group: FircGroup, id: string): string {
		const liveEntry = liveMap[group.id]?.interfaces?.find((x) => x.name === id);
		if (liveEntry) {
			if (liveEntry.state === 'alive') return `${id}: интерфейс активен (alive)`;
			if (liveEntry.state === 'dead')
				return `${id}: недоступен${liveEntry.error ? ` (${liveEntry.error})` : ''}`;
		}
		const state = ifaceDotState(group, id);
		if (state === 'alive') return `${id}: интерфейс активен (UP)`;
		if (state === 'dead') return `${id}: интерфейс выключен или недоступен (DOWN)`;
		return `${id}: статус неизвестен`;
	}

	function matchingProxyGroup(group: FircGroup): FircProxyGroup | undefined {
		const list = normalizeIfaceList(group.interface);
		if (list.length === 0) return undefined;
		return extConfig.proxyGroups.find(
			(pg) =>
				pg.interfaces.length === list.length &&
				pg.interfaces.every((val, idx) => val === list[idx]),
		);
	}

	function applyProxyGroupToGroup(group: FircGroup, pg: FircProxyGroup) {
		if (!pg.interfaces || pg.interfaces.length === 0) return;
		group.interface = denormalizeIfaceList(pg.interfaces);
		ensureDefaultGroupCheck(group);
		openPickerGroupId = null;
	}

	function shortIfaceTag(id: string): string {
		const opt = interfaces.find((x) => x.id === id);
		if (opt?.role === 'wan' || id === 'eth3') return `Direct (${id})`;
		return id;
	}

	function getIfaceProbe(id: string): FircInterfaceProbeResult | null {
		return getEffectiveInterfaceProbe(
			id,
			$fircInterfaceProbeResults,
			$fircProbeResults,
			tunnelsState,
		);
	}

	function resolveGroupActive(group: FircGroup) {
		const list = normalizeIfaceList(group.interface);
		const probesMap: Record<string, FircInterfaceProbeResult> = {};
		for (const id of list) {
			const p = getIfaceProbe(id);
			if (p) probesMap[id] = p;
		}
		return resolveGroupActiveInterface(
			group,
			liveMap[group.id],
			liveMap,
			tunnelsState,
			interfaces,
			probesMap,
		);
	}

	function resolveIfaceState(group: FircGroup | undefined, id: string) {
		return resolveInterfaceHealth(
			id,
			group ? liveMap[group.id] : undefined,
			liveMap,
			tunnelsState,
			interfaces,
			getIfaceProbe(id),
		);
	}

	function allFallbackInterfaceIds(): string[] {
		const out = new Set<string>();
		for (const pg of extConfig.proxyGroups) {
			for (const id of pg.interfaces ?? []) {
				if (id && !isSpecialIface(id)) out.add(id);
			}
		}
		for (const g of groups) {
			const list = normalizeIfaceList(g.interface);
			if (list.length > 1) {
				for (const id of list) {
					if (id && !isSpecialIface(id)) out.add(id);
				}
			}
		}
		return Array.from(out);
	}

	async function probeFallbackInterfaces(
		ids: string[],
		opts?: { groupKey?: string; url?: string; silent?: boolean },
	): Promise<void> {
		const uniqueIds = Array.from(
			new Set(ids.map((x) => x.trim()).filter((x) => x.length > 0 && !isSpecialIface(x))),
		);
		if (uniqueIds.length === 0) return;
		const gKey = opts?.groupKey;
		if (gKey && probingGroups[gKey]) return;

		const nextProbing = { ...probingIfaces };
		for (const id of uniqueIds) nextProbing[id] = true;
		probingIfaces = nextProbing;
		if (gKey) {
			probingGroups = { ...probingGroups, [gKey]: true };
		}

		try {
			const allResults: Record<string, FircInterfaceProbeResult> = {};
			for (let i = 0; i < uniqueIds.length; i += 16) {
				const chunk = uniqueIds.slice(i, i + 16);
				const res = await api.fircProbeInterfaces(chunk, opts?.url);
				if (destroyed) return;
				const ts = res?.checkedAt ?? Math.floor(Date.now() / 1000);
				for (const [k, r] of Object.entries(res?.results ?? {})) {
					allResults[k] = { ...r, checkedAt: ts };
				}
			}
			fircInterfaceProbeResults.update((prev) => ({
				...prev,
				...allResults,
			}));

			if (!opts?.silent) {
				if (uniqueIds.length === 1) {
					const onlyId = uniqueIds[0];
					const r = allResults[onlyId];
					const lbl = friendlyIfaceLabel(onlyId, interfaces, tunnelsState);
					if (r?.ok) {
						notifications.success(
							m.firc_fallback_toast_latency({ name: lbl, ms: Math.round(r.latencyMs) }),
						);
					} else {
						notifications.warning(
							m.firc_fallback_toast_timeout({
								name: lbl,
								error: r?.error ? ` (${r.error})` : '',
							}),
						);
					}
				} else {
					const okCount = uniqueIds.filter((id) => allResults[id]?.ok).length;
					notifications.success(
						m.firc_fallback_probe_updated({ ok: okCount, total: uniqueIds.length }),
					);
				}
			}
		} catch (e) {
			if (!opts?.silent && !destroyed) {
				notifications.error(
					m.firc_fallback_probe_error({
						error: e instanceof Error ? e.message : String(e),
					}),
				);
			}
		} finally {
			if (!destroyed) {
				const cleared = { ...probingIfaces };
				for (const id of uniqueIds) delete cleared[id];
				probingIfaces = cleared;
				if (gKey) {
					const nextG = { ...probingGroups };
					delete nextG[gKey];
					probingGroups = nextG;
				}
			}
		}
	}

	async function openProxyGroupsModal() {
		openPickerGroupId = null;
		if (!(await refreshProxyGroups())) return;
		pgDraft = extConfig.proxyGroups.map((pg) => ({
			id: pg.id || randomHexId(),
			name: pg.name,
			interfaces: [...(pg.interfaces ?? [])],
		}));
		if (pgDraft.length === 0) {
			pgDraft = [
				{
					id: randomHexId(),
					name: 'Основной Fallback',
					interfaces: [],
				},
			];
		}
		proxyGroupsModalOpen = true;
	}

	function addDraftProxyGroup() {
		pgDraft = [
			...pgDraft,
			{
				id: randomHexId(),
				name: `Fallback #${pgDraft.length + 1}`,
				interfaces: [],
			},
		];
	}

	function removeDraftProxyGroup(index: number) {
		pgDraft = pgDraft.filter((_, i) => i !== index);
	}

	function toggleIfaceInDraftProxyGroup(pgIndex: number, ifaceId: string) {
		const pg = pgDraft[pgIndex];
		if (!pg) return;
		const exists = pg.interfaces.includes(ifaceId);
		if (exists) {
			pg.interfaces = pg.interfaces.filter((x) => x !== ifaceId);
		} else {
			if (pg.interfaces.length >= MAX_GROUP_IFACES) {
				notifications.error(`Максимум ${MAX_GROUP_IFACES} интерфейса в прокси-группе Fallback`);
				return;
			}
			pg.interfaces = [...pg.interfaces, ifaceId];
		}
	}

	function moveIfaceInDraftProxyGroup(pgIndex: number, iIdx: number, delta: -1 | 1) {
		const pg = pgDraft[pgIndex];
		if (!pg) return;
		const target = iIdx + delta;
		if (target < 0 || target >= pg.interfaces.length) return;
		const copy = [...pg.interfaces];
		const [item] = copy.splice(iIdx, 1);
		copy.splice(target, 0, item);
		pg.interfaces = copy;
	}

	async function saveProxyGroupsModal() {
		for (const pg of pgDraft) {
			if (!pg.name.trim()) {
				notifications.error('Укажите название для каждой прокси-группы');
				return;
			}
			if (pg.interfaces.length === 0) {
				notifications.error(`Добавьте хотя бы один интерфейс в прокси-группу «${pg.name}»`);
				return;
			}
		}
		pgSaving = true;
		try {
			const nextExt: FircExtConfig = {
				...extConfig,
				proxyGroups: pgDraft.map((pg) => ({
					id: pg.id || randomHexId(),
					name: pg.name.trim(),
					interfaces: pg.interfaces.slice(0, MAX_GROUP_IFACES),
				})),
			};
			const saved = await api.fircSaveExt(nextExt);
			extConfig = saved;
			proxyGroupRefresh++;
			proxyGroupsModalOpen = false;
			await loadAll();
			notifications.success('Прокси-группы Fallback сохранены');
		} catch (e) {
			notifications.error(`Ошибка сохранения прокси-групп: ${(e as Error).message}`);
		} finally {
			pgSaving = false;
		}
	}

	function formatIfaceButtonText(group: FircGroup): string {
		const list = normalizeIfaceList(group.interface);
		if (list.length === 0) return 'не выбран';
		const first = list[0];
		if (first === 'blackhole') return 'Блокировать';
		if (first === 'block') return 'Блокировать по DNS';
		const matchedPg = matchingProxyGroup(group);
		if (matchedPg) {
			return `${matchedPg.name} (${matchedPg.interfaces.map(shortIfaceTag).join(' → ')})`;
		}
		if (list.length === 1) {
			const opt = interfaces.find((x) => x.id === first);
			if (opt) {
				const prefix = opt.role === 'wan' ? 'Direct: ' : '';
				return `${prefix}${formatIfaceOption(opt)}`;
			}
			return first;
		}
		return list.length === 2
			? `${shortIfaceTag(list[0])} → ${shortIfaceTag(list[1])}`
			: `${shortIfaceTag(list[0])} → +${list.length - 1} резерв`;
	}

	function formatCompactOutboundLabel(
		group: FircGroup,
		activeIface: string | null,
		isFallbackGroup: boolean,
		allDead: boolean,
	): string {
		if (isFallbackGroup) {
			if (allDead) return m.firc_fallback_no_active();
			const target = activeIface || primaryIface(group.interface);
			return target ? friendlyIfaceLabel(target, interfaces, tunnelsState) : '—';
		}
		return formatIfaceButtonText(group);
	}

	function isInternalServerIface(opt: FircInterfaceOption): boolean {
		return isInternalServerFircInterface(opt);
	}

	function tunnelSortPriority(id: string): number {
		if (id.startsWith('tunvless')) return 0;
		if (id.startsWith('nwg') || id.startsWith('awg')) return 1;
		return 2;
	}

	function getPickerGroups(group: FircGroup) {
		const selectedSet = new Set(normalizeIfaceList(group.interface));
		const tunnels: { id: string; label: string; role: string }[] = [];
		const wans: { id: string; label: string; role: string }[] = [];
		const others: { id: string; label: string; role: string }[] = [];
		const seen = new Set<string>();

		for (const opt of interfaces) {
			if (!opt.id || seen.has(opt.id)) continue;
			// Hide legacy Sing-box t2s* interfaces unless currently selected by this group
			if (opt.id.startsWith('t2s') && !selectedSet.has(opt.id)) continue;
			seen.add(opt.id);
			const role = opt.role || 'other';
			const item = { id: opt.id, label: formatIfaceOption(opt), role };
			if (role === 'tunnel') {
				if (isInternalServerIface(opt) && !selectedSet.has(opt.id)) {
					others.push(item);
				} else {
					tunnels.push(item);
				}
			} else if (role === 'wan') {
				wans.push(item);
			} else {
				others.push(item);
			}
		}

		for (const id of selectedSet) {
			if (!id || isSpecialIface(id) || seen.has(id)) continue;
			seen.add(id);
			tunnels.push({ id, label: `${id} (не найден)`, role: 'tunnel' });
		}

		tunnels.sort((a, b) => {
			const pa = tunnelSortPriority(a.id);
			const pb = tunnelSortPriority(b.id);
			if (pa !== pb) return pa - pb;
			return a.id.localeCompare(b.id, undefined, { numeric: true });
		});

		return { tunnels, wans, others };
	}

	let availableInterfaces = $derived.by(() => {
		const list: { value: string; label: string; role: string }[] = [];
		const seen = new Set<string>();
		for (const opt of interfaces) {
			if (!opt.id || opt.id === 'blackhole' || opt.id === 'block' || seen.has(opt.id) || opt.id.startsWith('t2s')) continue;
			if (isInternalServerIface(opt) && gmInterface !== opt.id) continue;
			seen.add(opt.id);
			const prefix = opt.role === 'wan' ? 'Direct: ' : '';
			list.push({
				value: opt.id,
				label: `${prefix}${formatIfaceOption(opt)}`,
				role: opt.role || 'other',
			});
		}
		list.sort((a, b) => {
			if (a.role !== b.role) {
				if (a.role === 'tunnel') return -1;
				if (b.role === 'tunnel') return 1;
				if (a.role === 'wan') return -1;
				if (b.role === 'wan') return 1;
			}
			const pa = tunnelSortPriority(a.value);
			const pb = tunnelSortPriority(b.value);
			if (pa !== pb) return pa - pb;
			return a.value.localeCompare(b.value, undefined, { numeric: true });
		});
		const out: { value: string; label: string }[] = list.map(({ value, label }) => ({
			value,
			label,
		}));
		out.push({ value: 'blackhole', label: 'Блокировать (Blackhole)' });
		out.push({ value: 'block', label: 'Блокировать по DNS' });
		return out;
	});

	function groupStatusDotVariant(
		group: FircGroup,
	): 'success' | 'warning' | 'error' | 'muted' {
		if (!group.enable) return 'muted';
		const live = liveMap[group.id];
		if (!live) return 'warning';
		if (live.live) return 'success';
		if (live.reason === 'error') return 'error';
		return 'warning';
	}

	function groupStatusTooltip(group: FircGroup): string {
		if (!group.enable) return 'Группа выключена';
		const live = liveMap[group.id];
		if (!live) return 'Ожидание статуса от демона fircd';
		const iface = live.active || primaryIface(group.interface);
		if (live.live) return `Работает: правила записаны в ядро, трафик идёт через ${iface}`;
		if (live.reason === 'waiting_iface') return `Ожидает поднятия интерфейса ${iface}`;
		if (live.reason === 'no_node') return `Туннель ${iface} не имеет активного рабочего узла`;
		if (live.reason === 'rewriting') return 'Правила перезаписываются в ядро роутера';
		if (live.detail) return `Не активна: ${live.detail}`;
		return 'Не активна — демон повторяет попытку';
	}

	function resolverBadgeText(group: FircGroup): string {
		const iface = primaryIface(group.interface);
		if (iface === 'block') return 'DNS: блокировка';
		const resolve = group.resolve ?? { tunnel: true, server: '' };
		if (!resolve.tunnel) return 'DNS: общий upstream';
		if (resolve.server) return `DNS: ${resolve.server}`;
		const liveRes = liveMap[group.id]?.resolver ?? group.resolver;
		if (liveRes?.servers && liveRes.servers.length > 0) {
			return `DNS: ${liveRes.servers.join(', ')}`;
		}
		const fw = resolvers.find((r) => r.interface === iface);
		if (fw?.servers?.length) return `DNS: ${fw.servers.join(', ')}`;
		return 'DNS: общий upstream';
	}

	const MANAGED_SERVER_SUBNETS = new Set([
		'10.10.0.0/24',
		'10.66.0.0/16',
		'10.70.0.0/16',
		'10.77.0.0/16',
		'10.10.0.2/32',
		'127.0.0.0/8',
		'172.16.6.0/24',
		'172.16.7.0/24',
		'172.16.2.0/24',
		'172.20.8.0/24',
		'172.20.0.0/24',
		'172.16.1.0/24',
		'172.16.3.0/24',
	]);

	function devicesBadgeText(group: FircGroup): string {
		const allow = (group.devices?.allow ?? []).filter((x) => !MANAGED_SERVER_SUBNETS.has(x));
		const deny = group.devices?.deny ?? [];
		if (allow.length === 0 && deny.length === 0) return 'Все устройства';
		if (allow.length > 0) return `Устройства: только выбранные (${allow.length})`;
		return `Все кроме исключённых (${deny.length})`;
	}

	function shortListName(url: string): string {
		if (!url) return 'список';
		try {
			const u = new URL(url);
			const seg = u.pathname.split('/').filter(Boolean).pop();
			return seg || u.hostname;
		} catch {
			return url;
		}
	}

	function outboundToneClass(iface: string): string {
		if (!iface || iface === 'block' || iface === 'blackhole') return 'tone-block';
		if (iface.startsWith('tunvless')) return 'tone-subscription';
		if (iface.startsWith('nwg') || iface.startsWith('awg') || iface.startsWith('wg')) return 'tone-awg';
		if (iface.startsWith('t2s')) return 'tone-proxy';
		return 'tone-composite';
	}

	function groupAccentClass(group: FircGroup): string {
		if (!group.enable) return 'accent-muted';
		const iface = primaryIface(group.interface);
		if (iface === 'block') return 'accent-block';
		const variant = groupStatusDotVariant(group);
		if (variant === 'success') return 'accent-ok';
		if (variant === 'error') return 'accent-err';
		return 'accent-warn';
	}

	function groupSubtitle(group: FircGroup): string {
		const parts: string[] = [];
		const ownCount = group.rules?.length ?? 0;
		const listCount = group.list?.rulesTotal ?? group.list?.rules ?? 0;
		if (ownCount > 0) parts.push(`${ownCount} правил`);
		if (listCount > 0) parts.push(`список: ${listCount}`);
		if (parts.length === 0) parts.push('нет правил');
		return parts.join(' · ');
	}

	function formatTimestamp(ts?: number): string {
		if (!ts) return 'ещё не обновлялся';
		const d = new Date(ts * 1000);
		return d.toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	let filteredGroups = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		if (!q) return groups.map((g, index) => ({ group: g, index }));
		return groups
			.map((g, index) => ({ group: g, index }))
			.filter(({ group }) => {
				if (group.name.toLowerCase().includes(q)) return true;
				if (primaryIface(group.interface).toLowerCase().includes(q)) return true;
				if (group.list?.url?.toLowerCase().includes(q)) return true;
				return group.rules.some(
					(r) =>
						r.rule.toLowerCase().includes(q) ||
						(r.comment && r.comment.toLowerCase().includes(q)),
				);
			});
	});

	let anyExpanded = $derived(groups.some((g) => expandedGroups[g.id]));

	function toggleExpandAll() {
		const nextState = !anyExpanded;
		const next: Record<string, boolean> = {};
		for (const g of groups) {
			next[g.id] = nextState;
			if (nextState && g.list && !listPages[g.id]) {
				void loadListRulesPage(g.id, 0);
			}
		}
		expandedGroups = next;
	}

	function toggleGroupExpand(group: FircGroup) {
		const next = !expandedGroups[group.id];
		expandedGroups = { ...expandedGroups, [group.id]: next };
		if (next && group.list && !listPages[group.id]) {
			void loadListRulesPage(group.id, 0);
		}
	}

	async function loadListRulesPage(groupId: string, offset: number) {
		const existing = listPages[groupId];
		listPages = {
			...listPages,
			[groupId]: {
				offset,
				total: existing?.total ?? 0,
				matched: existing?.matched ?? 0,
				loading: true,
				rules: existing?.rules ?? [],
				edits: existing?.edits ?? {},
			},
		};
		try {
			const res = await api.fircGetGroupListRules(groupId, offset, PAGE_SIZE, searchQuery);
			const edits = listPages[groupId]?.edits ?? {};
			const rules = (res.rules ?? []).map((r) => ({
				...r,
				enable: edits[r.id]?.enable !== undefined ? edits[r.id].enable! : r.enable,
				type: edits[r.id]?.type !== undefined ? edits[r.id].type! : r.type,
			}));
			listPages = {
				...listPages,
				[groupId]: {
					offset: res.offset ?? offset,
					total: res.total ?? 0,
					matched: res.matched ?? 0,
					loading: false,
					rules,
					edits,
				},
			};
		} catch {
			listPages = {
				...listPages,
				[groupId]: {
					...(listPages[groupId] ?? {
						offset: 0,
						total: 0,
						matched: 0,
						rules: [],
						edits: {},
					}),
					loading: false,
				},
			};
		}
	}

	function toggleListRuleEnable(groupId: string, rule: FircListRule, checked: boolean) {
		const page = listPages[groupId];
		if (!page) return;
		const updatedRules = page.rules.map((r) => (r.id === rule.id ? { ...r, enable: checked } : r));
		const updatedEdits = {
			...page.edits,
			[rule.id]: { ...page.edits[rule.id], enable: checked },
		};
		listPages = {
			...listPages,
			[groupId]: {
				...page,
				rules: updatedRules,
				edits: updatedEdits,
			},
		};
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
			notifications.success(`Синхронизация списка группы «${group.name}» запущена`);
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
						await loadListRulesPage(group.id, 0);
					}
				} finally {
					syncingGroup = { ...syncingGroup, [group.id]: false };
				}
			}, 1800);
		} catch (e) {
			syncingGroup = { ...syncingGroup, [group.id]: false };
			notifications.error(`Ошибка обновления списка: ${(e as Error).message}`);
		}
	}

	async function saveAllChanges() {
		if (saving || !isDirty) return;
		for (const g of groups) {
			const emptyRule = g.rules.find((r) => !r.rule.trim());
			if (emptyRule) {
				notifications.error(`В группе «${g.name || 'без имени'}» есть пустое правило`);
				expandedGroups = { ...expandedGroups, [g.id]: true };
				return;
			}
		}
		saving = true;
		try {
			// Save any list rule edits first
			for (const [groupId, page] of Object.entries(listPages)) {
				const editEntries = Object.entries(page.edits).map(([id, ed]) => ({
					id,
					...ed,
				}));
				if (editEntries.length > 0) {
					await api.fircUpdateGroupListRules(groupId, editEntries, false);
				}
			}

			const payload = groups.map((g) => ({
				...g,
				name: g.name.trim() || 'group',
				rules: g.rules.map((r) => ({
					...r,
					rule: r.rule.trim(),
				})),
			}));
			const res = await api.fircSaveGroups(payload, true);
			const normalized = (res?.groups ?? payload).map(normalizeGroup);
			groups = normalized;
			baselineGroups = normalized.map(normalizeGroup);
			baselineJson = serializeForDiff(normalized);

			draftedAt = null;
			clearFircGroupsDraft();

			// Clear pending list edits
			const cleanedPages: Record<string, GroupListPageState> = {};
			for (const [k, v] of Object.entries(listPages)) {
				cleanedPages[k] = { ...v, edits: {} };
			}
			listPages = cleanedPages;

			await Promise.all([pollLiveState(), reloadFircAll()]);
			lastHandledSyncNonce = get(fircSyncNonce);
			notifications.success('Группы маршрутизации FIRC сохранены');
		} catch (e) {
			notifications.error(`Ошибка сохранения групп: ${(e as Error).message}`);
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
		if (res.lines.length === 0) {
			const noteParts: string[] = [];
			if (res.skippedRegexOrCidr > 0) noteParts.push(`подсети/regex: ${res.skippedRegexOrCidr}`);
			if (res.skippedRemoteLists > 0) noteParts.push(`удалённые списки: ${res.skippedRemoteLists}`);
			notifications.warning(
				`Нет активных доменных правил для копирования${noteParts.length ? ` (пропущены ${noteParts.join(', ')})` : ''}`,
			);
			return;
		}
		const text = res.lines.join('\n');
		if (await copyToClipboard(text)) {
			const skippedParts: string[] = [];
			if (res.skippedRegexOrCidr > 0) skippedParts.push(`подсети/regex: ${res.skippedRegexOrCidr}`);
			if (res.skippedRemoteLists > 0) skippedParts.push(`списков по URL: ${res.skippedRemoteLists}`);
			notifications.success(
				`Скопировано доменов: ${res.lines.length}${skippedParts.length ? ` (пропущено ${skippedParts.join(', ')})` : ''}`,
			);
		} else {
			notifications.error('Не удалось скопировать домены в буфер обмена');
		}
	}

	function handleImportFileChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = () => {
			try {
				const raw = JSON.parse(String(reader.result));
				const imported: FircGroup[] = Array.isArray(raw?.groups)
					? raw.groups
					: Array.isArray(raw)
						? raw
						: [];
				if (imported.length === 0) {
					notifications.error('Файл не содержит групп FIRC');
					return;
				}
				const normalized = imported.map((g) =>
					normalizeGroup({ ...g, id: randomHexId() }),
				);
				groups = [...groups, ...normalized];
				notifications.success(`Добавлено групп: ${normalized.length}. Нажмите «Сохранить».`);
			} catch {
				notifications.error('Некорректный формат файла конфигурации');
			}
		};
		reader.readAsText(file);
		input.value = '';
	}

	// ── Modal 1: Create / Edit Group Settings (List URL + Catalog + DNS) ──
	let groupModalOpen = $state(false);
	let groupModalMode = $state<'create' | 'edit'>('create');
	let groupModalIndex = $state<number | null>(null);
	let gmName = $state('');
	let gmInterface = $state('');
	let gmDnsMode = $state<'auto' | 'own' | 'off'>('auto');
	let gmDnsServer = $state('');
	let gmCheckMode = $state<'none' | 'url' | 'keendns'>('none');
	let gmCheckUrl = $state('');
	let gmListEnabled = $state(false);
	let gmListUrl = $state('');
	let gmListInterval = $state(86400);
	let gmCatalog = $state<FircCatalog | null>(null);
	let gmCatalogLoading = $state(false);
	let gmPreviewLoading = $state(false);
	let gmPreviewData = $state<FircListPreviewRes | null>(null);
	let gmPreviewError = $state<string | null>(null);

	function openCreateGroupModal() {
		groupModalMode = 'create';
		groupModalIndex = null;
		gmName = '';
		gmInterface = availableInterfaces[0]?.value ?? 'block';
		gmDnsMode = 'auto';
		gmDnsServer = '';
		gmCheckMode = 'none';
		gmCheckUrl = '';
		gmListEnabled = false;
		gmListUrl = '';
		gmListInterval = 86400;
		gmPreviewData = null;
		gmPreviewError = null;
		groupModalOpen = true;
		void ensureCatalogLoaded();
	}

	function openEditGroupModal(index: number) {
		const g = groups[index];
		if (!g) return;
		groupModalMode = 'edit';
		groupModalIndex = index;
		gmName = g.name;
		gmInterface = primaryIface(g.interface);
		const res = g.resolve ?? { tunnel: true, server: '' };
		gmDnsMode = !res.tunnel ? 'off' : res.server ? 'own' : 'auto';
		gmDnsServer = res.server ?? '';
		const ifaceCount = normalizeIfaceList(g.interface).length;
		gmCheckMode = g.check?.mode ?? (ifaceCount >= 2 ? (g.id === 'c100d001' ? 'keendns' : 'url') : 'none');
		gmCheckUrl = g.check?.url ?? '';
		gmListEnabled = Boolean(g.list?.url);
		gmListUrl = g.list?.url ?? '';
		gmListInterval = g.list?.interval ?? 86400;
		gmPreviewData = null;
		gmPreviewError = null;
		groupModalOpen = true;
		void ensureCatalogLoaded();
	}

	async function ensureCatalogLoaded() {
		if ((gmCatalog?.sources?.length ?? 0) > 0 || gmCatalogLoading) return;
		gmCatalogLoading = true;
		try {
			gmCatalog = await api.fircGetListCatalog();
		} catch {
			// catalog optional
		} finally {
			gmCatalogLoading = false;
		}
	}

	let catalogSearch = $derived.by(() =>
		gmListEnabled ? searchFircCatalog(gmCatalog, gmListUrl, 100) : { items: [], total: 0 },
	);
	let catalogMatches = $derived(catalogSearch.items);

	async function handlePreviewList() {
		if (!gmListUrl.trim()) return;
		gmPreviewLoading = true;
		gmPreviewError = null;
		try {
			gmPreviewData = await api.fircPreviewGroupList(gmListUrl.trim());
		} catch (e) {
			gmPreviewError = (e as Error).message;
			gmPreviewData = null;
		} finally {
			gmPreviewLoading = false;
		}
	}

	function submitGroupModal() {
		const resolve =
			gmDnsMode === 'off'
				? { tunnel: false, server: '' }
				: gmDnsMode === 'own'
					? { tunnel: true, server: gmDnsServer.trim() }
					: { tunnel: true, server: '' };

		const listObj =
			gmListEnabled && gmListUrl.trim()
				? {
						url: gmListUrl.trim(),
						interval: gmListInterval,
						lastUpdate:
							groupModalMode === 'edit' && groupModalIndex !== null
								? (groups[groupModalIndex]?.list?.lastUpdate ?? 0)
								: 0,
						rulesTotal:
							groupModalMode === 'edit' && groupModalIndex !== null
								? (groups[groupModalIndex]?.list?.rulesTotal ?? 0)
								: 0,
					}
				: null;

		if (groupModalMode === 'edit' && groupModalIndex !== null && groups[groupModalIndex]) {
			const cur = groups[groupModalIndex];
			const curList = normalizeIfaceList(cur.interface);
			let nextIface: string | string[] = gmInterface;
			if (!isSpecialIface(gmInterface) && curList.length > 1 && !isSpecialIface(curList[0])) {
				const rest = curList.filter((x) => x !== gmInterface && !isSpecialIface(x));
				nextIface = denormalizeIfaceList([gmInterface, ...rest].slice(0, MAX_GROUP_IFACES));
			}
			const hasChain = normalizeIfaceList(nextIface).length >= 2 && !isSpecialIface(primaryIface(nextIface));
			const nextCheck: FircGroupCheck | undefined = hasChain
				? {
						...(cur.check ?? {}),
						mode: gmCheckMode,
						url: gmCheckMode === 'url' ? gmCheckUrl.trim() : '',
					}
				: cur.check
					? { ...cur.check, mode: 'none', url: '' }
					: undefined;
			groups[groupModalIndex] = {
				...cur,
				name: gmName.trim() || cur.name || 'group',
				interface: nextIface,
				resolve,
				...(nextCheck ? { check: nextCheck } : {}),
				list: listObj,
			};
		} else {
			const created = normalizeGroup({
				id: randomHexId(),
				name: gmName.trim() || `group-${groups.length + 1}`,
				interface: gmInterface,
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: listObj,
				resolve,
			});
			groups = [...groups, created];
		}
		groupModalOpen = false;
	}

	// ── Modal 2: Devices Filter Modal ──
	let devicesModalOpen = $state(false);
	let devicesModalIndex = $state<number | null>(null);

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

	let devicesModalBulk = $state(false);

	function openDevicesModal(index: number) {
		if (!groups[index]) return;
		devicesModalBulk = false;
		devicesModalIndex = index;
		devicesModalOpen = true;
	}

	function openBulkDevicesModal() {
		if (selectedGroupIds.size === 0) return;
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
	let groupSelectMode = $state(false);
	let selectedGroupIds = $state<Set<string>>(new Set());
	let confirmBulkDeleteGroupsOpen = $state(false);
	let selectedRuleIdsByGroup = $state<Record<string, Set<string>>>({});
	let bulkRulesEditorOpen = $state(false);
	let bulkRulesEditorGroupIndex = $state<number | null>(null);
	let bulkRulesEditorGroup = $derived(
		bulkRulesEditorGroupIndex !== null ? (groups[bulkRulesEditorGroupIndex] ?? null) : null,
	);

	function toggleGroupSelectMode() {
		groupSelectMode = !groupSelectMode;
		if (!groupSelectMode) {
			selectedGroupIds = new Set();
			confirmBulkDeleteGroupsOpen = false;
		}
	}

	function toggleGroupSelected(id: string) {
		const next = new Set(selectedGroupIds);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		selectedGroupIds = next;
	}

	function selectAllVisibleGroups() {
		selectedGroupIds = new Set(filteredGroups.map((entry) => entry.group.id));
	}

	function clearSelectedGroups() {
		selectedGroupIds = new Set();
	}

	function runBulkGroupAction(action: BulkGroupAction) {
		const res = applyBulkGroupAction(groups, selectedGroupIds, action);
		if (res.affected === 0) return;
		groups = res.groups;
		if (action.kind === 'delete') {
			const remainingIds = new Set(res.groups.map((g) => g.id));
			selectedGroupIds = new Set(Array.from(selectedGroupIds).filter((id) => remainingIds.has(id)));
			confirmBulkDeleteGroupsOpen = false;
			notifications.success(m.firc_bulk_groups_deleted_toast({ count: res.affected }));
		} else {
			notifications.success(m.firc_bulk_groups_updated_toast({ count: res.affected }));
		}
	}

	function getSelectedRuleSet(groupId: string): Set<string> {
		return selectedRuleIdsByGroup[groupId] ?? new Set<string>();
	}

	function toggleRuleSelected(groupId: string, ruleId: string) {
		const current = getSelectedRuleSet(groupId);
		const next = new Set(current);
		if (next.has(ruleId)) next.delete(ruleId);
		else next.add(ruleId);
		selectedRuleIdsByGroup = { ...selectedRuleIdsByGroup, [groupId]: next };
	}

	function selectAllRulesInGroup(group: FircGroup) {
		selectedRuleIdsByGroup = {
			...selectedRuleIdsByGroup,
			[group.id]: new Set(group.rules.map((r) => r.id)),
		};
	}

	function clearSelectedRulesInGroup(groupId: string) {
		selectedRuleIdsByGroup = {
			...selectedRuleIdsByGroup,
			[groupId]: new Set(),
		};
	}

	function runBulkRuleAction(groupIndex: number, action: BulkRuleAction) {
		const src = groups[groupIndex];
		if (!src) return;
		const sel = getSelectedRuleSet(src.id);
		const targetGroup =
			action.kind === 'move' ? groups.find((g) => g.id === action.targetGroupId) : undefined;
		const res = applyBulkRuleAction(groups, groupIndex, sel, action);
		if (res.affected === 0) return;
		groups = res.groups;
		if (action.kind === 'move') {
			clearSelectedRulesInGroup(src.id);
			expandedGroups = { ...expandedGroups, [action.targetGroupId]: true };
			notifications.success(
				m.firc_bulk_rules_moved_toast({
					target: targetGroup?.name ?? '',
					count: res.affected,
				}),
			);
		} else if (action.kind === 'delete') {
			clearSelectedRulesInGroup(src.id);
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
		clearSelectedRulesInGroup(groupId);
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
		const lines = importRulesText
			.split(/\r?\n/)
			.map((l) => l.trim())
			.filter((l) => l && !l.startsWith('#'));
		if (lines.length === 0) {
			importRulesModalOpen = false;
			return;
		}
		const newRules: FircRule[] = lines.map((line) => ({
			id: randomHexId(),
			type: detectFircRuleType(line),
			rule: line,
			enable: true,
		}));
		const g = groups[importRulesGroupIndex];
		groups[importRulesGroupIndex].rules = [...newRules, ...g.rules];
		expandedGroups = { ...expandedGroups, [g.id]: true };
		importRulesModalOpen = false;
		notifications.success(`Добавлено правил: ${newRules.length}`);
	}

	// ── Modal 4: Service Catalog (Sing-box style .srs catalog) ──
	let serviceCatalogOpen = $state(false);
	let serviceCatalogMode = $state<'batch' | 'single'>('batch');
	let serviceCatalogSubmitting = $state(false);

	function presetSrsUrl(p: CatalogPreset): string {
		return (
			p.engines.singbox?.ruleSets?.[0]?.url ??
			p.engines.dns?.subscriptionUrl ??
			''
		);
	}

	function groupServiceKey(group: FircGroup): string {
		const url = group.list?.url?.trim().toLowerCase();
		if (url && $presetCatalog.length > 0) {
			const match = $presetCatalog.find(
				(p) => presetSrsUrl(p).toLowerCase() === url,
			);
			if (match) return match.iconSlug || match.id;
		}
		return group.name.toLowerCase();
	}

	let catalogTunnelOptions = $derived.by<DropdownOption[]>(() => {
		const opts: DropdownOption[] = [];
		const seen = new Set<string>();
		for (const opt of interfaces) {
			if (!opt.id || seen.has(opt.id) || opt.id.startsWith('t2s') || isInternalServerIface(opt)) {
				continue;
			}
			if (opt.role === 'tunnel') {
				seen.add(opt.id);
				opts.push({
					value: opt.id,
					label: formatIfaceOption(opt),
					group: 'Туннели (VPN / Прокси)',
				});
			}
		}
		for (const opt of interfaces) {
			if (!opt.id || seen.has(opt.id)) continue;
			if (opt.role === 'wan') {
				seen.add(opt.id);
				opts.push({
					value: opt.id,
					label: `Direct (${formatIfaceOption(opt)})`,
					group: 'Напрямую (Провайдер WAN)',
				});
			}
		}
		opts.push({
			value: 'blackhole',
			label: 'Блокировать (Blackhole)',
			group: 'Блокировка',
		});
		opts.push({
			value: 'block',
			label: 'Блокировать по DNS',
			group: 'Блокировка',
		});
		return opts;
	});

	let existingCatalogNames = $derived.by<string[]>(() => {
		const names = new Set<string>();
		const urls = new Set<string>();
		for (const g of groups) {
			if (g.name) names.add(g.name);
			if (g.list?.url) urls.add(g.list.url.trim().toLowerCase());
		}
		for (const p of $presetCatalog) {
			const u = presetSrsUrl(p).toLowerCase();
			if (u && urls.has(u)) {
				names.add(p.name);
			}
		}
		return Array.from(names);
	});

	let existingCatalogRuleSetTags = $derived.by<string[]>(() => {
		const tags = new Set<string>();
		const urls = new Set<string>();
		const lowerNames = new Set(groups.map((g) => g.name.trim().toLowerCase()));
		for (const g of groups) {
			if (g.list?.url) urls.add(g.list.url.trim().toLowerCase());
		}
		for (const p of $presetCatalog) {
			const u = presetSrsUrl(p).toLowerCase();
			if ((u && urls.has(u)) || lowerNames.has(p.name.toLowerCase()) || lowerNames.has(p.id.toLowerCase())) {
				for (const rs of p.engines.singbox?.ruleSets ?? []) {
					if (rs.tag) tags.add(rs.tag);
				}
			}
		}
		return Array.from(tags);
	});

	function openBatchServiceCatalog() {
		serviceCatalogMode = 'batch';
		serviceCatalogOpen = true;
	}

	function openSingleServiceCatalog() {
		serviceCatalogMode = 'single';
		serviceCatalogOpen = true;
	}

	function handleSingleCatalogPick(presets: CatalogPreset[]) {
		const picked = presets[0];
		if (!picked) {
			serviceCatalogOpen = false;
			return;
		}
		const url = presetSrsUrl(picked);
		if (!gmName.trim()) {
			gmName = picked.name;
		}
		if (url) {
			gmListEnabled = true;
			gmListUrl = url;
			gmPreviewData = null;
			gmPreviewError = null;
		}
		serviceCatalogOpen = false;
	}

	async function handleBatchCatalogCreate(presets: CatalogPreset[], targetIface: string) {
		if (presets.length === 0 || serviceCatalogSubmitting) return;
		serviceCatalogSubmitting = true;
		try {
			const createdGroups: FircGroup[] = [];
			for (const p of presets) {
				const url = presetSrsUrl(p);
				if (!url) continue;
				const iface = targetIface || availableInterfaces[0]?.value || 'block';
				const isBlk = iface === 'block' || iface === 'blackhole';
				const ng = normalizeGroup({
					id: randomHexId(),
					name: p.name,
					interface: iface,
					enable: true,
					devices: { allow: [], deny: [] },
					rules: [],
					list: {
						url,
						interval: 86400,
						lastUpdate: 0,
						rulesTotal: 0,
						rules: 0,
					},
					resolve: {
						tunnel: !isBlk,
						server: '',
					},
					...(iface === 'block' ? { block: 'null' as FircBlockAnswer } : {}),
				});
				createdGroups.push(ng);
			}

			if (createdGroups.length === 0) {
				serviceCatalogOpen = false;
				return;
			}

			const nextGroups = [...groups, ...createdGroups];
			const payload = nextGroups.map((g) => ({
				...g,
				name: g.name.trim() || 'group',
				rules: g.rules.map((r) => ({
					...r,
					rule: r.rule.trim(),
				})),
			}));

			const res = await api.fircSaveGroups(payload, true);
			const normalized = (res?.groups ?? payload).map(normalizeGroup);
			groups = normalized;
			baselineGroups = normalized.map(normalizeGroup);
			baselineJson = serializeForDiff(normalized);

			draftedAt = null;
			clearFircGroupsDraft();
			serviceCatalogOpen = false;

			notifications.success(
				`Создано групп из каталога сервисов: ${createdGroups.length}. Синхронизация списков .srs запущена.`,
			);

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
			notifications.error(`Ошибка создания групп из каталога: ${(e as Error).message}`);
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
					Несохранённые изменения
					<span class="firc-staging-sub">
						· правки сохранены в черновике (не пропадут при переходе по вкладкам)
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
					Сбросить
				</Button>
				<Button
					variant="primary"
					size="sm"
					disabled={saving}
					loading={saving}
					onclick={saveAllChanges}
				>
					Сохранить изменения
				</Button>
			</div>
		</div>
	{/if}

	<!-- Top Toolbar (styled like Sing-box / AWG Manager toolbar) -->
	<div class="groups-toolbar">
		<div class="toolbar-left">
			<span class="groups-count">
				Групп: <strong>{groups.length}</strong>
				{#if netfilterState?.dirty}
					<Badge variant="warning" size="sm">Перезапись ядра</Badge>
				{/if}
			</span>
			<div class="search-box">
				<Search size={14} class="search-icon" />
				<input
					type="search"
					class="inp search-inp"
					placeholder="Поиск по группам и правилам..."
					bind:value={searchQuery}
				/>
			</div>
		</div>

		<div class="toolbar-actions">
			<div class="group-view-switch" aria-label="Вид групп">
				<button type="button" class:active={groupView === 'grid'} aria-pressed={groupView === 'grid'} onclick={() => setGroupView('grid')}>Карточки</button>
				<button type="button" class:active={groupView === 'list'} aria-pressed={groupView === 'list'} onclick={() => setGroupView('list')}>Список</button>
			</div>
			<button
				type="button"
				class="route-action-btn"
				hidden={groupView === 'grid'}
				title={anyExpanded ? 'Свернуть все группы' : 'Развернуть все группы'}
				onclick={toggleExpandAll}
			>
				{#if anyExpanded}
					<ChevronsDownUp size={15} />
				{:else}
					<ChevronsUpDown size={15} />
				{/if}
			</button>

			<input
				type="file"
				accept=".firc,.mtrickle,.json"
				style="display: none"
				bind:this={importFileInput}
				onchange={handleImportFileChange}
			/>
			<Button variant="ghost" size="sm" onclick={() => importFileInput?.click()}>
				{#snippet iconBefore()}<Download size={14} />{/snippet}
				Импорт
			</Button>
			<Button variant="ghost" size="sm" onclick={exportConfig}>
				{#snippet iconBefore()}<Upload size={14} />{/snippet}
				Экспорт
			</Button>
			<Button
				variant="ghost"
				size="sm"
				title="Скопировать включённые доменные правила отображаемых групп в буфер обмена (по одному в строке)"
				onclick={() => copySelectedGroupDomains()}
			>
				{#snippet iconBefore()}<Copy size={14} />{/snippet}
				Копировать домены
			</Button>
			<Button variant="secondary" size="sm" onclick={openProxyGroupsModal}>
				{#snippet iconBefore()}<Shield size={14} />{/snippet}
				Прокси-группы Fallback ({extConfig.proxyGroups.length})
			</Button>
			<Button variant="secondary" size="sm" onclick={openBatchServiceCatalog}>
				{#snippet iconBefore()}<LayoutGrid size={14} />{/snippet}
				Каталог сервисов
			</Button>
			<Button
				variant={groupSelectMode ? 'primary' : 'ghost'}
				size="sm"
				onclick={toggleGroupSelectMode}
			>
				{#snippet iconBefore()}<Edit3 size={14} />{/snippet}
				{groupSelectMode ? m.firc_bulk_select_groups_done() : m.firc_bulk_select_groups_btn()}
			</Button>
			<Button variant="secondary" size="sm" onclick={openCreateGroupModal}>
				{#snippet iconBefore()}<Plus size={14} />{/snippet}
				Добавить группу
			</Button>
		</div>
	</div>

	{#if groupSelectMode}
		<FircBulkGroupsBar
			selectedCount={selectedGroupIds.size}
			visibleCount={filteredGroups.length}
			interfaces={availableInterfaces}
			proxyGroups={extConfig.proxyGroups}
			onSelectAllVisible={selectAllVisibleGroups}
			onDeselectAll={clearSelectedGroups}
			onDone={toggleGroupSelectMode}
			onSetInterface={(iface) => runBulkGroupAction({ kind: 'setInterface', iface })}
			onOpenDevices={openBulkDevicesModal}
			onEnable={() => runBulkGroupAction({ kind: 'enable', enable: true })}
			onDisable={() => runBulkGroupAction({ kind: 'enable', enable: false })}
			onCopyDomains={() => copySelectedGroupDomains(Array.from(selectedGroupIds))}
			onDelete={() => (confirmBulkDeleteGroupsOpen = true)}
		/>
	{/if}

	<p class="priority-hint">
		Порядок групп определяет приоритет маршрутизации сверху вниз. Для каждого домена или IP применяется первая совпавшая группа.
	</p>

	{#if extConfig.proxyGroups.length > 0}
		{@const allFallbackIds = allFallbackInterfaceIds()}
		{@const singlePg = extConfig.proxyGroups.length === 1}
		<div class="pg-panel">
			<div class="pg-panel-head">
				<div class="pg-panel-title">
					<Shield size={13} />
					<span>{m.firc_fallback_panel_title()}</span>
				</div>
				<div class="pg-panel-actions">
					{#if allFallbackIds.length > 0}
						<button
							type="button"
							class="pg-action-btn"
							disabled={Boolean(probingGroups['__all_pg__'])}
							onclick={() => probeFallbackInterfaces(allFallbackIds, { groupKey: '__all_pg__' })}
							title={m.firc_fallback_test_all_title()}
						>
							<Zap size={12} class={probingGroups['__all_pg__'] ? 'spin' : ''} />
							<span>
								{probingGroups['__all_pg__']
									? m.firc_fallback_testing()
									: singlePg
										? m.firc_fallback_test()
										: m.firc_fallback_test_all()}
							</span>
						</button>
					{/if}
					<button
						type="button"
						class="pg-action-btn ghost"
						onclick={openProxyGroupsModal}
						title={m.firc_fallback_configure_chain()}
					>
						<Settings size={12} />
						<span>{m.firc_fallback_edit()}</span>
					</button>
				</div>
			</div>

			<div class="pg-rows">
				{#each extConfig.proxyGroups as pg (pg.id)}
					{@const rt = resolveProxyGroupRuntime(
						pg,
						groups,
						liveMap,
						tunnelsState,
						interfaces,
						$fircInterfaceProbeResults,
						$fircProbeResults,
					)}
					{@const isPgTesting = Boolean(probingGroups[`pg:${pg.id}`])}
					<div class="pg-row" class:is-backup={rt.isOnBackup} class:all-dead={rt.allDead}>
						<div class="pg-row-ident">
							<span
								class="iface-dot"
								class:alive={Boolean(rt.activeIface) && !rt.isOnBackup}
								class:backup={rt.isOnBackup}
								class:dead={rt.allDead}
								class:unknown={!rt.activeIface && !rt.allDead}
							></span>
							<strong class="pg-row-name" title={pg.name}>{pg.name}</strong>
							{#if rt.isOnBackup && rt.activeIface}
								<span
									class="backup-badge"
									title={m.firc_fallback_active_backup_title({ iface: rt.activeIface })}
								>
									{m.firc_fallback_backup_prefix()}
								</span>
							{:else if rt.allDead}
								<span class="dead-badge" title={m.firc_fallback_all_dead_title()}>
									{m.firc_fallback_unavailable()}
								</span>
							{/if}
						</div>

						<div class="pg-chain-inline">
							{#each rt.members as mem, mIdx (mem.id)}
								{@const tone = probeLatencyTone(mem.probe)}
								{@const checking = Boolean(probingIfaces[mem.id])}
								{#if mIdx > 0}
									<span class="pg-chain-sep" aria-hidden="true">›</span>
								{/if}
								<button
									type="button"
									class="pg-node-pill"
									class:is-active={mem.isActive && !rt.isOnBackup}
									class:is-backup={mem.isActive && rt.isOnBackup}
									class:is-dead={mem.state === 'dead'}
									disabled={checking}
									onclick={() => probeFallbackInterfaces([mem.id])}
									title={`${mem.id}${mem.isActive ? ` · ${m.firc_fallback_active_marker()}` : ''}${mem.error ? ` · ${mem.error}` : ''} · ${m.firc_fallback_test_iface_title({ name: mem.label })}`}
								>
									<span
										class="pg-mem-dot"
										class:active={mem.isActive && !rt.isOnBackup}
										class:backup={mem.isActive && rt.isOnBackup}
										class:alive={!mem.isActive && mem.state === 'alive'}
										class:dead={mem.state === 'dead'}
										class:unknown={!mem.isActive && mem.state === 'unknown'}
									></span>
									<span class="pg-node-name">{mem.label}</span>
									<span class="pg-node-delay {tone}">
										{#if checking}
											<RefreshCw size={10} class="spin" />
										{:else}
											{formatProbeLatency(mem.probe)}
										{/if}
									</span>
								</button>
							{/each}
						</div>

						{#if !singlePg}
							<button
								type="button"
								class="pg-action-btn mini icon-only"
								disabled={isPgTesting || pg.interfaces.length === 0}
								onclick={() => probeFallbackInterfaces(pg.interfaces, { groupKey: `pg:${pg.id}` })}
								title={m.firc_fallback_test_group_title({ name: pg.name })}
							>
								<Zap size={11} class={isPgTesting ? 'spin' : ''} />
							</button>
						{/if}
					</div>
				{/each}
			</div>
		</div>
	{/if}

	{#if loading}
		<div class="empty-box">Загрузка групп маршрутизации FIRC...</div>
	{:else if filteredGroups.length === 0}
		<div class="empty-box">
			{#if searchQuery.trim()}
				Ничего не найдено по запросу «{searchQuery}»
			{:else}
				Групп маршрутизации пока нет. Нажмите «Добавить группу» для создания.
			{/if}
		</div>
	{:else}
		<div class="cards-stack" class:compact-grid={groupView === 'grid'}>
			{#each filteredGroups as { group, index } (group.id)}
				{@const orderStr = String(index + 1).padStart(2, '0')}
				{@const isExpanded = Boolean(expandedGroups[group.id])}
				{@const ifaceList = normalizeIfaceList(group.interface)}
				{@const ifaceVal = primaryIface(group.interface)}
				{@const isBlock = ifaceVal === 'block'}
				{@const isBlackhole = ifaceVal === 'blackhole'}
				{@const currentPage = rulePageMap[group.id] ?? 1}
				{@const totalRulePages = Math.max(1, Math.ceil(group.rules.length / PAGE_SIZE))}
				{@const pagedRules = group.rules.slice(
					(currentPage - 1) * PAGE_SIZE,
					currentPage * PAGE_SIZE,
				)}
				{@const listPage = listPages[group.id]}
				{@const hasCustomDevices = (group.devices?.allow?.length ?? 0) > 0 || (group.devices?.deny?.length ?? 0) > 0}
				{@const toneCls = outboundToneClass(ifaceVal)}
				{@const activeRes = resolveGroupActive(group)}
				{@const activeIface = activeRes.active}
				{@const activeBackup = activeRes.isBackup ? activeIface : null}
				{@const isFallbackGroup = ifaceList.length > 1 && !isSpecialIface(ifaceList[0])}
				{@const matchedPg = matchingProxyGroup(group)}
				{@const primaryProbe = !isBlock && !isBlackhole && (activeIface || ifaceVal) ? getIfaceProbe(activeIface || ifaceVal) : null}
				{@const isPickerOpen = openPickerGroupId === group.id}
				{@const pickerGroups = getPickerGroups(group)}
				{@const canAddMoreIfaces = ifaceList.length > 0 && !isSpecialIface(ifaceList[0]) && ifaceList.length < MAX_GROUP_IFACES}
				{@const selectedRuleSet = getSelectedRuleSet(group.id)}

				<div
					class="card-wrap {groupAccentClass(group)}"
					class:disabled-card={!group.enable}
					class:picker-open-card={isPickerOpen}
					class:bulk-selected-card={groupSelectMode && selectedGroupIds.has(group.id)}
				>
					{#if groupView === 'grid'}
						<div class="compact-group-card">
							<div class="compact-group-heading">
								{#if groupSelectMode}
									<input
										type="checkbox"
										class="bulk-check"
										checked={selectedGroupIds.has(group.id)}
										aria-label={m.firc_bulk_groups_select_aria({ name: group.name })}
										onchange={() => toggleGroupSelected(group.id)}
									/>
								{/if}
								<span class="compact-priority" title="Приоритет правила">{orderStr}</span>
								<StatusDot variant={groupStatusDotVariant(group)} size="sm" />
								<strong>{group.name}</strong>
								<Badge variant={group.enable ? 'muted' : 'warning'} size="xs">{group.enable ? 'Включено' : 'Выключено'}</Badge>
							</div>
							{#if isFallbackGroup}
								{@const isGridTesting = Boolean(probingGroups[`grp:${group.id}`])}
								{@const targetId = activeIface || ifaceVal}
								{@const isTargetChecking = Boolean(targetId && probingIfaces[targetId])}
								<div class="compact-out-block">
									<div class="compact-group-output">
										<ArrowRight size={13} class="compact-out-arrow" />
										<span
											class="iface-dot"
											class:alive={Boolean(activeIface) && !activeBackup}
											class:backup={Boolean(activeBackup)}
											class:dead={activeRes.allDead}
											class:unknown={!activeIface && !activeRes.allDead}
										></span>
										<span class="compact-out-name">
											{formatCompactOutboundLabel(group, activeIface, true, activeRes.allDead)}
										</span>
										{#if !activeRes.allDead && primaryProbe && targetId}
											<button
												type="button"
												class="compact-delay-btn {probeLatencyTone(primaryProbe)}"
												disabled={isTargetChecking}
												onclick={() => probeFallbackInterfaces([targetId])}
												title={m.firc_fallback_test_iface_title({ name: friendlyIfaceLabel(targetId, interfaces, tunnelsState) })}
											>
												{#if isTargetChecking}
													<RefreshCw size={10} class="spin" />
												{:else}
													{formatProbeLatency(primaryProbe)}
												{/if}
											</button>
										{/if}
										{#if activeBackup}
											<span class="backup-badge">{m.firc_fallback_backup_prefix()}</span>
										{:else}
											<span class="fallback-count-chip">+{ifaceList.length - 1}</span>
										{/if}
										<button
											type="button"
											class="pg-action-btn mini icon-only compact-chain-probe"
											disabled={isGridTesting}
											onclick={() => probeFallbackInterfaces(ifaceList, { groupKey: `grp:${group.id}` })}
											title={m.firc_fallback_test_chain_title()}
										>
											<Zap size={10} class={isGridTesting ? 'spin' : ''} />
										</button>
									</div>
									<div
										class="compact-chain-sub"
										title={`${matchedPg ? `${matchedPg.name}: ` : ''}${ifaceList.map((id) => friendlyIfaceLabel(id, interfaces, tunnelsState)).join(' → ')}`}
									>
										{matchedPg ? `${matchedPg.name}: ` : ''}{ifaceList
											.map((id) => {
												const p = getIfaceProbe(id);
												return `${friendlyIfaceLabel(id, interfaces, tunnelsState)}${p ? ` ${formatProbeLatency(p)}` : ''}`;
											})
											.join(' › ')}
									</div>
								</div>
							{:else}
								{@const singleTarget = !isBlock && !isBlackhole ? (activeIface || ifaceVal) : ''}
								{@const isSingleChecking = Boolean(singleTarget && probingIfaces[singleTarget])}
								<div class="compact-group-output">
									<ArrowRight size={13} class="compact-out-arrow" />
									{#if !isBlock && !isBlackhole && singleTarget}
										<span class="iface-dot {ifaceDotState(group, singleTarget) ?? 'unknown'}"></span>
									{/if}
									<span class="compact-out-name">{formatIfaceButtonText(group)}</span>
									{#if !isBlock && !isBlackhole && primaryProbe && singleTarget}
										<button
											type="button"
											class="compact-delay-btn {probeLatencyTone(primaryProbe)}"
											disabled={isSingleChecking}
											onclick={() => probeFallbackInterfaces([singleTarget])}
											title={m.firc_fallback_test_iface_title({ name: friendlyIfaceLabel(singleTarget, interfaces, tunnelsState) })}
										>
											{#if isSingleChecking}
												<RefreshCw size={10} class="spin" />
											{:else}
												{formatProbeLatency(primaryProbe)}
											{/if}
										</button>
									{/if}
								</div>
							{/if}
							<span class="compact-group-meta">{group.rules.length} своих правил{group.list?.url ? ` · ${group.list.rulesTotal || group.list.rules || 0} из списка` : ''}</span>
							{#if hasCustomDevices}<span class="compact-group-meta">{devicesBadgeText(group)}</span>{/if}
							<div class="compact-group-actions">
								<Button variant="secondary" size="sm" onclick={() => { setGroupView('list'); expandedGroups = { ...expandedGroups, [group.id]: true }; if (group.list && !listPages[group.id]) void loadListRulesPage(group.id, 0); }}>Правила и выход</Button>
								<Button variant="ghost" size="sm" onclick={() => openEditGroupModal(index)}>Настройки</Button>
							</div>
						</div>
					{:else}
					<!-- Main Card Row -->
					<div class="card" class:select-mode={groupSelectMode}>
						{#if groupSelectMode}
							<input
								type="checkbox"
								class="bulk-check"
								checked={selectedGroupIds.has(group.id)}
								aria-label={m.firc_bulk_groups_select_aria({ name: group.name })}
								onchange={() => toggleGroupSelected(group.id)}
							/>
						{/if}
						<div class="order" title="Приоритет группы #{orderStr}">
							{orderStr}
						</div>

						<div class="reorder-slot">
							<button
								type="button"
								class="reorder-btn"
								disabled={index === 0}
								title="Поднять выше"
								onclick={() => moveGroup(index, -1)}
							>
								<ArrowUp size={12} />
							</button>
							<button
								type="button"
								class="reorder-btn"
								disabled={index === groups.length - 1}
								title="Опустить ниже"
								onclick={() => moveGroup(index, 1)}
							>
								<ArrowDown size={12} />
							</button>
						</div>

						<div class="main">
							<div class="group-head-line">
								<div class="tile-with-status" title={groupStatusTooltip(group)}>
									<StatusDot variant={groupStatusDotVariant(group)} size="sm" />
									<ServiceTile
										serviceKey={groupServiceKey(group)}
										name={group.name}
										sub=""
									/>
								</div>

								<div class="group-badges-row">
									{#if group.list?.url}
										<span class="meta-chip chip-list">список</span>
									{/if}
									<span class="meta-chip chip-dns" title="Режим разрешения DNS для доменов группы">
										{resolverBadgeText(group)}
									</span>
									{#if hasCustomDevices}
										<span class="meta-chip chip-devices-active" title={devicesBadgeText(group)}>
											{devicesBadgeText(group)}
										</span>
									{/if}
								</div>
							</div>

							{#if group.list?.url}
								<div class="firc-list-header">
									<div class="list-hdr-url-row">
										<Link2 size={12} class="list-hdr-ico" />
										<input
											type="url"
											class="list-url-inline font-mono"
											placeholder="https://..."
											bind:value={group.list.url}
											title="URL удалённого списка правил (.srs / .txt)"
										/>
										<button
											type="button"
											class="list-clear-btn"
											title="Отключить удалённый список у группы"
											onclick={() => (group.list = null)}
										>
											<X size={12} />
										</button>
									</div>
									<div class="list-hdr-meta-row">
										<RefreshCw size={11} class="list-hdr-ico {syncingGroup[group.id] ? 'spin' : ''}" />
										<span class="list-meta-time">{formatTimestamp(group.list.lastUpdate)}</span>
										<span class="list-meta-sep">·</span>
										<span class="list-meta-lbl">Обновлять:</span>
										<select
											class="list-interval-inline"
											value={group.list.interval ?? 86400}
											onchange={(e) => {
												if (group.list) {
													group.list.interval = Number(
														(e.currentTarget as HTMLSelectElement).value,
													);
												}
											}}
										>
											{#each INTERVAL_OPTIONS as opt}
												<option value={opt.value}>{opt.label.toLowerCase()}</option>
											{/each}
										</select>
										<span class="list-meta-sep">·</span>
										<button
											type="button"
											class="list-counts-btn"
											onclick={() => toggleGroupExpand(group)}
											title="Нажмите, чтобы развернуть список правил"
										>
											<strong>{group.list.rulesTotal || group.list.rules || 0}</strong> из списка{#if group.rules.length > 0}{' '}+ <strong>{group.rules.length}</strong> своих{/if}
										</button>
										{#if group.list.sync?.state === 'error' && group.list.sync.error}
											<span class="list-sync-err">· {group.list.sync.error}</span>
										{/if}
									</div>
								</div>
							{:else}
								<div class="firc-own-summary-row">
									<button
										type="button"
										class="list-counts-btn"
										onclick={() => toggleGroupExpand(group)}
										title="Нажмите, чтобы редактировать правила группы"
									>
										{#if group.rules.length > 0}
											<strong>{group.rules.length}</strong> своих правил
										{:else}
											нет своих правил
										{/if}
									</button>
								</div>
							{/if}
						</div>

						<div class="trail">
							<div class="outbound-select-wrap">
								<ArrowRight size={14} class="outbound-arrow" />
								<div class="iface-picker-root">
									<button
										type="button"
										class="tone-select-pill iface-picker-btn {toneCls}"
										class:on-backup={Boolean(activeBackup)}
										class:all-dead={isFallbackGroup && activeRes.allDead}
										onclick={(e) => {
											e.stopPropagation();
											openPickerGroupId = isPickerOpen ? null : group.id;
											if (!isPickerOpen) void refreshProxyGroups();
										}}
										title={activeBackup
											? `Активен резервный интерфейс ${activeBackup} (основной ${ifaceList[0]} недоступен)`
											: isFallbackGroup && activeIface
												? m.firc_fallback_active_chain_title({
														iface: friendlyIfaceLabel(activeIface, interfaces, tunnelsState),
														chain: matchedPg
															? `${matchedPg.name}: ${ifaceList.map((id) => friendlyIfaceLabel(id, interfaces, tunnelsState)).join(' → ')}`
															: ifaceList.map((id) => friendlyIfaceLabel(id, interfaces, tunnelsState)).join(' → '),
													})
												: ifaceList.length > 1
													? `Цепочка Fallback: ${ifaceList.join(' → ')}`
													: resolverBadgeText(group)}
									>
										{#if isBlock || isBlackhole}
											<Ban size={13} />
										{:else}
											{@const dotState = activeRes.allDead && isFallbackGroup ? 'dead' : ifaceDotState(group, activeIface || ifaceVal)}
											{#if dotState}
												<span class="iface-dot {dotState}" class:backup={Boolean(activeBackup)}></span>
											{:else if toneCls === 'tone-awg'}
												<Shield size={13} />
											{:else}
												<Globe size={13} />
											{/if}
										{/if}
										<span class="iface-btn-label">
											{formatCompactOutboundLabel(group, activeIface, isFallbackGroup, activeRes.allDead)}
										</span>
										{#if !isBlock && !isBlackhole && primaryProbe && !(isFallbackGroup && activeRes.allDead)}
											<span class="pill-delay-tag {probeLatencyTone(primaryProbe)}">
												{formatProbeLatency(primaryProbe)}
											</span>
										{/if}
										{#if isFallbackGroup}
											{#if activeBackup}
												<span class="backup-badge">{m.firc_fallback_backup_prefix()}</span>
											{:else}
												<span class="fallback-count-chip">+{ifaceList.length - 1}</span>
											{/if}
										{/if}
										<ChevronDown size={12} class="iface-chev" />
									</button>

									{#if isPickerOpen}
										<!-- svelte-ignore a11y_click_events_have_key_events -->
										<!-- svelte-ignore a11y_no_static_element_interactions -->
										<div class="iface-popover" use:anchorPopover onclick={(e) => e.stopPropagation()}>
											<div class="iface-pop-sec">
												<div class="iface-pop-cap pg-cap-row">
													<span>Прокси-группы (Fallback)</span>
													<button
														type="button"
														class="pg-cap-btn"
														onclick={openProxyGroupsModal}
													>
														{extConfig.proxyGroups.length > 0 ? 'Настроить' : '+ Создать'}
													</button>
												</div>
												{#if extConfig.proxyGroups.length === 0}
													<div class="pg-empty-hint">
														Нет прокси-групп. Нажмите «+ Создать», чтобы собрать цепочку Fallback (например, туннели + Direct).
													</div>
												{:else}
													{#each extConfig.proxyGroups as pg (pg.id)}
														{@const isPgActive = matchedPg?.id === pg.id}
														{@const pgRt = resolveProxyGroupRuntime(pg, groups, liveMap, tunnelsState, interfaces, $fircInterfaceProbeResults, $fircProbeResults)}
														{@const pgActProbe = pgRt.activeIface ? getIfaceProbe(pgRt.activeIface) : null}
														<div class="iface-pop-row" class:is-primary={isPgActive}>
															<button
																type="button"
																class="iface-row-main"
																onclick={() => applyProxyGroupToGroup(group, pg)}
																title={pgRt.activeIface ? m.firc_fallback_active_primary_title({ iface: `${friendlyIfaceLabel(pgRt.activeIface, interfaces, tunnelsState)} (${pgRt.activeIface})` }) : m.firc_fallback_all_dead_title()}
															>
																<span class="iface-dot {pgRt.activeIface ? 'alive' : pgRt.allDead ? 'dead' : 'unknown'}"></span>
																<span class="iface-row-text">
																	<strong>{pg.name}</strong> — {pg.interfaces.map(shortIfaceTag).join(' → ')}
																</span>
															</button>
															{#if pgRt.activeIface}
																<span class="pop-pg-runtime">
																	{friendlyIfaceLabel(pgRt.activeIface, interfaces, tunnelsState)}{pgActProbe ? ` · ${formatProbeLatency(pgActProbe)}` : ''}
																</span>
															{/if}
														</div>
													{/each}
												{/if}
											</div>

											{#if pickerGroups.tunnels.length > 0}
												<div class="iface-pop-sec">
													<div class="iface-pop-cap">Туннели (VPN / Прокси)</div>
													{#each pickerGroups.tunnels as item (item.id)}
														{@const rank = ifaceList.indexOf(item.id)}
														{@const dState = ifaceDotState(group, item.id)}
														{@const isActMember = isFallbackGroup && activeIface === item.id}
														{@const rowProbe = getIfaceProbe(item.id)}
														{@const rowChecking = Boolean(probingIfaces[item.id])}
														<div
															class="iface-pop-row"
															class:is-primary={rank === 0}
															class:is-backup={rank > 0}
														>
															<button
																type="button"
																class="iface-rank-btn"
																class:rank-primary={rank === 0}
																class:rank-backup={rank > 0}
																disabled={rank < 0 && !canAddMoreIfaces}
																onclick={(e) => toggleBackupIface(group, item.id, e)}
																title={rank === 0
																	? 'Основной интерфейс (#1). Нажмите, чтобы убрать из цепочки'
																	: rank > 0
																		? `Резервный интерфейс #${rank + 1} (Fallback). Нажмите, чтобы убрать`
																		: canAddMoreIfaces
																			? `Добавить резервным #${ifaceList.length + 1} (Fallback)`
																			: `Максимум ${MAX_GROUP_IFACES} интерфейса в цепочке`}
															>
																{rank >= 0 ? rank + 1 : ''}
															</button>
															<button
																type="button"
																class="iface-row-main"
																onclick={() => setPrimaryIface(group, item.id)}
															>
																{#if dState}
																	<span
																		class="iface-dot {dState}"
																		class:active-pulse={isActMember}
																		title={ifaceDotTooltip(group, item.id)}
																	></span>
																{/if}
																<span class="iface-row-text">{item.label}</span>
																{#if isActMember}
																	<span class="pop-active-tag">{m.firc_fallback_active_tag()}</span>
																{/if}
															</button>
															<button
																type="button"
																class="pg-delay-btn compact {probeLatencyTone(rowProbe)}"
																disabled={rowChecking}
																onclick={(e) => {
																	e.stopPropagation();
																	void probeFallbackInterfaces([item.id]);
																}}
																title={m.firc_fallback_test_iface_title({ name: item.label })}
															>
																<span class="pg-delay-val">{formatProbeLatency(rowProbe)}</span>
																{#if rowChecking}
																	<RefreshCw size={9} class="spin" />
																{/if}
															</button>
														</div>
													{/each}
												</div>
											{/if}

											{#if pickerGroups.wans.length > 0}
												<div class="iface-pop-sec">
													<div class="iface-pop-cap">Напрямую (Direct / Провайдер WAN)</div>
													{#each pickerGroups.wans as item (item.id)}
														{@const rank = ifaceList.indexOf(item.id)}
														{@const dState = ifaceDotState(group, item.id)}
														{@const isActMember = isFallbackGroup && activeIface === item.id}
														{@const rowProbe = getIfaceProbe(item.id)}
														{@const rowChecking = Boolean(probingIfaces[item.id])}
														<div
															class="iface-pop-row"
															class:is-primary={rank === 0}
															class:is-backup={rank > 0}
														>
															<button
																type="button"
																class="iface-rank-btn"
																class:rank-primary={rank === 0}
																class:rank-backup={rank > 0}
																disabled={rank < 0 && !canAddMoreIfaces}
																onclick={(e) => toggleBackupIface(group, item.id, e)}
																title={rank === 0
																	? 'Основной Direct-интерфейс (#1)'
																	: rank > 0
																		? `Резервный Direct #${rank + 1} (Fallback в обход VPN)`
																		: canAddMoreIfaces
																			? `Добавить Direct резервным #${ifaceList.length + 1} при падении туннеля`
																			: `Максимум ${MAX_GROUP_IFACES} интерфейса`}
															>
																{rank >= 0 ? rank + 1 : ''}
															</button>
															<button
																type="button"
																class="iface-row-main"
																onclick={() => setPrimaryIface(group, item.id)}
															>
																{#if dState}
																	<span
																		class="iface-dot {dState}"
																		class:active-pulse={isActMember}
																		title={ifaceDotTooltip(group, item.id)}
																	></span>
																{/if}
																<span class="iface-row-text">Direct: {item.label}</span>
																{#if isActMember}
																	<span class="pop-active-tag">{m.firc_fallback_active_tag()}</span>
																{/if}
															</button>
															<button
																type="button"
																class="pg-delay-btn compact {probeLatencyTone(rowProbe)}"
																disabled={rowChecking}
																onclick={(e) => {
																	e.stopPropagation();
																	void probeFallbackInterfaces([item.id]);
																}}
																title={m.firc_fallback_test_iface_title({ name: item.label })}
															>
																<span class="pg-delay-val">{formatProbeLatency(rowProbe)}</span>
																{#if rowChecking}
																	<RefreshCw size={9} class="spin" />
																{/if}
															</button>
														</div>
													{/each}
												</div>
											{/if}

											<div class="iface-pop-sec">
												<div class="iface-pop-cap">Блокировка</div>
												<button
													type="button"
													class="iface-special-row"
													class:is-primary={ifaceList[0] === 'blackhole'}
													onclick={() => setPrimaryIface(group, 'blackhole')}
												>
													<Ban size={13} class="special-ico" />
													<div class="special-txt">
														<span class="special-title">Блокировать</span>
														<span class="special-sub">соединения отвергаются (Blackhole)</span>
													</div>
												</button>
												<button
													type="button"
													class="iface-special-row"
													class:is-primary={ifaceList[0] === 'block'}
													onclick={() => setPrimaryIface(group, 'block')}
												>
													<Ban size={13} class="special-ico" />
													<div class="special-txt">
														<span class="special-title">Блокировать по DNS</span>
														<span class="special-sub">0.0.0.0 / NXDOMAIN / REFUSED</span>
													</div>
												</button>
											</div>

											{#if pickerGroups.others.length > 0}
												<div class="iface-pop-sec">
													<button
														type="button"
														class="iface-more-toggle"
														onclick={() =>
															(showOthersMap = {
																...showOthersMap,
																[group.id]: !showOthersMap[group.id],
															})}
													>
														<span>
															{showOthersMap[group.id]
																? 'Скрыть остальные'
																: `Ещё ${pickerGroups.others.length} интерфейсов`}
														</span>
														<ChevronDown
															size={12}
															class={showOthersMap[group.id] ? 'rot-180' : ''}
														/>
													</button>
													{#if showOthersMap[group.id]}
														{#each pickerGroups.others as item (item.id)}
															{@const rank = ifaceList.indexOf(item.id)}
															{@const dState = ifaceDotState(group, item.id)}
															{@const isActMember = isFallbackGroup && activeIface === item.id}
															{@const rowProbe = getIfaceProbe(item.id)}
															{@const rowChecking = Boolean(probingIfaces[item.id])}
															<div
																class="iface-pop-row"
																class:is-primary={rank === 0}
																class:is-backup={rank > 0}
															>
																<button
																	type="button"
																	class="iface-rank-btn"
																	class:rank-primary={rank === 0}
																	class:rank-backup={rank > 0}
																	disabled={rank < 0 && !canAddMoreIfaces}
																	onclick={(e) => toggleBackupIface(group, item.id, e)}
																>
																	{rank >= 0 ? rank + 1 : ''}
																</button>
																<button
																	type="button"
																	class="iface-row-main"
																	onclick={() => setPrimaryIface(group, item.id)}
																>
																	{#if dState}
																		<span class="iface-dot {dState}" class:active-pulse={isActMember}></span>
																	{/if}
																	<span class="iface-row-text">{item.label}</span>
																	{#if isActMember}
																		<span class="pop-active-tag">{m.firc_fallback_active_tag()}</span>
																	{/if}
																</button>
																<button
																	type="button"
																	class="pg-delay-btn compact {probeLatencyTone(rowProbe)}"
																	disabled={rowChecking}
																	onclick={(e) => {
																		e.stopPropagation();
																		void probeFallbackInterfaces([item.id]);
																	}}
																	title={m.firc_fallback_test_iface_title({ name: item.label })}
																>
																	<span class="pg-delay-val">{formatProbeLatency(rowProbe)}</span>
																	{#if rowChecking}
																		<RefreshCw size={9} class="spin" />
																	{/if}
																</button>
															</div>
														{/each}
													{/if}
												</div>
											{/if}

											<div class="iface-pop-footer">
												{#if ifaceList.length > 1}
													{@const isPopTesting = Boolean(probingGroups[`grp:${group.id}`])}
													<div class="iface-pop-footer-row">
														<span>
															{#if activeIface}
																{m.firc_fallback_active_prefix()}: {friendlyIfaceLabel(activeIface, interfaces, tunnelsState)} ·{' '}
															{/if}Fallback: <strong>{ifaceList.join(' → ')}</strong>
														</span>
														<button
															type="button"
															class="pg-action-btn mini"
															disabled={isPopTesting}
															onclick={(e) => {
																e.stopPropagation();
																void probeFallbackInterfaces(ifaceList, { groupKey: `grp:${group.id}` });
															}}
														>
															<Zap size={10} class={isPopTesting ? 'spin' : ''} />
															<span>{m.firc_fallback_test()}</span>
														</button>
													</div>
												{:else}
													Нажмите на кружок слева, чтобы добавить резервные интерфейсы (Fallback #2–4)
												{/if}
											</div>
										</div>
									{/if}
								</div>

								{#if isBlock}
									<select
										class="inp block-mode-sel"
										value={group.block ?? 'null'}
										onchange={(e) => {
											group.block = (e.currentTarget as HTMLSelectElement)
												.value as FircBlockAnswer;
										}}
										title="Тип DNS-ответа при блокировке"
									>
										<option value="null">0.0.0.0 / ::</option>
										<option value="nxdomain">NXDOMAIN</option>
										<option value="refused">REFUSED</option>
									</select>
								{/if}
							</div>

							<Toggle
								size="sm"
								spinner="none"
								checked={group.enable}
								onchange={(v) => (group.enable = v)}
								ariaLabel={`Включить или выключить группу ${group.name}`}
							/>

							<div class="right-slot">
								{#if group.list?.url}
									<button
										type="button"
										class="route-action-btn"
										disabled={syncingGroup[group.id]}
										title="Обновить удалённый список правил"
										onclick={() => handleSyncList(group)}
									>
										<RefreshCw size={14} class={syncingGroup[group.id] ? 'spin' : ''} />
									</button>
								{/if}
								<button
									type="button"
									class="route-action-btn"
									title="Добавить правило в группу"
									onclick={() => addRuleToGroup(index)}
								>
									<Plus size={15} />
								</button>
								<button
									type="button"
									class="route-action-btn"
									title="Фильтр устройств и политик группы"
									onclick={() => openDevicesModal(index)}
								>
									<Monitor size={14} />
								</button>
								<button
									type="button"
									class="route-action-btn"
									title="Скопировать включённые домены этой группы"
									onclick={() => copySelectedGroupDomains([group.id])}
								>
									<Copy size={14} />
								</button>
								<button
									type="button"
									class="route-action-btn"
									title="Массовый импорт правил текстом"
									onclick={() => openImportRulesModal(index)}
								>
									<Download size={14} />
								</button>
								<button
									type="button"
									class="route-action-btn"
									title="Настройки группы (список по URL и DNS)"
									onclick={() => openEditGroupModal(index)}
								>
									<Settings size={14} />
								</button>
								<button
									type="button"
									class="route-action-btn danger"
									title="Удалить группу"
									onclick={() => deleteGroup(index)}
								>
									<Trash2 size={14} />
								</button>
								<button
									type="button"
									class="route-action-btn"
									title={isExpanded ? 'Свернуть правила' : 'Развернуть правила'}
									onclick={() => toggleGroupExpand(group)}
								>
									{#if isExpanded}
										<ChevronUp size={15} />
									{:else}
										<ChevronDown size={15} />
									{/if}
								</button>
							</div>
						</div>
					</div>

					<!-- Expanded Rules Section -->
					{#if isExpanded}
						<div class="group-body">
							<div class="rules-sec-head">
								<div class="rules-sec-title">
									<span>Имя группы:</span>
									<input
										type="text"
										class="inp group-name-inp-expanded"
										placeholder="Имя группы..."
										bind:value={group.name}
									/>
									<span>· Свои правила ({group.rules.length})</span>
									{#if group.rules.length > 1}
										<button
											type="button"
											class="link-btn"
											onclick={() => sortGroupRules(index)}
										>
											Сортировать А–Я
										</button>
									{/if}
									{#if group.rules.length > 0}
										<button
											type="button"
											class="link-btn"
											onclick={() =>
												selectedRuleSet.size === group.rules.length
													? clearSelectedRulesInGroup(group.id)
													: selectAllRulesInGroup(group)}
										>
											{selectedRuleSet.size === group.rules.length
												? m.firc_bulk_deselect_all()
												: m.firc_bulk_select_all({ count: group.rules.length })}
										</button>
									{/if}
								</div>
								<div class="rules-sec-actions">
									<Button variant="ghost" size="sm" onclick={() => openBulkRulesEditor(index)}>
										{#snippet iconBefore()}<FilePen size={13} />{/snippet}
										{m.firc_bulk_rules_edit_list_btn()}
									</Button>
									<Button variant="secondary" size="sm" onclick={() => addRuleToGroup(index)}>
										{#snippet iconBefore()}<Plus size={13} />{/snippet}
										Добавить правило
									</Button>
								</div>
							</div>

							{#if selectedRuleSet.size > 0}
								<FircBulkRulesBar
									selectedCount={selectedRuleSet.size}
									totalCount={group.rules.length}
									otherGroups={groups
										.filter((og) => og.id !== group.id)
										.map((og) => ({ id: og.id, name: og.name }))}
									onSelectAll={() => selectAllRulesInGroup(group)}
									onDeselectAll={() => clearSelectedRulesInGroup(group.id)}
									onEnable={() => runBulkRuleAction(index, { kind: 'enable', enable: true })}
									onDisable={() => runBulkRuleAction(index, { kind: 'enable', enable: false })}
									onChangeType={(type) => runBulkRuleAction(index, { kind: 'setType', type })}
									onMoveToGroup={(targetGroupId) =>
										runBulkRuleAction(index, { kind: 'move', targetGroupId })}
									onDelete={() => runBulkRuleAction(index, { kind: 'delete' })}
								/>
							{/if}

							{#if group.rules.length === 0}
								<div class="rules-empty">
									Своих правил в группе нет. Нажмите «Добавить правило» или «Импорт правил».
								</div>
							{:else}
								<div class="rules-table">
									{#each pagedRules as rule, rIdx (rule.id)}
										{@const absIdx = (currentPage - 1) * PAGE_SIZE + rIdx}
										<div
											class="rule-row"
											class:rule-disabled={!rule.enable}
											class:rule-selected={selectedRuleSet.has(rule.id)}
										>
											<input
												type="checkbox"
												class="bulk-check"
												checked={selectedRuleSet.has(rule.id)}
												aria-label={m.firc_bulk_rules_select_aria({
													rule: rule.rule || String(absIdx + 1),
												})}
												onchange={() => toggleRuleSelected(group.id, rule.id)}
											/>
											<span class="rule-idx font-mono">#{absIdx + 1}</span>
											<select class="inp rule-type-sel" bind:value={rule.type}>
												{#each FIRC_RULE_TYPES as rt}
													<option value={rt.value}>{rt.label}</option>
												{/each}
											</select>
											<input
												type="text"
												class="inp rule-pattern-inp font-mono"
												placeholder="example.com или 1.2.3.0/24"
												bind:value={rule.rule}
											/>
											{#if rule.type === 'subnet' || rule.type === 'subnet6'}
												<select
													class="inp rule-proto-sel"
													value={rule.proto ?? ''}
													onchange={(e) => {
														const v = (e.currentTarget as HTMLSelectElement).value;
														rule.proto = v ? (v as 'tcp' | 'udp') : undefined;
														if (!v) rule.ports = undefined;
													}}
												>
													<option value="">Любой прот.</option>
													<option value="tcp">TCP</option>
													<option value="udp">UDP</option>
												</select>
												{#if rule.proto}
													<input
														type="text"
														class="inp rule-ports-inp font-mono"
														placeholder="Порты: 80,443"
														bind:value={rule.ports}
													/>
												{/if}
											{/if}
											<input
												type="text"
												class="inp rule-comment-inp"
												placeholder="Комментарий..."
												bind:value={rule.comment}
											/>
											{#if rule.autolist}
												<Badge
													variant="info"
													size="xs"
													title={rule.autolistByDomain
														? 'Добавлено по домену из диагностики обхода'
														: 'Добавлено из диагностики обхода'}
												>
													автообход
												</Badge>
											{/if}
											<Toggle
												checked={rule.enable}
												onchange={(v) => (rule.enable = v)}
												ariaLabel="Включить или выключить правило"
											/>
											<button
												type="button"
												class="route-action-btn danger"
												title="Удалить правило"
												onclick={() => deleteRuleFromGroup(index, absIdx)}
											>
												<Trash2 size={14} />
											</button>
										</div>
									{/each}
								</div>

								{#if totalRulePages > 1}
									<div class="pagination-bar">
										<Button
											variant="ghost"
											size="sm"
											disabled={currentPage <= 1}
											onclick={() =>
												(rulePageMap = { ...rulePageMap, [group.id]: currentPage - 1 })}
										>
											Назад
										</Button>
										<span class="page-info">
											Страница {currentPage} из {totalRulePages}
										</span>
										<Button
											variant="ghost"
											size="sm"
											disabled={currentPage >= totalRulePages}
											onclick={() =>
												(rulePageMap = { ...rulePageMap, [group.id]: currentPage + 1 })}
										>
											Вперёд
										</Button>
									</div>
								{/if}
							{/if}

							{#if group.list}
								<div class="rules-sec-head mt-3">
									<div class="rules-sec-title">
										<span>
											Из удалённого списка ({listPage?.matched ?? group.list.rulesTotal})
										</span>
									</div>
									<Button
										variant="ghost"
										size="sm"
										disabled={syncingGroup[group.id]}
										onclick={() => handleSyncList(group)}
									>
										{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
										Обновить список
									</Button>
								</div>

								{#if listPage?.loading}
									<div class="rules-empty">Загрузка правил из списка...</div>
								{:else if !listPage || listPage.rules.length === 0}
									<div class="rules-empty">
										Список пуст или ещё не синхронизирован. Нажмите «Обновить список».
									</div>
								{:else}
									{@const listPageNum = Math.floor(listPage.offset / PAGE_SIZE) + 1}
									{@const listTotalPages = Math.max(
										1,
										Math.ceil(listPage.matched / PAGE_SIZE),
									)}
									<div class="rules-table">
										{#each listPage.rules as lrule, lIdx (lrule.id)}
											<div class="rule-row list-rule-row" class:rule-disabled={!lrule.enable}>
												<span class="rule-idx font-mono">
													#{listPage.offset + lIdx + 1}
												</span>
												<Badge variant="muted" size="sm" mono>{lrule.type}</Badge>
												<span class="list-rule-pattern font-mono">{lrule.rule}</span>
												<Toggle
													checked={lrule.enable}
													onchange={(v) => toggleListRuleEnable(group.id, lrule, v)}
													ariaLabel="Включить или выключить правило из списка"
												/>
											</div>
										{/each}
									</div>

									{#if listTotalPages > 1}
										<div class="pagination-bar">
											<Button
												variant="ghost"
												size="sm"
												disabled={listPageNum <= 1}
												onclick={() =>
													loadListRulesPage(group.id, (listPageNum - 2) * PAGE_SIZE)}
											>
												Назад
											</Button>
											<span class="page-info">
												Страница {listPageNum} из {listTotalPages}
											</span>
											<Button
												variant="ghost"
												size="sm"
												disabled={listPageNum >= listTotalPages}
												onclick={() =>
													loadListRulesPage(group.id, listPageNum * PAGE_SIZE)}
											>
												Вперёд
											</Button>
										</div>
									{/if}
								{/if}
							{/if}
						</div>
					{/if}
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Modal 1: Group Create / Settings -->
<Modal
	open={groupModalOpen}
	onclose={() => (groupModalOpen = false)}
	title={groupModalMode === 'create' ? 'Новая группа маршрутизации' : 'Настройки группы'}
	size="lg"
>
	<div class="modal-form">
		<div class="form-grid-2">
			<div class="field">
				<label class="lbl" for="firc-gm-name">Название группы</label>
				<input
					id="firc-gm-name"
					type="text"
					class="inp"
					placeholder="Например, youtube или awg-main"
					bind:value={gmName}
				/>
			</div>
			<div class="field">
				<label class="lbl" for="firc-gm-iface">Целевой интерфейс</label>
				<select id="firc-gm-iface" class="inp" bind:value={gmInterface}>
					{#each availableInterfaces as opt}
						<option value={opt.value}>{opt.label}</option>
					{/each}
				</select>
			</div>
		</div>

		<div class="sec-box">
			<div class="field">
				<label class="lbl" for="firc-gm-dns">Разрешение DNS для доменов группы</label>
				<select
					id="firc-gm-dns"
					class="inp"
					bind:value={gmDnsMode}
					disabled={gmInterface === 'block'}
				>
					<option value="auto">Через туннель — автоматически</option>
					<option value="own">Через туннель — свои DNS-серверы</option>
					<option value="off">Не через туннель (общий upstream)</option>
				</select>
			</div>
			{#if gmDnsMode === 'own' && gmInterface !== 'block'}
				<div class="field mt-2">
					<label class="lbl" for="firc-gm-dns-srv">DNS-серверы (через запятую)</label>
					<input
						id="firc-gm-dns-srv"
						type="text"
						class="inp font-mono"
						placeholder="1.1.1.1, 8.8.8.8"
						bind:value={gmDnsServer}
					/>
					<p class="hint">
						Запросы к этим DNS-серверам принудительно отправляются через выбранный туннель группы.
					</p>
				</div>
			{/if}
		</div>

		{#if groupModalMode === 'edit' && groupModalIndex !== null && normalizeIfaceList(groups[groupModalIndex]?.interface ?? '').length >= 2 && !isSpecialIface(gmInterface)}
			<div class="sec-box">
				<div class="field">
					<label class="lbl" for="firc-gm-check-mode">{m.firc_group_check_label()}</label>
					<select id="firc-gm-check-mode" class="inp" bind:value={gmCheckMode}>
						<option value="url">{m.firc_group_check_mode_url()}</option>
						<option value="keendns">KeenDNS Cloud relay TCP</option>
						<option value="none">{m.firc_group_check_mode_none()}</option>
					</select>
				</div>
				{#if gmCheckMode === 'url'}
					<div class="field mt-2">
						<label class="lbl" for="firc-gm-check-url">{m.firc_group_check_url_label()}</label>
						<input
							id="firc-gm-check-url"
							type="text"
							class="inp font-mono"
							placeholder={m.firc_group_check_url_placeholder()}
							bind:value={gmCheckUrl}
						/>
						<p class="hint">
							{m.firc_group_check_url_hint()}
						</p>
					</div>
				{/if}
			</div>
		{/if}

		<div class="sec-box">
			<div class="field-row">
				<div class="field-row-info">
					<div class="field-title">Удалённый список правил по URL (.srs / .txt)</div>
					<div class="hint">Автоматическая загрузка гео-списков или доменных списков (.srs)</div>
				</div>
				<div class="list-toggle-actions">
					<Button variant="secondary" size="sm" onclick={openSingleServiceCatalog}>
						{#snippet iconBefore()}<LayoutGrid size={13} />{/snippet}
						Каталог сервисов
					</Button>
					<Toggle
						checked={gmListEnabled}
						onchange={(v) => { gmListEnabled = v; if (v) void ensureCatalogLoaded(); }}
						ariaLabel="Включить список по URL"
					/>
				</div>
			</div>

			{#if gmListEnabled}
				<div class="field mt-2">
					<label class="lbl" for="firc-gm-url">URL списка или поиск по каталогу (например, youtube, telegram)</label>
					<input
						id="firc-gm-url"
						type="text"
						class="inp font-mono"
						placeholder="https://... или введите название сервиса (youtube, discord...)"
						bind:value={gmListUrl}
					/>
					{#if catalogMatches.length > 0}
						<div class="catalog-dropdown">
							{#each catalogMatches as hit (hit.url)}
								<button
									type="button"
									class="catalog-item"
									onclick={() => (gmListUrl = hit.url)}
								>
									<span class="font-mono">{hit.name}</span>
									<Badge variant="muted" size="sm">{hit.source}</Badge>
								</button>
							{/each}
							{#if catalogSearch.total > catalogMatches.length}
								<div class="catalog-more-hint">
									{m.firc_catalog_more_hint({ shown: catalogMatches.length, total: catalogSearch.total })}
								</div>
							{/if}
						</div>
					{/if}
				</div>

				<div class="form-grid-2 mt-2">
					<div class="field">
						<label class="lbl" for="firc-gm-interval">Интервал автообновления</label>
						<select id="firc-gm-interval" class="inp" bind:value={gmListInterval}>
							{#each INTERVAL_OPTIONS as opt}
								<option value={opt.value}>{opt.label}</option>
							{/each}
						</select>
					</div>
					<div class="field" style="justify-content: flex-end;">
						<Button
							variant="secondary"
							size="sm"
							loading={gmPreviewLoading}
							onclick={handlePreviewList}
						>
							Предпросмотр списка
						</Button>
					</div>
				</div>

				{#if gmPreviewError}
					<div class="preview-err">{gmPreviewError}</div>
				{:else if gmPreviewData}
					<div class="preview-box">
						<div>
							Распознано правил: <strong>{gmPreviewData.total ?? gmPreviewData.rules.length}</strong>
						</div>
						{#if gmPreviewData.rules.length > 0}
							<div class="preview-sample font-mono">
								{#each gmPreviewData.rules.slice(0, 8) as pr}
									<div>[{pr.type}] {pr.rule}</div>
								{/each}
							</div>
						{/if}
					</div>
				{/if}
			{/if}
		</div>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={() => (groupModalOpen = false)}>Отмена</Button>
			<Button variant="primary" size="sm" onclick={submitGroupModal}>
				{groupModalMode === 'create' ? 'Создать группу' : 'Применить'}
			</Button>
		</div>
	{/snippet}
</Modal>

<!-- Modal 2: Devices & Policies Filter -->
<FircGroupDevicesModal
	open={devicesModalOpen}
	groupName={devicesModalBulk
		? m.firc_bulk_groups_devices_title({ count: selectedGroupIds.size })
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

<!-- Modal 3: Bulk Import Rules -->
<Modal
	open={importRulesModalOpen}
	onclose={() => (importRulesModalOpen = false)}
	title="Импорт списка правил в группу"
	size="lg"
>
	<div class="modal-form">
		<p class="hint">
			Вставьте домены, подсети IPv4/IPv6 CIDR или маски по одному на строку. Тип правила определяется автоматически.
		</p>
		<textarea
			class="inp font-mono"
			rows="10"
			placeholder={'example.com\n*.openai.com\n149.154.160.0/20'}
			bind:value={importRulesText}
		></textarea>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={() => (importRulesModalOpen = false)}>
				Отмена
			</Button>
			<Button variant="primary" size="sm" onclick={submitImportRules}>Импортировать</Button>
		</div>
	{/snippet}
</Modal>

<!-- Modal 4: Fallback Proxy Groups -->
<Modal
	open={proxyGroupsModalOpen}
	onclose={() => (proxyGroupsModalOpen = false)}
	title="Прокси-группы (Fallback)"
	size="lg"
>
	<div class="modal-form">
		<p class="hint">
			Соберите цепочку из 1–4 интерфейсов (туннели VLESS / AmneziaWG и резервный <strong>Direct WAN</strong>).
			Если основной туннель (#1) недоступен, трафик автоматически переключается на следующий интерфейс в списке.
		</p>

		{#each pgDraft as pg, pgIdx (pg.id)}
			{@const draftRt = resolveProxyGroupRuntime(
				pg,
				groups,
				liveMap,
				tunnelsState,
				interfaces,
				$fircInterfaceProbeResults,
				$fircProbeResults,
			)}
			{@const isModalTesting = Boolean(probingGroups[`modal:${pg.id}`])}
			<div class="sec-box pg-editor-card">
				<div class="pg-card-head">
					<input
						type="text"
						class="inp"
						placeholder="Название прокси-группы (например, Основной + Direct)"
						bind:value={pg.name}
					/>
					<button
						type="button"
						class="pg-action-btn"
						disabled={pg.interfaces.length === 0 || isModalTesting}
						title={m.firc_fallback_test_group_title({ name: pg.name })}
						onclick={() =>
							probeFallbackInterfaces(pg.interfaces, { groupKey: `modal:${pg.id}` })}
					>
						<Zap size={12} class={isModalTesting ? 'spin' : ''} />
						<span>{isModalTesting ? '...' : m.firc_fallback_test()}</span>
					</button>
					<button
						type="button"
						class="route-action-btn danger"
						title="Удалить прокси-группу"
						onclick={() => removeDraftProxyGroup(pgIdx)}
					>
						<Trash2 size={14} />
					</button>
				</div>

				<div class="pg-selected-chain">
					<div class="sec-cap">Цепочка переключения (порядок приоритета 1 → 4):</div>
					{#if pg.interfaces.length === 0}
						<div class="hint">Выберите интерфейсы ниже (до {MAX_GROUP_IFACES} шт.)</div>
					{:else}
						<div class="pg-chain-items">
							{#each pg.interfaces as ifaceId, iIdx (ifaceId)}
								{@const mem = draftRt.members.find((m) => m.id === ifaceId)}
								{@const mProbe = mem?.probe ?? getIfaceProbe(ifaceId)}
								{@const mTone = probeLatencyTone(mProbe)}
								{@const isAct = draftRt.activeIface === ifaceId}
								{@const checking = Boolean(probingIfaces[ifaceId])}
								<div class="pg-chain-pill" class:is-active={isAct}>
									<span class="pg-rank-num">#{iIdx + 1}</span>
									<span
										class="pg-mem-dot"
										class:active={isAct}
										class:alive={!isAct && mem?.state === 'alive'}
										class:dead={mem?.state === 'dead'}
										class:unknown={!isAct && (!mem || mem.state === 'unknown')}
									></span>
									<span>{friendlyIfaceLabel(ifaceId, interfaces, tunnelsState)}</span>
									<span class="pg-pill-id font-mono">({ifaceId})</span>
									{#if isAct}
										<span class="pg-mem-act-tag">{m.firc_fallback_active_tag()}</span>
									{/if}
									<button
										type="button"
										class="pg-delay-btn {mTone}"
										disabled={checking}
										title={m.firc_fallback_test_iface_title({ name: ifaceId })}
										onclick={() => probeFallbackInterfaces([ifaceId])}
									>
										<span class="pg-delay-val">{formatProbeLatency(mProbe)}</span>
										{#if checking}
											<RefreshCw size={9} class="spin" />
										{/if}
									</button>
									<button
										type="button"
										disabled={iIdx === 0}
										title="Выше приоритет"
										onclick={() => moveIfaceInDraftProxyGroup(pgIdx, iIdx, -1)}
									>
										↑
									</button>
									<button
										type="button"
										disabled={iIdx === pg.interfaces.length - 1}
										title="Ниже приоритет"
										onclick={() => moveIfaceInDraftProxyGroup(pgIdx, iIdx, 1)}
									>
										↓
									</button>
									<button
										type="button"
										title="Убрать из цепочки"
										onclick={() => toggleIfaceInDraftProxyGroup(pgIdx, ifaceId)}
									>
										✕
									</button>
								</div>
							{/each}
						</div>
					{/if}
				</div>

				<div class="pg-candidates">
					<div class="sec-cap">Добавить в группу (нажмите для включения / исключения):</div>
					<div class="pg-cand-grid">
						{#each interfaces.filter(isSelectableFircFallbackInterface) as opt (opt.id)}
							{@const idxInPg = pg.interfaces.indexOf(opt.id)}
							{@const isWan = opt.role === 'wan'}
							{@const cHealth = resolveIfaceState(undefined, opt.id)}
							{@const cProbe = getIfaceProbe(opt.id)}
							{@const cTone = probeLatencyTone(cProbe)}
							<button
								type="button"
								class="pg-cand-btn"
								class:selected={idxInPg >= 0}
								class:wan-cand={isWan}
								onclick={() => toggleIfaceInDraftProxyGroup(pgIdx, opt.id)}
							>
								<span class="iface-dot {cHealth.state}"></span>
								{#if idxInPg >= 0}
									<span class="pg-cand-rank">#{idxInPg + 1}</span>
								{/if}
								<span>{isWan ? `Direct (${formatIfaceOption(opt)})` : formatIfaceOption(opt)}</span>
								{#if cProbe}
									<span class="pill-delay-tag {cTone}">{formatProbeLatency(cProbe)}</span>
								{/if}
							</button>
						{/each}
					</div>
				</div>
			</div>
		{/each}

		<div>
			<Button variant="secondary" size="sm" onclick={addDraftProxyGroup}>
				{#snippet iconBefore()}<Plus size={14} />{/snippet}
				Добавить ещё прокси-группу
			</Button>
		</div>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={() => (proxyGroupsModalOpen = false)}>
				Отмена
			</Button>
			<Button variant="primary" size="sm" loading={pgSaving} onclick={saveProxyGroupsModal}>
				Сохранить
			</Button>
		</div>
	{/snippet}
</Modal>

<!-- Modal 5: Service Catalog (.srs) -->
<ServiceCatalogModal
	bind:open={serviceCatalogOpen}
	title="Каталог сервисов (.srs)"
	presetFilter={singboxRouterCatalogPresetFilter}
	markExisting={serviceCatalogMode === 'batch'}
	existingNames={existingCatalogNames}
	existingRuleSetTags={existingCatalogRuleSetTags}
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
	title="Сбросить несохранённые правки?"
	size="sm"
	onclose={() => (confirmDiscardOpen = false)}
>
	<p class="hint" style="font-size: 13px; color: var(--text-secondary);">
		Все накопленные изменения групп маршрутизации FIRC будут отброшены, и список вернётся к последнему сохранённому состоянию.
	</p>
	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={() => (confirmDiscardOpen = false)}>
				Отмена
			</Button>
			<Button variant="danger" size="sm" onclick={resetAllChanges}>
				Сбросить изменения
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
	message={m.firc_bulk_groups_delete_msg({ count: selectedGroupIds.size })}
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

	.groups-toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}

	.toolbar-left {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 12px;
		flex: 1 1 auto;
		min-width: 0;
	}

	.groups-count {
		font-size: 13px;
		color: var(--text-secondary);
		display: inline-flex;
		align-items: center;
		gap: 8px;
		white-space: nowrap;
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
		flex: 1 1 220px;
		max-width: 340px;
	}

	.search-box :global(.search-icon) {
		position: absolute;
		left: 10px;
		color: var(--text-muted);
		pointer-events: none;
	}

	.search-inp {
		width: 100%;
		padding-left: 30px !important;
	}

	.toolbar-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px;
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

	.group-view-switch { display: inline-flex; gap: .2rem; }
	.route-action-btn[hidden] { display: none; }
	.group-view-switch button { padding: .35rem .6rem; border: 1px solid var(--border); background: var(--bg-secondary); color: var(--text-secondary); border-radius: 6px; cursor: pointer; font: inherit; font-size: .75rem; }
	.group-view-switch button.active { border-color: var(--accent); color: var(--text-primary); }
	.group-view-switch button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
	.cards-stack.compact-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 260px), 1fr)); gap: .65rem; align-items: stretch; }
	.compact-group-card { display: flex; flex-direction: column; gap: .45rem; padding: .75rem .875rem; min-height: 138px; height: 100%; box-sizing: border-box; }
	.compact-group-heading { display: flex; flex-wrap: wrap; align-items: center; gap: .45rem; }
	.compact-group-heading strong { overflow-wrap: anywhere; flex: 1; min-width: 100px; }
	.compact-priority, .compact-group-meta { font-size: .75rem; color: var(--text-muted); }
	.compact-out-block { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
	.compact-group-output { display: flex; align-items: center; gap: 5px; min-width: 0; font-size: .82rem; }
	.compact-group-output :global(.compact-out-arrow) { color: var(--text-muted); flex-shrink: 0; }
	.compact-out-name { font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; flex: 0 1 auto; }
	.compact-delay-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		font-family: var(--font-mono);
		font-size: 11px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		padding: 0 4px;
		border-radius: 4px;
		border: 1px solid transparent;
		background: transparent;
		cursor: pointer;
		flex-shrink: 0;
		transition: border-color var(--t-fast), background var(--t-fast);
	}
	.compact-delay-btn:hover:not(:disabled) { background: var(--bg-tertiary); border-color: var(--border); }
	.compact-delay-btn.good { color: var(--latency-color-ok); }
	.compact-delay-btn.medium { color: var(--latency-color-slow); }
	.compact-delay-btn.bad { color: var(--latency-color-fail); }
	.compact-delay-btn.none { color: var(--text-muted); }
	:global(.compact-chain-probe) { margin-left: auto; flex-shrink: 0; }
	.compact-chain-sub {
		font-size: 11px;
		color: var(--text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		padding-left: 18px;
		min-width: 0;
	}
	.compact-group-actions { display: flex; flex-wrap: wrap; gap: .4rem; margin-top: auto; }
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

	.bulk-check {
		width: 15px;
		height: 15px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.card {
		display: grid;
		grid-template-columns: 24px 18px minmax(200px, 1fr) auto;
		gap: 6px 8px;
		align-items: center;
		padding: 7px 10px;
		min-width: 0;
	}

	:global(html[data-layout-compact='true']) .iface-btn-label {
		max-width: 120px;
	}

	:global(html[data-layout-compact='true']) .trail {
		gap: 4px;
	}

	:global(html[data-layout-compact='true']) .right-slot {
		gap: 2px;
	}

	.card.select-mode {
		grid-template-columns: 22px 24px 18px minmax(200px, 1fr) auto;
	}

	.order {
		font-family: var(--font-mono);
		font-size: 12px;
		font-weight: 600;
		color: var(--text-secondary);
		text-align: center;
	}

	.reorder-slot {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1px;
	}

	.reorder-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		padding: 1px;
		cursor: pointer;
		border-radius: 3px;
		display: inline-flex;
	}

	.reorder-btn:hover:not(:disabled) {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}

	.reorder-btn:disabled {
		opacity: 0.25;
		cursor: default;
	}

	.main {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 4px;
		min-width: 0;
		overflow: hidden;
	}

	.group-head-line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 10px;
		width: 100%;
		min-width: 0;
	}

	.tile-with-status {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 1;
		min-width: 0;
		max-width: 100%;
	}

	.group-badges-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 5px;
		min-width: 0;
	}

	.meta-chip {
		display: inline-flex;
		align-items: center;
		padding: 1.5px 7px;
		border-radius: var(--radius-sm);
		font-size: 11px;
		font-weight: 500;
		line-height: 1.35;
		white-space: nowrap;
		border: 1px solid transparent;
	}

	.meta-chip.chip-list {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.meta-chip.chip-dns {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
		border-color: var(--border);
		font-weight: 500;
	}

	.meta-chip.chip-devices-active {
		color: var(--color-warning);
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.firc-list-header {
		display: flex;
		flex-direction: column;
		gap: 2px;
		width: 100%;
		min-width: 0;
		padding-left: 2px;
	}

	.list-hdr-url-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		max-width: 100%;
	}

	.firc-list-header :global(.list-hdr-ico) {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.list-url-inline {
		background: transparent;
		border: 0;
		border-bottom: 1px dashed transparent;
		color: var(--text-secondary);
		font-size: 11.5px;
		min-width: 120px;
		width: 100%;
		max-width: 440px;
		padding: 0 2px;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.list-url-inline:hover {
		color: var(--text-primary);
		border-bottom-color: var(--border-hover);
	}

	.list-url-inline:focus {
		outline: none;
		border-bottom-color: var(--accent);
		color: var(--text-primary);
	}

	.list-clear-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
		padding: 2px;
		border-radius: 4px;
		display: inline-flex;
		align-items: center;
		opacity: 0.6;
	}

	.list-clear-btn:hover {
		opacity: 1;
		color: var(--color-danger);
		background: var(--bg-tertiary);
	}

	.list-hdr-meta-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 5px;
		font-size: 11px;
		color: var(--text-muted);
	}

	.list-interval-inline {
		appearance: none;
		background: transparent;
		border: 0;
		border-bottom: 1px dashed var(--border-hover);
		color: var(--text-secondary);
		font-size: 11px;
		font-family: inherit;
		padding: 0 2px;
		cursor: pointer;
	}

	.list-interval-inline:focus {
		outline: none;
		border-bottom-color: var(--accent);
	}

	.list-interval-inline option {
		background: var(--bg-secondary);
		color: var(--text-primary);
	}

	.list-counts-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-secondary);
		font-size: 11px;
		font-family: inherit;
		padding: 0;
		cursor: pointer;
	}

	.list-counts-btn:hover {
		color: var(--accent);
		text-decoration: underline;
	}

	.firc-own-summary-row {
		display: flex;
		align-items: center;
		padding-left: 2px;
	}

	.group-name-inp-expanded {
		width: 180px;
		font-weight: 600;
	}

	.list-sync-err {
		color: var(--color-danger);
		font-weight: 500;
	}

	.trail {
		display: flex;
		align-items: center;
		gap: 6px;
		justify-content: flex-end;
		flex-shrink: 0;
	}

	.outbound-select-wrap {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.outbound-select-wrap :global(.outbound-arrow) {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.iface-picker-root {
		position: relative;
		display: inline-flex;
	}

	.tone-select-pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 9px;
		border-radius: 6px;
		border: 1px solid var(--border);
		font-size: 11.5px;
		font-weight: 600;
		height: 27px;
		box-sizing: border-box;
		cursor: pointer;
		font-family: inherit;
		transition:
			border-color var(--t-fast),
			background var(--t-fast);
	}

	.tone-select-pill:hover {
		filter: brightness(1.08);
	}

	.tone-select-pill.on-backup {
		border-color: var(--color-warning-border) !important;
	}

	.tone-select-pill.all-dead {
		border-color: var(--color-error-border) !important;
	}

	.iface-btn-label {
		max-width: 145px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.backup-badge,
	.dead-badge,
	.fallback-count-chip {
		font-size: 10px;
		padding: 0 5px;
		line-height: 16px;
		border-radius: 4px;
		white-space: nowrap;
		font-variant-numeric: tabular-nums;
		flex-shrink: 0;
	}

	.backup-badge {
		background: var(--color-warning-tint);
		color: var(--color-warning);
		border: 1px solid var(--color-warning-border);
	}

	.dead-badge {
		background: var(--color-error-tint);
		color: var(--color-error);
		border: 1px solid var(--color-error-border);
	}

	.fallback-count-chip {
		font-family: var(--font-mono);
		font-weight: 600;
		color: var(--text-muted);
		border: 1px dotted color-mix(in srgb, var(--text-muted) 45%, transparent);
	}

	.pill-delay-tag {
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.pill-delay-tag.good {
		color: var(--latency-color-ok);
	}

	.pill-delay-tag.medium {
		color: var(--latency-color-slow);
	}

	.pill-delay-tag.bad {
		color: var(--latency-color-fail);
	}

	.pill-delay-tag.none {
		color: var(--text-muted);
	}

	.iface-picker-btn :global(.iface-chev) {
		opacity: 0.7;
		flex-shrink: 0;
	}

	.iface-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		display: inline-block;
	}

	.iface-dot.alive {
		background: var(--color-success);
		box-shadow: 0 0 4px var(--color-success-border);
	}

	.iface-dot.backup {
		background: var(--color-warning);
		box-shadow: 0 0 4px var(--color-warning-border);
	}

	.iface-dot.active-pulse {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success-border);
		animation: firc-active-pulse 1.5s ease-in-out infinite;
	}

	.iface-dot.dead {
		background: var(--color-error);
		box-shadow: 0 0 4px var(--color-error-border);
	}

	.iface-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.pop-active-tag {
		font-size: 9.5px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		padding: 1px 5px;
		border-radius: 4px;
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
		flex-shrink: 0;
	}

	.pop-pg-runtime {
		font-family: var(--font-mono);
		font-size: 10px;
		color: var(--color-success);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.iface-pop-footer-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	@keyframes firc-active-pulse {
		0%,
		100% {
			opacity: 1;
			transform: scale(1);
		}
		50% {
			opacity: 0.45;
			transform: scale(0.85);
		}
	}

	.iface-popover {
		position: absolute;
		top: calc(100% + 5px);
		right: 0;
		z-index: 80;
		width: 330px;
		max-width: calc(100vw - 24px);
		box-sizing: border-box;
		max-height: 390px;
		overflow-y: auto;
		background: var(--bg-secondary);
		border: 1px solid var(--border-hover, var(--border));
		border-radius: var(--radius);
		box-shadow: var(--shadow);
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.iface-pop-sec {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.iface-pop-sec + .iface-pop-sec {
		border-top: 1px solid var(--border);
		padding-top: 5px;
	}

	.iface-pop-cap {
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		padding: 2px 6px;
	}

	.iface-pop-row {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 3px 5px;
		border-radius: 5px;
		transition: background 0.12s ease;
	}

	.iface-pop-row:hover {
		background: var(--bg-tertiary);
	}

	.iface-pop-row.is-primary {
		background: var(--color-accent-tint);
	}

	.iface-pop-row.is-backup {
		background: var(--color-warning-tint);
	}

	.iface-rank-btn {
		appearance: none;
		width: 19px;
		height: 19px;
		border-radius: 50%;
		border: 1.5px solid var(--border-hover, var(--border));
		background: transparent;
		color: var(--text-secondary);
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 700;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		flex-shrink: 0;
		padding: 0;
		transition:
			background 0.12s ease,
			border-color 0.12s ease,
			color 0.12s ease;
	}

	.iface-rank-btn:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--text-primary);
	}

	.iface-rank-btn.rank-primary {
		background: var(--accent);
		border-color: var(--accent);
		color: #fff;
	}

	.iface-rank-btn.rank-backup {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}

	.iface-rank-btn:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}

	.iface-row-main {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		font-family: inherit;
		display: flex;
		align-items: center;
		gap: 7px;
		flex: 1;
		min-width: 0;
		text-align: left;
		cursor: pointer;
		padding: 2px 2px;
	}

	.iface-row-text {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.iface-special-row {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-family: inherit;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 5px 8px;
		border-radius: 5px;
		cursor: pointer;
		text-align: left;
		width: 100%;
	}

	.iface-special-row:hover {
		background: var(--bg-tertiary);
	}

	.iface-special-row.is-primary {
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.iface-special-row :global(.special-ico) {
		color: var(--color-error);
		flex-shrink: 0;
	}

	.special-txt {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.special-title {
		font-size: 12px;
		font-weight: 600;
	}

	.special-sub {
		font-size: 10.5px;
		color: var(--text-muted);
	}

	.iface-more-toggle {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--text-muted);
		font-size: 11px;
		font-family: inherit;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 4px 6px;
		cursor: pointer;
		border-radius: 4px;
	}

	.iface-more-toggle:hover {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}

	.iface-more-toggle :global(.rot-180) {
		transform: rotate(180deg);
	}

	.iface-pop-footer {
		border-top: 1px solid var(--border);
		padding: 5px 6px 2px;
		font-size: 10.5px;
		color: var(--text-muted);
		line-height: 1.35;
	}

	.tone-select-pill.tone-subscription {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.tone-select-pill.tone-awg {
		color: var(--color-accent);
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
	}

	.tone-select-pill.tone-proxy {
		color: var(--color-info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.tone-select-pill.tone-composite {
		color: var(--color-accent);
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
	}

	.tone-select-pill.tone-block {
		color: var(--color-error);
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.block-mode-sel {
		width: auto;
		max-width: 105px;
		padding: 2px 6px;
		height: 26px;
		font-size: 11px;
	}

	.right-slot {
		display: flex;
		align-items: center;
		gap: 2px;
	}

	.group-body {
		border-top: 1px solid var(--border);
		background: color-mix(in srgb, var(--bg-primary) 55%, var(--bg-secondary));
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.rules-sec-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 8px;
		min-width: 0;
	}

	.rules-sec-title {
		font-size: 11.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px 10px;
		min-width: 0;
		flex: 1 1 auto;
	}

	.rules-sec-title > span {
		white-space: nowrap;
		flex-shrink: 0;
	}

	.link-btn {
		appearance: none;
		border: 0;
		background: transparent;
		color: var(--accent);
		font-size: 11px;
		cursor: pointer;
		padding: 0;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.rules-empty {
		padding: 12px;
		font-size: 12px;
		color: var(--text-muted);
		text-align: center;
		border: 1px dashed var(--border);
		border-radius: var(--radius-sm);
	}

	.rules-table {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.rule-row {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 5px 8px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
	}

	.rule-row.rule-disabled {
		opacity: 0.55;
	}

	.rule-row.rule-selected {
		background: color-mix(in srgb, var(--color-accent-tint) 45%, var(--bg-secondary));
		border-color: var(--color-accent-border);
	}

	.rules-sec-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.rule-idx {
		font-size: 11px;
		color: var(--text-muted);
		min-width: 28px;
	}

	.rule-type-sel {
		width: 125px;
		flex-shrink: 0;
	}

	.rule-pattern-inp {
		flex: 2 1 180px;
		min-width: 120px;
	}

	.rule-proto-sel {
		width: 105px;
		flex-shrink: 0;
	}

	.rule-ports-inp {
		width: 120px;
		flex-shrink: 0;
	}

	.rule-comment-inp {
		flex: 1 1 120px;
		min-width: 90px;
	}

	.list-rule-row {
		justify-content: space-between;
	}

	.list-rule-pattern {
		flex: 1;
		font-size: 12.5px;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.pagination-bar {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 12px;
		padding-top: 6px;
	}

	.page-info {
		font-size: 12px;
		color: var(--text-muted);
	}

	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp.group-name-inp-expanded {
		width: 180px !important;
		max-width: 100%;
		flex: 0 1 180px;
		min-width: 120px;
		font-weight: 600;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.sec-box {
		padding: 10px 12px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.sec-cap {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.field-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 10px 12px;
		min-width: 0;
	}

	.field-row-info {
		flex: 1 1 180px;
		min-width: 0;
	}

	.field-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.catalog-dropdown {
		max-height: 220px;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		display: flex;
		flex-direction: column;
	}

	.catalog-more-hint {
		padding: 5px 10px;
		font-size: 11px;
		color: var(--text-muted);
		border-top: 1px solid var(--border);
		background: var(--bg-tertiary);
	}

	.catalog-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 5px 10px;
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		cursor: pointer;
		text-align: left;
	}

	.catalog-item:hover {
		background: var(--bg-tertiary);
	}

	.preview-err {
		font-size: 12px;
		color: var(--color-danger);
	}

	.preview-box {
		font-size: 12px;
		color: var(--text-secondary);
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.preview-sample {
		max-height: 120px;
		overflow-y: auto;
		padding: 6px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 4px;
		font-size: 11px;
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

	.mt-2 {
		margin-top: 8px;
	}

	.mt-3 {
		margin-top: 12px;
	}

	:global(.spin) {
		animation: firc-spin 1s linear infinite;
	}

	@keyframes firc-spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (max-width: 768px) {
		.card {
			grid-template-columns: 28px minmax(0, 1fr);
		}
		.card.select-mode {
			grid-template-columns: 22px 28px minmax(0, 1fr);
		}
		.reorder-slot {
			display: none;
		}
		.trail {
			grid-column: 1 / -1;
			flex-wrap: wrap;
			min-width: 0;
			justify-content: space-between;
			border-top: 1px dashed var(--border);
			padding-top: 8px;
		}
		.outbound-select-wrap,
		.iface-picker-root {
			max-width: 100%;
			min-width: 0;
		}
		.tone-select-pill {
			max-width: 100%;
			height: auto;
			min-height: 27px;
			flex-wrap: wrap;
			padding: 3px 8px;
		}
		.iface-btn-label {
			max-width: 150px;
		}
		.right-slot {
			flex-wrap: wrap;
		}
		.rule-row {
			flex-wrap: wrap;
		}
		.form-grid-2 {
			grid-template-columns: 1fr;
		}
	}

	.pg-panel {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}

	.pg-panel-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
	}

	.pg-panel-title {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		font-weight: 700;
		color: var(--text-primary);
		min-width: 0;
	}

	.pg-panel-actions {
		display: flex;
		align-items: center;
		gap: 5px;
		flex-wrap: wrap;
	}

	.pg-action-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		height: 24px;
		padding: 0 8px;
		border-radius: 5px;
		border: 1px solid var(--border);
		background: var(--bg-tertiary);
		color: var(--text-primary);
		font-family: inherit;
		font-size: 11px;
		font-weight: 600;
		cursor: pointer;
		transition: border-color var(--t-fast), color var(--t-fast), background var(--t-fast);
	}

	.pg-action-btn:hover:not(:disabled) {
		border-color: var(--color-accent);
		color: var(--color-accent);
	}

	.pg-action-btn:disabled {
		opacity: 0.55;
		cursor: wait;
	}

	.pg-action-btn.ghost {
		background: transparent;
		color: var(--text-secondary);
	}

	.pg-action-btn.icon-only {
		padding: 0 5px;
	}

	.pg-action-btn.mini {
		height: 20px;
		padding: 0 5px;
		font-size: 10.5px;
	}

	.pg-rows {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.pg-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px 10px;
		padding: 5px 9px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		min-width: 0;
	}

	.pg-row.is-backup {
		border-color: var(--color-warning-border);
	}

	.pg-row.all-dead {
		border-color: var(--color-error-border);
	}

	.pg-row-ident {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		flex-shrink: 0;
		max-width: 100%;
	}

	.pg-row-name {
		font-size: 12px;
		font-weight: 700;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 210px;
	}

	.pg-chain-inline {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 4px;
		flex: 1 1 auto;
		min-width: 0;
	}

	.pg-chain-sep {
		font-size: 11px;
		color: var(--text-muted);
		user-select: none;
	}

	.pg-node-pill {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 7px;
		min-height: 22px;
		border-radius: 5px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-family: inherit;
		font-size: 11.5px;
		cursor: pointer;
		max-width: 100%;
		min-width: 0;
		transition: border-color var(--t-fast), background var(--t-fast), color var(--t-fast);
	}

	.pg-node-pill:hover:not(:disabled) {
		border-color: var(--color-accent);
		color: var(--text-primary);
	}

	.pg-node-pill:disabled {
		opacity: 0.65;
		cursor: wait;
	}

	.pg-node-pill.is-active {
		background: color-mix(in srgb, var(--color-success) 10%, var(--bg-secondary));
		border-color: var(--color-success-border);
		color: var(--text-primary);
		font-weight: 600;
	}

	.pg-node-pill.is-backup {
		background: color-mix(in srgb, var(--color-warning) 10%, var(--bg-secondary));
		border-color: var(--color-warning-border);
		color: var(--text-primary);
		font-weight: 600;
	}

	.pg-node-pill.is-dead {
		opacity: 0.72;
		border-color: var(--color-error-border);
	}

	.pg-node-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 150px;
	}

	.pg-node-delay {
		display: inline-flex;
		align-items: center;
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.pg-node-delay.good {
		color: var(--latency-color-ok);
	}

	.pg-node-delay.medium {
		color: var(--latency-color-slow);
	}

	.pg-node-delay.bad {
		color: var(--latency-color-fail);
	}

	.pg-node-delay.none {
		color: var(--text-muted);
	}

	.pg-mem-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.pg-mem-dot.active {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success-border);
		animation: firc-active-pulse 1.5s ease-in-out infinite;
	}

	.pg-mem-dot.backup {
		background: var(--color-warning);
		box-shadow: 0 0 6px var(--color-warning-border);
	}

	.pg-mem-dot.alive {
		background: var(--color-success);
		opacity: 0.55;
	}

	.pg-mem-dot.dead {
		background: var(--color-error);
	}

	.pg-mem-dot.unknown {
		background: var(--text-muted);
		opacity: 0.55;
	}

	.pg-mem-act-tag {
		font-size: 9.5px;
		font-weight: 700;
		padding: 0 4px;
		border-radius: 3px;
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
	}

	.pg-delay-btn {
		appearance: none;
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 1px 5px;
		border-radius: 4px;
		border: 1px solid transparent;
		background: transparent;
		font-family: var(--font-mono);
		font-size: 10.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		line-height: 1.2;
		cursor: pointer;
		flex-shrink: 0;
		transition: background var(--t-fast), border-color var(--t-fast);
	}

	.pg-delay-btn.compact {
		font-size: 10px;
		padding: 1px 4px;
	}

	.pg-delay-btn:hover:not(:disabled) {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}

	.pg-delay-btn.good {
		color: var(--latency-color-ok);
	}

	.pg-delay-btn.medium {
		color: var(--latency-color-slow);
	}

	.pg-delay-btn.bad {
		color: var(--latency-color-fail);
	}

	.pg-delay-btn.none {
		color: var(--text-muted);
	}

	.pg-delay-val {
		white-space: nowrap;
	}

	.pg-chain-pill.is-active {
		border-color: var(--color-success-border);
		background: color-mix(in srgb, var(--color-success) 10%, var(--bg-tertiary));
	}

	.pg-pill-id {
		font-size: 10.5px;
		color: var(--text-muted);
	}

	.pg-cap-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.pg-cap-btn {
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 10px;
		font-weight: 600;
		border: 1px solid color-mix(in srgb, var(--color-accent) 35%, var(--border));
		background: color-mix(in srgb, var(--color-accent) 12%, transparent);
		color: var(--color-accent);
		cursor: pointer;
	}

	.pg-empty-hint {
		padding: 4px 8px 6px;
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.35;
	}

	.pg-editor-card {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.pg-card-head {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.pg-chain-items {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 6px;
	}

	.pg-chain-pill {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 4px 8px;
		border-radius: 6px;
		font-size: 12px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
	}

	.pg-chain-pill button {
		border: none;
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
		padding: 0 2px;
		font-size: 11px;
	}

	.pg-rank-num {
		font-weight: 700;
		color: var(--color-accent);
		font-size: 11px;
	}

	.pg-cand-grid {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 6px;
	}

	.pg-cand-btn {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 5px 10px;
		border-radius: 6px;
		font-size: 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		cursor: pointer;
	}

	.pg-cand-btn.selected {
		background: color-mix(in srgb, var(--color-accent) 14%, var(--bg-secondary));
		border-color: color-mix(in srgb, var(--color-accent) 45%, var(--border));
		color: var(--text-primary);
		font-weight: 500;
	}

	.pg-cand-btn.wan-cand {
		border-style: dashed;
	}

	.pg-cand-rank {
		font-size: 10px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-accent) 22%, transparent);
		color: var(--color-accent);
	}

	.list-toggle-actions {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		flex-shrink: 0;
	}
</style>
