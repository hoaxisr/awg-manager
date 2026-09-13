// === Amnezia Premium (мастер подписки; ручки /api/amnezia/premium/*) ===
//
// Имена полей — НАШИ (camelCase), как их отдаёт бэкенд: ответ портала он
// собирает полем за полем и наружу не проксирует. Ни ключа подписки, ни
// сессии портала здесь нет и быть не может.

/** Страна каталога подписки: элемент `countries` ответа GET /amnezia/premium/catalog. */
export interface AmneziaPremiumCountry {
	/** Код страны в том виде, в каком прислал портал; регистр не нормализован. */
	code: string;
	/** Название страны вместе с суффиксами вида «Switzerland [P2P]». */
	name: string;
	/**
	 * Протоколы, которыми подписка отдаёт страну. null/отсутствие — портал
	 * поля не прислал, и страну по нему отбрасывать нельзя; пустой список —
	 * страна не отдаётся ничем.
	 */
	protocols?: string[] | null;
}

/**
 * Уже выданная подпиской конфигурация: элемент `issuedConfigs` каталога.
 *
 * Отметки времени — СЫРЫЕ строки портала, а не готовые флаги: решение
 * «устарела ли выдача» принимает фронт (premiumCountryConfigFreshness), и
 * отсутствие отметки обязано отличаться от «отметка есть, но не подходит».
 */
export interface AmneziaPremiumIssuedConfig {
	/** Страна выданной конфигурации; регистр не нормализован. */
	countryCode?: string;
	/** Когда конфигурацию выдавали в последний раз. */
	lastIssuedAt?: string;
	/** Когда конфигурацию в последний раз меняли на стороне портала. */
	portalUpdatedAt?: string;
	/** Вид записи: gateway_account — активное устройство, остальное переиздаваемо. */
	sourceType?: string;
}

/** Данные подписки и список стран: GET /amnezia/premium/catalog. */
export interface AmneziaPremiumCatalog {
	planName?: string;
	/** «Действует до», строкой ISO 8601 в том виде, в каком прислал портал. */
	subscriptionEndDate?: string;
	activeDeviceCount?: number;
	maxDeviceCount?: number;
	/** Список стран подписки; бэкенд всегда шлёт список, пустой — не отказ. */
	countries: AmneziaPremiumCountry[];
	/** null — портал про выданное не сказал; [] — выданного нет. Это разные состояния. */
	issuedConfigs?: AmneziaPremiumIssuedConfig[] | null;
}

/** Состояние ключа подписки — общая форма ответа GET/POST/DELETE /amnezia/premium/key. */
export interface AmneziaPremiumKeyState {
	/** Шифротекст ключа лежит в настройках. */
	stored: boolean;
	/** Сохранённый шифротекст расшифровывается секретом устройства. */
	usable: boolean;
	/** Почему сохранить не вышло; пусто — сохранять не просили или сохранение прошло. */
	saveError: string;
}

/** Выданная конфигурация страны: POST /amnezia/premium/config. */
export interface AmneziaPremiumConfig {
	countryCode: string;
	/** Текст .conf; строки с ключом подписки бэкенд из него вырезал. */
	config: string;
}

/**
 * Отозванная конфигурация страны: POST /amnezia/premium/revoke. Слот устройств
 * подписки возвращён. Счётчика устройств здесь нет намеренно — его знает
 * каталог, и второй источник этого числа разошёлся бы с ним при первом же
 * отзыве из соседней вкладки.
 */
export interface AmneziaPremiumRevoke {
	countryCode: string;
}

/** ДЕЙСТВУЮЩИЙ адрес зеркала Amnezia: GET/POST /amnezia/premium/mirror. */
export interface AmneziaPremiumMirror {
	mirrorUrl: string;
}

// === Subscriptions ===

export interface SubscriptionHeader {
	name: string;
	value: string;
}

// Пресет заголовков клиента: GET /api/singbox/subscriptions/header-profiles.
export interface SubscriptionHeaderProfile {
	kind: string;
	label: string;
	headersText: string;
}

// Ответ POST /api/singbox/subscriptions/detect-headers.
export interface DetectedSubscriptionProfile {
	kind: string;
	decryptedUrl?: string;
	// URL, которым следует заменить введённый: снятая обёртка / расшифрованная
	// ссылка. Нормализацию делает сервер, фронт её не повторяет.
	normalizedUrl?: string;
	isEncrypted?: boolean;
	headers: SubscriptionHeader[];
	headersText: string;
	label: string;
	serverCount: number;
}

