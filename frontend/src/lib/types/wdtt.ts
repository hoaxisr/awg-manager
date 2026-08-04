export interface WdttClientConfig {
	enabled?: boolean;
	listen: string;
	peer: string;
	password: string;
	vkHashes: string;
	workers: number;
	obfs: string;
	fingerprint: string;
	deviceId?: string;
	captchaMode: string;
	vkAuthMode?: string;
	sub?: string;
	/** wg — WireGuard + AWG-туннель; raw — без WG (быстрее, нужен raw-сервер) */
	connMode?: 'wg' | 'raw';
	debug?: boolean;
}

export interface WdttClientInstance {
	id: string;
	name: string;
	config: WdttClientConfig;
}

export interface WdttServerConfig {
	enabled?: boolean;
	listen: string;
	wgPort: number;
	password: string;
	configDir?: string;
	adminId?: string;
	botToken?: string;
	debug?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	natStaticWan?: string;
	policy?: string;
	lanSegments?: string[];
	ingressEnabled?: boolean;
	natIface?: string;
	/** Kernel WG dev (opkgtunN); пусто → legacy wdtt0 */
	wgIface?: string;
	/** NDMS id (OpkgTun90..99) when registered in router */
	ndmsIface?: string;
	/** Открыть DTLS-порт в firewall Keenetic (INPUT). undefined = true */
	openFirewall?: boolean;
	/** wg — WireGuard relay; raw — без WG (нужен редеплой сервера) */
	relayMode?: 'wg' | 'raw';
	/** Клиенты сервера — источник правды, panel.db собирается из них */
	clients?: WdttServerClient[];
	/** peer и VK-хеши последней ссылки: чтобы wdtt:// восстанавливалась */
	linkPeer?: string;
	linkVkHashes?: string;
}

export interface WdttServerClient {
	password: string;
	comment?: string;
	vkHash?: string;
}

export interface WdttServerInstance {
	id: string;
	name: string;
	config: WdttServerConfig;
}

export interface WdttConfig {
	version?: number;
	clients: WdttClientInstance[];
	servers: WdttServerInstance[];
}

export interface WdttProcessStatus {
	running: boolean;
	pid?: number;
	startedAt?: string;
	lastError?: string;
	log?: string;
	wgConfig?: string;
	dtlsConnections?: number;
	binary: string;
	binaryPresent: boolean;
}

export interface WdttInstanceStatus {
	id: string;
	name: string;
	status: WdttProcessStatus;
}

export interface WdttStatus {
	clients: WdttInstanceStatus[];
	servers: WdttInstanceStatus[];
	client: WdttProcessStatus;
	server: WdttProcessStatus;
	/** Собирается ли wdtt-server под арку роутера (на mips/mipsel — нет). */
	serverSupported?: boolean;
	installAvailable: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable: boolean;
	installing: boolean;
	routerClock?: string;
}

export interface WdttImportPayload {
	name?: string;
	peer: string;
	password: string;
	vkHashes: string[];
	workers?: number;
	listen?: string;
	subUrl?: string;
	deviceId?: string;
	wg?: string;
	connMode?: 'wg' | 'raw';
}

export interface WdttSubscriptionPreview {
	name: string;
	description?: string;
	trafficUsedMb?: number;
	trafficLimitMb?: number;
	updatedAt?: string;
	subUrl: string;
	profiles: WdttImportPayload[];
}

export interface WdttLinkDecodeResult {
	profile?: WdttImportPayload;
	subscription?: WdttSubscriptionPreview;
}

export interface WdttGenerateLinkResult {
	link: string;
	linkQwdtt?: string;
	peer: string;
}

export interface WdttPanelUserEntry {
	password: string;
	comment?: string;
	vkHash?: string;
	isMain: boolean;
	isDeactivated: boolean;
	deviceCount: number;
	lastSeenAt?: number;
}

export interface WdttPanelUsersStatus {
	panelDbPath?: string;
	available: boolean;
	users: WdttPanelUserEntry[];
}
