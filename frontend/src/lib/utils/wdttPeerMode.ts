import type { WdttClientConfig } from '$lib/types';

type ConnMode = 'wg' | 'raw';

function modeOf(c: WdttClientConfig): ConnMode {
	return c.connMode === 'raw' ? 'raw' : 'wg';
}

/** Seed peerWg/peerRaw from legacy single peer field. */
export function hydratePeerSlots(c: WdttClientConfig): void {
	const peer = c.peer?.trim() ?? '';
	if (!peer) return;
	if (modeOf(c) === 'raw') {
		if (!c.peerRaw?.trim()) c.peerRaw = peer;
	} else if (!c.peerWg?.trim()) {
		c.peerWg = peer;
	}
	if (!c.peerWg?.trim() && modeOf(c) === 'wg') c.peerWg = peer;
	if (!c.peerRaw?.trim() && modeOf(c) === 'raw') c.peerRaw = peer;
}

/** Active peer for the current connMode (falls back to legacy peer). */
export function activePeerForMode(c: WdttClientConfig): string {
	if (modeOf(c) === 'raw') return c.peerRaw?.trim() || c.peer.trim();
	return c.peerWg?.trim() || c.peer.trim();
}

/** Copy mode-specific peer into client.peer before save/start. */
export function syncActivePeer(c: WdttClientConfig): void {
	c.peer = activePeerForMode(c);
}

/** Switch WG/Raw and restore the peer saved for each mode. */
export function switchConnMode(c: WdttClientConfig, next: ConnMode): void {
	hydratePeerSlots(c);
	const prev = modeOf(c);
	if (prev === 'wg') c.peerWg = activePeerForMode(c);
	else c.peerRaw = activePeerForMode(c);
	c.connMode = next;
	c.peer = activePeerForMode(c);
}

export function setPeerWg(c: WdttClientConfig, value: string): void {
	c.peerWg = value;
	if (modeOf(c) === 'wg') c.peer = value;
}

export function setPeerRaw(c: WdttClientConfig, value: string): void {
	c.peerRaw = value;
	if (modeOf(c) === 'raw') c.peer = value;
}
