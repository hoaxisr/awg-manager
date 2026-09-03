// ─────────────────────────────────────────────
// #region Singbox Router (Phase 2 — TProxy routing engine)
// ─────────────────────────────────────────────

export interface SingboxRouterSettings {
	enabled: boolean;
	policyName: string;
	deviceMode?: 'policy' | 'all';
	/**
	 * Active routing mode. Source of truth for the FakeIP page's engine-state
	 * derivation. Served by GET /singbox/router/settings (omitempty; absent on
	 * legacy payloads → treat as 'tproxy'). NOT on the status endpoint.
	 */
	routingMode?: 'tproxy' | 'fakeip-tun' | 'policy-tun';
	snifferEnabled: boolean;
	// WAN-binding discriminator (mirrors backend storage):
	//   wanAutoDetect=true  + wanInterface=""    → sing-box auto_detect_interface
	//   wanAutoDetect=false + wanInterface="X"   → sing-box default_interface=X
	// All other combinations are invalid; backend validator rejects them.
	wanAutoDetect: boolean;
	wanInterface?: string; // kernel system-name (e.g. "ppp0"); empty when wanAutoDetect=true
	bypassPresets?: string[];
	bypassExtraPorts?: string;
	bypassExtraSubnets?: string;
	/**
	 * Geoip-теги из настроенных .dat-файлов, чьи CIDR целиком обходят sing-box
	 * через ipset AWGM-BYPASS (та же семантика, что bypassExtraSubnets).
	 */
	bypassGeoipTags?: string[];
	ingressInterfaces?: string[];
	// fakeip-tun engine settings (user-editable; round-trip via GET/PUT
	// /singbox/router/settings). Defaults mirror DefaultFakeIPTunParams:
	//   fakeipStack: gvisor (system → lower throughput, backend forces gso:false)
	//   fakeipPool4: "198.18.0.0/15", fakeipPool6: "fc00::/18" ("" disables v6)
	//   fakeipMtu: 1500. All omitempty on the wire → absent on legacy payloads.
	fakeipStack?: 'gvisor' | 'system';
	fakeipPool4?: string;
	fakeipPool6?: string;
	fakeipMtu?: number;
	// Upstream резолвера "real" (default "1.1.1.1"). Правится через адрес
	// сервера «real» в DNS-панели fakeip (бэкенд перехватывает правку в это
	// поле — issue #487); строго IP-адрес.
	fakeipRealServer?: string;
	// UDP session timeout for tproxy-in. Go duration string (e.g. "3m0s", "10m0s").
	// Empty = backend default (3m0s). Increase to fix dropped sessions in games.
	udpTimeout?: string;
	/**
	 * policy-tun: перевести выбранные сегменты на static-NAT, чтобы sing-box
	 * видел реальные адреса клиентов вместо адреса tun-шлюза. Требует непустого
	 * policyTunNatSegments (бэкенд иначе отвечает 400); при false бэкенд сам
	 * обнуляет список.
	 */
	policyTunSourcePreserve?: boolean;
	/** Сегменты, выбранные для source-preserve (см. GET .../policy-tun/nat-preview). */
	policyTunNatSegments?: string[];
	/**
	 * QoS/DSCP routing classes (issue #371). Traffic marked with class DSCP is
	 * routed to the class outbound, trumping other route rules. Works in
	 * routingMode 'tproxy' and 'policy-tun' (DSCP-перехват — единственный
	 * netfilter policy-tun); не работает в 'fakeip-tun'. Max 8 classes; dscp 0–63 unique;
	 * name ≤ 32 chars; outbound = any valid outbound tag. Omitempty on the
	 * wire → absent on legacy/mock payloads (treat undefined as []).
	 */
	qosClasses?: SingboxQosClass[];
	/**
	 * Место хранения кэша sing-box (cache.db) (issue #842).
	 * 'flash' — на флеш-памяти (/opt/etc/awg-manager/singbox/cache.db).
	 * 'tmp' — в оперативной памяти (/tmp/singbox-cache.db, tmpfs/RAM) для защиты Flash.
	 * Отсутствует — не задано: путь из 00-base.json как есть (см. Status.cacheDbPath).
	 */
	cacheFileLocation?: 'flash' | 'tmp';
}

/** One QoS/DSCP routing class (SingboxRouterSettings.qosClasses entry). */
export interface SingboxQosClass {
	/** DSCP mark 0–63, unique within the list. */
	dscp: number;
	/** Display name, ≤ 32 chars. */
	name: string;
	/** Outbound tag the marked traffic is routed to. */
	outbound: string;
	enabled: boolean;
}

