import type { SingboxRouterDNSRule } from '$lib/types';
import { dnsMatcherParts } from './dnsMatcherParts';

/**
 * A DNS rule with zero matchers matches every query — a catch-all («всё
 * остальное»). Uses the same matcher detection as the compact rows/summary,
 * so a rule that only carries `query_type` (which DOES restrict the query) is
 * NOT treated as a catch-all.
 */
export function isCatchAllDnsRule(r: SingboxRouterDNSRule): boolean {
	return dnsMatcherParts(r).length === 0;
}

/**
 * Index of the first catch-all (matcher-less) rule, or -1 if none exists.
 * A matcher-less `evaluate` is skipped: it does not terminate the chain and
 * therefore shadows nothing (sing-box 1.14, mirrors backend
 * DNSRulesShadowedByCatchAll).
 */
export function firstCatchAllDnsRuleIndex(rules: readonly SingboxRouterDNSRule[]): number {
	return rules.findIndex((r) => r.action !== 'evaluate' && isCatchAllDnsRule(r));
}

/**
 * Indices of rules shadowed by an earlier catch-all. DNS rules are evaluated
 * first-match, top to bottom; once a matcher-less rule is reached it consumes
 * every remaining query, so any rule ordered AFTER it is dead (silently
 * ignored). Returns the set of those shadowed indices (empty if there is no
 * catch-all, or the catch-all is already the last rule).
 */
export function computeShadowedDnsRuleIndices(
	rules: readonly SingboxRouterDNSRule[],
): Set<number> {
	const shadowed = new Set<number>();
	const k = firstCatchAllDnsRuleIndex(rules);
	if (k === -1) return shadowed;
	for (let i = k + 1; i < rules.length; i++) shadowed.add(i);
	return shadowed;
}
