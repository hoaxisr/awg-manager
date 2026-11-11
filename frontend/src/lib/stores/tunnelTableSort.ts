import {
	AWG_TUNNEL_SORT_DEFAULTS,
	SINGBOX_TUNNEL_SORT_DEFAULTS,
	SUBSCRIPTION_SORT_DEFAULTS,
	SUBSCRIPTION_MEMBERS_SORT_DEFAULTS,
} from '$lib/utils/tunnelTableSort';
import { cycleTableSort } from '$lib/utils/tableSort';
import { createSortStore, type SortState } from './sortStore';

export type AwgTunnelSortKey = 'name' | 'status' | 'endpoint' | 'traffic' | 'handshake';
export type SingboxTunnelSortKey = 'delay' | 'name' | 'protocol' | 'server' | 'running' | 'traffic' | 'ping';
export type SubscriptionSortKey = 'delay' | 'label' | 'mode' | 'active' | 'traffic' | 'updated' | 'ping';
export type SubscriptionMemberSortKey = 'delay' | 'server' | 'protocol' | 'ping' | 'tag' | 'status';

export type TunnelTableSortState<T extends string> = SortState<T>;

function createTunnelTableSortStore<T extends string>(
	storageKey: string,
	validKeys: readonly T[],
	defaults: Record<T, boolean>,
) {
	const store = createSortStore<T>(storageKey, validKeys, defaults);
	return {
		subscribe: store.subscribe,
		toggleSort(key: T) {
			store.mutate((state) => cycleTableSort(state, key));
		},
		setSortBy(key: T | null) {
			store.mutate(() => ({
				sortBy: key,
				sortAsc: key ? defaults[key] : true,
			}));
		},
		toggleDir() {
			store.mutate((state) =>
				state.sortBy ? { ...state, sortAsc: !state.sortAsc } : state,
			);
		},
	};
}

export const awgTunnelTableSort = createTunnelTableSortStore<AwgTunnelSortKey>(
	'awgm:tunnels:awg-table-sort',
	['name', 'status', 'endpoint', 'traffic', 'handshake'],
	AWG_TUNNEL_SORT_DEFAULTS,
);

export const singboxTunnelTableSort = createTunnelTableSortStore<SingboxTunnelSortKey>(
	'awgm:tunnels:singbox-table-sort',
	['delay', 'name', 'protocol', 'server', 'running', 'traffic', 'ping'],
	SINGBOX_TUNNEL_SORT_DEFAULTS,
);

export const singboxSubscriptionTableSort = createTunnelTableSortStore<SubscriptionSortKey>(
	'awgm:tunnels:subscriptions-table-sort',
	['delay', 'label', 'mode', 'active', 'traffic', 'updated', 'ping'],
	SUBSCRIPTION_SORT_DEFAULTS,
);

export const subscriptionMembersTableSort = createTunnelTableSortStore<SubscriptionMemberSortKey>(
	'awgm:subscriptions:members-table-sort',
	['delay', 'server', 'protocol', 'ping', 'tag', 'status'],
	SUBSCRIPTION_MEMBERS_SORT_DEFAULTS,
);
