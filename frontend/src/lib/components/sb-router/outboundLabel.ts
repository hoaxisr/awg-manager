import type { SingboxRouterOutbound, Subscription, SubscriptionGroup } from '$lib/types';

export interface OutboundDisplay {
	title: string;
	subtitle: string;
}

export function isSubscriptionOutbound(
	o: SingboxRouterOutbound,
	subscriptions: Subscription[] | undefined | null = null,
): boolean {
	if (o.source === 'subscription') return true;
	return subscriptions?.some((s) => s.selectorTag === o.tag) ?? false;
}

/** #572: outbound принадлежит сводной группе подписок (matched by tag). */
export function isGroupOutbound(
	o: SingboxRouterOutbound,
	groups: SubscriptionGroup[] | undefined | null = null,
): boolean {
	return groups?.some((g) => g.tag === o.tag) ?? false;
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
	groups: SubscriptionGroup[] | undefined | null = null,
): OutboundDisplay {
	// #572: сводная группа — показываем её Label ("Все европейские"),
	// а не сырой тег "agg-xxxxxxxx".
	const group = groups?.find((g) => g.tag === o.tag);
	if (group) {
		return { title: group.label || o.tag, subtitle: typeSubtitle(o) };
	}
	if (isSubscriptionOutbound(o, subscriptions)) {
		const sub = subscriptions?.find((s) => s.selectorTag === o.tag);
		return { title: sub?.label || o.tag, subtitle: typeSubtitle(o) };
	}
	return { title: o.tag, subtitle: typeSubtitle(o) };
}
