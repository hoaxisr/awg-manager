// ─────────────────────────────────────────────
// #region Logging
// ─────────────────────────────────────────────

export interface LogEntry {
	timestamp: string;
	level: string;
	group: string;
	subgroup: string;
	action: string;
	target: string;
	message: string;
	/** true when target/message were sanitized by backend before delivery. */
	sanitized?: boolean;
	/** Схлопнутые повторы: сколько идентичных записей свёрнуто в эту (0/нет = уникальна). */
	repeats?: number;
	/** Время последнего повтора (timestamp — первое появление). */
	lastSeen?: string;
}

export interface LogsResponse {
	enabled: boolean;
	logs: LogEntry[];
	total: number;
	bucket: 'app' | 'singbox' | 'mihomo';
	bufferSize: number;
	bufferCapacity: number;
	/** true when every returned entry was sanitized by backend. */
	sanitized?: boolean;
	oldestTimestamp?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Testing — IP check, connectivity, speed
// ─────────────────────────────────────────────

export interface IPResult {
	directIp: string;
	vpnIp: string;
	endpointIp: string;
	ipChanged: boolean;
}

export interface ConnectivityResult {
	connected: boolean;
	latency?: number;
	reason?: string;
	httpCode?: number;
}

export interface IPCheckService {
	label: string;
	url: string;
}

export interface SpeedTestResult {
	server: string;
	direction: 'download' | 'upload';
	bandwidth: number;
	bytes: number;
	duration: number;
	retransmits: number;
}

export interface SpeedTestServer {
	label: string;
	host: string;
	port: number;
}

export interface SpeedTestInfo {
	available: boolean;
	servers: SpeedTestServer[];
}

// #endregion

// ─────────────────────────────────────────────
// #region Diagnostics
// ─────────────────────────────────────────────

export interface DiagnosticsStatus {
	status: 'idle' | 'running' | 'done' | 'error';
	progress: string;
	error?: string;
}

export interface DiagTestEvent {
	name: string;
	description: string;
	status: 'pass' | 'fail' | 'warn' | 'skip' | 'error';
	detail: string;
	tunnelId?: string;
	tunnelName?: string;
	level: 'basic' | 'detailed';
}

export interface DiagDoneSummary {
	total: number;
	passed: number;
	failed: number;
	skipped: number;
	hasReport: boolean;
}

export interface TargetSummary {
	id: string; // '__global__' | tunnelId
	name: string;
	isGlobal: boolean;
	/** Protocol/version badge, e.g. 'xray' | 'hy2' | 'ss' | 'awg2.0' | 'wg' */
	kind?: string;
	tunnelStatus?: 'running' | 'stopped';
	counts: {
		pass: number;
		warn: number;
		fail: number;
		error: number;
		skip: number;
		total: number;
	};
	overallLed: 'gray' | 'green' | 'yellow' | 'red';
}

export const GLOBAL_TARGET_ID = '__global__';

export interface DiagEvent {
	type: 'phase' | 'test' | 'done' | 'error';
	phase?: string;
	label?: string;
	test?: DiagTestEvent;
	summary?: DiagDoneSummary;
	message?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Connections viewer
// ─────────────────────────────────────────────

export interface RuleHit {
	listId: string;
	listName?: string;
	fqdn?: string;
	pattern?: string;
}

export interface ConntrackConnection {
	protocol: string;
	src: string;
	dst: string;
	srcPort: number;
	dstPort: number;
	state: string;
	packets: number;
	bytes: number;
	bytesIn: number;
	bytesOut: number;
	ttl: number;
	routeClass: 'tunnel' | 'singbox' | 'local' | 'direct';
	interface: string;
	tunnelId: string;
	tunnelName: string;
	clientMac: string;
	clientName: string;
	rules?: RuleHit[];
}

export interface ConnectionStats {
	total: number;
	direct: number;
	tunneled: number;
	singbox: number;
	local: number;
	protocols: { tcp: number; udp: number; icmp: number };
}

export interface ConnectionBucketAgg {
	key: string;
	label?: string;
	count: number;
	bytesIn: number;
	bytesOut: number;
}

export interface TunnelConnectionInfo {
	name: string;
	interface: string;
	count: number;
}

export interface ConnectionsPagination {
	total: number;
	offset: number;
	limit: number;
	returned: number;
}

export interface ConnectionsResponse {
	stats: ConnectionStats;
	tunnels: Record<string, TunnelConnectionInfo>;
	byTunnel: ConnectionBucketAgg[];
	byClient: ConnectionBucketAgg[];
	byDst: ConnectionBucketAgg[];
	connections: ConntrackConnection[];
	pagination: ConnectionsPagination;
	fetchedAt: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region SSE Events (re-exports from api/events.ts)
// ─────────────────────────────────────────────

// ─────────────────────────────────────────────
// #region DNS Check
// ─────────────────────────────────────────────

export interface DnsCheckResult {
	id: string;
	status: 'ok' | 'fail' | 'warning' | 'pending';
	title: string;
	message: string;
	detail?: string;
}

export interface DnsCheckStartResponse {
	clientIP: string;
	hostname: string;
	checks: DnsCheckResult[];
}

// #endregion

export type {
	LogEntryEvent,
	SystemBootingEvent,
	TunnelTrafficEvent,
	TunnelConnectivityEvent,
	PingCheckLogEvent
} from '$lib/api/events';

// #endregion
