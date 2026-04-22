/**
 * routing — seven per-section polling stores + a derived `routing`
 * composite that preserves the monolithic shape older pages read as
 * `$routing.dnsRoutes`, `$routing.staticRoutes`, etc.
 *
 * Each section polls its own GET endpoint at 30s (cold tier). Backend
 * mutations publish `resource:invalidated` hints keyed by
 * ResourceRoutingXxx; the storeRegistry wiring triggers an immediate
 * refetch of only the affected section.
 *
 * `hydrarouteStatus` is not a per-section polling store here — it is
 * owned by `systemInfo` (HR Neo status lives on system-wide polling)
 * and surfaced through the composite as a passthrough field when
 * available. For routing.ts it stays undefined unless a future task
 * wires a dedicated store; the composite exposes it optionally so
 * existing `$routing.hydrarouteStatus` reads don't crash.
 */
import { derived, type Readable } from 'svelte/store';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore, type ResourceKey } from './storeRegistry';
import type {
	DnsRoute,
	StaticRouteList,
	AccessPolicy,
	PolicyDevice,
	PolicyGlobalInterface,
	ClientRoute,
	RoutingTunnel,
	HydraRouteStatus,
} from '$lib/types';

function createSection<T>(url: string, resourceKey: ResourceKey): PollingStore<T> {
	const store = createPollingStore<T>(
		async () => {
			const res = await fetch(url);
			if (!res.ok) throw new Error(`${resourceKey} ${res.status}`);
			const body = await res.json();
			return (body.data ?? []) as T;
		},
		{
			staleTime: 30_000,
			pollInterval: 30_000,
		}
	);
	registerStore(resourceKey, store);
	return store;
}

export const dnsRoutesStore = createSection<DnsRoute[]>(
	'/api/routing/dns-routes',
	'routing.dnsRoutes'
);
export const staticRoutesStore = createSection<StaticRouteList[]>(
	'/api/routing/static-routes',
	'routing.staticRoutes'
);
export const accessPoliciesStore = createSection<AccessPolicy[]>(
	'/api/routing/access-policies',
	'routing.accessPolicies'
);
export const policyDevicesStore = createSection<PolicyDevice[]>(
	'/api/routing/policy-devices',
	'routing.policyDevices'
);
export const policyInterfacesStore = createSection<PolicyGlobalInterface[]>(
	'/api/routing/policy-interfaces',
	'routing.policyInterfaces'
);
export const clientRoutesStore = createSection<ClientRoute[]>(
	'/api/routing/client-routes',
	'routing.clientRoutes'
);
export const routingTunnelsStore = createSection<RoutingTunnel[]>(
	'/api/routing/tunnels',
	'routing.tunnels'
);

export type RoutingComposite = {
	dnsRoutes: DnsRoute[];
	staticRoutes: StaticRouteList[];
	accessPolicies: AccessPolicy[];
	policyDevices: PolicyDevice[];
	policyInterfaces: PolicyGlobalInterface[];
	clientRoutes: ClientRoute[];
	tunnels: RoutingTunnel[];
	hydrarouteStatus: HydraRouteStatus | null;
	loaded: boolean;
	missing: string[];
};

export const routing: Readable<RoutingComposite> = derived(
	[
		dnsRoutesStore,
		staticRoutesStore,
		accessPoliciesStore,
		policyDevicesStore,
		policyInterfacesStore,
		clientRoutesStore,
		routingTunnelsStore,
	],
	([d, s, a, pd, pi, cr, rt]) => {
		const missing: string[] = [];
		if (d.status === 'error') missing.push('dnsRoutes');
		if (s.status === 'error') missing.push('staticRoutes');
		if (a.status === 'error') missing.push('accessPolicies');
		if (pd.status === 'error') missing.push('policyDevices');
		if (pi.status === 'error') missing.push('policyInterfaces');
		if (cr.status === 'error') missing.push('clientRoutes');
		if (rt.status === 'error') missing.push('tunnels');
		return {
			dnsRoutes: d.data ?? [],
			staticRoutes: s.data ?? [],
			accessPolicies: a.data ?? [],
			policyDevices: pd.data ?? [],
			policyInterfaces: pi.data ?? [],
			clientRoutes: cr.data ?? [],
			tunnels: rt.data ?? [],
			hydrarouteStatus: null,
			loaded: [d, s, a, pd, pi, cr, rt].every(
				(x) => x.lastFetchedAt > 0 || x.status === 'error'
			),
			missing,
		};
	}
);

/**
 * subscribeRouting — convenience helper that subscribes to every section
 * store with a no-op listener. Pages that want all seven sections can
 * call this once in `onMount` and the returned function on `onDestroy`.
 * Each subscribe triggers the polling lifecycle (initial fetch + interval).
 */
export function subscribeRouting(): () => void {
	const unsubs = [
		dnsRoutesStore.subscribe(() => {}),
		staticRoutesStore.subscribe(() => {}),
		accessPoliciesStore.subscribe(() => {}),
		policyDevicesStore.subscribe(() => {}),
		policyInterfacesStore.subscribe(() => {}),
		clientRoutesStore.subscribe(() => {}),
		routingTunnelsStore.subscribe(() => {}),
	];
	return () => unsubs.forEach((u) => u());
}

/**
 * invalidateAllRouting — triggers immediate refetch across all seven
 * section stores. Used by the `/api/routing/refresh` button handler so
 * the user sees fresh data even when some sections previously errored.
 */
export function invalidateAllRouting(): void {
	dnsRoutesStore.invalidate();
	staticRoutesStore.invalidate();
	accessPoliciesStore.invalidate();
	policyDevicesStore.invalidate();
	policyInterfacesStore.invalidate();
	clientRoutesStore.invalidate();
	routingTunnelsStore.invalidate();
}
