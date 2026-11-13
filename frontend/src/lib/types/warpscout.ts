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
	createdAt: string;
}

export interface WarpAWGParams {
	signatureProfile: string;
	jc: number;
	jmin: number;
	jmax: number;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
}

export interface WarpFilters {
	protocol: WarpProtocol;
	excludeColos: string[];
	allowedCountries: string[];
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
	hasInnerAccount?: boolean;
	accountUnreadable: boolean;
	defaultWan?: string;
	routes?: WarpRouteRef[] | null;
	obfuscationSources?: WarpObfuscationSource[] | null;
	usedIpv4?: string[] | null;
	config: WarpConfig;
	scan: WarpScanStatus;
	watch: WarpWatchStatus[] | null;
	stats?: WarpProfileStats[] | null;
	history: WarpSwapEvent[] | null;
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
}

export interface WarpApplyResult {
	tunnel: WarpTunnelRef | null;
	profileId?: string;
}
