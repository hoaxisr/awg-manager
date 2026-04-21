import { writable } from 'svelte/store';
import type {
	DnsRoute, StaticRouteList, RoutingTunnel,
	AccessPolicy, PolicyDevice, PolicyGlobalInterface, ClientRoute, HydraRouteStatus
} from '$lib/types';
import type { RoutingSectionKey, SnapshotRoutingEvent } from '$lib/api/events';

interface RoutingState {
	dnsRoutes: DnsRoute[];
	staticRoutes: StaticRouteList[];
	tunnels: RoutingTunnel[];
	accessPolicies: AccessPolicy[];
	policyDevices: PolicyDevice[];
	policyInterfaces: PolicyGlobalInterface[];
	clientRoutes: ClientRoute[];
	hydrarouteStatus: HydraRouteStatus | null;
	missing: RoutingSectionKey[];
	loaded: boolean;
}

function createRoutingStore() {
	const { subscribe, set, update } = writable<RoutingState>({
		dnsRoutes: [],
		staticRoutes: [],
		tunnels: [],
		accessPolicies: [],
		policyDevices: [],
		policyInterfaces: [],
		clientRoutes: [],
		hydrarouteStatus: null,
		missing: [],
		loaded: false,
	});

	return {
		subscribe,
		setSnapshot(data: SnapshotRoutingEvent) {
			set({
				dnsRoutes: data.dnsRoutes ?? [],
				staticRoutes: data.staticRoutes ?? [],
				tunnels: data.tunnels ?? [],
				accessPolicies: data.accessPolicies ?? [],
				policyDevices: data.policyDevices ?? [],
				policyInterfaces: data.policyInterfaces ?? [],
				clientRoutes: data.clientRoutes ?? [],
				hydrarouteStatus: data.hydrarouteStatus ?? null,
				missing: data.missing ?? [],
				loaded: true,
			});
		},
		setDnsRoutes(routes: DnsRoute[]) { update(s => ({ ...s, dnsRoutes: routes ?? [], missing: withoutSection(s.missing, 'dnsRoutes') })); },
		setStaticRoutes(routes: StaticRouteList[]) { update(s => ({ ...s, staticRoutes: routes ?? [], missing: withoutSection(s.missing, 'staticRoutes') })); },
		setPolicies(policies: AccessPolicy[]) { update(s => ({ ...s, accessPolicies: policies ?? [], missing: withoutSection(s.missing, 'accessPolicies') })); },
		setPolicyDevices(devices: PolicyDevice[]) { update(s => ({ ...s, policyDevices: devices ?? [], missing: withoutSection(s.missing, 'policyDevices') })); },
		setPolicyInterfaces(interfaces: PolicyGlobalInterface[]) { update(s => ({ ...s, policyInterfaces: interfaces ?? [], missing: withoutSection(s.missing, 'policyInterfaces') })); },
		setClientRoutes(routes: ClientRoute[]) { update(s => ({ ...s, clientRoutes: routes ?? [], missing: withoutSection(s.missing, 'clientRoutes') })); },
		setRoutingTunnels(tunnels: RoutingTunnel[]) { update(s => ({ ...s, tunnels: tunnels ?? [], missing: withoutSection(s.missing, 'tunnels') })); },
		setHydraRouteStatus(status: HydraRouteStatus | null) { update(s => ({ ...s, hydrarouteStatus: status, missing: withoutSection(s.missing, 'hydrarouteStatus') })); },
	};
}

function withoutSection(missing: RoutingSectionKey[], key: RoutingSectionKey): RoutingSectionKey[] {
	if (missing.length === 0) return missing;
	const next = missing.filter(k => k !== key);
	return next.length === missing.length ? missing : next;
}

export const routing = createRoutingStore();
