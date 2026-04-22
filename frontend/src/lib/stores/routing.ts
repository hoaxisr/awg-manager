/**
 * routing — eight per-section polling stores + a derived `routing`
 * composite that preserves the monolithic shape older pages read as
 * `$routing.dnsRoutes`, `$routing.staticRoutes`, etc.
 *
 * Each section polls its own GET endpoint at 30s (cold tier). Backend
 * mutations publish `resource:invalidated` hints keyed by
 * ResourceRoutingXxx; the storeRegistry wiring triggers an immediate
 * refetch of only the affected section.
 *
 * `hydrarouteStatus` is a full polling store backed by
 * `/api/system/hydraroute-status`. Backend publishes
 * `routing.hydrarouteStatus` invalidations whenever the HR Neo daemon
 * state could change (Control start/stop/restart, config write, policy
 * order change).
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

// HR Neo status is a single object, not an array — use a dedicated
// fetcher so the empty-body fallback is a real Status rather than an
// empty array. Backed by the existing /api/system/hydraroute-status.
export const hydrarouteStatusStore: PollingStore<HydraRouteStatus> = createPollingStore<HydraRouteStatus>(
	async () => {
		const res = await fetch('/api/system/hydraroute-status');
		if (!res.ok) throw new Error(`routing.hydrarouteStatus ${res.status}`);
		const body = await res.json();
		return (body.data ?? { installed: false, running: false }) as HydraRouteStatus;
	},
	{
		staleTime: 30_000,
		pollInterval: 30_000,
	}
);
registerStore('routing.hydrarouteStatus', hydrarouteStatusStore);

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
		hydrarouteStatusStore,
	],
	([d, s, a, pd, pi, cr, rt, hr]) => {
		const missing: string[] = [];
		if (d.status === 'error') missing.push('dnsRoutes');
		if (s.status === 'error') missing.push('staticRoutes');
		if (a.status === 'error') missing.push('accessPolicies');
		if (pd.status === 'error') missing.push('policyDevices');
		if (pi.status === 'error') missing.push('policyInterfaces');
		if (cr.status === 'error') missing.push('clientRoutes');
		if (rt.status === 'error') missing.push('tunnels');
		if (hr.status === 'error') missing.push('hydrarouteStatus');
		return {
			dnsRoutes: d.data ?? [],
			staticRoutes: s.data ?? [],
			accessPolicies: a.data ?? [],
			policyDevices: pd.data ?? [],
			policyInterfaces: pi.data ?? [],
			clientRoutes: cr.data ?? [],
			tunnels: rt.data ?? [],
			hydrarouteStatus: hr.data ?? null,
			loaded: [d, s, a, pd, pi, cr, rt, hr].every(
				(x) => x.lastFetchedAt > 0 || x.status === 'error'
			),
			missing,
		};
	}
);

/**
 * subscribeRouting — convenience helper that subscribes to every section
 * store with a no-op listener. Pages that want all eight sections can
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
		hydrarouteStatusStore.subscribe(() => {}),
	];
	return () => unsubs.forEach((u) => u());
}

/**
 * invalidateAllRouting — triggers immediate refetch across all eight
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
	hydrarouteStatusStore.invalidate();
}
