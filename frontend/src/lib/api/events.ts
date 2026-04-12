// SSE event payloads

export interface TunnelStateEvent {
	id: string;
	name: string;
	state: string;
	backend?: string;
}

export interface TunnelDeletedEvent {
	id: string;
}

export interface TunnelCreatedEvent {
	id: string;
	name: string;
	backend: string;
}

export interface TunnelUpdatedEvent {
	id: string;
	name: string;
}

export interface LogEntryEvent {
	timestamp: string;
	level: string;
	group: string;
	subgroup?: string;
	action: string;
	target: string;
	message: string;
}

export interface PingCheckStateEvent {
	tunnelId: string;
	status: string;
	failCount: number;
	successCount: number;
	restartDetected?: boolean;
}

// --- Snapshot payloads ---

export interface SystemBootingEvent {
	phase: 'waiting' | 'starting';
	remainingSeconds?: number;
}

export interface SnapshotTunnelsEvent {
	tunnels: import('$lib/types').TunnelListItem[];
	external: import('$lib/types').ExternalTunnel[];
	system: import('$lib/types').SystemTunnel[];
}

export interface SnapshotServersEvent {
	servers: import('$lib/types').WireguardServer[];
	managed: import('$lib/types').ManagedServer | null;
	managedStats: import('$lib/types').ManagedServerStats | null;
	wanIP: string;
}

export interface SnapshotRoutingEvent {
	dnsRoutes: import('$lib/types').DnsRoute[];
	staticRoutes: import('$lib/types').StaticRouteList[];
	tunnels: import('$lib/types').RoutingTunnel[];
	accessPolicies: import('$lib/types').AccessPolicy[];
	policyDevices: import('$lib/types').PolicyDevice[];
	policyInterfaces: import('$lib/types').PolicyGlobalInterface[];
	clientRoutes: import('$lib/types').ClientRoute[];
}

export interface SnapshotPingcheckEvent {
	statuses: import('$lib/types').TunnelPingStatus[];
	logs: import('$lib/types').PingLogEntry[];
}

export interface SnapshotLogsEvent {
	enabled: boolean;
	logs: import('$lib/types').LogEntry[];
	total: number;
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

export interface SSEEventHandlers {
	// Existing
	onTunnelState?: (data: TunnelStateEvent) => void;
	onTunnelCreated?: (data: TunnelCreatedEvent) => void;
	onTunnelDeleted?: (data: TunnelDeletedEvent) => void;
	onTunnelUpdated?: (data: TunnelUpdatedEvent) => void;
	onLogEntry?: (data: LogEntryEvent) => void;
	onPingCheckState?: (data: PingCheckStateEvent) => void;
	onConnected?: () => void;
	onDisconnected?: () => void;

	// System
	onSystemReady?: (data: { ok: boolean; instanceId: string }) => void;
	onSystemBooting?: (data: SystemBootingEvent) => void;

	// Snapshots
	onSnapshotSystem?: (data: import('$lib/types').SystemInfo) => void;
	onSnapshotTunnels?: (data: SnapshotTunnelsEvent) => void;
	onSnapshotServers?: (data: SnapshotServersEvent) => void;
	onSnapshotRouting?: (data: SnapshotRoutingEvent) => void;
	onSnapshotPingcheck?: (data: SnapshotPingcheckEvent) => void;
	onSnapshotLogs?: (data: SnapshotLogsEvent) => void;

	// Incremental
	onTunnelTraffic?: (data: TunnelTrafficEvent) => void;
	onTunnelConnectivity?: (data: TunnelConnectivityEvent) => void;
	onPingCheckLog?: (data: PingCheckLogEvent) => void;
	onServerUpdated?: (data: SnapshotServersEvent) => void;
	onRoutingDnsUpdated?: (data: import('$lib/types').DnsRoute[]) => void;
	onRoutingStaticUpdated?: (data: import('$lib/types').StaticRouteList[]) => void;
	onRoutingPoliciesUpdated?: (data: import('$lib/types').AccessPolicy[]) => void;
	onRoutingPolicyDevicesUpdated?: (data: import('$lib/types').PolicyDevice[]) => void;
	onRoutingPolicyInterfacesUpdated?: (data: import('$lib/types').PolicyGlobalInterface[]) => void;
	onRoutingClientRoutesUpdated?: (data: import('$lib/types').ClientRoute[]) => void;
	onRoutingTunnelsUpdated?: (data: import('$lib/types').RoutingTunnel[]) => void;
	onTunnelsList?: (data: import('$lib/types').TunnelListItem[]) => void;
	onDnsRouteFailover?: (data: {
		listId: string;
		listName: string;
		tunnelId: string;
		fromTunnel?: string;
		toTunnel?: string;
		action: 'switched' | 'restored' | 'error';
		error?: string;
	}) => void;
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

	handle('tunnel:state', handlers.onTunnelState);
	handle('tunnel:created', handlers.onTunnelCreated);
	handle('tunnel:deleted', handlers.onTunnelDeleted);
	handle('tunnel:updated', handlers.onTunnelUpdated);
	handle('log:entry', handlers.onLogEntry);
	handle('pingcheck:state', handlers.onPingCheckState);

	// System events
	handle('system:ready', handlers.onSystemReady);
	handle('system:booting', handlers.onSystemBooting);

	// Snapshot events
	handle('snapshot:system', handlers.onSnapshotSystem);
	handle('snapshot:tunnels', handlers.onSnapshotTunnels);
	handle('snapshot:servers', handlers.onSnapshotServers);
	handle('snapshot:routing', handlers.onSnapshotRouting);
	handle('snapshot:pingcheck', handlers.onSnapshotPingcheck);
	handle('snapshot:logs', handlers.onSnapshotLogs);

	// Incremental events
	handle('tunnel:traffic', handlers.onTunnelTraffic);
	handle('tunnel:connectivity', handlers.onTunnelConnectivity);
	handle('pingcheck:log', handlers.onPingCheckLog);
	handle('server:updated', handlers.onServerUpdated);
	handle('routing:dns-updated', handlers.onRoutingDnsUpdated);
	handle('routing:static-updated', handlers.onRoutingStaticUpdated);
	handle('routing:policies-updated', handlers.onRoutingPoliciesUpdated);
	handle('routing:policy-devices-updated', handlers.onRoutingPolicyDevicesUpdated);
	handle('routing:policy-interfaces-updated', handlers.onRoutingPolicyInterfacesUpdated);
	handle('routing:client-routes-updated', handlers.onRoutingClientRoutesUpdated);
	handle('routing:tunnels-updated', handlers.onRoutingTunnelsUpdated);
	handle('tunnels:list', handlers.onTunnelsList);
	handle('dnsroute:failover', handlers.onDnsRouteFailover);

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
