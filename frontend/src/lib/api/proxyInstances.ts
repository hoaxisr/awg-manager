// Поверхность прокси-рантайма `/api/proxyrt/*` и мапперы в прежние модели
// представления страницы «Прокси».
//
// Страница и её компоненты читают `WdttStatus`/`WdttConfig`/`FreeTurnStatus`/
// `FreeTurnConfig` — модели старого мира. Новый мир отдаёт ОДИН список
// инстансов всех четырёх ролей плюс отдельный install-статус подсистемы.
// Здесь живёт перевод между ними: типы ответа новой поверхности, прямые
// мапперы (ответ → модель представления) и обратные (форма → тело PATCH).
//
// Таблица «старый путь → новый путь» (пути регистрирует проводка, задача 14):
//
//   GET    /wdtt/config                     → GET  /proxyrt/instances        (toWdttConfig)
//   GET    /wdtt/status                     → GET  /proxyrt/instances + /proxyrt/install/status?subsystem=wdtt
//   POST   /wdtt/clients                    → POST /proxyrt/instances        (kind=wdtt-client)
//   PUT    /wdtt/clients/{id}               → PATCH /proxyrt/instances/wdtt-client:{id}
//   PATCH  /wdtt/clients/{id}               → PATCH /proxyrt/instances/wdtt-client:{id}   (name)
//   DELETE /wdtt/clients/{id}               → POST …/linked-tunnels/clear + DELETE /proxyrt/instances/wdtt-client:{id}
//   POST   /wdtt/clients/{id}/start|stop    → PATCH /proxyrt/instances/wdtt-client:{id}   ({enabled})
//   POST   /wdtt/clients/{id}/ensure-wg-tunnel → POST /proxyrt/instances/wdtt-client:{id}/ensure-wg-tunnel
//   POST   /wdtt/clients/{id}/ensure-raw-tunnel → УДАЛЕНА (зеркальную запись ведёт движок)
//   POST   /wdtt/clients/{id}/subscription/refresh → POST /proxyrt/instances/wdtt-client:{id}/subscription/refresh
//   POST   /wdtt/link/decode                → POST /proxyrt/wdtt/link/decode
//   POST   /wdtt/install                    → POST /proxyrt/install          ({subsystem:'wdtt'})
//   POST   /wdtt/servers                    → POST /proxyrt/instances        (kind=wdtt-server)
//   PUT    /wdtt/servers/{id}               → PATCH /proxyrt/instances/wdtt-server:{id}
//   POST   /wdtt/servers/{id}/nat|policy|lan-segments → PATCH /proxyrt/instances/wdtt-server:{id} (config)
//   POST   /wdtt/servers/{id}/link          → POST /proxyrt/instances/wdtt-server:{id}/link
//   *      /wdtt/servers/{id}/users[/{pass}] → * /proxyrt/instances/wdtt-server:{id}/users[/{pass}]
//   GET    /freeturn/config                 → GET  /proxyrt/instances        (toFreeTurnConfig)
//   GET    /freeturn/status                 → GET  /proxyrt/instances + /proxyrt/install/status?subsystem=freeturn
//   GET    /freeturn/captcha/status         → GET  /proxyrt/freeturn/captcha/status
//   POST   /freeturn/link/decode            → POST /proxyrt/freeturn/link/decode
//   POST   /freeturn/install                → POST /proxyrt/install          ({subsystem:'freeturn'})
//   *      /freeturn/servers/{id}/allowlist[/{cid}] → * /proxyrt/instances/freeturn-server:{id}/allowlist[/{cid}]
//   POST   /freeturn/server[s/{id}]/link    → POST /proxyrt/instances/freeturn-server:{id}/link
//
// Секреты (Н5): ответ отдаёт не значение, а признак `passwordSet`/`botTokenSet`/
// `obfKeySet`; пустое поле секрета в теле правки означает «не менять», поэтому
// обратные мапперы пустой секрет НЕ шлют.
//
// Поля ЗАПИСИ против полей конфига роли: `sub` (подписка wdtt-клиента) и
// `statsLog` (режим журнала статистики сервера) лежат на записи и едут
// отдельными полями тела PATCH, мимо `config`. Семантика у них не секретная:
// пустая строка — законное значение («подписки больше нет», «дефолтный
// режим»), «не менять» означает только отсутствие поля.

