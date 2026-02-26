export interface AWGInterface {
	privateKey: string;
	address: string;
	mtu: number;
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
	warnings?: string[];
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

export interface WANInterface {
	name: string;
	label: string;
	state: string;
}

export interface WANStatus {
	interfaces: Record<string, WANInterfaceStatus>;
	anyWANUp: boolean;
}

export interface WANInterfaceStatus {
	up: boolean;
	label: string;
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
	isDeadByMonitoring?: boolean;
	hasAddressConflict?: boolean;
	rxBytes?: number;
	txBytes?: number;
	lastHandshake?: string;
	awgVersion?: 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0';
	mtu?: number;
	startedAt?: string;
}

export interface TunnelStatus {
	id: string;
	name?: string;
	status: string;
	enabled?: boolean;
	rxBytes: number;
	txBytes: number;
	latestHandshake: string;
}

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

export type KernelModuleDownloadStatus = 'not_needed' | 'downloading' | 'downloaded' | 'download_failed' | 'unsupported';

export interface SystemInfo {
	version: string;
	goVersion: string;
	goArch: string;
	goOS: string;
	keeneticOS: string;
	isOS5: boolean;
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
	kernelModuleDownloadStatus: KernelModuleDownloadStatus;
	kernelModuleDownloadError: string;
	activeBackend: 'kernel' | 'userspace';
}

export interface KmodVersionsInfo {
	versions: string[];
	current: string;
	recommended: string;
}

export interface UpdateInfo {
	available: boolean;
	currentVersion: string;
	latestVersion?: string;
	checkedAt: string;
	checking: boolean;
	error?: string;
}

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
}

export interface UpdateSettings {
	checkEnabled: boolean;
}

export interface Settings {
	schemaVersion?: number;
	authEnabled: boolean;
	server: ServerSettings;
	pingCheck: PingCheckSettings;
	logging: LoggingSettings;
	disableMemorySaving: boolean;
	backendMode: 'auto' | 'kernel' | 'userspace';
	bootDelaySeconds: number;
	updates: UpdateSettings;
}

export interface TunnelPingCheck {
	enabled: boolean;
	useCustomSettings: boolean;
	method: string;
	target: string;
	interval: number;
	deadInterval: number;
	failThreshold: number;
	isDeadByMonitoring: boolean;
	deadSince?: string | null;
}

export interface PingCheckStatus {
	enabled: boolean;
	tunnels: TunnelPingStatus[];
}

export interface TunnelPingStatus {
	tunnelId: string;
	tunnelName: string;
	enabled: boolean;
	status: 'alive' | 'dead' | 'disabled' | 'paused';
	method: string;
	lastCheck?: string;
	lastLatency: number;
	failCount: number;
	failThreshold: number;
	isDeadByMonitor: boolean;
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
}

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

export interface LogEntry {
	timestamp: string;
	level: 'info' | 'warn' | 'error';
	category: 'tunnel' | 'peer' | 'settings' | 'system';
	action: string;
	target: string;
	message: string;
	error?: string;
}

export interface LogsResponse {
	enabled: boolean;
	logs: LogEntry[];
}

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

export interface DeleteResult {
	success: boolean;
	tunnelId: string;
	verified: boolean;
}

export interface BootStatus {
	initializing: boolean;
	remainingSeconds: number;
	phase: 'waiting' | 'starting' | 'ready';
	instanceId: string;
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

export interface Policy {
	id: string;
	name: string;
	clientIP: string;
	clientHostname: string;
	tunnelID: string;
	fallback: 'drop' | 'bypass';
	enabled: boolean;
}

export interface HotspotClient {
	ip: string;
	mac: string;
	hostname: string;
	online: boolean;
}

export interface IPCheckService {
	label: string;
	url: string;
}

export interface SpeedTestInfo {
	available: boolean;
	servers: SpeedTestServer[];
}

export interface DiagnosticsStatus {
	status: 'idle' | 'running' | 'done' | 'error';
	progress: string;
	error?: string;
}
