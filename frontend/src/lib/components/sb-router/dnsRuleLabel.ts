import type { SingboxRouterDNSRule } from '$lib/types';

export interface DnsRuleTarget {
	label: string;
	/**
	 * route = DNS server detour; block = reject/drop/predefined;
	 * evaluate = промежуточный запрос (не терминирует цепочку, sing-box 1.14);
	 * respond = собственный ответ по match_response; none = unset
	 */
	kind: 'route' | 'block' | 'evaluate' | 'respond' | 'none';
}

/**
 * Maps a DNS rule to its target label. Mirrors DNSRuleEditModal's build logic:
 *   action 'route'                  → server tag
 *   action 'reject' + method 'drop' → DROP
 *   action 'reject' (default)       → REFUSED
 *   action 'predefined'             → rcode (e.g. NXDOMAIN)
 *   action 'evaluate'               → server + tag (sing-box 1.14)
 *   action 'respond'                → собственный ответ (sing-box 1.14)
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
	if (r.action === 'evaluate') {
		const tag = r.tag ? ` (${r.tag})` : '';
		return { label: `evaluate → ${r.server ?? '—'}${tag}`, kind: 'evaluate' };
	}
	if (r.action === 'respond') {
		return { label: 'respond — вернуть ответ', kind: 'respond' };
	}
	if (r.server) {
		return { kind: 'route', label: r.server };
	}
	return { kind: 'none', label: '—' };
}
