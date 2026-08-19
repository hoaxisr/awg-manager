// === Amnezia Premium (cp.amnezia.org via backend proxy) ===

/** Country row from GET account-info `data.available_countries`. */
export interface AmneziaPremiumCountry {
	server_country_code: string;
	server_country_name: string;
}

/** Запись из `data.issued_configs` (уже выданные конфиги в Amnezia CP). */
export interface AmneziaPremiumIssuedConfig {
	installation_uuid?: string;
	/** Время последнего изменения адреса/воркера на стороне CP. */
	worker_last_updated?: string;
	/** Время последней выдачи конфига клиенту; если раньше worker_last_updated — конфиг устарел. */
	last_downloaded?: string;
	server_country_code?: string;
	server_country_name?: string;
	source_type?: string;
	os_version?: string;
}

/** Nested JSON under Amnezia CP account-info `data`. */
export interface AmneziaPremiumAccountInfo {
	http_status?: number;
	available_countries?: AmneziaPremiumCountry[];
	issued_configs?: AmneziaPremiumIssuedConfig[];
	subscription_status?: string;
	vpn_key?: string;
}

// === Subscriptions ===

export interface SubscriptionHeader {
	name: string;
	value: string;
}

// Ответ POST /api/singbox/subscriptions/detect-headers.
export interface DetectedSubscriptionProfile {
	kind: string;
	decryptedUrl?: string;
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
