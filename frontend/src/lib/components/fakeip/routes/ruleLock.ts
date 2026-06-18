import type { SingboxRouterRule } from '$lib/types';

/**
 * Returns true for rules that are protected infrastructure rules in fakeip mode
 * and must not be reordered or deleted:
 *   - hijack-dns  — intercepts DNS port-53 traffic; removing it breaks DNS routing
 *   - sniff       — protocol sniffer; must come before any match-by-domain rule
 *   - LAN direct  — ip_is_private + outbound=direct; prevents VPN loop for LAN
 *
 * This is the same predicate as `isSystemRule` in sb-router's simpleRule.ts, but
 * extracted here so fakeip RoutesTab tests can import it without importing the
 * full sb-router adapter tree.
 */
export function isSystemRouteRule(rule: SingboxRouterRule): boolean {
	if (rule.action === 'sniff' || rule.action === 'hijack-dns') return true;
	if (rule.ip_is_private && rule.outbound === 'direct') return true;
	return false;
}
