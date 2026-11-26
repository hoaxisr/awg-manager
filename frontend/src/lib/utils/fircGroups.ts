// Чистые помощники панели групп FIRC: нормализация, сравнение с базой,
// цепочки интерфейсов, подписи статусов. Состояние и API — в FircGroupsPanel.
import { m, formatLocale } from '$lib/i18n';
import type { DropdownOption } from '$lib/components/ui';
import type { CatalogPreset } from '$lib/types';
import { detectFircRuleType } from '$lib/utils/fircBulkRules';
import { carryCheckExtras, storedIntervalSec } from '$lib/utils/fircGroupCheck';
import {
	compareFircInterfaces,
	fircTunnelSortPriority,
	friendlyIfaceLabel,
	getEffectiveInterfaceProbe,
	isAwgmSingboxProxyInterface,
	isInternalServerFircInterface,
	resolveGroupActiveInterface,
	resolveInterfaceHealth,
	resolveProxyGroupRuntime,
} from '$lib/utils/fircFallback';
import {
	DNS_ONLY_IFACE,
	dnsOnlyBlocker,
	fitGroupToOutbound,
	isDnsOnlyGroup,
	shortServer,
	splitServers,
	supportsDnsOnly,
} from '$lib/utils/fircResolve';
import type {
	FircExtConfig,
	FircExtractedDomains,
	FircGroup,
	FircGroupCheck,
	FircGroupLive,
	FircInterfaceOption,
	FircInterfaceProbeResult,
	FircListRule,
	FircProxyGroup,
	FircRule,
	FircSystemResolver,
	FircTunnelProbeRow,
	FircTunnelState,
} from '$lib/types/firc';

export const PAGE_SIZE = 50;
export const MAX_GROUP_IFACES = 4;
/** Зарезервированная группа облака Keenetic (KeenDNSGroupID в internal/firc). */
const KEENDNS_GROUP_ID = 'c100d001';

export function intervalOptions(): { value: number; label: string }[] {
	return [
		{ value: 3600, label: m.firc_groups_interval_hourly() },
		{ value: 21600, label: m.firc_groups_interval_6h() },
		{ value: 86400, label: m.firc_groups_interval_daily() },
		{ value: 604800, label: m.firc_groups_interval_weekly() },
		{ value: 0, label: m.firc_groups_interval_manual() },
	];
}

/** Живое состояние, нужное подписям и точкам статуса в дочерних компонентах. */
export interface FircGroupsRuntime {
	groups: FircGroup[];
	liveMap: Record<string, FircGroupLive>;
	tunnelsState: FircTunnelState[];
	interfaces: FircInterfaceOption[];
	resolvers: FircSystemResolver[];
	proxyGroups: FircProxyGroup[];
	ifaceProbes: Record<string, FircInterfaceProbeResult>;
	tunnelProbes: Record<string, Record<string, FircTunnelProbeRow>>;
}

export type FircProbeOptions = { groupKey?: string; url?: string; silent?: boolean };

export interface GroupListPageState {
	offset: number;
	total: number;
	matched: number;
	loading: boolean;
	rules: FircListRule[];
	edits: Record<string, { type?: string; enable?: boolean }>;
}

export function randomHexId(): string {
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

export function normalizeGroup(g: FircGroup): FircGroup {
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
		resolve: isDnsOnlyGroup(g)
			? { server: g.resolve?.server ?? '', fallback: g.resolve?.fallback ?? true }
			: { tunnel: g.resolve?.tunnel ?? true, server: g.resolve?.server ?? '' },
		...(g.check
			? {
					check: {
						mode: g.check.mode || 'none',
						url: g.check.url ?? '',
						...(g.check.intervalSec ? { intervalSec: g.check.intervalSec } : {}),
						...(g.check.timeoutMs ? { timeoutMs: g.check.timeoutMs } : {}),
						...(g.check.failThreshold ? { failThreshold: g.check.failThreshold } : {}),
						...(g.check.recoverThreshold ? { recoverThreshold: g.check.recoverThreshold } : {}),
					},
				}
			: {}),
		...(g.resolver ? { resolver: g.resolver } : {}),
		...(g.block ? { block: g.block } : {}),
		...(g.cloudflare ? { cloudflare: true } : {}),
		...(g.proxyGroupId ? { proxyGroupId: g.proxyGroupId } : {}),
	};
}

/** Группы с приложенными связями из ext-конфига: демон их не хранит. */
export function withGroupLinks(
	groups: FircGroup[],
	links: Record<string, string> | null | undefined,
): FircGroup[] {
	return links ? groups.map((g) => ({ ...g, proxyGroupId: links[g.id] })) : groups;
}

export function groupLinksOf(groups: FircGroup[]): Record<string, string> {
	return Object.fromEntries(groups.flatMap((g) => (g.proxyGroupId ? [[g.id, g.proxyGroupId]] : [])));
}

