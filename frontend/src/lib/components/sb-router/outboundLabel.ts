import type { SingboxRouterOutbound, Subscription } from '$lib/types';

export interface OutboundDisplay {
	title: string;
	subtitle: string;
}

function typeSubtitle(o: SingboxRouterOutbound): string {
	if (o.type === 'direct') return o.bind_interface ? `direct · → ${o.bind_interface}` : 'direct';
	return o.type; // selector / urltest / loadbalance
}

/**
 * Resolves how a composite outbound is shown in the outbounds list.
 *
 * Subscription-sourced composites carry a generated `sub-<hash>` tag; the
 * human name lives on the Subscription (matched by selectorTag === o.tag).
 * Show that name instead of the raw tag so the outbounds list matches the
 * subscriptions section ("Veesp LV", not "sub-1a98d416"). Same mapping the
 * dropdown already does via buildOutboundOptions.
 */
export function outboundDisplay(
	o: SingboxRouterOutbound,
	subscriptions: Subscription[] | undefined | null,
): OutboundDisplay {
	if (o.source === 'subscription') {
		const sub = subscriptions?.find((s) => s.selectorTag === o.tag);
		return { title: sub?.label || o.tag, subtitle: 'подписка' };
	}
	return { title: o.tag, subtitle: typeSubtitle(o) };
}
