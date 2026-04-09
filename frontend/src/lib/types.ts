// ─────────────────────────────────────────────
// #region Tunnels — config, state, list items
// ─────────────────────────────────────────────

export interface AWGInterface {
	privateKey: string;
	address: string;
	mtu: number;
	dns?: string;
	jc: number;
	jmin: number;
	jmax: number;
	s1: number;
	s2: number;
	s3: number;
	s4: number;
	h1: string;
	h2: string;
	h3: string;
	h4: string;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
}

export interface AWGPeer {
	publicKey: string;
	presharedKey?: string;
	endpoint: string;
	allowedIPs: string[];
	persistentKeepalive?: number;
}

export interface ConnectivityCheckConfig {
	method: 'http' | 'ping' | 'handshake' | 'disabled';
	pingTarget?: string;
}

export interface TunnelPingCheck {
	enabled: boolean;
	method: string;
	target: string;
	interval: number;
	deadInterval: number;
	failThreshold: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
}

export interface TunnelStateInfo {
	state: number;
	opkgTunExists: boolean;
	interfaceUp: boolean;
	processRunning: boolean;
	processPID: number;
	hasPeer: boolean;
	hasHandshake: boolean;
	lastHandshake: string;
	rxBytes: number;
	txBytes: number;
	error: unknown;
	details?: string;
}

export interface AWGTunnel {
	id: string;
	name: string;
	type: string;
	enabled: boolean;
	defaultRoute: boolean;
	ispInterface?: string;
	ispInterfaceLabel?: string;
	interfaceName?: string;
	configPreview?: string;
	state?: string;
	stateInfo?: TunnelStateInfo;
	interface: AWGInterface;
	peer: AWGPeer;
	pingCheck?: TunnelPingCheck;
	connectivityCheck?: ConnectivityCheckConfig;
	warnings?: string[];
	backend?: 'nativewg' | 'kernel';
}

export interface TunnelListItem {
	id: string;
	name: string;
	type: string;
	status: string;
	enabled: boolean;
	defaultRoute?: boolean;
	ispInterface?: string;
	ispInterfaceLabel?: string;
	resolvedIspInterface?: string;
	resolvedIspInterfaceLabel?: string;
	endpoint: string;
	address: string;
	interfaceName?: string;
	hasAddressConflict?: boolean;
	rxBytes?: number;
	txBytes?: number;
	lastHandshake?: string;
	awgVersion?: 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0';
	mtu?: number;
	startedAt?: string;
	backend?: 'nativewg' | 'kernel';
	connectivityCheck?: ConnectivityCheckConfig;
	pingCheck: {
		status: 'alive' | 'recovering' | 'disabled';
		restartCount: number;
		failCount: number;
		failThreshold: number;
	};
}