export function serializeForDiff(list: FircGroup[]): string {
	return JSON.stringify(
		list.map((g) => ({
			id: g.id,
			name: g.name,
			interface: g.interface,
			proxyGroupId: g.proxyGroupId || undefined,
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

/** Тело запроса сохранения: имена и шаблоны правил без пробелов по краям. */
export function savePayload(list: FircGroup[]): FircGroup[] {
	return list.map((g) => ({
		...g,
		proxyGroupId: undefined,
		name: g.name.trim() || 'group',
		rules: g.rules.map((r) => ({
			...r,
			rule: r.rule.trim(),
		})),
	}));
}

export function normalizeIfaceList(raw: string | string[] | undefined): string[] {
	if (Array.isArray(raw)) return raw.filter((s) => typeof s === 'string' && s.length > 0);
	if (typeof raw === 'string' && raw.length > 0) return [raw];
	return [];
}

export function denormalizeIfaceList(list: string[]): string | string[] {
	const cleaned = list.filter((s) => s.length > 0);
	if (cleaned.length === 0) return '';
	if (cleaned.length === 1) return cleaned[0];
	return cleaned;
}

export function isSpecialIface(id: string): boolean {
	return id === 'blackhole' || id === 'block' || id === DNS_ONLY_IFACE;
}

export function primaryIface(iface: string | string[]): string {
	if (Array.isArray(iface)) return iface[0] ?? '';
	return iface ?? '';
}

export function formatIfaceOption(opt: FircInterfaceOption): string {
	const rawDesc = opt.name || opt.ndmsName || '';
	const shortDesc = rawDesc.split(' (')[0].trim();
	return shortDesc && shortDesc !== opt.id ? `${opt.id} — ${shortDesc}` : opt.id;
}

/** Делает id основным выходом группы; true — пикер надо закрыть. */
/** Выход, выбранный вручную: другая цепочка уже не от прокси-группы. */
function setManualIface(group: FircGroup, iface: string | string[]) {
	if (!sameChain(normalizeIfaceList(group.interface), normalizeIfaceList(iface))) {
		group.proxyGroupId = undefined;
	}
	group.interface = iface;
	fitGroupToOutbound(group);
}

export function setPrimaryIface(group: FircGroup, id: string): boolean {
	if (isSpecialIface(id)) {
		setManualIface(group, id);
		if (id === 'block' && !group.block) group.block = 'null';
		return true;
	}
	const cur = normalizeIfaceList(group.interface);
	if (cur.length <= 1 || isSpecialIface(cur[0])) {
		setManualIface(group, id);
		return true;
	}
	const rest = cur.filter((x) => x !== id && !isSpecialIface(x));
	const next = [id, ...rest].slice(0, MAX_GROUP_IFACES);
	setManualIface(group, denormalizeIfaceList(next));
	return false;
}

/** Умолчание для новой цепочки; у прежней (wasChain) none — выбор пользователя. */
export function ensureDefaultGroupCheck(group: FircGroup, wasChain: boolean) {
	const list = normalizeIfaceList(group.interface);
	if (list.length >= 2 && !isSpecialIface(list[0])) {
		if (!group.check || (group.check.mode === 'none' && !wasChain)) {
			group.check = {
				...group.check,
				mode: group.id === KEENDNS_GROUP_ID ? 'keendns' : 'url',
				url: group.check?.url ?? '',
			};
		}
	} else if (list.length < 2 && group.check && group.check.mode !== 'none') {
		group.check = { ...group.check, mode: 'none' };
	}
}

export function toggleBackupIface(group: FircGroup, id: string) {
	if (isSpecialIface(id)) return;
	const cur = normalizeIfaceList(group.interface);
	const idx = cur.indexOf(id);
	if (idx >= 0) {
		if (cur.length <= 1) return;
		const next = cur.filter((x) => x !== id);
		setManualIface(group, denormalizeIfaceList(next));
		ensureDefaultGroupCheck(group, true);
	} else {
		if (cur.length === 0 || isSpecialIface(cur[0])) {
			setManualIface(group, id);
			ensureDefaultGroupCheck(group, false);
			return;
		}
		if (cur.length >= MAX_GROUP_IFACES) return;
		setManualIface(group, denormalizeIfaceList([...cur, id]));
		ensureDefaultGroupCheck(group, cur.length >= 2);
	}
}

/** Проверка целиком, но без чужих для режима полей: адрес только у url, у keendns — таймаут 5 с. */
export function normalizeGroupCheck(check: FircGroupCheck): FircGroupCheck {
	const mode = check.mode || 'none';
	const { intervalSec: rawInterval, ...rest } = check;
	const intervalSec = storedIntervalSec(rawInterval);
	return {
		...rest,
		mode,
		url: mode === 'url' ? (check.url ?? '').trim() : '',
		...(intervalSec !== undefined ? { intervalSec } : {}),
		...(mode === 'keendns' ? { timeoutMs: check.timeoutMs || 5000 } : {}),
	};
}

/** Ноль и отсутствие поля для fircd одно и то же — умолчание демона. */
function checkKey(check: FircGroupCheck | undefined): string {
	const c = normalizeGroupCheck(check ?? { mode: 'none' });
	return JSON.stringify([
		c.mode,
		c.url,
		c.intervalSec ?? 0,
		c.timeoutMs ?? 0,
		c.failThreshold ?? 0,
		c.recoverThreshold ?? 0,
	]);
}

function sameChain(a: string[], b: string[]): boolean {
	return a.length === b.length && a.every((v, idx) => v === b[idx]);
}

/**
 * Проверка прокси-группы для черновика. Без своей — общая проверка групп этой цепочки;
 * разошлись или групп нет — undefined, группы живут со своими. Группа KeenDNS лишь
 * подтверждает совпадение: её проверкой управляет переключатель облака.
 */
export function inferDraftProxyGroupCheck(
	pg: FircProxyGroup,
	groups: FircGroup[],
): FircGroupCheck | undefined {
	const ifaces = pg.interfaces ?? [];
	// Одиночному интерфейсу проверять нечего; это умолчание на случай роста до цепочки.
	if (ifaces.length < 2) return { mode: 'url', url: '' };
	if (pg.check) return normalizeGroupCheck(pg.check);
	const matching = groups.filter((g) => sameChain(normalizeIfaceList(g.interface), ifaces));
	const source = matching.find((g) => g.id !== KEENDNS_GROUP_ID);
	if (!source || matching.some((g) => checkKey(g.check) !== checkKey(source.check))) return undefined;
	return normalizeGroupCheck(source.check ?? { mode: 'none' });
}

/** Проверка, которую прокси-группа задаёт своим группам; undefined — не задаёт. */
export function buildGroupCheckFromProxyGroup(pg: FircProxyGroup): FircGroupCheck | undefined {
	return pg.check && pg.interfaces.length >= 2 ? normalizeGroupCheck(pg.check) : undefined;
}

export function normalizeProxyGroupForSave(pg: FircProxyGroup): FircProxyGroup {
	const interfaces = (pg.interfaces ?? []).slice(0, MAX_GROUP_IFACES);
	const check: FircGroupCheck | undefined =
		interfaces.length < 2 ? { mode: 'none' } : pg.check && normalizeGroupCheck(pg.check);
	return {
		id: pg.id || randomHexId(),
		name: pg.name.trim(),
		interfaces,
		...(check ? { check } : {}),
	};
}

/** Пара прокси-групп с одной цепочкой; пары, лежащие так уже в saved, не считаются. */
function findNewDuplicateChain(
	draft: FircProxyGroup[],
	saved: FircProxyGroup[],
): [FircProxyGroup, FircProxyGroup] | null {
	const unchanged = (pg: FircProxyGroup) =>
		saved.some((s) => s.id === pg.id && sameChain(s.interfaces, pg.interfaces));
	for (const [i, pg] of draft.entries()) {
		const prev = draft.slice(0, i).find(
			(p) => sameChain(p.interfaces, pg.interfaces) && !(unchanged(p) && unchanged(pg)),
		);
		if (prev) return [prev, pg];
	}
	return null;
}

export function applyProxyGroupToGroup(group: FircGroup, pg: FircProxyGroup): boolean {
	if (!pg.interfaces || pg.interfaces.length === 0) return false;
	const wasChain = normalizeIfaceList(group.interface).length >= 2;
	group.interface = denormalizeIfaceList(pg.interfaces);
	fitGroupToOutbound(group);
	group.proxyGroupId = pg.id;
	const pgCheck = buildGroupCheckFromProxyGroup(pg);
	if (pgCheck) {
		group.check = pgCheck;
	} else {
		ensureDefaultGroupCheck(group, wasChain);
	}
	return true;
}

export function ifaceDotState(
	rt: FircGroupsRuntime,
	group: FircGroup,
	id: string,
): 'alive' | 'dead' | 'unknown' | null {
	if (isSpecialIface(id)) return null;
	const liveEntry = rt.liveMap[group.id]?.interfaces?.find((x) => x.name === id);
	if (liveEntry) return liveEntry.state;
	const groupLive = rt.liveMap[group.id];
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
	const tunSt = rt.tunnelsState.find((t) => t.device === id);
	if (tunSt) {
		if (tunSt.status === 'up') return 'alive';
		if (tunSt.status === 'starting') return 'unknown';
		return 'dead';
	}
	const sysOpt = rt.interfaces.find((x) => x.id === id);
	if (sysOpt && typeof sysOpt.up === 'boolean') {
		return sysOpt.up ? 'alive' : 'dead';
	}
	return 'unknown';
}

export function ifaceDotTooltip(rt: FircGroupsRuntime, group: FircGroup, id: string): string {
	const liveEntry = rt.liveMap[group.id]?.interfaces?.find((x) => x.name === id);
	if (liveEntry) {
		if (liveEntry.state === 'alive') return m.firc_groups_iface_tip_alive({ id });
		if (liveEntry.state === 'dead')
			return m.firc_groups_iface_tip_dead({
				id,
				error: liveEntry.error ? ` (${liveEntry.error})` : '',
			});
	}
	const state = ifaceDotState(rt, group, id);
	if (state === 'alive') return m.firc_groups_iface_tip_up({ id });
	if (state === 'dead') return m.firc_groups_iface_tip_down({ id });
	return m.firc_groups_iface_tip_unknown({ id });
}

/** Прокси-группа, с которой группа связана; цепочка должна оставаться её цепочкой. */
function linkedProxyGroup(
	proxyGroups: FircProxyGroup[],
	group: Pick<FircGroup, 'interface' | 'proxyGroupId'>,
): FircProxyGroup | undefined {
	const list = normalizeIfaceList(group.interface);
	return proxyGroups.find((pg) => pg.id === group.proxyGroupId && sameChain(pg.interfaces, list));
}

/** Прокси-группа для показа: связанная, иначе первая с той же цепочкой. */
export function matchingProxyGroup(
	proxyGroups: FircProxyGroup[],
	group: Pick<FircGroup, 'interface' | 'proxyGroupId'>,
): FircProxyGroup | undefined {
	const list = normalizeIfaceList(group.interface);
	if (list.length === 0) return undefined;
	const linked = linkedProxyGroup(proxyGroups, group);
	if (linked) return linked;
	let firstMatch: FircProxyGroup | undefined;
	for (const pg of proxyGroups) {
		if (sameChain(pg.interfaces, list)) {
			if (pg.check != null) return pg;
			if (!firstMatch) firstMatch = pg;
		}
	}
	return firstMatch;
}

/** Связанная прокси-группа, чью проверку наследует группа; без check она ничем не управляет. */
export function managingProxyGroup(
	proxyGroups: FircProxyGroup[],
	group: Pick<FircGroup, 'interface' | 'proxyGroupId'>,
): FircProxyGroup | undefined {
	const pg = linkedProxyGroup(proxyGroups, group);
	return pg && buildGroupCheckFromProxyGroup(pg) ? pg : undefined;
}

export function shortIfaceTag(interfaces: FircInterfaceOption[], id: string): string {
	const opt = interfaces.find((x) => x.id === id);
	if (opt?.role === 'wan' || id === 'eth3') return `Direct (${id})`;
	return id;
}

export function ifaceLabel(rt: FircGroupsRuntime, id: string): string {
	return friendlyIfaceLabel(id, rt.interfaces, rt.tunnelsState);
}

export function getIfaceProbe(rt: FircGroupsRuntime, id: string): FircInterfaceProbeResult | null {
	return getEffectiveInterfaceProbe(id, rt.ifaceProbes, rt.tunnelProbes, rt.tunnelsState);
}

export function resolveGroupActive(rt: FircGroupsRuntime, group: FircGroup) {
	const list = normalizeIfaceList(group.interface);
	const probesMap: Record<string, FircInterfaceProbeResult> = {};
	for (const id of list) {
		const p = getIfaceProbe(rt, id);
		if (p) probesMap[id] = p;
	}
	return resolveGroupActiveInterface(
		group,
		rt.liveMap[group.id],
		rt.liveMap,
		rt.tunnelsState,
		rt.interfaces,
		probesMap,
	);
}

export function resolveIfaceState(rt: FircGroupsRuntime, group: FircGroup | undefined, id: string) {
	return resolveInterfaceHealth(
		id,
		group ? rt.liveMap[group.id] : undefined,
		rt.liveMap,
		rt.tunnelsState,
		rt.interfaces,
		getIfaceProbe(rt, id),
	);
}

export function proxyGroupRuntime(rt: FircGroupsRuntime, pg: FircProxyGroup) {
	return resolveProxyGroupRuntime(
		pg,
		rt.groups,
		rt.liveMap,
		rt.tunnelsState,
		rt.interfaces,
		rt.ifaceProbes,
		rt.tunnelProbes,
	);
}

export function allFallbackInterfaceIds(proxyGroups: FircProxyGroup[], groups: FircGroup[]): string[] {
	const out = new Set<string>();
	for (const pg of proxyGroups) {
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

export function formatIfaceButtonText(rt: FircGroupsRuntime, group: FircGroup): string {
	const list = normalizeIfaceList(group.interface);
	if (list.length === 0) return m.firc_groups_iface_none();
	const first = list[0];
	if (first === 'blackhole') return m.firc_groups_block();
	if (first === 'block') return m.firc_groups_block_dns();
	if (first === DNS_ONLY_IFACE) return m.firc_dns_only();
	const tag = (id: string) => shortIfaceTag(rt.interfaces, id);
	const matchedPg = matchingProxyGroup(rt.proxyGroups, group);
	if (matchedPg) {
		return `${matchedPg.name} (${matchedPg.interfaces.map(tag).join(' → ')})`;
	}
	if (list.length === 1) {
		const opt = rt.interfaces.find((x) => x.id === first);
		if (opt) {
			const prefix = opt.role === 'wan' ? 'Direct: ' : '';
			return `${prefix}${formatIfaceOption(opt)}`;
		}
		return first;
	}
	return list.length === 2
		? `${tag(list[0])} → ${tag(list[1])}`
		: m.firc_groups_iface_chain_more({ primary: tag(list[0]), count: list.length - 1 });
}

export function formatCompactOutboundLabel(
	rt: FircGroupsRuntime,
	group: FircGroup,
	activeIface: string | null,
	isFallbackGroup: boolean,
	allDead: boolean,
): string {
	if (isFallbackGroup) {
		if (allDead) return m.firc_fallback_no_active();
		const target = activeIface || primaryIface(group.interface);
		return target ? ifaceLabel(rt, target) : '—';
	}
	return formatIfaceButtonText(rt, group);
}

export type PickerItem = { id: string; label: string; role: string };

export function getPickerGroups(interfaces: FircInterfaceOption[], group: FircGroup) {
	const selectedSet = new Set(normalizeIfaceList(group.interface));
	const tunnels: PickerItem[] = [];
	const wans: PickerItem[] = [];
	const others: PickerItem[] = [];
	const seen = new Set<string>();

	for (const opt of interfaces) {
		if (!opt.id || seen.has(opt.id) || isSpecialIface(opt.id)) continue;
		if (isAwgmSingboxProxyInterface(opt) && !selectedSet.has(opt.id)) continue;
		seen.add(opt.id);
		const role = opt.role || 'other';
		const item = { id: opt.id, label: formatIfaceOption(opt), role };
		if (role === 'tunnel') {
			if (isInternalServerFircInterface(opt) && !selectedSet.has(opt.id)) {
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
		tunnels.push({ id, label: m.firc_groups_iface_not_found({ id }), role: 'tunnel' });
	}

	tunnels.sort((a, b) => {
		const pa = fircTunnelSortPriority(a.id);
		const pb = fircTunnelSortPriority(b.id);
		if (pa !== pb) return pa - pb;
		return a.id.localeCompare(b.id, undefined, { numeric: true });
	});
	wans.sort((a, b) => a.id.localeCompare(b.id, undefined, { numeric: true }));
	others.sort((a, b) => a.id.localeCompare(b.id, undefined, { numeric: true }));

	return { tunnels, wans, others };
}

/** Варианты выхода для формы группы; служебный интерфейс, прокси sing-box и «Только DNS» у старого демона — только если уже выбраны. */
export function buildAvailableInterfaces(
	interfaces: FircInterfaceOption[],
	selectedIface: string,
): { value: string; label: string }[] {
	const list: { value: string; label: string; role: string }[] = [];
	const seen = new Set<string>();
	for (const opt of interfaces) {
		if (!opt.id || isSpecialIface(opt.id) || seen.has(opt.id)) continue;
		if ((isInternalServerFircInterface(opt) || isAwgmSingboxProxyInterface(opt)) && selectedIface !== opt.id) {
			continue;
		}
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
		const pa = fircTunnelSortPriority(a.value);
		const pb = fircTunnelSortPriority(b.value);
		if (pa !== pb) return pa - pb;
		return a.value.localeCompare(b.value, undefined, { numeric: true });
	});
	const out: { value: string; label: string }[] = list.map(({ value, label }) => ({
		value,
		label,
	}));
	out.push({ value: 'blackhole', label: m.firc_groups_block_blackhole() });
	out.push({ value: 'block', label: m.firc_groups_block_dns() });
	if (supportsDnsOnly(interfaces) || selectedIface === DNS_ONLY_IFACE) {
		out.push({ value: DNS_ONLY_IFACE, label: m.firc_dns_only() });
	}
	return out;
}

/** Варианты массовой смены выхода: «Только DNS» закрыт с пояснением, если подходит не всем выбранным. */
export function buildBulkInterfaces(
	interfaces: FircInterfaceOption[],
	selected: FircGroup[],
): { value: string; label: string; disabled?: boolean }[] {
	const blocker = selected.map((g) => dnsOnlyBlocker(g, interfaces)).find(Boolean);
	return [
		...buildAvailableInterfaces(interfaces, '').filter((o) => o.value !== DNS_ONLY_IFACE),
		{
			value: DNS_ONLY_IFACE,
			label: blocker ? m.firc_dns_only_blocked({ reason: blocker }) : m.firc_dns_only(),
			disabled: Boolean(blocker),
		},
	];
}

export function buildCatalogTunnelOptions(interfaces: FircInterfaceOption[]): DropdownOption[] {
	const opts: DropdownOption[] = [];
	const seen = new Set<string>();
	const sorted = [...interfaces].sort(compareFircInterfaces);
	for (const opt of sorted) {
		if (
			!opt.id ||
			seen.has(opt.id) ||
			isInternalServerFircInterface(opt) ||
			isAwgmSingboxProxyInterface(opt)
		) {
			continue;
		}
		if (opt.role === 'tunnel') {
			seen.add(opt.id);
			opts.push({
				value: opt.id,
				label: formatIfaceOption(opt),
				group: m.firc_groups_opt_tunnels(),
			});
		} else if (opt.role === 'wan') {
			seen.add(opt.id);
			opts.push({
				value: opt.id,
				label: `Direct (${formatIfaceOption(opt)})`,
				group: m.firc_groups_opt_direct_wan(),
			});
		}
	}
	opts.push({
		value: 'blackhole',
		label: m.firc_groups_block_blackhole(),
		group: m.firc_groups_opt_block(),
	});
	opts.push({
		value: 'block',
		label: m.firc_groups_block_dns(),
		group: m.firc_groups_opt_block(),
	});
	return opts;
}

export function groupStatusDotVariant(
	liveMap: Record<string, FircGroupLive>,
	group: FircGroup,
): 'success' | 'warning' | 'error' | 'muted' {
	if (!group.enable) return 'muted';
	const live = liveMap[group.id];
	if (!live) return 'warning';
	if (live.live) return 'success';
	if (live.reason === 'error') return 'error';
	return 'warning';
}

export function groupStatusTooltip(liveMap: Record<string, FircGroupLive>, group: FircGroup): string {
	if (!group.enable) return m.firc_groups_status_disabled();
	const live = liveMap[group.id];
	if (!live) return m.firc_groups_status_waiting();
	const iface = live.active || primaryIface(group.interface);
	if (live.live && isDnsOnlyGroup(group)) return m.firc_dns_only_live();
	if (live.live) return m.firc_groups_status_live({ iface });
	if (live.reason === 'waiting_iface') return m.firc_groups_status_waiting_iface({ iface });
	if (live.reason === 'no_node') return m.firc_groups_status_no_node({ iface });
	if (live.reason === 'rewriting') return m.firc_groups_status_rewriting();
	if (live.detail) return m.firc_groups_status_inactive_detail({ detail: live.detail });
	return m.firc_groups_status_inactive();
}

/** Серверы, отвечающие на имена группы; пусто — общий апстрим. */
function resolverServers(rt: FircGroupsRuntime, group: FircGroup, iface: string): string[] {
	if (!isDnsOnlyGroup(group) && !(group.resolve?.tunnel ?? true)) return [];
	const own = splitServers(group.resolve?.server);
	if (own.length > 0) return own;
	const liveRes = rt.liveMap[group.id]?.resolver ?? group.resolver;
	if (liveRes?.servers?.length) return liveRes.servers;
	return rt.resolvers.find((r) => r.interface === iface)?.servers ?? [];
}

function resolverBadge(rt: FircGroupsRuntime, group: FircGroup, show: (server: string) => string): string {
	const iface = rt.liveMap[group.id]?.active || primaryIface(group.interface);
	if (iface === 'block') return m.firc_groups_dns_block();
	const servers = resolverServers(rt, group, iface);
	return servers.length > 0 ? `DNS: ${servers.map(show).join(', ')}` : m.firc_groups_dns_shared();
}

export function resolverBadgeText(rt: FircGroupsRuntime, group: FircGroup): string {
	return resolverBadge(rt, group, shortServer);
}

/** Подсказка к бейджу: адреса DoH целиком. */
export function resolverBadgeTitle(rt: FircGroupsRuntime, group: FircGroup): string {
	return resolverBadge(rt, group, (s) => s);
}

/** Подсети серверов под нашим управлением: в фильтре устройств их не показываем. */
export const MANAGED_SERVER_SUBNETS = new Set([
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

export function devicesBadgeText(group: FircGroup): string {
	const allow = (group.devices?.allow ?? []).filter((x) => !MANAGED_SERVER_SUBNETS.has(x));
	const deny = group.devices?.deny ?? [];
	if (allow.length === 0 && deny.length === 0) return m.firc_groups_devices_all();
	if (allow.length > 0) return m.firc_groups_devices_only({ count: allow.length });
	return m.firc_groups_devices_except({ count: deny.length });
}

export function outboundToneClass(iface: string): string {
	if (!iface || iface === 'block' || iface === 'blackhole') return 'tone-block';
	if (iface.startsWith('tunvless')) return 'tone-subscription';
	if (iface.startsWith('nwg') || iface.startsWith('awg') || iface.startsWith('wg')) return 'tone-awg';
	if (iface.startsWith('t2s')) return 'tone-proxy';
	return 'tone-composite';
}

export function groupAccentClass(liveMap: Record<string, FircGroupLive>, group: FircGroup): string {
	if (!group.enable) return 'accent-muted';
	const iface = primaryIface(group.interface);
	if (iface === 'block') return 'accent-block';
	const variant = groupStatusDotVariant(liveMap, group);
	if (variant === 'success') return 'accent-ok';
	if (variant === 'error') return 'accent-err';
	return 'accent-warn';
}

export function formatTimestamp(ts?: number): string {
	if (!ts) return m.firc_groups_list_never_updated();
	const d = new Date(ts * 1000);
	return d.toLocaleString(formatLocale(), {
		day: '2-digit',
		month: '2-digit',
		year: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
	});
}

export function filterGroups(groups: FircGroup[], query: string) {
	const q = query.trim().toLowerCase();
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
}

/** Группы из файла экспорта: `{ groups: [...] }` или голый массив. */
export function parseImportedGroups(text: string): FircGroup[] {
	const raw = JSON.parse(text);
	const imported: FircGroup[] = Array.isArray(raw?.groups)
		? raw.groups
		: Array.isArray(raw)
			? raw
			: [];
	return imported.map((g) => normalizeGroup({ ...g, id: randomHexId(), proxyGroupId: undefined }));
}

/** Правила из текста по одному на строку; `#` — комментарий. */
export function parseRulesText(text: string): FircRule[] {
	return text
		.split(/\r?\n/)
		.map((l) => l.trim())
		.filter((l) => l && !l.startsWith('#'))
		.map((line) => ({
			id: randomHexId(),
			type: detectFircRuleType(line),
			rule: line,
			enable: true,
		}));
}

export function presetSrsUrl(p: CatalogPreset): string {
	return (
		p.engines.singbox?.ruleSets?.[0]?.url ??
		p.engines.dns?.subscriptionUrl ??
		''
	);
}

export function groupServiceKey(catalog: CatalogPreset[], group: FircGroup): string {
	const url = group.list?.url?.trim().toLowerCase();
	if (url && catalog.length > 0) {
		const match = catalog.find((p) => presetSrsUrl(p).toLowerCase() === url);
		if (match) return match.iconSlug || match.id;
	}
	return group.name.toLowerCase();
}

export function existingCatalogNames(groups: FircGroup[], catalog: CatalogPreset[]): string[] {
	const names = new Set<string>();
	const urls = new Set<string>();
	for (const g of groups) {
		if (g.name) names.add(g.name);
		if (g.list?.url) urls.add(g.list.url.trim().toLowerCase());
	}
	for (const p of catalog) {
		const u = presetSrsUrl(p).toLowerCase();
		if (u && urls.has(u)) {
			names.add(p.name);
		}
	}
	return Array.from(names);
}

export function existingCatalogRuleSetTags(groups: FircGroup[], catalog: CatalogPreset[]): string[] {
	const tags = new Set<string>();
	const urls = new Set<string>();
	const lowerNames = new Set(groups.map((g) => g.name.trim().toLowerCase()));
	for (const g of groups) {
		if (g.list?.url) urls.add(g.list.url.trim().toLowerCase());
	}
	for (const p of catalog) {
		const u = presetSrsUrl(p).toLowerCase();
		if ((u && urls.has(u)) || lowerNames.has(p.name.toLowerCase()) || lowerNames.has(p.id.toLowerCase())) {
			for (const rs of p.engines.singbox?.ruleSets ?? []) {
				if (rs.tag) tags.add(rs.tag);
			}
		}
	}
	return Array.from(tags);
}

/** Новые группы из пресетов каталога; пресеты без URL пропускаются. */
export function groupsFromPresets(presets: CatalogPreset[], iface: string): FircGroup[] {
	const out: FircGroup[] = [];
	for (const p of presets) {
		const url = presetSrsUrl(p);
		if (!url) continue;
		const isBlk = iface === 'block' || iface === 'blackhole';
		out.push(
			normalizeGroup({
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
				...(iface === 'block' ? { block: 'null' } : {}),
			}),
		);
	}
	return out;
}

/** Поля формы «Новая группа / Настройки группы». */
export interface GroupForm {
	name: string;
	iface: string;
	dnsMode: 'auto' | 'own' | 'off';
	dnsServers: string[];
	/** Только у группы «только DNS»: при отказе серверов отвечает общий апстрим. */
	dnsFallback: boolean;
	checkMode: 'none' | 'url' | 'keendns';
	checkUrl: string;
	listEnabled: boolean;
	listUrl: string;
	listInterval: number;
}

export function emptyGroupForm(iface: string): GroupForm {
	return {
		name: '',
		iface,
		dnsMode: 'auto',
		dnsServers: [''],
		dnsFallback: true,
		checkMode: 'none',
		checkUrl: '',
		listEnabled: false,
		listUrl: '',
		listInterval: 86400,
	};
}

export function groupFormFrom(g: FircGroup): GroupForm {
	const servers = splitServers(g.resolve?.server);
	const ifaceCount = normalizeIfaceList(g.interface).length;
	return {
		name: g.name,
		iface: primaryIface(g.interface),
		dnsMode: !(g.resolve?.tunnel ?? true) ? 'off' : servers.length > 0 ? 'own' : 'auto',
		dnsServers: servers.length > 0 ? servers : [''],
		dnsFallback: g.resolve?.fallback ?? true,
		checkMode: g.check?.mode ?? (ifaceCount >= 2 ? (g.id === 'c100d001' ? 'keendns' : 'url') : 'none'),
		checkUrl: g.check?.url ?? '',
		listEnabled: Boolean(g.list?.url),
		listUrl: g.list?.url ?? '',
		listInterval: g.list?.interval ?? 86400,
	};
}

/** Группа после «Применить»/«Создать»: cur — редактируемая группа, без неё — новая. */
export function groupFromForm(
	form: GroupForm,
	cur: FircGroup | undefined,
	groupsCount: number,
	proxyGroups: FircProxyGroup[] = [],
): FircGroup {
	const server = form.dnsServers.map((s) => s.trim()).filter(Boolean).join(', ');
	const resolve =
		form.iface === DNS_ONLY_IFACE
			? { server, fallback: form.dnsFallback }
			: { tunnel: form.dnsMode !== 'off', server: form.dnsMode === 'own' ? server : '' };

	const listObj =
		form.listEnabled && form.listUrl.trim()
			? {
					url: form.listUrl.trim(),
					interval: form.listInterval,
					lastUpdate: cur ? (cur.list?.lastUpdate ?? 0) : 0,
					rulesTotal: cur ? (cur.list?.rulesTotal ?? 0) : 0,
				}
			: null;

	if (cur) {
		const curList = normalizeIfaceList(cur.interface);
		let nextIface: string | string[] = form.iface;
		if (!isSpecialIface(form.iface) && curList.length > 1 && !isSpecialIface(curList[0])) {
			const rest = curList.filter((x) => x !== form.iface && !isSpecialIface(x));
			nextIface = denormalizeIfaceList([form.iface, ...rest].slice(0, MAX_GROUP_IFACES));
		}
		const hasChain = normalizeIfaceList(nextIface).length >= 2 && !isSpecialIface(primaryIface(nextIface));
		const proxyGroupId = sameChain(curList, normalizeIfaceList(nextIface)) ? cur.proxyGroupId : undefined;
		const managing = managingProxyGroup(proxyGroups, { interface: nextIface, proxyGroupId });
		const nextCheck: FircGroupCheck | undefined = hasChain
			? normalizeGroupCheck(
					managing?.check ?? {
						...carryCheckExtras(cur.check, form.checkMode),
						mode: form.checkMode,
						url: form.checkUrl,
					},
				)
			: cur.check
				? { ...cur.check, mode: 'none', url: '' }
				: undefined;
		const next: FircGroup = {
			...cur,
			name: form.name.trim() || cur.name || 'group',
			interface: nextIface,
			proxyGroupId,
			resolve,
			...(nextCheck ? { check: nextCheck } : {}),
			list: listObj,
		};
		fitGroupToOutbound(next);
		return next;
	}
	return normalizeGroup({
		id: randomHexId(),
		name: form.name.trim() || `group-${groupsCount + 1}`,
		interface: form.iface,
		enable: true,
		devices: { allow: [], deny: [] },
		rules: [],
		list: listObj,
		resolve,
	});
}

export function emptyExtConfig(): FircExtConfig {
	return {
		proxyGroups: [],
		keenDnsCloud: { enabled: false, interfaces: [] },
		serverRouting: {
			enabledServers: [],
			awgServer: false,
			wdttServer: false,
			freeTurnServer: false,
			vlessXhttpServer: false,
		},
	};
}

/** Ответ fircGetExt с умолчаниями на месте отсутствующих разделов. */
export function normalizeExtConfig(ext: FircExtConfig): FircExtConfig {
	const empty = emptyExtConfig();
	return {
		proxyGroups: ext.proxyGroups ?? [],
		globalTarget: ext.globalTarget,
		keenDnsCloud: ext.keenDnsCloud ?? empty.keenDnsCloud,
		serverRouting: ext.serverRouting ?? empty.serverRouting,
		groupLinks: ext.groupLinks,
	};
}

/** Черновик редактора прокси-групп; пустой список — одна заготовка. */
export function proxyGroupsDraft(saved: FircProxyGroup[], groups: FircGroup[]): FircProxyGroup[] {
	const draft = saved.map((pg) => ({
		id: pg.id || randomHexId(),
		name: pg.name,
		interfaces: [...(pg.interfaces ?? [])],
		check: inferDraftProxyGroupCheck(pg, groups),
	}));
	if (draft.length > 0) return draft;
	return [
		{
			id: randomHexId(),
			name: m.firc_groups_pg_default_name(),
			interfaces: [],
			check: { mode: 'url', url: '' },
		},
	];
}

/** Текст ошибки для первой невалидной прокси-группы черновика или null; saved — до правки. */
export function proxyGroupsDraftError(
	draft: FircProxyGroup[],
	saved: FircProxyGroup[],
): string | null {
	for (const pg of draft) {
		if (!pg.name.trim()) return m.firc_groups_pg_name_required();
		if (pg.interfaces.length === 0) return m.firc_groups_pg_iface_required({ name: pg.name });
	}
	const dup = findNewDuplicateChain(draft, saved);
	if (dup) {
		return m.firc_groups_pg_duplicate_chain({
			first: dup[0].name.trim() || dup[0].id,
			second: dup[1].name.trim() || dup[1].id,
		});
	}
	return null;
}

/** Тост после «Копировать домены»: что скопировано и что пропущено. */
export function domainCopyNotice(
	res: Pick<FircExtractedDomains, 'lines' | 'skippedRegexOrCidr' | 'skippedRemoteLists'>,
	copied: boolean,
): { kind: 'warning' | 'success' | 'error'; text: string } {
	const regex = res.skippedRegexOrCidr > 0 ? m.firc_groups_copy_skip_regex({ count: res.skippedRegexOrCidr }) : '';
	if (res.lines.length === 0) {
		const remote = res.skippedRemoteLists > 0 ? m.firc_groups_copy_skip_remote({ count: res.skippedRemoteLists }) : '';
		const skipped = [regex, remote].filter(Boolean).join(', ');
		return {
			kind: 'warning',
			text: skipped ? m.firc_groups_copy_none_skipped({ skipped }) : m.firc_groups_copy_none(),
		};
	}
	if (!copied) return { kind: 'error', text: m.firc_groups_copy_failed() };
	const remote = res.skippedRemoteLists > 0 ? m.firc_groups_copy_skip_url_lists({ count: res.skippedRemoteLists }) : '';
	const skipped = [regex, remote].filter(Boolean).join(', ');
	return {
		kind: 'success',
		text: skipped
			? m.firc_groups_copy_done_skipped({ count: res.lines.length, skipped })
			: m.firc_groups_copy_done({ count: res.lines.length }),
	};
}
