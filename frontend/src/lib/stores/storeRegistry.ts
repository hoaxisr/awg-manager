import type { PollingStore } from './polling';

/**
 * Closed set of resource keys recognised by the invalidation pipeline.
 * Each value MUST match the corresponding Go constant (ResourceXxx) in
 * `internal/api/publish.go`. When adding a new state resource, update
 * BOTH sides in the same commit.
 */
export type ResourceKey =
	| 'awg3'                        // ResourceAwg3
	| 'tunnels'                     // ResourceTunnels
	| 'servers'                     // ResourceServers
	| 'singbox.status'              // ResourceSingboxStatus
	| 'singbox.tunnels'             // ResourceSingboxTunnels
	| 'singbox.proxies'             // ResourceSingboxProxies (no backend publisher yet — invalidate via store.refetch)
	| 'sysInfo'                     // ResourceSysInfo
	| 'pingcheck'                   // ResourcePingcheck
	| 'saveStatus'                  // ResourceSaveStatus
	| 'settings'                    // ResourceSettings
	| 'routing.dnsRoutes'           // ResourceRoutingDnsRoutes
	| 'routing.staticRoutes'        // ResourceRoutingStaticRoutes
	| 'routing.accessPolicies'      // ResourceRoutingAccessPolicies
	| 'routing.policyDevices'       // ResourceRoutingPolicyDevices
	| 'routing.policyInterfaces'    // ResourceRoutingPolicyInterfaces
	| 'routing.clientRoutes'        // ResourceRoutingClientRoutes
	| 'routing.tunnels'             // ResourceRoutingTunnels
	| 'routing.hydrarouteStatus'    // ResourceRoutingHydrarouteStatus
	| 'deviceproxy.config'           // ResourceDeviceProxyConfig   — also clears missing-target banner
	| 'deviceproxy.outbounds'       // ResourceDeviceProxyOutbounds
	| 'deviceproxy.runtime'         // ResourceDeviceProxyRuntime
	| 'singbox.router.staging'      // emitted by emitStagingEvent — triggers loadStaging()
	| 'singbox.router.rules'        // emitted by emitRulesEvent — triggers loadRulesSnapshot()
	| 'proxy.instances'             // ResourceProxyInstances — состав инстансов прокси
	| 'bypass-set';                 // публикуется после наполнения AWGM-BYPASS (storeBypassSetOutcome)

/**
 * Resource key → list of polling stores. Multiple stores can register under
 * the same resource key (e.g. legacy deviceProxyConfig and new
 * deviceProxyInstances both subscribe to "deviceproxy.config" invalidations).
 * The SSE `resource:invalidated` handler iterates all registered stores and
 * calls `.invalidate()` on each.
 */
const registry = new Map<string, PollingStore<unknown>[]>();

/**
 * Register a polling store under a resource key. Call this once per store,
 * typically at store construction. Subsequent `invalidateResource(key)` calls
 * will trigger an immediate refetch on all stores registered for that key.
 * Typed by `ResourceKey` so typos become compile errors rather than silent
 * invalidation misses.
 */
export function registerStore<T>(resource: ResourceKey, store: PollingStore<T>): void {
	const stores = registry.get(resource) ?? [];
	stores.push(store as PollingStore<unknown>);
	registry.set(resource, stores);
}

/**
 * Trigger `invalidate()` on all stores registered under `resource`. No-op if
 * no store is registered — either because the resource key is unknown, or
 * because the store has not been migrated to createPollingStore yet.
 *
 * Accepts plain `string` because this is called from the SSE listener with
 * payload that is unknown at compile time; the no-op-on-unknown-key behaviour
 * is intentional.
 */
export function invalidateResource(resource: string): void {
	for (const store of registry.get(resource) ?? []) {
		store.invalidate();
	}
}

/**
 * Invalidate every registered store. Called when the backend recovers
 * from a full outage (Tier 3 overlay) so all polling stores pick up
 * fresh state rather than keeping whatever cached data they had before
 * the outage.
 */
export function invalidateAll(): void {
	const seen = new Set<PollingStore<unknown>>();
	for (const stores of registry.values()) {
		for (const store of stores) {
			if (seen.has(store)) continue;
			seen.add(store);
			store.invalidate();
		}
	}
}