import type {
	FreeTurnCaptchaOverview,
	FreeTurnClientConfig,
	FreeTurnConfig,
	FreeTurnProcessStatus,
	FreeTurnServerConfig,
	FreeTurnStatus,
	WdttClientConfig,
	WdttConfig,
	WdttProcessStatus,
	WdttServerConfig,
	WdttStatus,
} from '$lib/types';

// ─────────────────────────────────────────────
// #region Формы ответа новой поверхности
// ─────────────────────────────────────────────

export type ProxyKind = 'wdtt-client' | 'wdtt-server' | 'freeturn-client' | 'freeturn-server';

/**
 * Блок процесса инстанса — форма зафиксирована ручкой инстансов
 * (`api.ProcessView`). `running` — не поле снимка: снимок есть И pid > 0.
 */
export interface ProxyProcessView {
	running: boolean;
	pid?: number;
	/** Адрес из НАБЛЮДЕНИЯ процесса (rawClientIp старого мира). */
	address?: string;
	uptimeS?: number;
	lastError?: string;
	mode?: string;
	wgConfig?: string;
	/** dtlsConnections старого мира. */
	clients?: number;
	log?: string;
	binary: string;
	binaryPresent: boolean;
}

/** Один ресурс инстанса в состоянии реконсиляции. */
export interface ProxyResourceView {
	id: string;
	status: string;
	detail?: string;
	error?: string;
}

/** Шаг последнего плана реконсиляции. */
export interface ProxyStepView {
	resource: string;
	op: string;
	args?: Record<string, string>;
	reason?: string;
}

/** Состояние реконсиляции инстанса; отсутствует, пока движок не публиковал. */
export interface ProxyStateView {
	intent: string;
	phase: string;
	resources?: ProxyResourceView[];
	lastPlan?: ProxyStepView[];
	updatedAt?: string;
}

/**
 * Состояние посева. Признака ДВА, и слить их нельзя: `seeded` — запуск
 * подсистемы состоялся, `certified` — посев подтверждён реестру и уборка
 * разрешена. «Гейт заперт» (seeded && !certified) обязано быть видно.
 */
export interface ProxySeedView {
	seeded: boolean;
	certified: boolean;
	error?: string;
	/**
	 * Старые конфиги, которые посев не разобрал и пропустил: их инстансы не
	 * перенесены. Признак отдельный от `error` — только по имени файла можно
	 * сказать пользователю, ЧЬИ инстансы потеряны.
	 */
	skipped?: ProxySkippedSourceView[];
	/**
	 * Инстансы, которым посев сменил listen-адрес, разводя конфликт за порт:
	 * дефолт у обеих подсистем был один и тот же. Молчать об этом нельзя — у
	 * человека снаружи мог быть настроен клиент на прежний порт.
	 */
	movedListen?: ProxyListenMoveView[];
}

/** Один пропущенный посевом старый конфиг. */
export interface ProxySkippedSourceView {
	file: string;
	reason?: string;
}

/** Один переезд listen-адреса, сделанный посевом. */
export interface ProxyListenMoveView {
	instance: string;
	name?: string;
	from: string;
	to: string;
}

export interface ProxyInstanceView {
	key: string;
	id: string;
	kind: ProxyKind;
	name: string;
	enabled: boolean;
	createdAt?: string;
	sub?: string;
	peerWg?: string;
	peerRaw?: string;
	linkPeer?: string;
	linkVkHashes?: string;
	statsLog?: string;
	config: Record<string, unknown>;
	state?: ProxyStateView;
	process: ProxyProcessView;
}

export interface ProxyListData {
	seed: ProxySeedView;
	instances: ProxyInstanceView[];
}

