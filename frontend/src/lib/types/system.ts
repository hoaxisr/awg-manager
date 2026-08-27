import type { UsageLevel } from './usageLevel';

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
	/** Прошивка умеет интерфейсы OpkgTun (KeeneticOS 5.x) — от них зависят режимы fakeip-tun и policy-tun. */
	supportsOpkgTun?: boolean;
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
	/** Version reported by the module currently in the kernel, "" if not loaded. */
	kernelModuleLoadedVersion?: string;
	/** Loaded awg_proxy version (NativeWG); >= 1.4.0 supports AWG 3.1. */
	awgProxyVersion?: string;
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
	// Легаси-одиночный интерфейс (downgrade-совместимость); новый код
	// читает interfaces.
	interface: string;
	// kernel-имена интерфейсов, на IPv4 которых слушает HTTP-сервер;
	// пусто = все (0.0.0.0). Живая смена — через /server/listen/change.
	interfaces?: string[];
}

// GET /server/listen — текущее состояние HTTP-листенеров.
export interface ServerListenState {
	port: number;
	interfaces: string[];
	boundAddrs: string[];
	pendingConfirm: boolean;
	confirmDeadline?: string;
}

// POST /server/listen/change — живая смена адреса (confirm-or-revert).
export interface ServerListenChangeResult {
	confirmToken: string;
	confirmDeadline: string;
	boundAddrs: string[];
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
	autoInstallEnabled: boolean;
	autoInstallIntervalDays: number;
	autoInstallTime: string;
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

export interface GeoFileSettings {
	autoRefreshEnabled: boolean;
	refreshIntervalHours: number;
	refreshMode?: 'interval' | 'daily';
	refreshDailyTime?: string;
}

export interface Settings {
	schemaVersion?: number;
	authEnabled: boolean;
	/**
	 * Session idle lifetime in hours (1..720, default 24). Optional —
	 * legacy backends omit it; UI falls back to 24.
	 */
	sessionTtlHours?: number;
	/**
	 * Allow login with Entware credentials (/opt/etc/shadow) in addition
	 * to router admin credentials. Optional — legacy backends omit it;
	 * UI treats absence as false.
	 */
	entwareAuthEnabled?: boolean;
	apiKey?: string;
	server: ServerSettings;
	pingCheck: PingCheckSettings;
	logging: LoggingSettings;
	disableMemorySaving: boolean;
	updates: UpdateSettings;
	download: DownloadSettings;
	dnsRoute: DNSRouteSettings;
	geoFile: GeoFileSettings;
	connectivityCheckUrl: string;
	usageLevel: UsageLevel;
	hiddenSystemTunnels?: string[];
	monitoringExcludedTunnels?: string[];
	/**
	 * Адрес bootstrap-резолвера sing-box (dns-bootstrap в 00-base.json):
	 * им резолвятся доменные адреса endpoint'ов туннелей и серверов
	 * подписок. Отвечает раньше любого другого DNS, поэтому только IP.
	 * Пусто — адрес в конфиге не навязывается (issue #770).
	 */
	singboxBootstrapDNS?: string;
	/**
	 * Порт experimental.clash_api.external_controller в 00-base.json.
	 * Хост всегда 127.0.0.1: Clash API — служебный канал управления
	 * awg-manager, а не пользовательский слушатель. 0 — порт по
	 * умолчанию (9099), issue #788.
	 */
	singboxClashPort?: number;
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
	/**
	 * True when login via Entware credentials (/opt/etc/shadow) is enabled.
	 * Present regardless of auth state; optional — legacy backends omit it.
	 */
	entwareAuthEnabled?: boolean;
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
	/** Computed by the auto-install scheduler; absent when auto-install is disabled. */
	nextAutoInstallAt?: string;
	/** Absent until the first auto-install attempt has run. */
	lastAutoInstallAt?: string;
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
