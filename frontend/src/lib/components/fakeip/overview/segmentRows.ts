// Pure row-model for the FakeIP «Доставка DNS · сегменты» list (FE-spec §5.1 /
// 2.2). Side-effect-free so the mapping can be unit-tested without mounting the
// Svelte component.
//
// HONESTY (§2): inFakeip is the ground-truth from the backend (the pool's
// DHCP dns-server == fakeip-tun .2). We do NOT fabricate a "held until egress
// healthy" status — the backend egress-health field is task #25 and absent
// today. Delivery to clients additionally depends on the engine running +
// healthy; the component surfaces that as a plain note, not a per-row green.

import type { FakeIPSegment } from '$lib/types';

export interface SegmentRow {
	/** Stable key for keyed #each (the pool name is unique per router). */
	key: string;
	/** DHCP pool name, e.g. "_WEBADMIN". */
	pool: string;
	/** Pool subnet CIDR, e.g. "192.168.0.1/24". */
	subnet: string;
	/**
	 * Ground-truth: the pool already advertises the fakeip-tun DNS (.2 of the
	 * tun /30). Drives the controlled Toggle.
	 */
	inFakeip: boolean;
	/** DHCP-advertised DNS, or "" when the pool has no explicit dns-server. */
	dnsServer: string;
}

/**
 * Map the backend segment DTOs to display rows. Order is preserved (the backend
 * already sorts by pool name). dnsServer is normalized to "" when absent so the
 * view never renders `undefined`.
 */
export function segmentRows(segments: FakeIPSegment[]): SegmentRow[] {
	return segments.map((s) => ({
		key: s.pool,
		pool: s.pool,
		subnet: s.subnet,
		inFakeip: s.inFakeip,
		dnsServer: s.dnsServer ?? '',
	}));
}
