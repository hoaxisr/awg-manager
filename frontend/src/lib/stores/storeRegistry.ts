import type { PollingStore } from './polling';

/**
 * Closed set of resource keys recognised by the invalidation pipeline.
 * Each value MUST match the corresponding Go constant (ResourceXxx) in
 * `internal/events/resources.go`. When adding a new state resource, update
 * BOTH sides in the same commit — TestResourceKeys_KnownToFrontend читает
 * этот union и падает на ключе, который бэкенд публикует, а фронт не знает.
 */
export type ResourceKey =
	| 'awg3'                        // ResourceAwg3
	| 'tunnels'                     // ResourceTunnels
	| 'servers'                     // ResourceServers
	| 'singbox.status'              // ResourceSingboxStatus
	| 'singbox.tunnels'             // ResourceSingboxTunnels
	| 'singbox.subscriptions'       // ResourceSingboxSubscriptions — любая запись подписки
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
	| 'routing.zapretStatus'        // ResourceRoutingZapretStatus
	| 'routing.autolistStatus'      // публикатора НЕТ: сателлит — внешний процесс, стор живёт на опросе
	| 'deviceproxy.config'           // ResourceDeviceProxyConfig   — also clears missing-target banner
	| 'deviceproxy.outbounds'       // публикатора НЕТ: ключ живёт ради invalidateAll() при выходе из отказа
	| 'deviceproxy.runtime'         // ResourceDeviceProxyRuntime
	| 'singbox.router.staging'      // emitted by emitStagingEvent — triggers loadStaging()
	| 'singbox.router.rules'        // emitted by emitRulesEvent — triggers loadRulesSnapshot()
	| 'proxyrt.instances'             // ResourceProxyInstances — состав инстансов прокси
	| 'mcpKeys'                     // ResourceMcpKeys — ключи MCP-сервера
	| 'updateSnapshots'             // ResourceUpdateSnapshots — снимки настроек перед обновлением
	| 'bypass-set'                  // публикуется после наполнения AWGM-BYPASS (storeBypassSetOutcome)
	| 'firc'                        // ResourceFirc — состояние/настройки FIRC, установка рантайма
	| 'servers.ingress'             // ResourceServersIngress — VLESS, Telegram-прокси, CDN-диспетчер
	| 'uplink';                     // ResourceUplink — политики Multi-WAN и их состояние

/**
 * Resource key → list of polling stores. Multiple stores can register under
 * the same resource key (e.g. legacy deviceProxyConfig and new
 * deviceProxyInstances both subscribe to "deviceproxy.config" invalidations).
 * The SSE `resource:invalidated` handler iterates all registered stores and
 * calls `.invalidate()` on each.
 */
const registry = new Map<string, { store: PollingStore<unknown>; skipReason?: string }[]>();

/**
 * Register a polling store under a resource key. Call this once per store,
 * typically at store construction. Subsequent `invalidateResource(key)` calls
 * will trigger an immediate refetch on all stores registered for that key.
 * Typed by `ResourceKey` so typos become compile errors rather than silent
 * invalidation misses.
 *
 * `skipReason` — причина подсказки, по которой этот стор НЕ перезапрашивается
 * (остальные сторы под тем же ключом её получают). Нужна, когда ключ шире
 * того, что стор показывает: `pingcheck` приходит на каждую проверку, а
 * снимку туннелей важна только смена состояния.
 */
export function registerStore<T>(resource: ResourceKey, store: PollingStore<T>, skipReason?: string): void {
	const stores = registry.get(resource) ?? [];
	stores.push({ store: store as PollingStore<unknown>, skipReason });
	registry.set(resource, stores);
}

/**
 * Подписчики-функции. Нужны компонентам, у которых данные — локальный
 * `$state`, а не PollingStore: они перечитывают своё по подсказке вместо
 * таймера. Возвращает отписку — её отдают из `onMount`/`$effect`.
 */
const listeners = new Map<string, Set<(reason?: string) => void>>();

/**
 * Подписчиков-функций зовём не на каждую подсказку, а раз на пачку: одна
 * мутация на бэкенде часто даёт несколько подсказок подряд (хендлер + смена
 * рантайма), и без склейки каждая вела бы к своему перечитыванию. В вызов
 * уходит причина последней подсказки пачки.
 */
export const LISTENER_COALESCE_MS = 150;
const pendingListeners = new Map<string, { reason?: string }>();

function notifyListeners(resource: string, reason?: string): void {
	if (!listeners.get(resource)?.size) return;
	const pending = pendingListeners.get(resource);
	if (pending) {
		pending.reason = reason;
		return;
	}
	const entry: { reason?: string } = { reason };
	pendingListeners.set(resource, entry);
	setTimeout(() => {
		pendingListeners.delete(resource);
		for (const fn of listeners.get(resource) ?? []) fn(entry.reason);
	}, LISTENER_COALESCE_MS);
}

export function onResourceInvalidated(resource: ResourceKey, fn: (reason?: string) => void): () => void {
	const set = listeners.get(resource) ?? new Set();
	set.add(fn);
	listeners.set(resource, set);
	return () => {
		set.delete(fn);
	};
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
export function invalidateResource(resource: string, reason?: string): void {
	for (const { store, skipReason } of registry.get(resource) ?? []) {
		if (skipReason && reason === skipReason) continue;
		store.invalidate();
	}
	notifyListeners(resource, reason);
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
		for (const { store } of stores) {
			if (seen.has(store)) continue;
			seen.add(store);
			store.invalidate();
		}
	}
	for (const resource of listeners.keys()) notifyListeners(resource);
}
