import type { ManagedPeer, ManagedPeerStats } from '$lib/types';
import { formatBytes, formatRelativeTime } from '$lib/utils/format';

export type PeerStatus = 'online' | 'offline' | 'disabled';

export interface PeerRowVM {
	publicKey: string;
	name: string;
	enabled: boolean;
	status: PeerStatus;
	ip: string;            // tunnelIP без /32 (показ и копирование)
	endpointHost: string;  // host без порта; '—' если отсутствует
	rx: string;
	tx: string;
	handshake: { main: string; suffix?: string } | null;
}

/** Общий контракт пропсов для desktop-строки и mobile-карточки клиента. */
export interface PeerRowProps {
	peer: ManagedPeer;
	vm: PeerRowVM;
	showToggle: boolean;
	showDownload: boolean;
	showActions: boolean;
	toggling: boolean;
	onToggle: (peer: ManagedPeer) => void;
	onConf: (peer: ManagedPeer) => void;
	onEdit: (peer: ManagedPeer) => void;
	onDelete: (peer: ManagedPeer) => void;
	onCopy: (value: string, label: string) => void;
}

export const STATUS_LABEL: Record<PeerStatus, string> = {
	online: 'ONLINE',
	offline: 'OFFLINE',
	disabled: 'OFF',
};

/** Срезает только host-маску /32. Прочие маски и голый ip не трогает. */
export function stripHostMask(ip: string): string {
	return ip.endsWith('/32') ? ip.slice(0, -'/32'.length) : ip;
}

/** Host из endpoint без порта; '—' если пусто/'-'. IPv6 без порта остаётся как есть. */
export function endpointHost(endpoint: string | undefined): string {
	const t = (endpoint ?? '').trim();
	if (!t || t === '-') return '—';
	const bracket = /^(\[[^\]]+\]):\d+$/.exec(t);
	if (bracket) return bracket[1];
	const lastColon = t.lastIndexOf(':');
	if (lastColon <= 0) return t;
	const host = t.slice(0, lastColon);
	const port = t.slice(lastColon + 1);
	if (!/^\d+$/.test(port) || host.includes(':')) return t;
	return host;
}

export function peerStatus(enabled: boolean, online: boolean | null | undefined): PeerStatus {
	if (!enabled) return 'disabled';
	return online ? 'online' : 'offline';
}

export function splitHandshake(value: string): { main: string; suffix?: string } {
	const t = value.trim();
	if (t.endsWith(' назад')) return { main: t.slice(0, -' назад'.length), suffix: 'назад' };
	return { main: t };
}

export function buildPeerRowVM(peer: ManagedPeer, stats: ManagedPeerStats | undefined): PeerRowVM {
	return {
		publicKey: peer.publicKey,
		name: peer.description || `${peer.publicKey.slice(0, 8)}...`,
		enabled: peer.enabled,
		status: peerStatus(peer.enabled, stats?.online),
		ip: stripHostMask(peer.tunnelIP),
		endpointHost: endpointHost(stats?.endpoint),
		rx: formatBytes(stats?.rxBytes ?? 0),
		tx: formatBytes(stats?.txBytes ?? 0),
		handshake: stats?.lastHandshake ? splitHandshake(formatRelativeTime(stats.lastHandshake)) : null,
	};
}
