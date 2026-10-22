import type { SingboxProxyGroup, SingboxRouterOutbound, Subscription } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import { resolveMemberLabel } from '$lib/utils/memberLabel';
import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
import { outboundDisplay as compositeTitle } from './outboundLabel';

export const COMPOSITE_OUTBOUND_TYPES = new Set(['selector', 'urltest', 'loadbalance', 'fallback']);

export type CompositeOutboundType = 'selector' | 'urltest' | 'loadbalance' | 'fallback';

export interface CompositeOutboundView {
	compositeType: CompositeOutboundType;
	groupTitle: string;
	isSubscription: boolean;
	activeMemberTag: string;
	activeMemberLabel: string;
	otherMemberTags: string[];
	otherMemberLabels: string[];
}

function memberTagsFor(
	ob: SingboxRouterOutbound | undefined,
	subscriptions: Subscription[] | null | undefined,
	tag: string,
): string[] {
	if (ob?.outbounds?.length) return ob.outbounds;
	const sub = subscriptions?.find((s) => s.selectorTag === tag);
	if (sub?.memberTags?.length) return sub.memberTags;
	return sub?.members?.map((m) => m.tag).filter(Boolean) ?? [];
}

function subscriptionCompositeType(sub: Subscription): CompositeOutboundType {
	if (sub.mode === 'fallback') return 'fallback';
	return sub.mode === 'urltest' ? 'urltest' : 'selector';
}

/** Активный участник: clash `now` → подписка activeMember → первый в списке. */
export function resolveCompositeActiveMemberTag(
	tag: string,
	members: string[],
	proxyGroups: SingboxProxyGroup[],
	subscription?: Subscription | null,
): string {
	const clashNow = proxyGroups.find((g) => g.tag === tag)?.now;
	if (clashNow && members.includes(clashNow)) return clashNow;
	if (subscription) {
		return resolveSubscriptionMemberTag(subscription, clashNow && members.includes(clashNow) ? clashNow : null);
	}
	return members[0] ?? '';
}

/**
 * Composite для простого режима: один активный туннель + остальные в +N.
 * Возвращает null, если тег не composite или нет участников.
 */
export function resolveCompositeOutboundView(
	tag: string,
	outbounds: SingboxRouterOutbound[],
	outboundOptions: OutboundGroup[],
	subscriptions: Subscription[] | null | undefined,
	proxyGroups: SingboxProxyGroup[] = [],
): CompositeOutboundView | null {
	const ob = outbounds.find((o) => o.tag === tag);

	if (ob && !COMPOSITE_OUTBOUND_TYPES.has(ob.type)) return null;

	const sub = subscriptions?.find((s) => s.selectorTag === tag);

	if (ob && COMPOSITE_OUTBOUND_TYPES.has(ob.type)) {
		const members = memberTagsFor(ob, subscriptions, tag);
		if (members.length === 0) return null;
		const activeTag = resolveCompositeActiveMemberTag(tag, members, proxyGroups, sub);
		const otherTags = members.filter((t) => t !== activeTag);
		return {
			compositeType: ob.type as CompositeOutboundType,
			groupTitle: sub?.label || compositeTitle(ob, subscriptions).title,
			isSubscription: !!sub || ob.source === 'subscription',
			activeMemberTag: activeTag,
			activeMemberLabel: resolveMemberLabel(activeTag, subscriptions, outboundOptions),
			otherMemberTags: otherTags,
			otherMemberLabels: otherTags.map((t) => resolveMemberLabel(t, subscriptions, outboundOptions)),
		};
	}

	if (!sub) return null;

	const members = memberTagsFor(undefined, subscriptions, tag);
	if (members.length === 0) return null;

	const activeTag = resolveCompositeActiveMemberTag(tag, members, proxyGroups, sub);
	const otherTags = members.filter((t) => t !== activeTag);

	return {
		compositeType: subscriptionCompositeType(sub),
		groupTitle: sub.label || tag,
		isSubscription: true,
		activeMemberTag: activeTag,
		activeMemberLabel: resolveMemberLabel(activeTag, subscriptions, outboundOptions),
		otherMemberTags: otherTags,
		otherMemberLabels: otherTags.map((t) => resolveMemberLabel(t, subscriptions, outboundOptions)),
	};
}