export interface DeleteResult {
	success: boolean;
	tunnelId: string;
	verified: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region External & System Tunnels
// ─────────────────────────────────────────────

export interface ExternalTunnel {
	interfaceName: string;
	tunnelNumber: number;
	isAWG: boolean;
	publicKey?: string;
	endpoint?: string;
	lastHandshake?: string;
	rxBytes: number;
	txBytes: number;
}

export interface SystemTunnel {
	id: string;
	interfaceName: string;
	description: string;
	status: 'up' | 'down';
	connected: boolean;
	mtu: number;
	peer?: {
		publicKey: string;
		endpoint: string;
		rxBytes: number;
		txBytes: number;
		lastHandshake: string;
		online: boolean;
	};
}

export interface ASCParamsBase {
	jc: number;
	jmin: number;
	jmax: number;
	s1: number;
	s2: number;
	h1: string;
	h2: string;
	h3: string;
	h4: string;
}

export interface ASCParamsExtended extends ASCParamsBase {
	s3: number;
	s4: number;
	i1: string;
	i2: string;
	i3: string;
	i4: string;
	i5: string;
}

export type ASCParams = ASCParamsBase | ASCParamsExtended;

export interface SignatureCaptureResult {
	ok: boolean;
	source: string;
	packets: {
		i1: string;
		i2: string;
		i3: string;
		i4: string;
		i5: string;
	};
	warning?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Routing — DNS routes, static routes, tunnels
// ─────────────────────────────────────────────

export interface DnsRouteSubscription {
	url: string;
	name: string;
	lastFetched?: string;
	lastCount?: number;
	lastError?: string;
}

export interface DnsRouteTarget {
	interface: string;
	tunnelId: string;
	fallback?: 'auto' | 'reject' | '';
}

export interface DedupeItem {
	domain: string;
	reason: 'exact' | 'wildcard' | 'subnet_covered';
	coveredBy: string;
	listId: string;
	listName: string;
}

export interface DedupeReport {
	totalInput: number;
	totalKept: number;
	totalRemoved: number;
	exactDupes: number;
	wildcardDupes: number;
	items?: DedupeItem[];
}

export interface DnsRoute {
	id: string;
	name: string;
	domains: string[];
	excludes?: string[];
	subnets?: string[];
	manualDomains: string[];
	subscriptions?: DnsRouteSubscription[];
	routes: DnsRouteTarget[];
	enabled: boolean;
	createdAt: string;
	updatedAt: string;
	lastDedupeReport?: DedupeReport;
}

export interface StaticRouteList {
	id: string;
	name: string;
	tunnelID: string;
	subnets: string[];
	fallback?: '' | 'reject';
	enabled: boolean;
	createdAt: string;
	updatedAt: string;
}

export interface RoutingTunnel {
	id: string;
	name: string;
	type: 'managed' | 'system' | 'wan';
	status: string;
	available: boolean;
}

export interface ResolveResult {
	domain: string;
	ips: string[];
	error?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Servers — WireGuard, managed server
// ─────────────────────────────────────────────

export interface WireguardServer {
	id: string;
	interfaceName: string;
	description: string;
	status: 'up' | 'down';
	connected: boolean;
	mtu: number;
	address: string;
	mask: string;
	publicKey: string;
	listenPort: number;
	peers: WireguardServerPeer[];
}

export interface WireguardServerPeer {
	publicKey: string;
	description: string;
	endpoint: string;
	allowedIPs?: string[];
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
	enabled: boolean;
}

export interface WireguardServerConfig {
	publicKey: string;
	listenPort: number;
	mtu: number;
	address: string;
	peers: WireguardServerPeerConfig[];
}

export interface WireguardServerPeerConfig {
	publicKey: string;
	description: string;
	presharedKey: string;
	allowedIPs: string[];
	address: string;
}

export interface ManagedServer {
	interfaceName: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	peers: ManagedPeer[];
}

export interface ManagedPeer {
	publicKey: string;
	privateKey: string;
	presharedKey: string;
	description: string;
	tunnelIP: string;
	dns?: string;
	enabled: boolean;
}

export interface ManagedServerStats {
	status: string;
	peers: ManagedPeerStats[];
}

export interface ManagedPeerStats {
	publicKey: string;
	endpoint: string;
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
}

export interface CreateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
}

export interface UpdateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
}

export interface AddManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
}

export interface UpdateManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Access Policies — ip policy
// ─────────────────────────────────────────────

export interface AccessPolicy {
	name: string;
	description: string;
	standalone: boolean;
	interfaces: AccessPolicyInterface[];
	deviceCount: number;
}

export interface AccessPolicyInterface {
	name: string;
	label?: string;
	order: number;
	denied?: boolean;
}

export interface PolicyDevice {
	mac: string;
	ip: string;
	name: string;
	hostname: string;
	active: boolean;
	link: string;
	policy: string;
}

