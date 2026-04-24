// Frontend polling stores for the device proxy feature. Two stores:
//   - config: cold-tier poll (30s); reflects persisted Config.
//   - outbounds: slightly hotter poll (15s); the dropdown list in UI.
// Both refresh sooner when the backend publishes
// resource:invalidated{kind:"deviceproxy"} via SSE.
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
