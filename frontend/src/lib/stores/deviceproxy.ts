// Frontend polling stores for the device proxy feature:
//   - config (без таймера): reflects persisted Config; SSE-invalidated by
//     resource:invalidated{resource:"deviceproxy.config"}.
//   - outbounds (120 с): available outbound tags for the dropdowns — своего
//     публикатора у каталога нет.
//   - runtime (без таймера): live selector.now + persisted default for
//     the "Активный туннель" card; SSE-invalidated by
//     resource:invalidated{resource:"deviceproxy.runtime"}, в том числе
//     сторожем движка на смене живости.
//   - instances (без таймера): list of all proxy instances for multi-instance UI.
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { DeviceProxyConfig, DeviceProxyInstance, DeviceProxyOutbound, DeviceProxyRuntime } from '$lib/types';

export const deviceProxyConfig: PollingStore<DeviceProxyConfig> = createPollingStore<DeviceProxyConfig>(
	() => api.getDeviceProxyConfig(),
	{ staleTime: 30_000, pollInterval: 0 },
);
registerStore('deviceproxy.config', deviceProxyConfig);

export const deviceProxyInstances: PollingStore<DeviceProxyInstance[]> = createPollingStore<DeviceProxyInstance[]>(
	() => api.listDeviceProxyInstances(),
	{ staleTime: 30_000, pollInterval: 0 },
);
registerStore('deviceproxy.config', deviceProxyInstances);

// Каталог выходов — дорогой запрос (backend enumerate() дёргает полный RCI
// show/interface), а содержимое меняется только при CRUD туннелей/подписок.
// Панель Inbounds (DeviceProxyCompact) держит подписку постоянно, поэтому
// длинный интервал важен: 15s-поллинг возвращал бы фоновую RCI-нагрузку
// класса slow-RCI. Изменения имён доезжают за ≤2 мин или при перезаходе.
export const deviceProxyOutbounds: PollingStore<DeviceProxyOutbound[]> = createPollingStore<DeviceProxyOutbound[]>(
	() => api.listDeviceProxyOutbounds(),
	// Таймер сохранён: публикатора у deviceproxy.outbounds нет (см. ниже).
	{ staleTime: 60_000, pollInterval: 120_000 },
);
// Регистрация нужна НЕ ради SSE — точечной инвалидации у каталога нет и
// бэкенд такого ключа не публикует (см. пометку у ключа в storeRegistry).
// Она нужна ради invalidateAll(): при выходе из Tier-3 отказа каталог
// обязан перечитаться сразу, а не через ≤2 мин поллинга.
registerStore('deviceproxy.outbounds', deviceProxyOutbounds);

export const deviceProxyRuntime: PollingStore<DeviceProxyRuntime> = createPollingStore<DeviceProxyRuntime>(
	() => api.getDeviceProxyRuntime(),
	// Таймера нет: сторож движка публикует deviceproxy.runtime на СМЕНЕ
	// живости (internal/singbox/watchdog.go). Раньше падение sing-box (OOM на
	// 256 МБ — рабочий сценарий) публиковало только singbox.status, и карточка
	// показывала бы «работает» бессрочно — ради этого и стоял таймер (F355).
	// Теперь обновление приходит событием (F364).
	{ staleTime: 5_000, pollInterval: 0 },
);
registerStore('deviceproxy.runtime', deviceProxyRuntime);

// missingTarget holds the tag name of the outbound that was deleted while
// the proxy was active. Set by the deviceproxy:missing-target SSE event,
// cleared when resource:invalidated{resource:"deviceproxy.config"} arrives
// (which the backend publishes immediately after disabling and saving).
export const deviceProxyMissingTarget = writable<string | null>(null);

export function setDeviceProxyMissingTarget(wasTag: string): void {
	deviceProxyMissingTarget.set(wasTag);
	// Also kick both polling stores so the UI reflects the disabled state.
	deviceProxyConfig.invalidate();
	deviceProxyInstances.invalidate();
	deviceProxyOutbounds.invalidate();
}

export function clearDeviceProxyMissingTarget(): void {
	deviceProxyMissingTarget.set(null);
}