export interface PolicyGlobalInterface {
	name: string;
	label: string;
	up: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region Client Routes — per-device VPN routing
// ─────────────────────────────────────────────

export interface ClientRoute {
	id: string;
	clientIp: string;
	clientHostname: string;
	tunnelId: string;
	fallback: 'drop' | 'bypass';
	enabled: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region System — info, WAN, interfaces
// ─────────────────────────────────────────────

export interface SystemInfo {
	version: string;
	goVersion: string;
	goArch: string;
	goOS: string;
	keeneticOS: string;
	isOS5: boolean;
	firmwareVersion: string;
	supportsExtendedASC: boolean;
	supportsHRanges: boolean;
	supportsPingCheck: boolean;
	totalMemoryMB: number;
	isLowMemory: boolean;
	gcMemLimit: string;
	gogc: string;
	disableMemorySaving: boolean;
	kernelModuleExists: boolean;
	kernelModuleLoaded: boolean;
	kernelModuleModel: string;
	kernelModuleVersion: string;
	isAarch64: boolean;
	activeBackend: string;
	routerIP: string;
	bootInProgress: boolean;
	backendAvailability: { nativewg: boolean; kernel: boolean };
}

export interface WANInterface {
	name: string;
	label: string;
	state: string;
}

export interface RouterInterface {
	name: string;
	label: string;
	up: boolean;
}

export interface WANStatus {
	interfaces: Record<string, WANInterfaceStatus>;
	anyWANUp: boolean;
}

export interface WANInterfaceStatus {
	up: boolean;
	label: string;
}

export interface TerminalStatus {
	installed: boolean;
	running: boolean;
	sessionActive: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region Settings
// ─────────────────────────────────────────────

export interface ServerSettings {
	port: number;
	interface: string;
}

export interface PingCheckDefaults {
	method: 'http' | 'icmp';
	target: string;
	interval: number;
	deadInterval: number;
	failThreshold: number;
}

export interface PingCheckSettings {
	enabled: boolean;
	defaults: PingCheckDefaults;
}

export interface LoggingSettings {
	enabled: boolean;
	maxAge: number;
	logLevel: string;
}

export interface UpdateSettings {
	checkEnabled: boolean;
}

export interface DNSRouteSettings {
	autoRefreshEnabled: boolean;
	refreshIntervalHours: number;
	refreshMode?: string;       // "interval" (default) or "daily"
	refreshDailyTime?: string;  // "HH:MM" 24h format
}

export interface Settings {
	schemaVersion?: number;
	authEnabled: boolean;
	server: ServerSettings;
	pingCheck: PingCheckSettings;
	logging: LoggingSettings;
	disableMemorySaving: boolean;
	updates: UpdateSettings;
	dnsRoute: DNSRouteSettings;
	hiddenSystemTunnels?: string[];
}

// #endregion

// ─────────────────────────────────────────────
// #region Auth & Boot
// ─────────────────────────────────────────────

export interface AuthStatus {
	authenticated: boolean;
	authDisabled?: boolean;
	login?: string;
	expiresIn?: number;
}

export interface LoginResult {
	success: boolean;
	login: string;
}

export interface BootStatus {
	initializing: boolean;
	remainingSeconds: number;
	phase: 'waiting' | 'starting' | 'ready';
	instanceId: string;
}

export interface UpdateInfo {
	available: boolean;
	currentVersion: string;
	latestVersion?: string;
	checkedAt: string;
	checking: boolean;
	error?: string;
	warning?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region PingCheck — status, logs, native config
// ─────────────────────────────────────────────

export interface NativePingCheckConfig {
	host: string;
	mode: 'icmp' | 'connect' | 'tls' | 'uri';
	updateInterval: number;
	maxFails: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
}

export interface NativePingCheckStatus {
	exists: boolean;
	host: string;
	mode: string;
	interval: number;
	maxFails: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
	bound: boolean;
	status: string;
	failCount: number;
	successCount: number;
}

export interface PingCheckStatus {
	enabled: boolean;
	tunnels: TunnelPingStatus[];
}

export interface TunnelPingStatus {
	tunnelId: string;
	tunnelName: string;
	enabled: boolean;
	backend: 'kernel' | 'nativewg';
	status: 'alive' | 'recovering' | 'disabled';
	method: string;
	lastCheck?: string;
	lastLatency: number;
	failCount: number;
	successCount?: number;
	failThreshold: number;
	restartCount: number;
}

export interface PingLogEntry {
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

// #endregion

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
}

export interface LogsResponse {
	enabled: boolean;
	logs: LogEntry[];
	total: number;
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
	status: 'pass' | 'fail' | 'skip' | 'error';
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

export interface DiagEvent {
	type: 'phase' | 'test' | 'done' | 'error';
	phase?: string;
	label?: string;
	test?: DiagTestEvent;
	summary?: DiagDoneSummary;
	message?: string;
}

export type DiagMode = 'quick' | 'full';

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
	protocols: { tcp: number; udp: number; icmp: number };
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
	connections: ConntrackConnection[];
	pagination: ConnectionsPagination;
	fetchedAt: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region SSE Events (re-exports from api/events.ts)
// ─────────────────────────────────────────────

export type {
	TunnelStateEvent,
	TunnelDeletedEvent,
	TunnelCreatedEvent,
	TunnelUpdatedEvent,
	LogEntryEvent,
	PingCheckStateEvent,
	SystemBootingEvent,
	SnapshotTunnelsEvent,
	SnapshotServersEvent,
	SnapshotRoutingEvent,
	SnapshotPingcheckEvent,
	SnapshotLogsEvent,
	TunnelTrafficEvent,
	TunnelConnectivityEvent,
	PingCheckLogEvent
} from '$lib/api/events';

// #endregion
