import type { SingboxRouterDNSRule } from '$lib/types';

export interface DnsRuleTarget {
	label: string;
	/** route = DNS server detour; block = reject/drop/predefined; none = unset */
	kind: 'route' | 'block' | 'none';
}

/**
 * Maps a DNS rule to its target label. Mirrors DNSRuleEditModal's build logic:
 *   action 'route'                  → server tag
 *   action 'reject' + method 'drop' → DROP
 *   action 'reject' (default)       → REFUSED
 *   action 'predefined'             → rcode (e.g. NXDOMAIN)
 * A legacy rule with only `server` set and no action is treated as route.
 *
 * Without this, block rules (reject/predefined carry no `server`) rendered as
 * a bare "—", hiding the REFUSED/DROP/NXDOMAIN action from the user.
 */
export function dnsRuleTarget(r: SingboxRouterDNSRule): DnsRuleTarget {
	if (r.action === 'reject') {
		return { kind: 'block', label: r.method === 'drop' ? 'DROP' : 'REFUSED' };
	}
	if (r.action === 'predefined') {
		return { kind: 'block', label: (r.rcode || 'PREDEFINED').toUpperCase() };
	}
	if (r.server) {
		return { kind: 'route', label: r.server };
	}
	return { kind: 'none', label: '—' };
}
