import type { UsageLevel } from './types/usageLevel';

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
	ndmsName?: string;
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
	address?: string; // IPv4 e.g. "10.8.1.3"
	mask?: string;
	uptime?: number; // seconds since up
	peer?: {
		publicKey: string;
		endpoint: string;
		via?: string; // ISP/connection iface (e.g. "PPPoE0")
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
	/** Raw excludes editor text. Preserves comments and blank lines; active excludes are derived from it. */
	excludesText?: string;
	excludeSubnets?: string[];
	subnets?: string[];
	manualDomains: string[];
	/** Raw manual editor text. Preserves comments and blank lines; active entries are derived from it. */
	manualText?: string;
	subscriptions?: DnsRouteSubscription[];
	routes: DnsRouteTarget[];
	enabled: boolean;
	createdAt: string;
	updatedAt: string;
	lastDedupeReport?: DedupeReport;
	backend?: 'ndms' | 'hydraroute';
	hrRouteMode?: 'interface' | 'policy';
	hrPolicyName?: string;
	/**
	 * Tunnel IDs permitted in a newly-created HR policy, in priority order.
	 * Only honored when hrRouteMode === 'policy' and the policy is new.
	 * Absent for existing-policy and interface-mode flows.
	 */
	hrPolicyInterfaces?: string[];
	/** Optional URL for a custom icon (e.g. Qure CDN PNG or user-supplied URL). */
	iconUrl?: string;
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
	/** Optional URL for a custom icon (e.g. Qure CDN PNG or user-supplied URL). */
	iconUrl?: string;
}

export interface RoutingTunnel {
	id: string;
	name: string;
	iface?: string; // kernel interface name ("nwg0", "opkgtun10", "ppp0"); used to match HR file targets
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
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	lanSegments?: string[];
	policy: string;
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
	description?: string;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	generateAsc?: boolean;
}

// UpdateManagedServerRequest matches the Go-side pointer-field semantics:
// - omit a field entirely (do not include it in the body) to PRESERVE the existing value
// - include a field (even with empty string / 0) to SET it (empty string CLEARS)
// Build the payload conditionally on the call site so a value the user
// didn't touch never appears in the request.
export interface UpdateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	description?: string;
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
	/** true for Policy0..PolicyN; false for HydraRoute Neo and other custom NDMS profiles */
	isStandard?: boolean;
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

export interface HydraRouteStatus {
	installed: boolean;
	running: boolean;
	version?: string;
	pid?: number;
	stalePid?: number;
	processState?: 'not_installed' | 'stopped' | 'running' | 'dead';
	lastError?: string;
}

export interface HydraRouteConfig {
	autoStart: boolean;
	clearIPSet: boolean;
	cidr: boolean;
	ipsetEnableTimeout: boolean;
	ipsetTimeout: number;
	ipsetMaxElem: number;
	directRouteEnabled: boolean;
	globalRouting: boolean;
	conntrackFlush: boolean;
	log: string;
	logFile: string;
	geoIPFiles: string[];
	geoSiteFiles: string[];
	policyOrder: string[];
}

export interface GeoFileEntry {
	type: 'geosite' | 'geoip';
	path: string;
	url: string;
	size: number;
	tagCount: number;
	updated: string;
	/** True for files discovered in hrneo.conf but not managed by awg-manager. */
	external?: boolean;
}

export interface DownloadRoute {
	tag: string;
	kind?: 'direct' | 'awg' | 'singbox' | 'subscription';
}

export interface DownloadOutbound {
	tag: string;
	kind: 'direct' | 'awg' | 'singbox' | 'subscription';
	label: string;
	detail?: string;
	available: boolean;
}

export interface GeoTag {
	name: string;
	count: number;
}

export interface IpsetUsage {
	maxElem: number;
	usage: Record<string, number>;
}

export interface OversizedTag {
	name: string;
	count: number;
	file: string;
}

