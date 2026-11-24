// Пункты рейла страницы «Серверы»: managed, системные и три встроенных
// сервиса (VLESS, Telegram, диспетчер CDN). Чистая сборка из снимков — сама
// страница держит загрузку и выбор активного пункта.
import { m } from '$lib/i18n';
import type {
	ManagedServer,
	ManagedServerStats,
	WireguardServer,
	XrayStatus,
	TgWebProxyStatus,
	CDNDispatcherConfig,
} from '$lib/types';
import type { RailItem } from './ServerRail.svelte';
import { EXTERNAL_ACCESS_LABELS } from './external-access/connectionLabels';
import { dedupBy } from '$lib/utils/dedupBy';
import { countActiveManagedPeers, countActiveSystemPeers } from '$lib/utils/serverPeerActivity';
import { systemServerIsUp } from '$lib/utils/systemServerState';
import { browser } from '$app/environment';

// Format: '__managed__:Wireguard5'. Prefix lets us distinguish managed
// rail items from system server ids without an extra `kind` lookup.
export const MANAGED_PREFIX = '__managed__:';

export function managedRailId(iface: string): string {
	return MANAGED_PREFIX + iface;
}

/** System servers in the rail are built-in VPN Server or user-marked interfaces only. */
export function isMarkedSystemServer(server: { builtIn?: boolean; description: string }): boolean {
	return !(server.builtIn ?? server.description === 'Wireguard VPN Server');
}

/** Последний выбранный пункт рейла — переживает перезагрузку страницы. */
const ACTIVE_SERVER_STORAGE_KEY = 'awgm:servers:activeId';

export function readStoredActiveId(): string {
	if (!browser) return '';

	try {
		return localStorage.getItem(ACTIVE_SERVER_STORAGE_KEY) ?? '';
	} catch {
		return '';
	}
}

export function persistActiveId(id: string) {
	if (!browser) return;

	try {
		if (id) {
			localStorage.setItem(ACTIVE_SERVER_STORAGE_KEY, id);
		} else {
			localStorage.removeItem(ACTIVE_SERVER_STORAGE_KEY);
		}
	} catch {
		// localStorage can be unavailable; ignore and keep in-memory selection.
	}
}

export function buildRailItems(
	managedServers: ManagedServer[],
	managedStatsMap: Record<string, ManagedServerStats>,
	serverList: WireguardServer[],
	xrayStatus: XrayStatus | null,
	tgStatus: TgWebProxyStatus | null,
	dispatcherConfig: CDNDispatcherConfig | null,
): RailItem[] {
	const items: RailItem[] = [];
	for (const ms of managedServers) {
		const stats = managedStatsMap[ms.interfaceName] ?? null;
		const mPeers = ms.peers ?? [];
		const statsPeers = stats?.peers ?? [];
		items.push({
			id: managedRailId(ms.interfaceName),
			name: ms.description || ms.interfaceName,
			iface: ms.interfaceName,
			listenPort: ms.listenPort,
			// Backend ManagedServerStats.Status mirrors NDMS interface state
			// ("up"/"down"), not the layer-state word "running" that NDMS
			// hooks emit. Comparing against "running" never matched and
			// flagged the rail item as stopped even on healthy servers.
			status: stats?.status === 'up' ? 'running' : 'stopped',
			peerActive: countActiveManagedPeers(mPeers, statsPeers),
			peerCount: mPeers.length,
			kind: 'managed',
		});
	}
	for (const s of serverList) {
		const sPeers = s.peers ?? [];
		items.push({
			id: s.id,
			name: s.description || s.interfaceName,
			iface: s.interfaceName,
			listenPort: s.listenPort,
			status: systemServerIsUp(s) ? 'running' : 'stopped',
			peerCount: sPeers.length,
			peerActive: countActiveSystemPeers(sPeers),
			kind: 'system',
		});
	}

	const xrayClients = xrayStatus?.clients ?? [];
	const activeClients = xrayClients.filter((c) => c.enabled).length;
	items.push({
		id: '__xray_server__',
		name: EXTERNAL_ACCESS_LABELS.vlessTitle,
		iface: m.servers_rail_xray_sub(),
		listenPort: '',
		status: xrayStatus?.running ? 'running' : 'stopped',
		peerCount: xrayClients.length,
		peerActive: activeClients,
		kind: 'xray',
	});

	items.push({
		id: '__tg_webproxy__',
		name: EXTERNAL_ACCESS_LABELS.telegramTitle,
		iface: m.servers_rail_tg_sub(),
		listenPort: '',
		status: tgStatus?.running ? 'running' : 'stopped',
		kind: 'tgwebproxy',
	});

	items.push({
		id: '__dispatcher__',
		name: EXTERNAL_ACCESS_LABELS.dispatcherRailTitle,
		iface: m.servers_rail_dispatcher_sub(),
		listenPort: '',
		status: dispatcherConfig?.running ? 'running' : 'stopped',
		kind: 'dispatcher',
	});

	return dedupBy(items, (i) => i.id, { warnTag: 'server rail' });
}
