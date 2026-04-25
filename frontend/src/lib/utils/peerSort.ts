export type PeerSortKey = 'name' | 'traffic' | 'ip' | 'online' | 'handshake';

export const PEER_SORT_DEFAULTS: Record<PeerSortKey, boolean> = {
	name: true,       // A→Z
	traffic: false,   // most first
	ip: true,         // low→high
	online: false,    // online first
	handshake: false, // recent first
};

export interface PeerSortFields {
	name: string;
	ip: string;
	rxBytes: number | null;
	txBytes: number | null;
	online: boolean | null;
	lastHandshake: string | null;
}

export function parseIPv4(ip: string): number {
	const parts = ip.split('.').map(Number);
	return ((parts[0] ?? 0) << 24) + ((parts[1] ?? 0) << 16) + ((parts[2] ?? 0) << 8) + (parts[3] ?? 0);
}

export function comparePeerFields(a: PeerSortFields, b: PeerSortFields, sortBy: PeerSortKey): number {
	switch (sortBy) {
		case 'name':
			return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
		case 'ip':
			return parseIPv4(a.ip) - parseIPv4(b.ip);
		case 'traffic': {
			const ta = a.rxBytes !== null && a.txBytes !== null ? a.rxBytes + a.txBytes : -1;
			const tb = b.rxBytes !== null && b.txBytes !== null ? b.rxBytes + b.txBytes : -1;
			if (ta === -1 && tb === -1) return 0;
			if (ta === -1) return 1;
			if (tb === -1) return -1;
			return ta - tb;
		}
		case 'online': {
			if (a.online === null && b.online === null) return 0;
			if (a.online === null) return 1;
			if (b.online === null) return -1;
			return (a.online ? 1 : 0) - (b.online ? 1 : 0);
		}
		case 'handshake': {
			const ha = a.lastHandshake ? new Date(a.lastHandshake).getTime() : -1;
			const hb = b.lastHandshake ? new Date(b.lastHandshake).getTime() : -1;
			if (ha === -1 && hb === -1) return 0;
			if (ha === -1) return 1;
			if (hb === -1) return -1;
			return ha - hb;
		}
	}
}
