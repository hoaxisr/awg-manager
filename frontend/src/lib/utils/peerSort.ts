export type PeerSortKey = 'name' | 'traffic' | 'ip' | 'endpoint' | 'online' | 'handshake';
export type PeerAriaSort = 'ascending' | 'descending' | 'none';

export const PEER_SORT_DEFAULTS: Record<PeerSortKey, boolean> = {
	name: true,       // A→Z
	traffic: false,   // most first
	ip: true,         // low→high
	endpoint: true,   // A→Z
	online: false,    // online first
	handshake: false, // recent first
};

export interface PeerSortFields {
	name: string;
	ip: string;
	endpoint: string;
	rxBytes: number | null;
	txBytes: number | null;
	online: boolean | null;
	lastHandshake: string | null;
}

export interface PeerSortStateLike {
	sortBy: PeerSortKey;
	sortAsc: boolean;
}

export function peerAriaSort(state: PeerSortStateLike, key: PeerSortKey): PeerAriaSort {
	if (state.sortBy !== key) return 'none';
	return state.sortAsc ? 'ascending' : 'descending';
}

function parseHandshakeOrNull(v: string | null): number | null {
	if (!v) return null;
	const ts = new Date(v).getTime();
	return Number.isFinite(ts) ? ts : null;
}

function compareNullableNumbers(a: number | null, b: number | null, sortAsc: boolean): number {
	// Missing values are always last, regardless of direction.
	if (a === null && b === null) return 0;
	if (a === null) return 1;
	if (b === null) return -1;
	return sortAsc ? a - b : b - a;
}

function normalizeEndpointSortValue(endpoint: string): string | null {
	const trimmed = endpoint.trim();
	if (!trimmed || trimmed === '-') return null;
	return trimmed.toLowerCase();
}

function compareNullableStrings(a: string | null, b: string | null, sortAsc: boolean): number {
	// Missing values are always last, regardless of direction.
	if (a === null && b === null) return 0;
	if (a === null) return 1;
	if (b === null) return -1;
	const cmp = a.localeCompare(b);
	return sortAsc ? cmp : -cmp;
}

export function parseIPv4(ip: string): number {
	const bare = ip.split('/')[0] ?? '';
	const parts = bare.split('.').map((s) => {
		const n = Number(s);
		return Number.isFinite(n) && n >= 0 && n <= 255 ? n : 0;
	});
	return (parts[0] ?? 0) * 0x1000000 + (parts[1] ?? 0) * 0x10000 + (parts[2] ?? 0) * 0x100 + (parts[3] ?? 0);
}

// Base field comparator (direction-agnostic). For UI sorting, always use
// comparePeerFieldsDirected(...) so direction and "missing values last"
// behavior are applied consistently.
function comparePeerFields(a: PeerSortFields, b: PeerSortFields, sortBy: PeerSortKey): number {
	switch (sortBy) {
		case 'name':
			return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
		case 'ip':
			return parseIPv4(a.ip) - parseIPv4(b.ip);
		case 'endpoint': {
			const ea = normalizeEndpointSortValue(a.endpoint);
			const eb = normalizeEndpointSortValue(b.endpoint);
			if (ea === null && eb === null) return 0;
			if (ea === null) return 1;
			if (eb === null) return -1;
			return ea.localeCompare(eb);
		}
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
			const ha = parseHandshakeOrNull(a.lastHandshake);
			const hb = parseHandshakeOrNull(b.lastHandshake);
			if (ha === null && hb === null) return 0;
			if (ha === null) return 1;
			if (hb === null) return -1;
			return ha - hb;
		}
	}
}

export function comparePeerFieldsDirected(
	a: PeerSortFields,
	b: PeerSortFields,
	sortBy: PeerSortKey,
	sortAsc: boolean,
): number {
	if (sortBy === 'traffic') {
		const ta = a.rxBytes !== null && a.txBytes !== null ? a.rxBytes + a.txBytes : null;
		const tb = b.rxBytes !== null && b.txBytes !== null ? b.rxBytes + b.txBytes : null;
		return compareNullableNumbers(ta, tb, sortAsc);
	}
	if (sortBy === 'endpoint') {
		const ea = normalizeEndpointSortValue(a.endpoint);
		const eb = normalizeEndpointSortValue(b.endpoint);
		return compareNullableStrings(ea, eb, sortAsc);
	}
	if (sortBy === 'online') {
		const oa = a.online === null ? null : (a.online ? 1 : 0);
		const ob = b.online === null ? null : (b.online ? 1 : 0);
		return compareNullableNumbers(oa, ob, sortAsc);
	}
	if (sortBy === 'handshake') {
		const ha = parseHandshakeOrNull(a.lastHandshake);
		const hb = parseHandshakeOrNull(b.lastHandshake);
		return compareNullableNumbers(ha, hb, sortAsc);
	}
	const cmp = comparePeerFields(a, b, sortBy);
	return sortAsc ? cmp : -cmp;
}
