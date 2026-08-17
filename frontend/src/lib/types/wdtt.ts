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
	/** Peer для режима WG (DTLS-порт); если пуст — используется peer. */
	peerWg?: string;
	/** Peer для режима Raw; если пуст — используется peer. */
	peerRaw?: string;
	debug?: boolean;
	/** Raw client: OpkgTun17..49 для маршрутизации LAN (NAT — на wdtt-server) */
	ndmsIface?: string;
	rawIface?: string;
	rawClientIp?: string;
	rawClientMTU?: number;
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
	/** NDMS id (OpkgTun17..49) when registered in router */
	ndmsIface?: string;
	/** Открыть DTLS-порт в firewall Keenetic (INPUT). undefined = true */
	openFirewall?: boolean;
	/** wg — WireGuard relay; raw — без WG (нужен сервер qWDTT 1.4+ с -listen-raw) */
	relayMode?: 'wg' | 'raw';
	/** UDP-порт Raw (-listen-raw). Пусто → DTLS+1 */
	rawListen?: string;
	/** WG peer-порт (-listen-direct, WRAP без DTLS). Пусто → DTLS-порт */
	directListen?: string;
	/** Клиенты сервера — источник правды, panel.db собирается из них */
	clients?: WdttServerClient[];
	/** peer и VK-хеши последней ссылки: чтобы wdtt:// восстанавливалась */
	linkPeer?: string;
	linkVkHashes?: string;
	/** server.log (JSON ~2 с): ram (default), off, disk */
	statsLog?: 'ram' | 'off' | 'disk';
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
	rawClientIp?: string;
	rawIface?: string;
	ndmsIface?: string;
	/** NDMS-имя raw-интерфейса сервера; пусто на старом бинаре (без -raw-iface) */
	rawNdmsIface?: string;
	dtlsConnections?: number;
	binary: string;
	binaryPresent: boolean;
	/** Процесс наш и живой, но pid-файл унаследован: startedAt нет, надзор слеп */
	orphanedPid?: boolean;
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
	isDeactivated: boolean;
	/** Срок, назначенный сервером, истёк: в passwords.json не пишется */
	isExpired: boolean;
	/** Пароль абонента совпадает с главным паролем сервера */
	isMainPassword: boolean;
	/** Абонента завёл инвариант непустоты списка */
	isAuto: boolean;
}

/**
 * Судьба SIGHUP после изменения состава абонентов. Заполняют только мутации
 * состава (добавление, удаление); у чтения и переименования поля нет.
 */
export type WdttServerClientsReload = 'delivered' | 'serverStopped' | 'failed';

export interface WdttPanelUsersStatus {
	available: boolean;
	users: WdttPanelUserEntry[];
	reload?: WdttServerClientsReload;
}