export interface HydraRouteOversizedResponse {
	installed: boolean;
	maxelem: number;
	tags: OversizedTag[];
}

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
	routerTime?: string;
	routerTimezone?: string;
	routerTimezoneOffsetMinutes?: number;
	bootInProgress: boolean;
	/** >0 when started with -slow-request-ms (init script); drives Profiling log filter chip */
	slowRequestThresholdMs?: number;
	backendAvailability: { nativewg: boolean; kernel: boolean };
	/** Why NativeWG is unavailable (empty when available): 'no-component' | 'no-obfuscation'. */
	nativewgReason?: string;
	singbox?: {
		installed: boolean;
		version: string;
	};
	routerDetails?: {
		model?: string;
		modelDisplay?: string;
		portedBuild?: boolean;
		hardwareId?: string;
		region?: string;
		architecture?: string;
		cpuModel?: string;
		cpuTempC?: number;
		wifi24TempC?: number;
		wifi5TempC?: number;
		memoryUsedMB?: number;
		memoryTotalMB?: number;
		memoryUsedPercent?: number;
		firmwareTitle?: string;
		firmwareRelease?: string;
		firmwareSandbox?: string;
		firmwareBuildDate?: string;
		bootSlot?: string;
		uptimeHuman?: string;
		loadAverage?: string;
		opkgStorage?: string;
		vpnComponents?: string[];
		storageComponents?: string[];
		featureComponents?: string[];
		meshMembers?: string[];
	};
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
	singboxLogLevel: string;
	appMaxEntries: number;
	singboxMaxEntries: number;
}

export interface UpdateSettings {
	checkEnabled: boolean;
	channel: 'stable' | 'develop';
}

