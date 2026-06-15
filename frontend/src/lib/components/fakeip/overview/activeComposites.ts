// Pure model for the «Активные выборы (composite)» card (FE-spec §5.1).
//
// SOURCE & HONESTY (§2): the active member of every selector/urltest/loadbalance
// group comes from the live Clash snapshot (singboxRouterListProxies → proxy
// `now`), resolved through the shared sb-router helper
// resolveCompositeOutboundView (clash `now` → subscription activeMember → first
// member). We reuse that helper rather than re-deriving the active-member rule.
// No throughput / latency is shown here — just «какой участник активен сейчас».

import type {
	SingboxProxyGroup,
	SingboxRouterOutbound,
	Subscription,
} from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import { resolveCompositeOutboundView } from '$lib/components/sb-router/compositeOutboundDisplay';

export interface ActiveCompositeRow {
	/** Composite outbound tag (stable key). */
	tag: string;
	/** Human group title (subscription label or composite display title). */
	groupTitle: string;
	/** selector | urltest | loadbalance. */
	compositeType: string;
	/** Resolved label of the currently-active member. */
	activeMemberLabel: string;
	/** How many other members the group holds (for a «+N» hint). */
	otherCount: number;
}

export interface ActiveCompositesInput {
	outbounds: SingboxRouterOutbound[];
	outboundOptions: OutboundGroup[];
	subscriptions: Subscription[] | null | undefined;
	proxyGroups: SingboxProxyGroup[];
}

/**
 * Build one row per composite outbound, each carrying its active member as
 * resolved by the shared sb-router view helper. Iterates the configured
 * outbounds (config truth) and enriches with the live proxy snapshot; composite
 * outbounds with no members are skipped (helper returns null).
 */
export function activeCompositeRows(input: ActiveCompositesInput): ActiveCompositeRow[] {
	const rows: ActiveCompositeRow[] = [];
	for (const ob of input.outbounds) {
		const view = resolveCompositeOutboundView(
			ob.tag,
			input.outbounds,
			input.outboundOptions,
			input.subscriptions,
			input.proxyGroups,
		);
		if (!view) continue;
		rows.push({
			tag: ob.tag,
			groupTitle: view.groupTitle,
			compositeType: view.compositeType,
			activeMemberLabel: view.activeMemberLabel,
			otherCount: view.otherMemberTags.length,
		});
	}
	return rows;
}
