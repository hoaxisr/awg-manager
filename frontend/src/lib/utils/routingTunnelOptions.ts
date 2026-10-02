import { m } from '$lib/i18n';
import type { DropdownOption } from '$lib/components/ui';
import type { PolicyGlobalInterface, RoutingTunnel, TunnelListItem } from '$lib/types';

/**
 * Стабильный код группы туннелей: по нему идёт сортировка и группировка.
 * Подпись строится на показе — routingGroupLabel(id).
 */
export type RoutingGroupId =
	| 'provider'
	| 'awg'
	| 'systemWg'
	| 'serverWg'
	| 'proxy'
	| 'opkgtun'
	| 'system'
	| 'lte'
	| 'wifi'
	| 'wan';

/** Display order for grouped tunnel dropdowns (aligned with sing-box router groups). */
const GROUP_ORDER: readonly RoutingGroupId[] = [
	'provider',
	'awg',
	'systemWg',
	'serverWg',
	'proxy',
	'opkgtun',
	'system',
	'lte',
	'wifi',
	'wan',
];

/** Подпись группы на текущем языке; вызывать при показе, не сохранять. */
export function routingGroupLabel(id: RoutingGroupId): string {
	switch (id) {
		case 'provider':
			return m.routing_tunnel_group_provider();
		case 'awg':
			return m.routing_singbox_group_awg();
		case 'systemWg':
			return m.routing_singbox_group_system();
		case 'serverWg':
			return m.routing_tunnel_group_server_wg();
		case 'proxy':
			return m.routing_tunnel_group_proxy();
		case 'opkgtun':
			return 'OpkgTun';
		case 'system':
			return m.routing_tunnel_group_system();
		case 'lte':
			return 'LTE / USB';
		case 'wifi':
			return 'Wi‑Fi';
		case 'wan':
			return 'WAN';
	}
}

function groupSortIndex(id: RoutingGroupId): number {
	const idx = GROUP_ORDER.indexOf(id);
	return idx >= 0 ? idx : GROUP_ORDER.length;
}

/**
 * Keenetic sing-box proxy: NDMS `Proxy0` ↔ kernel `t2s0` (см. ndms/query/interfaces).
 * В каталоге для маршрутизации нужен NDMS-id (`system:Proxy0`), не kernel.
 */
const SINGBOX_PROXY_KERNEL_RE = /^t2s\d+$/i;

function proxyIndexFromName(name: string): string | null {
	const n = name.trim().toLowerCase();
	const t2s = n.match(/^t2s(\d+)$/);
	if (t2s) return t2s[1];
	const proxy = n.match(/^proxy(\d+)$/);
	if (proxy) return proxy[1];
	return null;
}

/** Скрывает kernel `t2sN`, если в том же каталоге есть NDMS `ProxyN`. */
function shouldOmitT2sWhenProxyPresent(ndmsOrKernelName: string, proxyNames: string[]): boolean {
	const name = ndmsOrKernelName.trim();
	if (!name || !SINGBOX_PROXY_KERNEL_RE.test(name)) return false;
	const idx = proxyIndexFromName(name);
	if (idx === null) return false;
	const peer = `proxy${idx}`;
	return proxyNames.some((n) => n.toLowerCase() === peer);
}

/**
 * Скрывает дубль по kernel `t2sN`, если в каталоге уже есть `system:ProxyN`.
 * AWG на `nwg*` не затрагивается.
 */
export function shouldOmitSingboxProxyKernelDuplicate(
	t: RoutingTunnel,
	catalog: RoutingTunnel[],
): boolean {
	const kernel =
		t.type === 'wan'
			? t.id.replace(/^wan:/i, '').trim()
			: (t.iface ?? '').trim();
	const proxyNames = catalog
		.filter((o) => o.id.toLowerCase().startsWith('system:proxy'))
		.map((o) => o.id.slice('system:'.length));
	return shouldOmitT2sWhenProxyPresent(kernel, proxyNames);
}