export interface SubscriptionMember {
	tag: string;
	label?: string;
	protocol: string;
	server: string;
	port: number;
	sni?: string;
	transport?: string;
	security?: string;
}

export interface SubscriptionPreviewMember {
	key: string; // identity-суффикс (subID-независимый) для исключения при создании
	label?: string;
	protocol: string;
	server: string;
	port: number;
	sni?: string;
	transport?: string;
	security?: string;
}

export type SubscriptionMode = 'selector' | 'urltest';

export interface SubscriptionURLTest {
	url: string;
	intervalSec: number;
	toleranceMs: number;
}

export const DEFAULT_SUBSCRIPTION_URLTEST: SubscriptionURLTest = {
	url: 'https://www.gstatic.com/generate_204',
	intervalSec: 60,
	toleranceMs: 50,
};

export interface SubscriptionInfoItem {
	id: string;
	label: string;
	tag?: string;
	source?: 'auto' | 'user' | string;
}

export interface SubscriptionRejectedMember {
	tag?: string;
	label?: string;
	protocol?: string;
	server?: string;
	port?: number;
	reason: string;
}

export interface Subscription {
	id: string;
	label: string;
	url: string;
	isInline: boolean;
	headers: SubscriptionHeader[];
	refreshHours: number;
	lastFetched: string; // RFC 3339, "" when never fetched
	lastError?: string;
	selectorTag: string;
	inboundTag: string;
	listenPort: number;
	proxyIndex: number;
	memberTags: string[];
	members: SubscriptionMember[];
	orphanTags: string[];
	rejectedMembers?: SubscriptionRejectedMember[];
	infoItems?: SubscriptionInfoItem[];
	activeMember: string;
	enabled: boolean;
	mode: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
	excludedTags?: string[];
	excludedMembers?: SubscriptionMember[];
	/** Regex (Go RE2) «включать только» — матчится по имени сервера. */
	filterInclude?: string;
	/** Regex (Go RE2) «исключать» — матчится по имени сервера. */
	filterExclude?: string;
	/** Display-зеркало серверов, скрытых фильтром (перестраивается при refresh). */
	filteredMembers?: SubscriptionMember[];
	/** Kernel iface для dial всех member-outbound'ов (#709). */
	bindInterface?: string;
}

export interface SubscriptionRefreshResult {
	when: string;
	added: number;
	updated: number;
	orphaned: number;
	skippedVmess: number;
	skippedOther: number;
	skippedDuplicate: number;
	parseErrors?: string[];
}

export interface SubscriptionActiveNowResponse {
	now: string;
}

export interface CreateSubscriptionInput {
	label: string;
	url?: string;
	inline?: string;
	headers: SubscriptionHeader[];
	refreshHours: number;
	enabled: boolean;
	mode?: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
	excludedKeys?: string[];
	filterInclude?: string;
	filterExclude?: string;
	bindInterface?: string;
}

export interface UpdateSubscriptionInput {
	label?: string;
	url?: string;
	headers?: SubscriptionHeader[];
	refreshHours?: number;
	enabled?: boolean;
	mode?: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
	filterInclude?: string;
	filterExclude?: string;
	bindInterface?: string;
}

// === Subscription aggregate groups (#372) ===

/** Сводная группа: один selector/urltest поверх членов нескольких подписок. */
export interface SubscriptionGroup {
	id: string;
	label: string;
	tag: string;
	inboundTag: string;
	listenPort: number;
	proxyIndex: number;
	mode: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
	useSubscriptionIds: string[];
	filterInclude?: string;
	filterExclude?: string;
	enabled: boolean;
	/** Серверное разрешение состава на момент запроса. */
	memberCount: number;
	members: SubscriptionGroupMemberPreview[];
}

export interface SubscriptionGroupMemberPreview {
	tag: string;
	label?: string;
}

export interface CreateSubscriptionGroupInput {
	label: string;
	/** Пользовательский outbound-тег (#572); пусто = авто "agg-<id8>". */
	tag?: string;
	mode?: SubscriptionMode; // default 'urltest'
	urlTest?: SubscriptionURLTest;
	useSubscriptionIds: string[];
	filterInclude?: string;
	filterExclude?: string;
	enabled: boolean;
}

export interface UpdateSubscriptionGroupInput {
	label?: string;
	mode?: SubscriptionMode;
	urlTest?: SubscriptionURLTest;
	useSubscriptionIds?: string[];
	filterInclude?: string;
	filterExclude?: string;
	enabled?: boolean;
}