/**
 * Состояние набора обхода AWGM-BYPASS (GET /singbox/router/bypass-set/status).
 * Зеркало api.BypassSetStatusData.
 */
export interface BypassSetStatus {
	/** Бинарь ipset есть на роутере. */
	available: boolean;
	/** Бинарь conntrack есть: без него правки бьют только по новым соединениям. */
	conntrackAvailable: boolean;
	/** Идёт установка пакета, запущенная через install-эндпоинты. */
	installing: boolean;
	/** Записей в наборе после последнего наполнения; факт только при entryCountOK. */
	entryCount: number;
	/**
	 * entryCount — факт. false значит «размер набора неизвестен», и entryCount=0
	 * в этом случае НЕ равно «набор пуст» (в UI — «н/д»).
	 */
	entryCountOK: boolean;
	/** RFC3339-время последнего наполнения; пусто — наполнения не было. */
	lastPopulate?: string;
	/** Ошибка последнего наполнения; пусто — ошибок нет. */
	lastError?: string;
	/** Выбранные теги, не найденные в .dat при последнем наполнении. */
	missingTags?: string[];
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
	// Открытый набор: бэкенд добавляет kind'ы (orphan-rule, orphan-outbound,
	// orphan-rule-set, policy-missing, dns-domain-resolver, dns-detour-dial,
	// dns-duplicate-tag, ...) — закрытый union здесь молча превращал бы новые
	// значения в «невозможные» для switch-narrowing.
	kind: string;
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
	/**
	 * Active fakeip tun iface (kernel name, e.g. "opkgtun0"). Present only in
	 * fakeip-tun mode once the tun is provisioned (backend Status.FakeIPIface
	 * omitempty); absent otherwise.
	 */
	fakeipIface?: string;
	/** DNS-адрес для ручной настройки клиентов в режиме fakeip-tun. */
	fakeipDns?: string;
	/** Адрес tun-шлюза (хост /30, e.g. «172.18.0.1») в режиме fakeip-tun. */
	fakeipTunAddr?: string;
	/** Эффективный путь cache.db: по cacheFileLocation, при пустой настройке — из 00-base.json. */
	cacheDbPath?: string;
	/**
	 * Kernel-имя tun-интерфейса режима policy-tun (e.g. "opkgtun0"). Пусто вне
	 * policy-tun и при выключенном движке.
	 */
	policyTunIface?: string;
	/** NDMS-имя того же интерфейса (e.g. "OpkgTun0") — так он виден в политиках доступа. */
	policyTunNdmsName?: string;
	/**
	 * Применённый режим NAT сегментов (static-NAT вместо маскарада). Отсутствует
	 * = «неприменимо» (не policy-tun / движок выключен), а не «выключено».
	 */
	policyTunSourcePreserve?: boolean;
	issues?: SingboxRouterIssue[];
	/** Последняя fatal-причина sing-box; непусто только при «СБОЙ» (enabled && !active). */
	lastError?: string;
	/** Падений sing-box за последние 10 минут (окно анти-crash-loop backoff'а, #456). */
	crashCount?: number;
	/** Причина последнего падения в окне (например, распознанный OOM-kill). */
	lastCrashReason?: string;
	/**
	 * RFC3339-время, до которого авто-перезапуск приостановлен backoff'ом
	 * (анти crash-loop). Пусто/absent, когда не подавлен; ручной
	 * «Перезапустить» не подавляется и сбрасывает паузу.
	 */
	restartSuppressedUntil?: string;
	/**
	 * xt_dscp kernel module is available for DSCP matching (issue #371 QoS).
	 * Strict `false` → QoS classes cannot be applied (UI shows a warning).
	 * Optional: absent on legacy/mock payloads → treated as unknown, no warning.
	 */
	xtDscpAvailable?: boolean;
}

export interface SingboxRouterTransitionStep {
	step: 'start' | 'teardown' | 'provision' | 'readiness' | 'ready' | 'rollback' | 'error';
	status: 'current' | 'done' | 'error';
	message?: string;
}

export interface SingboxRouterTransitionData {
	transitionId: string;
	from: 'off' | 'tproxy' | 'fakeip-tun' | 'policy-tun';
	to: 'off' | 'tproxy' | 'fakeip-tun' | 'policy-tun';
	step: SingboxRouterTransitionStep;
	done?: boolean;
	finalState?: 'off' | 'tproxy' | 'fakeip-tun' | 'policy-tun';
	error?: string;
}

