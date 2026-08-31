export interface WdttClientConfig {
	enabled?: boolean;
	listen: string;
	peer: string;
	password: string;
	/**
	 * Пароль задан на бэкенде. Значение секрета наружу не отдаётся (Н5), а
	 * пустое поле в теле правки означает «не менять» — по нему же считается
	 * «инстанс настроен».
	 */
	passwordSet?: boolean;
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
	/** Имя старого конфига, из которого запись перенёс посев; пусто у заведённых через UI. */
	seededFrom?: string;
}

export interface WdttServerConfig {
	enabled?: boolean;
	listen: string;
	wgPort: number;
	password: string;
	/** Пароль сервера задан на бэкенде — значение наружу не отдаётся (Н5). */
	passwordSet?: boolean;
	configDir?: string;
	/** Токен бота задан на бэкенде — значение наружу не отдаётся (Н5). */
	debug?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	natStaticWan?: string;
	/** Выходы static-NAT для internet-only. Источник правды бэкенда —
	 *  StaticNATList(): список старше одиночки. Мигрированная с post-#750
	 *  конфигом запись несёт ТОЛЬКО его. */
	natStaticWans?: string[];
	policy?: string;
	lanSegments?: string[];
	ingressEnabled?: boolean;
	/** Kernel WG dev (opkgtunN); пусто → legacy wdtt0 */
	wgIface?: string;
	/** Kernel raw dev (opkgtunN); пусто → legacy wdttraw0 (`kernelRawIface`) */
	rawIface?: string;
	/** NDMS id (OpkgTun17..49) when registered in router */
	ndmsIface?: string;
	/** Открыть DTLS-порт в firewall Keenetic (INPUT). undefined = true */
	openFirewall?: boolean;
	/** wg — WireGuard relay; raw — без WG (нужен сервер qWDTT 1.4+ с -listen-raw) */
	relayMode?: 'wg' | 'raw';
	/** UDP-порт Raw (-listen-raw). Пусто → DTLS+1 */
	rawListen?: string;
	/** peer и VK-хеши последней ссылки: чтобы wdtt:// восстанавливалась */
	linkPeer?: string;
	linkVkHashes?: string;
	/** server.log (JSON ~2 с): ram (default), off, disk */
	statsLog?: 'ram' | 'off' | 'disk';
	/**
	 * Показывать интерфейсы сервера роутеру как подключения (public + `ip
	 * global`) — тогда он предлагает их в политиках доступа. Применяется на
	 * старте: живой сервер от смены не перезапускается (`internal/wdtt/types.go`).
	 */
	exposeToPolicies?: boolean;
}

export interface WdttServerInstance {
	id: string;
	name: string;
	config: WdttServerConfig;
	/** Имя старого конфига, из которого запись перенёс посев; пусто у заведённых через UI. */
	seededFrom?: string;
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
	/**
	 * Значение `exposeToPolicies`, с которым РЕАЛЬНО стартовал живой процесс.
	 *
	 * Производителя больше нет: прокси-рантайм применяет тумблер
	 * реконсиляцией, а не «на старте», и понятия «значение, с которым
	 * стартовали» у него не существует. Поле всегда пусто — расхождение с
	 * выбранным показывать не из чего, и бейдж SH-56 молчит.
	 */
	appliedExposeToPolicies?: boolean;
	dtlsConnections?: number;
	binary: string;
	binaryPresent: boolean;
	/**
	 * Процесс наш и живой, но pid-файл унаследован: startedAt нет, надзор слеп.
	 *
	 * Производителя больше нет: усыновление по pid-файлу заменил управляющий
	 * сокет — процесс либо отвечает по нему, либо не наш. Поле всегда пусто.
	 */
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
	/** Бинари подсистемы на диске. Принадлежит ПОДСИСТЕМЕ, а не инстансу. */
	binariesPresent?: boolean;
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
