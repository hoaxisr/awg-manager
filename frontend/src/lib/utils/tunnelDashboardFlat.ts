import type {
	Awg3Tunnel,
	ExternalTunnel,
	Subscription,
	SubscriptionMember,
	SingboxTunnel,
	SystemTunnel,
	TunnelListItem,
} from '$lib/types';
import { stripFircEmojis, type FircTunnel } from '$lib/types/firc';
import { compareString } from '$lib/utils/tunnelTableSort';

export type SubscriptionActiveCard = {
	subscription: Subscription;
	activeMember: SubscriptionMember;
};

export type TunnelDashboardFlatItem =
	| { kind: 'awg-managed'; key: string; name: string; tunnel: TunnelListItem; index: number }
	| { kind: 'awg-system'; key: string; name: string; tunnel: SystemTunnel }
	| { kind: 'awg-external'; key: string; name: string; tunnel: ExternalTunnel }
	| { kind: 'awg3'; key: string; name: string; tunnel: Awg3Tunnel; index: number }
	| { kind: 'firc-proxy'; key: string; name: string; tunnel: FircTunnel; index: number }
	| { kind: 'singbox'; key: string; name: string; tunnel: SingboxTunnel; index: number }
	| { kind: 'firc-subscription'; key: string; name: string; tunnel: FircTunnel; index: number }
	| { kind: 'sub-active'; key: string; name: string; card: SubscriptionActiveCard; index: number }
	| { kind: 'sub-stopped'; key: string; name: string; subscription: Subscription };

/** Flat dashboard interleave order (alphabetical within each group). */
const FLAT_DASHBOARD_KIND_ORDER: Record<TunnelDashboardFlatItem['kind'], number> = {
	'awg-managed': 0,
	'awg-system': 1,
	'awg-external': 2,
	awg3: 3,
	'firc-proxy': 4,
	singbox: 5,
	'firc-subscription': 6,
	'sub-active': 7,
	'sub-stopped': 8,
};

function compareFlatDashboardItems(a: TunnelDashboardFlatItem, b: TunnelDashboardFlatItem): number {
	const byKind = FLAT_DASHBOARD_KIND_ORDER[a.kind] - FLAT_DASHBOARD_KIND_ORDER[b.kind];
	if (byKind !== 0) return byKind;
	return compareString(a.name, b.name);
}

export function buildFlatDashboardItems(input: {
	awg: TunnelListItem[];
	system: SystemTunnel[];
	external: ExternalTunnel[];
	awg3: Awg3Tunnel[];
	fircProxies?: FircTunnel[];
	singbox: SingboxTunnel[];
	fircSubscriptions?: FircTunnel[];
	subscriptionsActive: SubscriptionActiveCard[];
	subscriptionsStopped: Subscription[];
}): TunnelDashboardFlatItem[] {
	const items: TunnelDashboardFlatItem[] = [];

	input.awg.forEach((tunnel, index) => {
		items.push({
			kind: 'awg-managed',
			key: `awg:${tunnel.id}`,
			name: tunnel.name,
			tunnel,
			index,
		});
	});

	for (const tunnel of input.system) {
		items.push({
			kind: 'awg-system',
			key: `system:${tunnel.id}`,
			name: tunnel.description || tunnel.interfaceName || tunnel.id,
			tunnel,
		});
	}

	for (const tunnel of input.external) {
		items.push({
			kind: 'awg-external',
			key: `external:${tunnel.interfaceName}`,
			name: tunnel.interfaceName,
			tunnel,
		});
	}

	input.awg3.forEach((tunnel, index) => {
		items.push({
			kind: 'awg3',
			key: `awg3:${tunnel.id}`,
			name: tunnel.tag,
			tunnel,
			index,
		});
	});

	(input.fircProxies ?? []).forEach((tunnel, index) => {
		items.push({
			kind: 'firc-proxy',
			key: `firc-proxy:${tunnel.id}`,
			name: stripFircEmojis(tunnel.description || tunnel.id) || tunnel.device || tunnel.id,
			tunnel,
			index,
		});
	});

	input.singbox.forEach((tunnel, index) => {
		items.push({
			kind: 'singbox',
			key: `singbox:${tunnel.tag}`,
			name: tunnel.tag,
			tunnel,
			index,
		});
	});

	(input.fircSubscriptions ?? []).forEach((tunnel, index) => {
		items.push({
			kind: 'firc-subscription',
			key: `firc-subscription:${tunnel.id}`,
			name: stripFircEmojis(tunnel.description || tunnel.id) || tunnel.device || tunnel.id,
			tunnel,
			index,
		});
	});

	input.subscriptionsActive.forEach((card, index) => {
		items.push({
			kind: 'sub-active',
			// Единый ключ активной/остановленной подписки: персонализация
			// (порядок, теги) переживает старт/стоп.
			key: `sub:${card.subscription.id}`,
			name: card.subscription.label,
			card,
			index,
		});
	});

	for (const subscription of input.subscriptionsStopped) {
		items.push({
			kind: 'sub-stopped',
			key: `sub:${subscription.id}`,
			name: subscription.label,
			subscription,
		});
	}

	return items.sort(compareFlatDashboardItems);
}
