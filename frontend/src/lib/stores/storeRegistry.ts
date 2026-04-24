import type { PollingStore } from './polling';

/**
 * Closed set of resource keys recognised by the invalidation pipeline.
 * Each value MUST match the corresponding Go constant (ResourceXxx) in
 * `internal/api/publish.go`. When adding a new state resource, update
 * BOTH sides in the same commit.
 */
export type ResourceKey =
	| 'tunnels'                     // ResourceTunnels
	| 'servers'                     // ResourceServers
	| 'singbox.status'              // ResourceSingboxStatus
	| 'singbox.tunnels'             // ResourceSingboxTunnels
	| 'sysInfo'                     // ResourceSysInfo
	| 'pingcheck'                   // ResourcePingcheck
	| 'saveStatus'                  // ResourceSaveStatus
	| 'routing.dnsRoutes'           // ResourceRoutingDnsRoutes
	| 'routing.staticRoutes'        // ResourceRoutingStaticRoutes
	| 'routing.accessPolicies'      // ResourceRoutingAccessPolicies
	| 'routing.policyDevices'       // ResourceRoutingPolicyDevices
	| 'routing.policyInterfaces'    // ResourceRoutingPolicyInterfaces
	| 'routing.clientRoutes'        // ResourceRoutingClientRoutes
	| 'routing.tunnels'             // ResourceRoutingTunnels
	| 'routing.hydrarouteStatus'    // ResourceRoutingHydrarouteStatus
	| 'deviceproxy.config'          // ResourceDeviceProxyConfig
	| 'deviceproxy.outbounds'       // ResourceDeviceProxyOutbounds
	| 'deviceproxy';                // ResourceDeviceProxy (invalidation-only, no store — clears missing-target)

/**
 * Resource key → polling store. Populated as stores are migrated to
 * createPollingStore (Phase B). The SSE `resource:invalidated` handler
 * looks up the store here and calls `.invalidate()`.
 */
const registry = new Map<string, PollingStore<unknown>>();

/**
 * Register a polling store under a resource key. Call this once per store,
 * typically at store construction. Subsequent `invalidateResource(key)` calls
 * will trigger an immediate refetch on that store. Typed by `ResourceKey`
 * so typos become compile errors rather than silent invalidation misses.
 */
export function registerStore<T>(resource: ResourceKey, store: PollingStore<T>): void {
	if (import.meta.env.DEV && registry.has(resource)) {
		console.warn(`storeRegistry: overwriting store for "${resource}"`);
	}
	registry.set(resource, store as PollingStore<unknown>);
}

/**
 * Trigger `invalidate()` on the store registered under `resource`. No-op if
 * no store is registered — either because the resource key is unknown, or
 * because the store has not been migrated to createPollingStore yet.
 *
 * Accepts plain `string` because this is called from the SSE listener with
 * payload that is unknown at compile time; the no-op-on-unknown-key behaviour
 * is intentional.
 */
export function invalidateResource(resource: string): void {
	registry.get(resource)?.invalidate();
}

/**
 * Invalidate every registered store. Called when the backend recovers
 * from a full outage (Tier 3 overlay) so all polling stores pick up
 * fresh state rather than keeping whatever cached data they had before
 * the outage.
 */
export function invalidateAll(): void {
	for (const store of registry.values()) {
		store.invalidate();
	}
}
