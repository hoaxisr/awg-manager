import type { DropdownOption } from '$lib/components/ui';
import type { PolicyGlobalInterface, RoutingTunnel, TunnelListItem } from '$lib/types';

/** Display order for grouped tunnel dropdowns (aligned with sing-box router groups). */
const GROUP_ORDER = [
	'Провайдер',
	'AWG туннели',
	'Системные WireGuard',
	'Серверы WireGuard',
	'Прокси',
	'OpkgTun',
	'Системные',
	'LTE / USB',
	'Wi‑Fi',
	'WAN',
] as const;

function groupSortIndex(group: string): number {
	const idx = (GROUP_ORDER as readonly string[]).indexOf(group);
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
export function policyInterfaceGroup(ndmsName: string): string {
	const lower = ndmsName.trim().toLowerCase();
	if (lower.startsWith('pppoe') || lower.startsWith('isp')) return 'Провайдер';
	if (lower.startsWith('wireguard')) return 'Системные WireGuard';
	if (lower.startsWith('proxy')) return 'Прокси';
	if (lower.startsWith('opkgtun') || lower.startsWith('awgm')) return 'AWG туннели';
	if (lower.startsWith('lte') || lower.startsWith('usb')) return 'LTE / USB';
	if (lower.startsWith('apcli') || lower.startsWith('wlan')) return 'Wi‑Fi';
	return 'Системные';
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

/**
 * Strips internal awg-manager ownership tokens like "[awg-manager:...]" from NDMS descriptions
 * and resolves raw "awg-manager:<engine>:<kind>:<id>" strings to human-readable names.
 */
export function cleanPolicyInterfaceLabel(
	label: string | undefined | null,
	idResolver?: (id: string) => string | undefined,
): string {
	const trimmed = (label ?? '').trim();
	if (!trimmed) return '';

	// 1. If formatted as "Friendly Name [awg-manager:...]", strip the bracketed token
	const bracketMatch = trimmed.match(/^(.*?)\s*\[awg-manager:[^\]]+\]$/);
	if (bracketMatch && bracketMatch[1].trim()) {
		return bracketMatch[1].trim();
	}

	// 2. If it's a raw canonical token: awg-manager:<engine>:<kind>:<id>
	const tokenMatch = trimmed.match(/^awg-manager:(?:mihomo|singbox):(?:proxy|subscription|group):([a-zA-Z0-9_-]+)$/);
	if (tokenMatch) {
		const id = tokenMatch[1];
		if (idResolver) {
			const resolved = idResolver(id);
			if (resolved && resolved.trim()) {
				return resolved.trim();
			}
		}
	}

	return trimmed;
}

export function policyInterfaceDisplayLabel(
	gi: PolicyGlobalInterface,
	idResolver?: (id: string) => string | undefined,
): string {
	const name = gi.name.trim();
	const clean = cleanPolicyInterfaceLabel(gi.label, idResolver);
	if (!clean) return name;
	if (clean.toLowerCase().includes(name.toLowerCase())) return clean;
	if (clean.startsWith('awg-manager:')) {
		return name;
	}
	return `${clean} (${name})`;
}

export interface PolicyInterfaceGroup {
	group: string;
	items: PolicyGlobalInterface[];
}

/** Unassigned policy ifaces grouped for the «Добавить» picker (HR Neo + NDMS policies). */
export function groupPolicyGlobalInterfaces(items: PolicyGlobalInterface[]): PolicyInterfaceGroup[] {
	const byGroup = new Map<string, PolicyGlobalInterface[]>();
	for (const gi of items) {
		const group = policyInterfaceGroup(gi.name);
		const bucket = byGroup.get(group) ?? [];
		bucket.push(gi);
		byGroup.set(group, bucket);
	}
	return [...byGroup.keys()]
		.sort((a, b) => groupSortIndex(a) - groupSortIndex(b))
		.map((group) => ({ group, items: byGroup.get(group) ?? [] }));
}

/** Human-readable option label with kernel/NDMS iface suffix (like sing-box outbound dropdown). */
export function routingTunnelLabel(t: RoutingTunnel): string {
	const iface = t.iface?.trim();
	let label = iface ? `${t.name} (${iface})` : t.name;
	if (t.type === 'system' && t.status === 'down') label += ' — нет несущей';
	if (t.warning) label += ` (${t.warning})`;
	return label;
}

/** Resolves the dropdown group for one routing catalog entry. */
export function routingTunnelGroup(t: RoutingTunnel): string {
	if (t.type === 'managed') return 'AWG туннели';
	if (t.type === 'wan') return wanGroup(t);
	return systemGroup(t);
}

function systemGroup(t: RoutingTunnel): string {
	const ndmsId = t.id.startsWith('system:') ? t.id.slice('system:'.length) : t.id;
	const lower = ndmsId.toLowerCase();
	if (t.server) return 'Серверы WireGuard';
	if (lower.startsWith('wireguard')) return 'Системные WireGuard';
	if (lower.startsWith('proxy')) return 'Прокси';
	if (lower.startsWith('opkgtun')) return 'OpkgTun';
	return 'Системные';
}

function wanGroup(t: RoutingTunnel): string {
	const kernel = (t.iface ?? t.id.replace(/^wan:/, '')).toLowerCase();
	// PPPoE, physical Ethernet, GPON, VLAN subifs → один блок «Провайдер»
	if (
		kernel.startsWith('ppp') ||
		kernel.startsWith('eth') ||
		kernel.startsWith('gpon') ||
		kernel.includes('.')
	) {
		return 'Провайдер';
	}
	if (kernel.startsWith('lte') || kernel.startsWith('usb')) return 'LTE / USB';
	if (kernel.startsWith('apcli') || kernel.startsWith('wlan')) return 'Wi‑Fi';
	return 'WAN';
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

	const byGroup = new Map<string, DropdownOption[]>();
	for (const t of list) {
		const group = routingTunnelGroup(t);
		const bucket = byGroup.get(group) ?? [];
		bucket.push({ value: t.id, label: routingTunnelLabel(t), group });
		byGroup.set(group, bucket);
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
			group: 'AWG туннели',
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
