// Frontend polling stores for the device proxy feature:
//   - config (30s poll): reflects persisted Config; SSE-invalidated by
//     resource:invalidated{resource:"deviceproxy.config"}.
//   - outbounds (15s poll): available outbound tags for the dropdowns.
//   - runtime (5s poll): live selector.now + persisted default for
//     the "Активный туннель" card; SSE-invalidated by
//     resource:invalidated{resource:"deviceproxy.runtime"}.
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { DeviceProxyConfig, DeviceProxyOutbound, DeviceProxyRuntime } from '$lib/types';

export const deviceProxyConfig: PollingStore<DeviceProxyConfig> = createPollingStore<DeviceProxyConfig>(
	() => api.getDeviceProxyConfig(),
	{ staleTime: 30_000, pollInterval: 30_000 },
);
registerStore('deviceproxy.config', deviceProxyConfig);

export const deviceProxyOutbounds: PollingStore<DeviceProxyOutbound[]> = createPollingStore<DeviceProxyOutbound[]>(
	() => api.listDeviceProxyOutbounds(),
	{ staleTime: 15_000, pollInterval: 15_000 },
);
registerStore('deviceproxy.outbounds', deviceProxyOutbounds);

export const deviceProxyRuntime: PollingStore<DeviceProxyRuntime> = createPollingStore<DeviceProxyRuntime>(
	() => api.getDeviceProxyRuntime(),
	{ staleTime: 5_000, pollInterval: 5_000 },
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
	deviceProxyOutbounds.invalidate();
}

export function clearDeviceProxyMissingTarget(): void {
	deviceProxyMissingTarget.set(null);
}
