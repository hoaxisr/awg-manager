import { writable } from 'svelte/store';
import type {
	DnsRoute, StaticRouteList, RoutingTunnel,
	AccessPolicy, PolicyDevice, PolicyGlobalInterface, ClientRoute
} from '$lib/types';
import type { SnapshotRoutingEvent } from '$lib/api/events';

interface RoutingState {
	dnsRoutes: DnsRoute[];
	staticRoutes: StaticRouteList[];
	tunnels: RoutingTunnel[];
	accessPolicies: AccessPolicy[];
	policyDevices: PolicyDevice[];
	policyInterfaces: PolicyGlobalInterface[];
	clientRoutes: ClientRoute[];
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
				loaded: true,
			});
		},
		setDnsRoutes(routes: DnsRoute[]) { update(s => ({ ...s, dnsRoutes: routes ?? [] })); },
		setStaticRoutes(routes: StaticRouteList[]) { update(s => ({ ...s, staticRoutes: routes ?? [] })); },
		setPolicies(policies: AccessPolicy[]) { update(s => ({ ...s, accessPolicies: policies ?? [] })); },
		setPolicyDevices(devices: PolicyDevice[]) { update(s => ({ ...s, policyDevices: devices ?? [] })); },
		setPolicyInterfaces(interfaces: PolicyGlobalInterface[]) { update(s => ({ ...s, policyInterfaces: interfaces ?? [] })); },
		setClientRoutes(routes: ClientRoute[]) { update(s => ({ ...s, clientRoutes: routes ?? [] })); },
		setRoutingTunnels(tunnels: RoutingTunnel[]) { update(s => ({ ...s, tunnels: tunnels ?? [] })); },
	};
}

export const routing = createRoutingStore();
