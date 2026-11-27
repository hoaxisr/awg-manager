// Типы модуля «WARP Сканер» (internal/warpscout, internal/api/warpscout.go).
// Поля повторяют json-теги Go-структур один в один.

export type WarpTargetMode = 'system' | 'native' | 'kernel';
export type WarpProfileMode = 'single' | 'multi';
export type WarpProtocol = 'awg' | 'wg';

export interface WarpAccount {
	id: string;
	publicKey: string;
	peerPublicKey: string;
	addressV4: string;
	addressV6?: string;
	clientId?: string;
	accountType?: string;
	warpPlus: boolean;
	hasToken?: boolean;
	imported?: boolean;
	createdAt: string;
}

export interface WarpRegisterOptions {
	proxyUrl?: string;
	relayUrl?: string;
}

export type WarpSignatureHostPreset = 'default' | 'ru_whitelist' | 'cdn' | 'custom';

export interface WarpAWGParams {
	signatureProfile: string;
	signatureHostPreset?: WarpSignatureHostPreset;
	signatureDomain?: string;
	generatedDomain?: string;
	jc: number;
	jmin: number;
	jmax: number;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	timingJitter?: boolean;
	contentPadding?: string;
	randomTrailers?: boolean;
	disableCookies?: boolean;
}

export interface WarpFilters {
	protocol: WarpProtocol;
	excludeColos: string[];
	allowedCountries: string[];
	adaptive?: boolean;
 searchSeconds?: number;
 maxRttMs: number;
	requireTelegram: boolean;
	maxCandidates?: number;
	ports?: number[];
}

export interface WarpProfile {
	id: string;
	name: string;
	enabled: boolean;
	targetMode: WarpTargetMode;
	targetId: string;
	filters: WarpFilters;
	checkIntervalSec: number;
	failThreshold: number;
	cooldownSec: number;
}

export interface WarpConfig {
	profileMode: WarpProfileMode;
	activeProfileId?: string;
	scanInterface?: string;
	autoRename?: boolean;
	profiles: WarpProfile[];
	awg: WarpAWGParams;
}

export interface WarpRouteRef {
	name: string;
	label: string;
	kind: 'wan' | 'tunnel' | 'warp' | string;
	up: boolean;
	tunnelMode?: WarpTargetMode;
	tunnelId?: string;
}

export interface WarpObfuscationSource {
	id: string;
	name: string;
	iface?: string;
	mode: WarpTargetMode;
	running: boolean;
	isWarp: boolean;
	awg: WarpAWGParams;
}

export interface WarpHit {
 awg?: WarpAWGParams;
	endpoint: string;
	handshakeMs: number;
	pingMs: number;
	colo: string;
	loc: string;
	warp: string;
	tgOk: boolean;
}

export interface WarpScanStatus {
	running: boolean;
	owner?: string;
	startedAt?: string;
	finishedAt?: string;
	total: number;
	done: number;
	hits: WarpHit[] | null;
	error?: string;
	aborted?: boolean;
	elapsedMs?: number;
}

export interface WarpSwapEvent {
	at: string;
	profileId: string;
	profileName: string;
	targetMode: WarpTargetMode;
	targetId: string;
	reason: string;
	oldEndpoint: string;
	newEndpoint: string;
	colo: string;
	loc: string;
	pingMs: number;
	ok: boolean;
	error?: string;
}

export interface WarpTraceResult {
	colo: string;
	loc: string;
	warp: string;
	ip: string;
	latencyMs: number;
}

export interface WarpCheckSample {
	at: string;
	ok: boolean;
	latencyMs?: number;
	colo?: string;
	loc?: string;
	swapped?: boolean;
	reason?: string;
}

export interface WarpProfileStats {
	profileId: string;
	startedAt?: string;
	endpointSince?: string;
	currentEndpoint?: string;
	checksTotal: number;
	checksOk: number;
	checksFail: number;
	swapsOk: number;
	swapsFail: number;
	latencySumMs?: number;
	latencyCount?: number;
	minLatencyMs?: number;
	maxLatencyMs?: number;
	avgLatencyMs?: number;
	colos?: Record<string, number>;
	samples?: WarpCheckSample[];
}

