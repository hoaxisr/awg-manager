// Pure model for the FakeIP «Обзор» hero summary (FE-spec §5.1).
// Side-effect-free so the count derivation can be unit-tested without mounting
// the Svelte component.
//
// HONESTY (§2/§4): every count here has a real config source —
//   - rule_sets / DNS rules / DNS servers / outbounds → config counts from the
//     Status DTO and the router store's dns sub-stores.
//   - `final` → the configured default egress (Status.final).
// These are CONFIG counts (honest regardless of engine running-state). The hero
// is a summary line, NOT a duplicate of the chip-strip counters (FE-spec §5.1:
// «без дублирования чип-счётчиков»).

import type { SingboxRouterStatus } from '$lib/types';

export interface OverviewSummaryInput {
	/** Live Status DTO (singboxRouter.status). May be null during cold-load. */
	status: SingboxRouterStatus | null;
	/** Length of singboxRouter.dnsServers. */
	dnsServerCount: number;
	/** Length of singboxRouter.dnsRules. */
	dnsRuleCount: number;
}

export interface OverviewSummary {
	ruleSetCount: number;
	dnsServerCount: number;
	dnsRuleCount: number;
	/** Atomic AWG/tunnel outbounds (Status.outboundAwgCount). */
	outboundAtomicCount: number;
	/** Composite (selector/urltest/loadbalance) outbounds (Status.outboundCompositeCount). */
	outboundCompositeCount: number;
	/** Atomic + composite. */
	outboundTotalCount: number;
	/** Configured default egress (Status.final); '' when unknown. */
	final: string;
}

/**
 * Build the hero-summary counts from the live Status DTO plus the DNS
 * sub-store lengths. Defensive against a null Status (cold-load) → zeros.
 */
export function overviewSummary(input: OverviewSummaryInput): OverviewSummary {
	const st = input.status;
	const atomic = st?.outboundAwgCount ?? 0;
	const composite = st?.outboundCompositeCount ?? 0;
	return {
		ruleSetCount: st?.ruleSetCount ?? 0,
		dnsServerCount: input.dnsServerCount,
		dnsRuleCount: input.dnsRuleCount,
		outboundAtomicCount: atomic,
		outboundCompositeCount: composite,
		outboundTotalCount: atomic + composite,
		final: st?.final ?? '',
	};
}