/** Ответ GET /proxyrt/install/status — семь полей install-блока старого статуса. */
export interface ProxyInstallStatus {
	serverSupported?: boolean;
	installAvailable?: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	installing?: boolean;
	routerClock?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Адресация
// ─────────────────────────────────────────────

/**
 * Ключ инстанса новой поверхности. `id` уникален только ВНУТРИ роли
 * («default» есть у всех четырёх), поэтому адрес — роль:id.
 */
export function instanceKey(kind: ProxyKind, id: string): string {
	return `${kind}:${id}`;
}

/** Путь ручки инстанса (хвост — уже готовые сегменты). */
export function instancePath(kind: ProxyKind, id: string, tail = ''): string {
	return `/proxyrt/instances/${encodeURIComponent(instanceKey(kind, id))}${tail}`;
}

// #endregion

// ─────────────────────────────────────────────
// #region Чтение полей конфига
// ─────────────────────────────────────────────

type Cfg = Record<string, unknown>;

function str(cfg: Cfg, key: string): string | undefined {
	const v = cfg[key];
	return typeof v === 'string' ? v : undefined;
}

function num(cfg: Cfg, key: string): number | undefined {
	const v = cfg[key];
	return typeof v === 'number' ? v : undefined;
}

function bool(cfg: Cfg, key: string): boolean | undefined {
	const v = cfg[key];
	return typeof v === 'boolean' ? v : undefined;
}

function strArr(cfg: Cfg, key: string): string[] | undefined {
	const v = cfg[key];
	return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : undefined;
}

function instancesOf(list: ProxyListData, kind: ProxyKind): ProxyInstanceView[] {
	return list.instances.filter((i) => i.kind === kind);
}

/**
 * Режим NAT сервера — ОДИН дефолт на чтение и на запись.
 *
 * Поле сериализуется с пропуском пустого, и у записи, которой NAT никогда не
 * задавали, значения в ответе нет вовсе. Разойдись дефолты — интерфейс врал бы
 * о том, что сделает: контрол показывал бы «Полный» (его собственный дефолт),
 * а сохранение формы записало бы другое. Дефолт «полный» — тот же, что у
 * контрола и у подписи схемы (`shareConfig.natModeOptions`/`natModeLabel`), и
 * тот же, что был в старом мире.
 */
function natModeOf(mode: string | undefined): NonNullable<WdttServerConfig['natMode']> {
	switch (mode) {
		case 'internet-only':
		case 'none':
			return mode;
		default:
			return 'full';
	}
}

// #endregion

// ─────────────────────────────────────────────
// #region Прямые мапперы: конфиг
// ─────────────────────────────────────────────

/**
 * Конфиг wdtt-клиента в модели представления. `sub`/`peerWg`/`peerRaw` живут
 * на самой записи, а не в конфиге роли; `password` наружу не уходит — вместо
 * него признак `passwordSet`.
 */
export function toWdttClientConfig(v: ProxyInstanceView): WdttClientConfig {
	const c = v.config;
	return {
		enabled: v.enabled,
		listen: str(c, 'listen') ?? '',
		peer: str(c, 'peer') ?? '',
		password: '',
		passwordSet: bool(c, 'passwordSet') === true,
		vkHashes: str(c, 'vkHashes') ?? '',
		workers: num(c, 'workers') ?? 0,
		obfs: str(c, 'obfs') ?? '',
		fingerprint: str(c, 'fingerprint') ?? '',
		deviceId: str(c, 'deviceId'),
		captchaMode: str(c, 'captchaMode') ?? '',
		vkAuthMode: str(c, 'vkAuthMode'),
		sub: v.sub,
		connMode: str(c, 'connMode') === 'raw' ? 'raw' : 'wg',
		peerWg: v.peerWg,
		peerRaw: v.peerRaw,
		ndmsIface: str(c, 'ndmsIface'),
		rawIface: str(c, 'rawIface'),
	};
}

/**
 * Конфиг wdtt-сервера в модели представления. `linkPeer`/`linkVkHashes`/
 * `statsLog` лежат на записи; `clients` (абоненты) новой поверхностью здесь не
 * отдаются — их знает только ручка абонентов, и урезанный список был бы
 * приманкой.
 */
export function toWdttServerConfig(v: ProxyInstanceView): WdttServerConfig {
	const c = v.config;
	return {
		enabled: v.enabled,
		listen: str(c, 'listen') ?? '',
		wgPort: num(c, 'wgPort') ?? 0,
		password: '',
		passwordSet: bool(c, 'passwordSet') === true,
		configDir: str(c, 'configDir'),
		adminId: str(c, 'adminId'),
		botToken: '',
		botTokenSet: bool(c, 'botTokenSet') === true,
		debug: bool(c, 'debug'),
		natMode: natModeOf(str(c, 'natMode')),
		natStaticWan: str(c, 'natStaticWan'),
		policy: str(c, 'policy'),
		lanSegments: strArr(c, 'lanSegments'),
		natIface: str(c, 'natIface'),
		wgIface: str(c, 'wgIface'),
		rawIface: str(c, 'rawIface'),
		ndmsIface: str(c, 'ndmsIface'),
		openFirewall: bool(c, 'openFirewall'),
		relayMode: str(c, 'relayMode') === 'raw' ? 'raw' : 'wg',
		rawListen: str(c, 'rawListen'),
		directListen: str(c, 'directListen'),
		linkPeer: v.linkPeer,
		linkVkHashes: v.linkVkHashes,
		statsLog: v.statsLog as WdttServerConfig['statsLog'],
		exposeToPolicies: bool(c, 'exposeToPolicies'),
	};
}

export function toFreeTurnClientConfig(v: ProxyInstanceView): FreeTurnClientConfig {
	const c = v.config;
	return {
		enabled: v.enabled,
		listen: str(c, 'listen') ?? '',
		peer: str(c, 'peer') ?? '',
		provider: str(c, 'provider') ?? '',
		links: str(c, 'links'),
		streams: num(c, 'streams') ?? 0,
		transport: (str(c, 'transport') as FreeTurnClientConfig['transport']) ?? 'tcp',
		mode: (str(c, 'mode') as FreeTurnClientConfig['mode']) ?? 'udp',
		bond: bool(c, 'bond') === true,
		turnHost: str(c, 'turnHost'),
		turnPort: num(c, 'turnPort'),
		obfProfile: (str(c, 'obfProfile') as FreeTurnClientConfig['obfProfile']) ?? 'none',
		obfKey: '',
		obfKeySet: bool(c, 'obfKeySet') === true,
		streamsPerCred: num(c, 'streamsPerCred') ?? 0,
		platform: (str(c, 'platform') as FreeTurnClientConfig['platform']) ?? 'desktop',
		dnsMode: (str(c, 'dnsMode') as FreeTurnClientConfig['dnsMode']) ?? 'auto',
		dnsServers: str(c, 'dnsServers'),
		clientId: str(c, 'clientId'),
		sub: str(c, 'sub'),
		debug: bool(c, 'debug') === true,
	};
}

export function toFreeTurnServerConfig(v: ProxyInstanceView): FreeTurnServerConfig {
	const c = v.config;
	return {
		enabled: v.enabled,
		listen: str(c, 'listen') ?? '',
		connect: str(c, 'connect') ?? '',
		mode: (str(c, 'mode') as FreeTurnServerConfig['mode']) ?? 'udp',
		obfProfile: (str(c, 'obfProfile') as FreeTurnServerConfig['obfProfile']) ?? 'none',
		obfKey: '',
		obfKeySet: bool(c, 'obfKeySet') === true,
		clientsFile: str(c, 'clientsFile'),
		debug: bool(c, 'debug') === true,
		openFirewall: bool(c, 'openFirewall'),
	};
}

export function toWdttConfig(list: ProxyListData): WdttConfig {
	return {
		clients: instancesOf(list, 'wdtt-client').map((v) => ({
			id: v.id,
			name: v.name,
			config: toWdttClientConfig(v),
		})),
		servers: instancesOf(list, 'wdtt-server').map((v) => ({
			id: v.id,
			name: v.name,
			config: toWdttServerConfig(v),
		})),
	};
}

export function toFreeTurnConfig(list: ProxyListData): FreeTurnConfig {
	return {
		clients: instancesOf(list, 'freeturn-client').map((v) => ({
			id: v.id,
			name: v.name,
			config: toFreeTurnClientConfig(v),
		})),
		servers: instancesOf(list, 'freeturn-server').map((v) => ({
			id: v.id,
			name: v.name,
			config: toFreeTurnServerConfig(v),
		})),
	};
}

// #endregion

// ─────────────────────────────────────────────
// #region Прямые мапперы: статус
// ─────────────────────────────────────────────

/**
 * Момент старта процесса из аптайма снимка: новая поверхность отдаёт `uptimeS`,
 * а страница считает «в работе» от метки времени. Аптайма нет — метки тоже нет:
 * выдумывать «стартовал сейчас» нельзя, на этой метке стоит признак «инстанс
 * уже поднимался» (proxyOpsMode).
 */
export function startedAtFromUptime(uptimeS: number | undefined, nowMs: number): string | undefined {
	if (!uptimeS || uptimeS <= 0) return undefined;
	return new Date(nowMs - uptimeS * 1000).toISOString();
}

/**
 * Процесс инстанса в модели представления.
 *
 * Умирают классом (производителя в новом мире нет, маппер отдаёт undefined):
 * `appliedExposeToPolicies` — движок применяет тумблер реконсиляцией, а не
 * «на старте»; `orphanedPid` — усыновление pid-файла заменил управляющий
 * сокет.
 */
function toProcessStatus(v: ProxyInstanceView, nowMs: number): FreeTurnProcessStatus {
	const p = v.process;
	return {
		running: p.running === true,
		pid: p.pid,
		startedAt: p.running ? startedAtFromUptime(p.uptimeS, nowMs) : undefined,
		lastError: p.lastError,
		log: p.log,
		dtlsConnections: p.clients,
		binary: p.binary ?? '',
		binaryPresent: p.binaryPresent === true,
	};
}

function toWdttProcessStatus(v: ProxyInstanceView, nowMs: number): WdttProcessStatus {
	const c = v.config;
	return {
		...toProcessStatus(v, nowMs),
		wgConfig: v.process.wgConfig,
		// rawClientIp — адрес из наблюдения процесса (ProcessView.Address).
		rawClientIp: v.process.address,
		rawIface: str(c, 'rawIface'),
		ndmsIface: str(c, 'ndmsIface'),
		rawNdmsIface: str(c, 'rawNdmsIface'),
	};
}

function wdttInstanceStatus(v: ProxyInstanceView, nowMs: number) {
	return { id: v.id, name: v.name, status: toWdttProcessStatus(v, nowMs) };
}

function ftInstanceStatus(v: ProxyInstanceView, nowMs: number) {
	return { id: v.id, name: v.name, status: toProcessStatus(v, nowMs) };
}

/**
 * Пустой блок процесса для legacy-зеркал `client`/`server`: их читает полоса
 * бинарей. Инстансов подсистемы может не быть вовсе, а признак наличия бинаря
 * новая поверхность несёт только внутри инстанса.
 */
function emptyProcess(): WdttProcessStatus {
	return { running: false, binary: '', binaryPresent: false };
}

export function toWdttStatus(
	list: ProxyListData,
	install: ProxyInstallStatus,
	nowMs: number = Date.now(),
): WdttStatus {
	const clients = instancesOf(list, 'wdtt-client').map((v) => wdttInstanceStatus(v, nowMs));
	const servers = instancesOf(list, 'wdtt-server').map((v) => wdttInstanceStatus(v, nowMs));
	return {
		clients,
		servers,
		client: clients[0]?.status ?? emptyProcess(),
		server: servers[0]?.status ?? emptyProcess(),
		serverSupported: install.serverSupported,
		installAvailable: install.installAvailable === true,
		installVersion: install.installVersion,
		installedVersion: install.installedVersion,
		updateAvailable: install.updateAvailable === true,
		installing: install.installing === true,
		routerClock: install.routerClock,
	};
}

export function toFreeTurnStatus(
	list: ProxyListData,
	install: ProxyInstallStatus,
	nowMs: number = Date.now(),
): FreeTurnStatus {
	const clients = instancesOf(list, 'freeturn-client').map((v) => ftInstanceStatus(v, nowMs));
	const servers = instancesOf(list, 'freeturn-server').map((v) => ftInstanceStatus(v, nowMs));
	return {
		clients,
		servers,
		client: clients[0]?.status ?? emptyProcess(),
		server: servers[0]?.status ?? emptyProcess(),
		installAvailable: install.installAvailable === true,
		installVersion: install.installVersion,
		installedVersion: install.installedVersion,
		updateAvailable: install.updateAvailable === true,
		installing: install.installing === true,
		routerClock: install.routerClock,
	};
}

/**
 * Обзор капчи в прежней форме. `clientId` новой поверхности несёт ПОЛНЫЙ ключ
 * инстанса (`freeturn-client:default`), а страница адресует инстансы голым id —
 * иначе секция капчи не нашла бы свой инстанс и молча замолчала бы.
 */
export function toCaptchaOverview(raw: FreeTurnCaptchaOverview): FreeTurnCaptchaOverview {
	return {
		...raw,
		ownerClientId: raw.ownerClientId ? bareId(raw.ownerClientId) : raw.ownerClientId,
		clients: (raw.clients ?? []).map((c) => ({ ...c, clientId: bareId(c.clientId) })),
	};
}

/** Голый id из ключа инстанса; строка без роли отдаётся как есть. */
export function bareId(key: string): string {
	const idx = key.indexOf(':');
	return idx < 0 ? key : key.slice(idx + 1);
}

// #endregion

// ─────────────────────────────────────────────
// #region Обратные мапперы: форма → тело PATCH
// ─────────────────────────────────────────────

/**
 * Секрет в тело правки: пустое значение НЕ шлётся — на новой поверхности
 * пустой секрет означает «не менять», и отправка пустой строки была бы
 * попыткой стереть пароль (Н5).
 */
function putSecret(out: Cfg, key: string, value: string | undefined): void {
	if (value && value.trim()) out[key] = value;
}

/**
 * Конфиг wdtt-клиента в тело PATCH.
 *
 * `policies` НЕ шлётся: писателя членства в политиках на странице нет, а
 * присланный срез заменяет старый ЦЕЛИКОМ — пустой массив снёс бы permit'ы.
 * Пины (`ndmsIface`/`rawIface`) тоже не шлются: их выделяет менеджер.
 * `peerWg`/`peerRaw` не шлются — слот активного режима store заполняет сам из
 * `peer` и `connMode`.
 */
export function toWdttClientPatch(cfg: WdttClientConfig): Cfg {
	const out: Cfg = {
		connMode: cfg.connMode === 'raw' ? 'raw' : 'wg',
		listen: cfg.listen ?? '',
		peer: cfg.peer ?? '',
		vkHashes: cfg.vkHashes ?? '',
		workers: cfg.workers ?? 0,
		obfs: cfg.obfs ?? '',
		fingerprint: cfg.fingerprint ?? '',
		deviceId: cfg.deviceId ?? '',
		captchaMode: cfg.captchaMode ?? '',
		vkAuthMode: cfg.vkAuthMode ?? '',
	};
	putSecret(out, 'password', cfg.password);
	return out;
}

/**
 * Конфиг wdtt-сервера в тело PATCH. Пины половин (`wgIface`/`rawIface`/
 * `ndmsIface`/`natIface`) не шлются — их выделяет менеджер, а форма их не
 * правит.
 */
export function toWdttServerPatch(cfg: WdttServerConfig): Cfg {
	const out: Cfg = {
		listen: cfg.listen ?? '',
		wgPort: cfg.wgPort ?? 0,
		configDir: cfg.configDir ?? '',
		adminId: cfg.adminId ?? '',
		rawListen: cfg.rawListen ?? '',
		directListen: cfg.directListen ?? '',
		relayMode: cfg.relayMode === 'raw' ? 'raw' : 'wg',
		natMode: natModeOf(cfg.natMode),
		natStaticWan: cfg.natStaticWan ?? '',
		policy: cfg.policy ?? '',
		lanSegments: cfg.lanSegments ?? [],
		debug: cfg.debug === true,
		exposeToPolicies: cfg.exposeToPolicies === true,
		openFirewall: cfg.openFirewall !== false,
	};
	putSecret(out, 'password', cfg.password);
	putSecret(out, 'botToken', cfg.botToken);
	return out;
}

export function toFreeTurnClientPatch(cfg: FreeTurnClientConfig): Cfg {
	const out: Cfg = {
		listen: cfg.listen ?? '',
		peer: cfg.peer ?? '',
		provider: cfg.provider ?? '',
		links: cfg.links ?? '',
		streams: cfg.streams ?? 0,
		transport: cfg.transport ?? 'tcp',
		mode: cfg.mode ?? 'udp',
		bond: cfg.bond === true,
		turnHost: cfg.turnHost ?? '',
		turnPort: cfg.turnPort ?? 0,
		obfProfile: cfg.obfProfile ?? 'none',
		streamsPerCred: cfg.streamsPerCred ?? 0,
		platform: cfg.platform ?? '',
		dnsMode: cfg.dnsMode ?? '',
		dnsServers: cfg.dnsServers ?? '',
		clientId: cfg.clientId ?? '',
		sub: cfg.sub ?? '',
		debug: cfg.debug === true,
	};
	putSecret(out, 'obfKey', cfg.obfKey);
	return out;
}

export function toFreeTurnServerPatch(cfg: FreeTurnServerConfig): Cfg {
	const out: Cfg = {
		listen: cfg.listen ?? '',
		connect: cfg.connect ?? '',
		mode: cfg.mode ?? 'udp',
		obfProfile: cfg.obfProfile ?? 'none',
		clientsFile: cfg.clientsFile ?? '',
		debug: cfg.debug === true,
		openFirewall: cfg.openFirewall !== false,
	};
	putSecret(out, 'obfKey', cfg.obfKey);
	return out;
}

// #endregion