/** Группа для NDMS-имени в «Интерфейсы политики» (ip policy permit). */
export function policyInterfaceGroupId(ndmsName: string): RoutingGroupId {
	const lower = ndmsName.trim().toLowerCase();
	if (lower.startsWith('pppoe') || lower.startsWith('isp')) return 'provider';
	if (lower.startsWith('wireguard')) return 'systemWg';
	if (lower.startsWith('proxy')) return 'proxy';
	if (lower.startsWith('opkgtun') || lower.startsWith('awgm')) return 'awg';
	if (lower.startsWith('lte') || lower.startsWith('usb')) return 'lte';
	if (lower.startsWith('apcli') || lower.startsWith('wlan')) return 'wifi';
	return 'system';
}

/** Подпись группы для NDMS-имени на текущем языке. */
export function policyInterfaceGroup(ndmsName: string): string {
	return routingGroupLabel(policyInterfaceGroupId(ndmsName));
}

export function filterPolicyGlobalInterfaces(
	list: PolicyGlobalInterface[] | undefined | null,
): PolicyGlobalInterface[] {
	const catalog = list ?? [];
	const proxyNames = catalog
		.map((g) => g.name)
		.filter((n) => n.toLowerCase().startsWith('proxy'));
	return catalog.filter((g) => !shouldOmitT2sWhenProxyPresent(g.name, proxyNames));
}

export function policyInterfaceDisplayLabel(gi: PolicyGlobalInterface): string {
	const name = gi.name.trim();
	const label = (gi.label ?? '').trim();
	if (!label) return name;
	if (label.toLowerCase().includes(name.toLowerCase())) return label;
	return `${label} (${name})`;
}

export interface PolicyInterfaceGroup {
	id: RoutingGroupId;
	/** Подпись группы на момент сборки. */
	group: string;
	items: PolicyGlobalInterface[];
}

/** Unassigned policy ifaces grouped for the «Добавить» picker (HR Neo + NDMS policies). */
export function groupPolicyGlobalInterfaces(items: PolicyGlobalInterface[]): PolicyInterfaceGroup[] {
	const byGroup = new Map<RoutingGroupId, PolicyGlobalInterface[]>();
	for (const gi of items) {
		const id = policyInterfaceGroupId(gi.name);
		const bucket = byGroup.get(id) ?? [];
		bucket.push(gi);
		byGroup.set(id, bucket);
	}
	return [...byGroup.keys()]
		.sort((a, b) => groupSortIndex(a) - groupSortIndex(b))
		.map((id) => ({ id, group: routingGroupLabel(id), items: byGroup.get(id) ?? [] }));
}

/** Human-readable option label with kernel/NDMS iface suffix (like sing-box outbound dropdown). */
export function routingTunnelLabel(t: RoutingTunnel): string {
	const iface = t.iface?.trim();
	let label = iface ? `${t.name} (${iface})` : t.name;
	if (t.type === 'system' && t.status === 'down') label = m.routing_tunnel_label_down({ label });
	if (t.warning) label += ` (${t.warning})`;
	return label;
}

/** Resolves the stable group id for one routing catalog entry. */
export function routingTunnelGroupId(t: RoutingTunnel): RoutingGroupId {
	if (t.type === 'managed') return 'awg';
	if (t.type === 'wan') return wanGroup(t);
	return systemGroup(t);
}

/** Dropdown group label for one routing catalog entry, in the current language. */
export function routingTunnelGroup(t: RoutingTunnel): string {
	return routingGroupLabel(routingTunnelGroupId(t));
}

function systemGroup(t: RoutingTunnel): RoutingGroupId {
	const ndmsId = t.id.startsWith('system:') ? t.id.slice('system:'.length) : t.id;
	const lower = ndmsId.toLowerCase();
	if (t.server) return 'serverWg';
	if (lower.startsWith('wireguard')) return 'systemWg';
	if (lower.startsWith('proxy')) return 'proxy';
	if (lower.startsWith('opkgtun')) return 'opkgtun';
	return 'system';
}

