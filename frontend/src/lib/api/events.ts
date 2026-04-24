// SSE event payloads
import type { SingboxTraffic, SingboxDelayEvent } from '$lib/types';

export interface LogEntryEvent {
	timestamp: string;
	level: string;
	group: string;
	subgroup?: string;
	action: string;
	target: string;
	message: string;
}

export interface SystemBootingEvent {
	phase: 'waiting' | 'starting';
	remainingSeconds?: number;
}

// --- Incremental payloads ---

export interface TunnelTrafficEvent {
	id: string;
	rxBytes: number;
	txBytes: number;
	lastHandshake?: string;
	startedAt?: string;
}

export interface TunnelConnectivityEvent {
	id: string;
	connected: boolean;
	latency: number | null;
}

export interface PingCheckLogEvent {
	timestamp: string;
	tunnelId: string;
	tunnelName: string;
	success: boolean;
	latency: number;
	error: string;
	failCount: number;
	threshold: number;
	stateChange: string;
	backend?: string;
}

export interface GeoDownloadProgressEvent {
	url: string;
	fileType: 'geosite' | 'geoip';
	downloaded: number;
	total: number; // 0 when unknown
	phase: 'download' | 'validate' | 'done' | 'error';
	error?: string;
}

export interface DnsRouteFailoverEvent {
	listId: string;
	listName: string;
	tunnelId: string;
	fromTunnel?: string;
	toTunnel?: string;
	action: 'switched' | 'restored' | 'error';
	error?: string;
}

export interface ResourceInvalidatedEvent {
	resource: string;
	reason?: string;
}

export interface SSEEventHandlers {
	// Connection lifecycle
	onConnected?: () => void;
	onDisconnected?: () => void;

	// System events
	onSystemReady?: (data: { ok: boolean; instanceId: string }) => void;
	onSystemBooting?: (data: SystemBootingEvent) => void;

	// Incremental streams (push-only — no REST equivalent)
	onTunnelTraffic?: (data: TunnelTrafficEvent) => void;
	onTunnelConnectivity?: (data: TunnelConnectivityEvent) => void;
	onLogEntry?: (data: LogEntryEvent) => void;
	onPingCheckLog?: (data: PingCheckLogEvent) => void;

	// Sing-box streams (traffic + delay remain push-only)
	onSingboxTraffic?: (data: SingboxTraffic[]) => void;
	onSingboxDelay?: (data: SingboxDelayEvent) => void;

	// HydraRoute geo download progress
	onHydraRouteGeoProgress?: (data: GeoDownloadProgressEvent) => void;

	// DNS-route failover notification (user-visible toast)
	onDnsRouteFailover?: (data: DnsRouteFailoverEvent) => void;

	// Generic resource invalidation hint (state-sync redesign)
	onResourceInvalidated?: (data: ResourceInvalidatedEvent) => void;

	// Device-proxy: selected outbound was deleted while the proxy was active.
	// Backend disables the proxy and emits this event so the UI can show a banner.
	onDeviceProxyMissingTarget?: (data: { wasTag: string }) => void;
}

export function connectSSE(handlers: SSEEventHandlers): () => void {
	const es = new EventSource('/api/events');

	const handle = (type: string, handler?: (data: any) => void) => {
		if (!handler) return;
		es.addEventListener(type, ((e: MessageEvent) => {
			try {
				handler(JSON.parse(e.data));
			} catch {
				/* ignore parse errors */
			}
		}) as EventListener);
	};

	// System events
	handle('system:ready', handlers.onSystemReady);
	handle('system:booting', handlers.onSystemBooting);

	// Incremental streams
	handle('tunnel:traffic', handlers.onTunnelTraffic);
	handle('tunnel:connectivity', handlers.onTunnelConnectivity);
	handle('log:entry', handlers.onLogEntry);
	handle('pingcheck:log', handlers.onPingCheckLog);

	// Sing-box streams
	handle('singbox:traffic', handlers.onSingboxTraffic);
	handle('singbox:delay', handlers.onSingboxDelay);

	// HydraRoute events
	handle('hydraroute:geo-progress', handlers.onHydraRouteGeoProgress);

	// DNS-route failover
	handle('dnsroute:failover', handlers.onDnsRouteFailover);

	// Generic resource invalidation hint (state-sync redesign)
	handle('resource:invalidated', handlers.onResourceInvalidated);

	// Device-proxy missing-target notification
	handle('deviceproxy:missing-target', handlers.onDeviceProxyMissingTarget);

	// Server sends "connected" event immediately on stream start
	es.addEventListener('connected', () => {
		handlers.onConnected?.();
	});

	es.onerror = () => {
		if (es.readyState === EventSource.CLOSED) {
			handlers.onDisconnected?.();
		}
	};

	return () => es.close();
}
