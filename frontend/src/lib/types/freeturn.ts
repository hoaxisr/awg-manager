// ─────────────────────────────────────────────
// #region FreeTurn — TURN-tunnel client + server config/status
// See https://github.com/samosvalishe/free-turn-proxy/blob/master/docs/flags.md
// ─────────────────────────────────────────────

export interface FreeTurnClientConfig {
	enabled: boolean;
	listen: string;
	peer: string;
	provider: string;
	links?: string;
	streams: number;
	transport: 'tcp' | 'udp';
	mode: 'udp' | 'tcp';
	bond: boolean;
	obfProfile: 'none' | 'rtpopus' | 'rtpopus2' | 'rtpopus3';
	obfKey?: string;
	/** Ключ обфускации задан на бэкенде — значение наружу не отдаётся (Н5). */
	obfKeySet?: boolean;
	streamsPerCred: number;
	platform: 'desktop' | 'mobile';
	dnsMode: 'plain' | 'doh' | 'auto';
	dnsServers?: string;
	clientId?: string;
	sub?: string;
	debug: boolean;
}

export interface FreeTurnServerConfig {
	enabled: boolean;
	listen: string;
	connect: string;
	mode: 'udp' | 'tcp';
	obfProfile: 'none' | 'rtpopus' | 'rtpopus2' | 'rtpopus3';
	obfKey?: string;
	/** Ключ обфускации задан на бэкенде — значение наружу не отдаётся (Н5). */
	obfKeySet?: boolean;
	clientsFile?: string;
	debug: boolean;
	/** Открыть listen-порт в firewall Keenetic (INPUT). undefined = true */
	openFirewall?: boolean;
}

export interface FreeTurnClientInstance {
	id: string;
	name: string;
	config: FreeTurnClientConfig;
	/** Имя старого конфига, из которого запись перенёс посев; пусто у заведённых через UI. */
	seededFrom?: string;
}

export interface FreeTurnServerInstance {
	id: string;
	name: string;
	config: FreeTurnServerConfig;
	/** Имя старого конфига, из которого запись перенёс посев; пусто у заведённых через UI. */
	seededFrom?: string;
}

export interface FreeTurnConfig {
	version?: number;
	clients: FreeTurnClientInstance[];
	servers: FreeTurnServerInstance[];
}

export interface FreeTurnProcessStatus {
	running: boolean;
	pid?: number;
	startedAt?: string;
	lastError?: string;
	log?: string;
	dtlsConnections?: number;
	binary: string;
	binaryPresent: boolean;
	/**
	 * Процесс наш и живой, но pid-файл унаследован. Производителя больше нет:
	 * усыновление по pid-файлу заменил управляющий сокет. Поле всегда пусто.
	 */
	orphanedPid?: boolean;
}

export interface FreeTurnInstanceStatus {
	id: string;
	name: string;
	status: FreeTurnProcessStatus;
}

export interface FreeTurnStatus {
	clients: FreeTurnInstanceStatus[];
	servers: FreeTurnInstanceStatus[];
	/** Legacy mirror of default client instance */
	client: FreeTurnProcessStatus;
	/** Legacy mirror of default server instance */
	server: FreeTurnProcessStatus;
	/** Бинари подсистемы на диске. Принадлежит ПОДСИСТЕМЕ, а не инстансу. */
	binariesPresent?: boolean;
	installAvailable: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	installing: boolean;
	/** Текущее время роутера — для сверки с метками в логе freeturn. */
	routerClock?: string;
}

export interface FreeTurnLinkPayload {
	v: number;
	provider?: string;
	peer?: string;
	transport?: string;
	mode?: string;
	bond?: boolean;
	obf?: string;
	key?: string;
	n?: number;
	spc?: number;
	cid?: string;
	listen?: string;
	dns?: string;
	dnss?: string;
	mcap?: boolean;
	name?: string;
	mtu?: number;
	wg?: string;
}

export interface FreeTurnGenerateLinkRequest {
	peer?: string;
	provider?: string;
	mtu?: number;
	wg?: string;
	clientId?: string;
	name?: string;
	n?: number;
	streamsPerCred?: number;
	serverId?: string;
}

export interface FreeTurnGenerateLinkResult {
	link: string;
	peer: string;
	clientId?: string;
}

export interface FreeTurnAllowlistEntry {
	clientId: string;
	comment?: string;
}

export interface FreeTurnAllowlistStatus {
	enabled: boolean;
	clientsFile?: string;
	clients: FreeTurnAllowlistEntry[];
}

export interface FreeTurnAllowlistAddResult extends FreeTurnAllowlistStatus {
	needsRestart?: boolean;
}

export interface FreeTurnCaptchaClientStatus {
	clientId: string;
	clientName: string;
	waiting: boolean;
	active: boolean;
	queued: boolean;
	canOpen: boolean;
	url?: string;
	pendingStreams?: number;
	portContention?: boolean;
	captchaSession?: number;
}

export interface FreeTurnCaptchaOverview {
	portOpen: boolean;
	ownerClientId?: string;
	ownerName?: string;
	clients: FreeTurnCaptchaClientStatus[];
}

export interface FreeTurnDeleteClientResult {
	deletedTunnels?: string[];
	tunnelErrors?: string[];
}

// #endregion