/**
 * Один сегмент роутера в предпоказе source-preserve (policy-tun).
 * Источник: GET /api/singbox/router/policy-tun/nat-preview.
 * staticWan заполнен только при mode==='static'.
 */
export interface PolicyTunNATSegmentInfo {
	name: string;
	mode: 'dynamic' | 'static' | 'none';
	staticWan?: string;
	/** Описание сегмента из NDMS. Пусто, если не задано. */
	label?: string;
	/** Подсеть сегмента, напр. "192.168.1.0/24". Пусто, если адресации нет. */
	subnet?: string;
}

/** Выход роутера, на котором подмена адреса сохранится (туда встанет static). */
export interface PolicyTunNATEgress {
	name: string;
	label?: string;
}

/**
 * Предпоказ source-preserve: сегменты и ВЫХОДЫ, на которых подмена адреса
 * сохранится. Без второй половины экран не отвечает на главный вопрос —
 * между чем и чем подмена снимается. Выходов несколько: `ip static` —
 * общероутерная настройка, правило встаёт на каждый интерфейс с `ip global`.
 */
export interface PolicyTunNATPreview {
	segments: PolicyTunNATSegmentInfo[];
	/** Пусто, когда выходы определить нельзя. */
	egresses?: PolicyTunNATEgress[];
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
	// Exact-domain matcher. Sits in the same sing-box matcher as
	// domain_suffix (destination-address group) — the two are OR-ed.
	domain?: string[];
	domain_suffix?: string[];
	ip_cidr?: string[];
	source_ip_cidr?: string[];
	port?: number[];
	rule_set?: string[];
	inbound?: string[];
	protocol?: string;
	// When true, matches packets whose destination is private (RFC1918,
	// loopback, link-local, CGNAT, multicast). System ip_is_private
	// bypass rule has this set + outbound:"direct".
	ip_is_private?: boolean;
	// Optional — sing-box defaults to `route` when omitted. The system
	// ip_is_private rule omits action because that's how SKeen's
	// reference config writes it and the backend's `omitempty` mirrors
	// the same shape. `route-options` is the system UDP-timeout rule.
	action?: 'route' | 'reject' | 'sniff' | 'hijack-dns' | 'route-options';
	outbound?: string;
	// L4 matcher ("tcp" | "udp"). The system route-options rule scopes its
	// udp_timeout override to "udp".
	network?: string;
	// `udp_timeout` route option carried by the system `route-options` rule.
	udp_timeout?: string;
	// A `logical` rule combines its nested `rules` by `mode`. The backend
	// stores «пресет ИЛИ свои адреса» in this form (see flattenRouterRule):
	// sing-box would otherwise AND a rule_set with the rule's own addresses
	// and the rule would match almost nothing.
	type?: string;
	mode?: string;
	rules?: SingboxRouterRule[];
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

/**
 * One per-rule decision from the DNS-branch inspector. Mirrors
 * SingboxRouterInspectMatch but targets a DNS server tag instead of a
 * route outbound.
 */
export interface SingboxRouterInspectDNSMatch {
	index: number;
	matched: boolean;
	server?: string;
	conditions?: string[];
	reason?: string;
}

export interface SingboxRouterInspectDNSResult {
	input: string;
	inputType: 'domain' | 'ip';
	matches: SingboxRouterInspectDNSMatch[];
	matchedRule: number;
	/** Resolved DNS-server tag (matched rule's server, or the final server). */
	server: string;
	/** How the resolved server answers the query. */
	classification: 'fakeip' | 'real' | 'local';
	/** fakeip pool (inet4_range [+ inet6_range]); empty unless fakeip. */
	pool?: string;
	final: string;
	note?: string;
}

export interface SingboxRouterInspectDNSRequest {
	domain: string;
	queryType?: string;
	sourceIP?: string;
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

/** GET /singbox/router/geosites/list — каталог SagerNet sing-geosite. */
export interface SingboxGeositesData {
	/** Имена без префикса geosite- и суффикса .srs. */
	names: string[];
	/** baseUrl + "geosite-" + name + ".srs" — готовый URL rule-set'а. */
	baseUrl: string;
	fetchedAt: string;
	/** Обновление не удалось — отдана сохранённая копия. */
	stale?: boolean;
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

export type SingboxRouterDNSType = 'udp' | 'tls' | 'https' | 'quic' | 'h3' | 'local' | 'fakeip';

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

export interface SingboxRouterDNSClientTLSOptions {
	server_name?: string;
	insecure?: boolean;
	alpn?: string[];
	min_version?: '1.0' | '1.1' | '1.2' | '1.3';
	max_version?: '1.0' | '1.1' | '1.2' | '1.3';
	certificate_public_key_sha256?: string[];
}

export interface SingboxRouterDNSCertificate {
	subject: string;
	issuer: string;
	not_after: string;
	certificate_public_key_sha256: string;
}

export interface SingboxRouterDNSLookupResult {
	ips: string[];
	certificate_public_key_sha256: string[];
	certificates: SingboxRouterDNSCertificate[];
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
	tls?: SingboxRouterDNSClientTLSOptions;
}

export interface SingboxRouterDNSRule {
	rule_set?: string[];
	domain_suffix?: string[];
	domain?: string[];
	domain_keyword?: string[];
	domain_regex?: string[];
	query_type?: string[];
	// A source-address matcher (backend DNSRule.SourceIPCIDR). No UI input, but
	// modeled so a hand-edited source-scoped rule isn't mistaken for a catch-all.
	source_ip_cidr?: string[];
	server?: string;
	action?: '' | 'route' | 'reject' | 'predefined' | 'evaluate' | 'respond';
	rcode?: string;
	method?: string;
	tag?: string;
	/**
	 * union sing-box: true (последний анонимный evaluate) | "<tag>".
	 * false в конфиг не пишем, но hand-edited конфиг его допускает — это
	 * «выключено» (backend DNSMatchResponse.IsEnabled).
	 */
	match_response?: boolean | string;
	ip_cidr?: string[];
	response_rcode?: string;
	response_answer?: string[];
	response_ns?: string[];
	response_extra?: string[];
	race?: boolean;
	speculative?: boolean;
}

export interface SingboxRouterDNSGlobals {
	final: string;
	strategy: SingboxRouterDNSStrategy;
}

/** Режим DNS-пресета: '' — выключен. */
export type SingboxRouterDNSChainMode = '' | 'resilient' | 'antipoison';

/**
 * Пресет DNS-цепочки (sing-box 1.14 evaluate/match_response). Managed-правила
 * цепочки собирает бэкенд; пользователь задаёт только режим и серверы.
 */
export interface SingboxRouterDNSChainPreset {
	mode: SingboxRouterDNSChainMode;
	directServer?: string;
	proxyServer?: string;
	/** ip_cidr «отравленных» ответов для antipoison (пусто = серверный сид). */
	poisonCidrs?: string[];
}

export interface SingboxRouterDNSRewrite {
	pattern: string;
	ips: string[];
	/** Non-empty when owned by a preset (e.g. keendns). */
	managed?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region AWG outbounds catalog (15-awg.json)
// ─────────────────────────────────────────────

export interface AWGTagInfo {
	tag: string;
	label: string;
	kind: 'managed' | 'system' | 'awg3';
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

// === Эксперт-редактор конфигурации sing-box (config.d слоты) ===

/** Один слот config.d в обзоре редактора. */
export interface ConfigSlotInfo {
	slot: string;
	filename: string;
	/** system — генерируется продюсером; user — 90-user.json эксперт-редактора. */
	ownership: 'system' | 'user';
	enabled: boolean;
	hasDraft: boolean;
	/** Размер эффективного содержимого (pending → active → disabled), 0 если не сконфигурирован. */
	size: number;
	mtime?: string;
}

export interface ConfigSlotsResponse {
	slots: ConfigSlotInfo[];
}

/** Эффективное содержимое слота для просмотра/редактирования. */
export interface ConfigSlotContentResponse {
	slot: string;
	filename: string;
	content: string;
	state: 'active' | 'disabled' | 'absent';
	hasDraft: boolean;
}

/** Результат POST /singbox/config/user/check (200 и при ok:false — это запрос-вопрос). */
export interface UserConfigCheckResponse {
	ok: boolean;
	errors?: RouterValidationErrorDTO[];
	/** Advisory-предупреждения (severity=warning): применение не блокируют. */
	warnings?: RouterValidationErrorDTO[];
}

/** 200-ответ POST /singbox/config/user/apply — применено, но могут быть предупреждения. */
export interface UserConfigApplyResponse {
	ok: boolean;
	warnings?: RouterValidationErrorDTO[];
}


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