export interface DownloadSettings {
	routeTag: string;
	routeKind?: 'direct' | 'awg' | 'singbox' | 'subscription';
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
	apiKey?: string;
	server: ServerSettings;
	pingCheck: PingCheckSettings;
	logging: LoggingSettings;
	disableMemorySaving: boolean;
	updates: UpdateSettings;
	download: DownloadSettings;
	dnsRoute: DNSRouteSettings;
	connectivityCheckUrl: string;
	usageLevel: UsageLevel;
	hiddenSystemTunnels?: string[];
	monitoringExcludedTunnels?: string[];
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

export interface ChangelogGroup {
	heading: string;
	items: string[];
}

export interface ChangelogEntry {
	version: string;
	date: string;
	groups: ChangelogGroup[];
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
	status: 'alive' | 'recovering' | 'disabled' | 'stopped';
	method: string;
	lastCheck?: string;
	lastLatency: number;
	failCount: number;
	successCount?: number;
	failThreshold: number;
	restartCount: number;
	tunnelRunning?: boolean;
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
// #region Device Proxy
// ─────────────────────────────────────────────

export interface DeviceProxyAuth {
	enabled: boolean;
	username: string;
	password: string;
}

export interface DeviceProxyConfig {
	enabled: boolean;
	listenAll: boolean;
	listenInterface: string;
	port: number;
	auth: DeviceProxyAuth;
	selectedOutbound: string;
}

export interface DeviceProxyInstance extends DeviceProxyConfig {
	id: string;
	name: string;
}

export type DeviceProxyOutboundKind = 'direct' | 'singbox' | 'subscription' | 'awg';

export interface DeviceProxyOutbound {
	tag: string;
	kind: DeviceProxyOutboundKind;
	label: string;
	detail: string;
}

export interface DeviceProxyRuntime {
	alive: boolean;
	activeTag: string;
	defaultTag: string;
}

export interface DeviceProxyInstanceIPCheckResult {
	directIp: string;
	proxyIp: string;
	ipChanged: boolean;
	service: string;
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
	/** true when target/message were sanitized by backend before delivery. */
	sanitized?: boolean;
}

export interface LogsResponse {
	enabled: boolean;
	logs: LogEntry[];
	total: number;
	bucket: 'app' | 'singbox';
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

// ─────────────────────────────────────────────
// #region Sing-box
// ─────────────────────────────────────────────

export interface SingboxTunnel {
	tag: string;
	protocol: 'vless' | 'hysteria2' | 'naive' | 'trojan' | 'shadowsocks' | 'mieru';
	server: string;
	port: number;
	security: 'reality' | 'tls' | 'none';
	transport: 'tcp' | 'grpc' | 'quic' | 'https';
	listenPort: number;
	proxyInterface: string;
	sni?: string;
	fingerprint?: string;
	username?: string;
	connectivity: {
		connected: boolean;
		latency: number | null;
	};
	kernelInterface?: string;
	/**
	 * True when sing-box process is alive AND the TUN interface (t2sX)
	 * exists in the kernel. Distinct from connectivity.connected, which
	 * reports latency health from the Clash API. Running=false with
	 * connected=true is impossible; running=true with connected=false
	 * means "process up, but outbound not reachable" (bad server, etc).
	 */
	running: boolean;
}

export interface SingboxStatus {
	installed: boolean;
	version?: string;
	running: boolean;
	pid?: number;
	tunnelCount: number;
	proxyComponent: boolean;
	/** Mirrors Settings.CreateNDMSProxyForSingbox. When false, the UI hides ProxyComponent warnings. */
	ndmsProxyEnabled: boolean;
	/**
	 * Build tags of the installed sing-box binary (parsed from
	 * `sing-box version` Tags: line). Missing when not installed.
	 * Example: ["with_gvisor","with_quic","with_naive_outbound"].
	 */
	features?: string[];
	/**
	 * Last fatal stderr message captured when sing-box exited (if any).
	 * Surfaced when `running === false` to explain why the daemon is
	 * down — typically a config-validation FATAL after a rule-set
	 * download failed.
	 */
	lastError?: string;
	/** Version of the currently installed sing-box binary. Missing when not installed. */
	currentVersion?: string;
	/** Minimum required sing-box version for full functionality. */
	requiredVersion: string;
	/** SHA256 of the currently installed sing-box binary. Missing when not installed. */
	currentSha256?: string;
	/** SHA256 of the sing-box binary pinned to this awg-manager build. */
	requiredSha256?: string;
	/** True when the installed sing-box version or SHA256 differs from the pinned binary. */
	updateAvailable: boolean;
	/**
	 * Классификация состояния installation: 'installed' | 'missing' |
	 * 'missing_no_space' | 'outdated_no_space' | 'installing' | 'error'.
	 */
	installState?: string;
	/** Требуемое свободное место (байты) для скачивания/обновления sing-box. */
	requiredBytes?: number;
	/** Свободное место (байты) на FS, где хранится managed binary. */
	freeBytes?: number;
}

export interface SingboxImportResponse {
	imported: SingboxTunnel[];
	errors: Array<{ line: number; input: string; error: string }>;
	tunnels: SingboxTunnel[]; // fresh full list
}

/**
 * Response envelope payload for GET /api/singbox/config-preview.
 * `json` is the pretty-printed merged sing-box config produced by
 * stitching all `01-*.json` fragments onto `00-base.json`.
 */
export interface SingboxConfigPreview {
	json: string;
}

export interface SingboxTraffic {
	tag: string;
	upload: number;
	download: number;
}

export interface SingboxDelayEvent {
	tag: string;
	delay: number;
	timestamp: number;
}

// #endregion

// #region Monitoring (Phase 3)

export interface MonitoringTarget {
	id: string;
	host: string;
	name: string;
	url?: string;
}

export interface MonitoringTunnel {
	id: string;
	name: string;
	ifaceName: string;
	pingcheckTarget: string;
	selfTarget: string;
	selfMethod: string;
	/** "awg" | "system" | "singbox" — drives row visual hints. */
	source?: 'awg' | 'system' | 'singbox';
	/** AWG backend kind (for source==='awg'): "kernel" | "nativewg". */
	backend?: 'kernel' | 'nativewg' | 'system' | string;
	/** AWG protocol/version kind (for source==='awg'). */
	awgVersion?: 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0' | string;
	/** Managed AWG tunnel has "default route" enabled. */
	defaultRoute?: boolean;
	/** Sing-box row came from subscription member list. */
	subscription?: boolean;
	/** Optional sing-box metadata for badge rendering. */
	protocol?: string;
	security?: string;
	transport?: string;
	/** Sing-box outbound tag; empty unless source==='singbox'. */
	singboxTag?: string;
	/** Last Clash urltest delay in ms; 0 = no urltest data. */
	clashDelay?: number;
	/** urltest group tag this sing-box tunnel belongs to. */
	urltestGroup?: string;
}

export interface MonitoringCell {
	targetId: string;
	tunnelId: string;
	latencyMs: number | null;
	ok: boolean;
	activeForRestart: boolean;
	isSelf: boolean;
	ts: string;
}

export interface MonitoringSnapshot {
	targets: MonitoringTarget[];
	tunnels: MonitoringTunnel[];
	cells: MonitoringCell[];
	updatedAt: string;
}

export interface MonitoringSample {
	ts: string;
	latencyMs: number | null;
	ok: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region Singbox Router (Phase 2 — TProxy routing engine)
// ─────────────────────────────────────────────

export interface SingboxRouterSettings {
	enabled: boolean;
	policyName: string;
	deviceMode?: 'policy' | 'all';
	snifferEnabled: boolean;
	refreshMode?: 'interval' | 'daily';
	refreshIntervalHours?: number;
	refreshDailyTime?: string;
	// WAN-binding discriminator (mirrors backend storage):
	//   wanAutoDetect=true  + wanInterface=""    → sing-box auto_detect_interface
	//   wanAutoDetect=false + wanInterface="X"   → sing-box default_interface=X
	// All other combinations are invalid; backend validator rejects them.
	wanAutoDetect: boolean;
	wanInterface?: string; // kernel system-name (e.g. "ppp0"); empty when wanAutoDetect=true
	bypassPresets?: string[];
	bypassExtraPorts?: string;
	ingressInterfaces?: string[];
}

// WAN interface for the sing-box router WAN-binding picker. `name` is
// the kernel system-name (stable across NDMS re-creation) and is what
// gets persisted into SingboxRouterSettings.wanInterface. `up` is
// info-only — never gates selection (UI shows all, user picks).
export interface SingboxRouterWANInterface {
	name: string;
	id: string;
	label: string;
	up: boolean;
	priority: number;
}

export interface SingboxRouterIssue {
	severity: 'warning' | 'error';
	kind: 'orphan-rule' | 'policy-missing';
	ruleIndex?: number;
	tag?: string;
	message: string;
}

export interface SingboxRouterStatus {
	enabled: boolean;
	installed: boolean;
	/**
	 * Interception path is actually live: chains exist AND PREROUTING jumps
	 * into them. `installed` alone only proves the chains exist — the jumps
	 * can be wiped while chains survive, so the engine looks installed but
	 * routes nothing. Drive the "working" badge on `active`, not `enabled`.
	 */
	active: boolean;
	netfilterAvailable: boolean;
	netfilterComponentName?: string;
	tproxyTargetAvailable: boolean;
	policyName: string;
	policyMark?: string;
	policyExists: boolean;
	deviceMode: 'policy' | 'all';
	snifferEnabled: boolean;
	deviceCount: number;
	ruleCount: number;
	ruleSetCount: number;
	outboundAwgCount: number;
	outboundCompositeCount: number;
	final: string;
	issues?: SingboxRouterIssue[];
}

/**
 * One NDMS access policy as exposed to the router-policy UI dropdown.
 * Source: GET /api/singbox/router/policies.
 */
export interface RouterPolicy {
	name: string;
	description: string;
	mark: string;
	deviceCount: number;
	isOurDefault: boolean;
}

export interface SingboxRouterRule {
	domain_suffix?: string[];
	ip_cidr?: string[];
	source_ip_cidr?: string[];
	port?: number[];
	rule_set?: string[];
	protocol?: string;
	// When true, matches packets whose destination is private (RFC1918,
	// loopback, link-local, CGNAT, multicast). System ip_is_private
	// bypass rule has this set + outbound:"direct".
	ip_is_private?: boolean;
	// Optional — sing-box defaults to `route` when omitted. The system
	// ip_is_private rule omits action because that's how SKeen's
	// reference config writes it and the backend's `omitempty` mirrors
	// the same shape.
	action?: 'route' | 'reject' | 'sniff' | 'hijack-dns';
	outbound?: string;
}

/**
 * One per-rule decision from the route inspector. matchedRule == -1 in
 * SingboxRouterInspectResult means no rule produced a final destination
 * — the route.final outbound was used instead.
 */
export interface SingboxRouterInspectMatch {
	index: number;
	matched: boolean;
	action: string;
	outbound?: string;
	conditions?: string[];
	reason?: string;
}

export interface SingboxRouterInspectResult {
	input: string;
	inputType: 'domain' | 'ip';
	matches: SingboxRouterInspectMatch[];
	destination: string;
	matchedRule: number;
	final: string;
	note?: string;
}

export interface SingboxRouterInspectRequest {
	domain: string;
	port?: number;
	protocol?: string;
}

export interface SingboxRouterInspectProgress {
	phase: string;
	message: string;
	ruleIndex?: number;
	ruleTotal?: number;
	ruleSetTag?: string;
	ruleSetIndex?: number;
	ruleSetTotal?: number;
	final?: string;
	usingDraft?: boolean;
}

export interface SingboxRouterRuleSet {
	tag: string;
	type: 'remote' | 'local' | 'inline';
	format?: 'binary' | 'source';
	url?: string;
	update_interval?: string;
	download_detour?: string;
	path?: string;
	rules?: Record<string, unknown>[];
	/** True when a compiled .srs sibling exists (inline only). */
	materialized_srs?: boolean;
}

export interface SingboxRouterOutbound {
	type: 'direct' | 'urltest' | 'selector' | 'loadbalance';
	tag: string;
	bind_interface?: string;
	outbounds?: string[];
	url?: string;
	interval?: string;
	tolerance?: number;
	default?: string;
	strategy?: string;
	/**
	 * Which orchestrator slot owns this outbound. "router" entries are
	 * editable from the UI; "subscription" entries are managed by the
	 * subscription service and shown read-only.
	 */
	source?: 'router' | 'subscription';
}

/**
 * Live state of one composite outbound (selector / urltest / loadbalance).
 * Returned by GET /api/singbox/router/proxies/list.
 */
export interface SingboxProxyMember {
	tag: string;
	type: string;
	/** Last latency in ms; 0 = not tested or unreachable. */
	lastDelay?: number;
}

export interface SingboxProxyGroup {
	tag: string;
	type: 'selector' | 'urltest' | 'loadbalance';
	now: string;
	members: SingboxProxyMember[];
}

export interface SingboxProxiesListResponse {
	groups: SingboxProxyGroup[];
}

export interface SingboxProxiesSelectRequest {
	group: string;
	member: string;
}

export interface SingboxProxiesTestRequest {
	group: string;
	url?: string;
	timeout?: number;
}

export interface SingboxProxiesTestResponse {
	/** memberTag → delay in ms; 0 = unreachable. */
	delays: Record<string, number>;
}

export interface SingboxRouterPresetLink {
	ruleSetRef: string;
	actionTarget: 'tunnel' | 'reject' | 'direct';
}

export type SingboxRouterPresetCategory =
	| 'social'
	| 'media'
	| 'ai'
	| 'developer'
	| 'cloud'
	| 'gaming'
	| 'block';

export interface SingboxRouterPreset {
	id: string;
	name: string;
	category?: SingboxRouterPresetCategory;
	iconSlug?: string;
	ruleSets: Array<{ tag: string; url: string }>;
	rules: SingboxRouterPresetLink[];
	notice?: string;
	covers?: string[];
	featured?: boolean;
	sensitive?: boolean;
}

export interface SingboxRouterAvailableClient {
	mac: string;
	name?: string;
	ip?: string;
	registered?: boolean;
	active?: boolean;
}

export type SingboxRouterDNSType = 'udp' | 'tls' | 'https' | 'quic' | 'h3' | 'local';

export type SingboxRouterDNSStrategy =
	| ''
	| 'prefer_ipv4'
	| 'prefer_ipv6'
	| 'ipv4_only'
	| 'ipv6_only';

export interface SingboxRouterDNSDomainResolver {
	server: string;
	strategy?: SingboxRouterDNSStrategy;
}

export interface SingboxRouterDNSServer {
	tag: string;
	type: SingboxRouterDNSType;
	server: string;
	server_port?: number;
	path?: string;
	detour?: string;
	domain_strategy?: SingboxRouterDNSStrategy;
	domain_resolver?: SingboxRouterDNSDomainResolver;
}

export interface SingboxRouterDNSRule {
	rule_set?: string[];
	domain_suffix?: string[];
	domain?: string[];
	domain_keyword?: string[];
	domain_regex?: string[];
	query_type?: string[];
	server?: string;
	action?: '' | 'route' | 'reject' | 'predefined';
	rcode?: string;
	method?: string;
}

export interface SingboxRouterDNSGlobals {
	final: string;
	strategy: SingboxRouterDNSStrategy;
}

export interface SingboxRouterDNSRewrite {
	pattern: string;
	ips: string[];
}

// #endregion

// ─────────────────────────────────────────────
// #region AWG outbounds catalog (15-awg.json)
// ─────────────────────────────────────────────

export interface AWGTagInfo {
	tag: string;
	label: string;
	kind: 'managed' | 'system';
	iface: string;
}

/**
 * Structured payload returned by DELETE /api/tunnels/{id} when the
 * tunnel is referenced by deviceproxy or a router rule (HTTP 409).
 * The frontend uses this to render TunnelReferencedModal.
 */
export interface TunnelReferencedError {
	tunnelId: string;
	deviceProxy: boolean;
	// Go marshals empty (nil) slices as null, so these arrive null when empty.
	routerRules: number[] | null;
	routerOther: string[] | null;
}

// #endregion

// === Amnezia Premium (cp.amnezia.org via backend proxy) ===

/** Country row from GET account-info `data.available_countries`. */
export interface AmneziaPremiumCountry {
	server_country_code: string;
	server_country_name: string;
}

/** Запись из `data.issued_configs` (уже выданные конфиги в Amnezia CP). */
export interface AmneziaPremiumIssuedConfig {
	installation_uuid?: string;
	/** Время последнего изменения адреса/воркера на стороне CP. */
	worker_last_updated?: string;
	/** Время последней выдачи конфига клиенту; если раньше worker_last_updated — конфиг устарел. */
	last_downloaded?: string;
	server_country_code?: string;
	server_country_name?: string;
	source_type?: string;
	os_version?: string;
}

/** Nested JSON under Amnezia CP account-info `data`. */
export interface AmneziaPremiumAccountInfo {
	http_status?: number;
	available_countries?: AmneziaPremiumCountry[];
	issued_configs?: AmneziaPremiumIssuedConfig[];
	subscription_status?: string;
	vpn_key?: string;
}

// === Subscriptions ===

export interface SubscriptionHeader {
	name: string;
	value: string;
}

export interface SubscriptionMember {
	tag: string;
	label?: string;
	protocol: string;
	server: string;
	port: number;
	sni?: string;
	transport?: string;
	security?: string;
}

export type SubscriptionMode = 'selector' | 'urltest';

export interface SubscriptionURLTest {
	url: string;
	intervalSec: number;
	toleranceMs: number;
}

export const DEFAULT_SUBSCRIPTION_URLTEST: SubscriptionURLTest = {
	url: 'https://www.gstatic.com/generate_204',
	intervalSec: 60,
	toleranceMs: 50,
};

export interface SubscriptionInfoItem {
	id: string;
	label: string;
	tag?: string;
	source?: 'auto' | 'user' | string;
}

export interface SubscriptionRejectedMember {
	tag?: string;
	label?: string;
	protocol?: string;
	server?: string;
	port?: number;
	reason: string;
}

export interface Subscription {
	id: string;
	label: string;
	url: string;
	isInline: boolean;
	headers: SubscriptionHeader[];
	refreshHours: number;
	lastFetched: string; // RFC 3339, "" when never fetched
	lastError?: string;
	selectorTag: string;
	inboundTag: string;
	listenPort: number;
	proxyIndex: number;
	memberTags: string[];
	members: SubscriptionMember[];
	orphanTags: string[];
	rejectedMembers?: SubscriptionRejectedMember[];
	infoItems?: SubscriptionInfoItem[];
	activeMember: string;
	enabled: boolean;
	mode: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
}

export interface SubscriptionRefreshResult {
	when: string;
	added: number;
	updated: number;
	orphaned: number;
	skippedVmess: number;
	skippedOther: number;
	skippedDuplicate: number;
	parseErrors?: string[];
}

export interface SubscriptionActiveNowResponse {
	now: string;
}

export interface CreateSubscriptionInput {
	label: string;
	url?: string;
	inline?: string;
	headers: SubscriptionHeader[];
	refreshHours: number;
	enabled: boolean;
	mode?: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
}

export interface UpdateSubscriptionInput {
	label?: string;
	url?: string;
	headers?: SubscriptionHeader[];
	refreshHours?: number;
	enabled?: boolean;
	mode?: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
}

// ─────────────────────────────────────────────
// #region Managed Server Backup / Restore
// ─────────────────────────────────────────────

/**
 * Single managed server entry as exported to a backup file.
 * Shape mirrors storage.ManagedServer JSON; policy is optional
 * because newly-created servers may not have one assigned yet.
 */
export interface ManagedServerExport {
	interfaceName: string;
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	policy?: string;
	privateKey?: string;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	peers: ManagedPeer[];
}

export interface ManagedServerBackupFile {
	version: number;
	type: string;
	exportedAt: string;
	managedServers: ManagedServerExport[];
	warnings?: Array<{
		interfaceName?: string;
		message: string;
	}>;
}

export interface RestoreOptions {
	allowRenumber: boolean;
}

export interface RestoreOutcome {
	name: string;
	newName?: string;
	action: 'created' | 'merged' | 'renamed' | 'conflict' | 'failed';
	addedPeers?: number;
	conflicts?: string[];
	error?: string;
}

export interface ManagedServerRestoreResponse {
	outcomes: RestoreOutcome[];
}

export interface ManagedServerDriftResponse {
	drift: ManagedServerExport[];
}

// #endregion

// === Singbox Router Staging ===

export interface RouterValidationErrorDTO {
	slot: string;
	kind: string;
	tag?: string;
	inRule?: string;
	message: string;
}

export interface RouterValidationDTO {
	errors: RouterValidationErrorDTO[];
}

export interface RouterStagingStatusResponse {
	hasDraft: boolean;
	draftedAt?: string;
	validation?: RouterValidationDTO;
}

export interface RouterStagingValidationError {
	validation?: RouterValidationDTO;
	sbCheck?: string;
}

// ─────────────────────────────────────────────
// #region DNS Proxy Info
// ─────────────────────────────────────────────

export interface DnsUpstream {
	address: string;
	port: number;
	encryption: 'DoT' | 'DoH' | 'plain';
	sni: string;
	scope: string; // 'all' | 'ru' | ...
	rSent: number;
	aRcvd: number;
	nxRcvd: number;
	medResp: string;
	avgResp: string;
	rank: number;
}

export interface DnsStaticRecord {
	host: string;
	type: 'A' | 'AAAA';
	value: string;
	flag: number;
}

export interface DnsRebind {
	enabled: boolean;
	nets: string[];
	excludes: string[];
}

export interface DnsProxyStat {
	totalRequests: number;
	proxyRequestsSent: number;
	cacheHitRatio: number;
	cacheHits: number;
	memory: string;
}

export interface DnsProxy {
	name: string;
	displayName: string;
	tcpPort: number;
	udpPort: number;
	stat: DnsProxyStat;
	upstreams: DnsUpstream[];
	staticRecords: DnsStaticRecord[];
	rebind: DnsRebind;
}

export interface DnsProxyInfo {
	proxies: DnsProxy[];
}

// #endregion

// ─────────────────────────────────────────────
// #region Preset Catalog
// ─────────────────────────────────────────────

export interface PresetRuleRef {
	tag: string;
	url: string;
}
export interface PresetDNSEngine {
	domains?: string[];
	subnets?: string[];
	subscriptionUrl?: string;
}
export interface PresetSingboxEngine {
	ruleSets?: PresetRuleRef[];
	action: string;
}
export interface PresetHydraRouteEngine {
	geoTags?: string[];
}
export interface PresetEngines {
	dns?: PresetDNSEngine;
	singbox?: PresetSingboxEngine;
	hydraroute?: PresetHydraRouteEngine;
}
export interface CatalogPreset {
	id: string;
	name: string;
	iconSlug: string;
	category: string;
	notice?: string;
	covers?: string[];
	featured?: boolean;
	sensitive?: boolean;
	origin: 'builtin' | 'user';
	engines: PresetEngines;
}

// #endregion