export type WarpWatchState =
	| 'idle'
	| 'ok'
	| 'degraded'
	| 'swapping'
	| 'backoff'
	| 'wan_down'
	| 'target_missing'
	| 'target_stopped'
	| 'no_account'
	| 'engine_missing'
	| 'protected'
	| 'not_warp'
	| 'engine_busy';

export interface WarpWatchStatus {
	profileId: string;
	state: WarpWatchState | string;
	message?: string;
	lastCheck?: string;
	trace?: WarpTraceResult;
	fails: number;
	endpoint?: string;
	nextSwapAfter?: string;
	lastSwap?: WarpSwapEvent;
}

export interface WarpEngineInfo {
	path: string;
	available: boolean;
}

export interface WarpScoutState {
	engine: WarpEngineInfo;
	account: WarpAccount | null;
	accountKeyInUseBy?: string;
	accountKeyCheckError?: string;
	hasInnerAccount?: boolean;
	accountUnreadable: boolean;
	defaultWan?: string;
	routes?: WarpRouteRef[] | null;
	obfuscationSources?: WarpObfuscationSource[] | null;
	usedIpv4?: string[] | null;
	config: WarpConfig;
	scan: WarpScanStatus;
	findJunk?: WarpFindJunkStatus;
	watch: WarpWatchStatus[] | null;
	stats?: WarpProfileStats[] | null;
	history: WarpSwapEvent[] | null;
	supportsAsc3?: boolean;
	supportsKernelAwg31?: boolean;
}

export interface WarpFindJunkRequest {
	i1Profile: string;
	hostPreset?: WarpSignatureHostPreset;
	domains?: string;
	maxAttempts?: number;
	thresholdPct?: number;
	perSubnet?: number;
	ifname?: string;
	autoApply?: boolean;
}

export interface WarpFindJunkApplyRequest {
	attempt?: number;
	timingJitter?: boolean;
	contentPadding?: string;
	randomTrailers?: boolean;
	disableCookies?: boolean;
}

export interface WarpJunkAttempt {
	attempt: number;
	awg: WarpAWGParams;
	profile: string;
	domain?: string;
	working: number;
	scanned: number;
	total: number;
	pct: number;
	avgPingMs?: number;
	durationMs: number;
	incomplete?: boolean;
	goodEnough?: boolean;
}

export interface WarpFindJunkStatus {
	runId?: number;
	running: boolean;
	startedAt?: string;
	finishedAt?: string;
	i1Profile?: string;
	hostPreset?: WarpSignatureHostPreset;
	domains?: string;
	attempt: number;
	maxAttempts: number;
	thresholdPct: number;
	attempts: WarpJunkAttempt[];
	best?: WarpJunkAttempt;
	applied?: boolean;
	appliedAttempt?: number;
	aborted?: boolean;
	error?: string;
}

export interface WarpTunnelRef {
	mode: WarpTargetMode;
	id: string;
	name: string;
	iface: string;
	endpoint: string;
	addressV4?: string;
	isWarp: boolean;
	running: boolean;
	protected: boolean;
	protectReason?: string;
	viaMode?: WarpTargetMode;
	viaTunnelId?: string;
	viaIface?: string;
	viaLabel?: string;
	viaEndpoint?: string;
	viaRunning?: boolean;
}

export interface WarpScanRequest {
	filters: WarpFilters;
	maxHits: number;
	maxCandidates: number;
	ifname?: string;
}

export interface WarpApplyRequest {
	action: 'create' | 'swap';
	endpoint: string;
	colo?: string;
	mode: WarpTargetMode;
	targetId?: string;
	name?: string;
	addressV4?: string;
	protocol?: WarpProtocol;
	viaMode?: WarpTargetMode;
	viaId?: string;
	awg?: WarpAWGParams;
	enableAutopilot: boolean;
	filters?: WarpFilters;
	reuseAccount?: boolean;
}

export interface WarpApplyResult {
	tunnel: WarpTunnelRef | null;
	profileId?: string;
	accountReused?: boolean;
}

export interface WarpExportRequest {
	endpoint: string;
	colo?: string;
	protocol?: WarpProtocol;
	mtu?: number;
	awg?: WarpAWGParams;
}

export interface WarpExportResult {
	filename: string;
	content: string;
}