function wanGroup(t: RoutingTunnel): RoutingGroupId {
	const kernel = (t.iface ?? t.id.replace(/^wan:/, '')).toLowerCase();
	// PPPoE, physical Ethernet, GPON, VLAN subifs → один блок «Провайдер»
	if (
		kernel.startsWith('ppp') ||
		kernel.startsWith('eth') ||
		kernel.startsWith('gpon') ||
		kernel.includes('.')
	) {
		return 'provider';
	}
	if (kernel.startsWith('lte') || kernel.startsWith('usb')) return 'lte';
	if (kernel.startsWith('apcli') || kernel.startsWith('wlan')) return 'wifi';
	return 'wan';
}

export interface BuildRoutingTunnelDropdownOptions {
	/** Only tunnels with `available` or type `wan` (WAN is always listed). */
	requireSelectable?: boolean;
	/** Include `type: wan` entries. Default true. */
	includeWan?: boolean;
	/** Extra filter applied after built-in filters. */
	filter?: (t: RoutingTunnel) => boolean;
	/** Optional first row (e.g. «— выберите —»). */
	placeholder?: DropdownOption;
}

/**
 * Builds grouped `DropdownOption[]` for NDMS / HydraRoute tunnel pickers.
 * Mirrors sing-box router outbound grouping where the catalog overlaps.
 */
export function buildRoutingTunnelDropdownOptions(
	tunnels: RoutingTunnel[] | undefined | null,
	opts: BuildRoutingTunnelDropdownOptions = {},
): DropdownOption[] {
	const { requireSelectable = false, includeWan = true, filter, placeholder } = opts;

	const catalog = tunnels ?? [];
	let list = catalog.filter((t) => !shouldOmitSingboxProxyKernelDuplicate(t, catalog));
	if (requireSelectable) {
		list = list.filter((t) => t.available || t.type === 'wan');
	}
	if (!includeWan) {
		list = list.filter((t) => t.type !== 'wan');
	}
	if (filter) {
		list = list.filter(filter);
	}

	const byGroup = new Map<RoutingGroupId, DropdownOption[]>();
	for (const t of list) {
		const id = routingTunnelGroupId(t);
		const bucket = byGroup.get(id) ?? [];
		bucket.push({ value: t.id, label: routingTunnelLabel(t), group: routingGroupLabel(id) });
		byGroup.set(id, bucket);
	}

	const sortedGroups = [...byGroup.keys()].sort((a, b) => groupSortIndex(a) - groupSortIndex(b));

	const options: DropdownOption[] = [];
	if (placeholder) options.push(placeholder);
	for (const g of sortedGroups) {
		options.push(...(byGroup.get(g) ?? []));
	}
	return options;
}

/** Label for a tunnel id in route chains / cards; falls back to raw id. */
export function findRoutingTunnelLabel(tunnels: RoutingTunnel[], tunnelId: string): string {
	const t = tunnels.find((x) => x.id === tunnelId);
	return t ? routingTunnelLabel(t) : tunnelId;
}

function managedTunnelListLabel(t: TunnelListItem): string {
	const name = t.name || t.id;
	const suffix = t.endpoint || t.interfaceName || t.id;
	return `${name} · ${suffix}`;
}

/**
 * Dropdown options for AWG config editor — all managed tunnels from /tunnels/all.
 * Unlike the routing catalog, includes stopped tunnels that are not yet routable.
 */
export function buildManagedTunnelListDropdownOptions(
	tunnels: TunnelListItem[] | undefined | null,
): DropdownOption[] {
	return (tunnels ?? [])
		.filter((t) => t.id && t.type !== 'singbox')
		.map((t) => ({
			value: t.id,
			label: managedTunnelListLabel(t),
			group: routingGroupLabel('awg'),
		}));
}

/** Dropdown options for AWG (managed) tunnels only. */
export function buildAwgTunnelDropdownOptions(
	tunnels: RoutingTunnel[] | undefined | null,
): DropdownOption[] {
	return buildRoutingTunnelDropdownOptions(tunnels, {
		includeWan: false,
		filter: (t) => t.type === 'managed',
	}).map((opt) => {
		const t = (tunnels ?? []).find((x) => x.id === opt.value);
		if (!t) return opt;
		return { ...opt, disabled: !t.available };
	});
}
