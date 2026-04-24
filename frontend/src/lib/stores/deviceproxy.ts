// Frontend polling stores for the device proxy feature. Two stores:
//   - config: cold-tier poll (30s); reflects persisted Config.
//   - outbounds: slightly hotter poll (15s); the dropdown list in UI.
// Both refresh sooner when the backend publishes
// resource:invalidated{resource:"deviceproxy"} via SSE.
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { DeviceProxyConfig, DeviceProxyOutbound } from '$lib/types';

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

// missingTarget holds the tag name of the outbound that was deleted while
// the proxy was active. Set by the deviceproxy:missing-target SSE event,
// cleared when resource:invalidated{resource:"deviceproxy"} arrives (which
// the backend publishes immediately after disabling and saving).
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
