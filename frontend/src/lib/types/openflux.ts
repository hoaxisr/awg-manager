// ─────────────────────────────────────────────
// #region OpenFlux — SOCKS5-клиент + выходная нода (транспорты-релеи)
// See https://github.com/p1neappleXpress/OpenFlux — флаги CLI в README.
// ─────────────────────────────────────────────

export type OpenFluxTransport = 'yandex' | 'vyandex' | 'oneme' | 'cupsonline' | 'mailru';
export type OpenFluxCodec = 'batched' | 'legacy';
export type OpenFluxExitMode = 'l3' | 'l4';

export interface OpenFluxClientConfig {
	enabled: boolean;
	listen: string;
	transport: OpenFluxTransport | string;
	url?: string;
	maxToken?: string;
	maxUid?: string;
	codec: OpenFluxCodec | string;
	/** Ключ шифрования канала задан на бэкенде — значение наружу не отдаётся (Н5). */
	encryptionKey?: string;
	encryptionKeySet?: boolean;
	debug: boolean;
}

export interface OpenFluxServerConfig {
	enabled: boolean;
	transport: OpenFluxTransport | string;
	url?: string;
	maxToken?: string;
	maxUid?: string;
	mode: OpenFluxExitMode | string;
	/** Egress-адрес выхода для l3 (RST-drop и SNAT); обязателен в l3. */
	localIp?: string;
	codec: OpenFluxCodec | string;
	encryptionKey?: string;
	encryptionKeySet?: boolean;
	/** Резолверы процесса через запятую (форк -dns); пусто — системный. */
	dns?: string;
	/** TCP-трафик абонентов ноды идёт через правила sing-box (l4). */
	singboxRoute: boolean;
	debug: boolean;
}

export interface OpenFluxClientInstance {
	id: string;
	name: string;
	config: OpenFluxClientConfig;
	seededFrom?: string;
}

export interface OpenFluxServerInstance {
	id: string;
	name: string;
	config: OpenFluxServerConfig;
	seededFrom?: string;
}

export interface OpenFluxConfig {
	version?: number;
	clients: OpenFluxClientInstance[];
	servers: OpenFluxServerInstance[];
}

export interface OpenFluxProcessStatus {
	running: boolean;
	pid?: number;
	startedAt?: string;
	lastError?: string;
	log?: string;
	/** Канал из наблюдения процесса (адрес документа-релея). */
	address?: string;
	/** Транспорт (для выхода — с режимом, например «yandex:l4»). */
	mode?: string;
	binary: string;
	binaryPresent: boolean;
}

export interface OpenFluxInstanceStatus {
	id: string;
	name: string;
	status: OpenFluxProcessStatus;
}

export interface OpenFluxStatus {
	clients: OpenFluxInstanceStatus[];
	servers: OpenFluxInstanceStatus[];
	/** Бинари подсистемы на диске — признак ПОДСИСТЕМЫ, а не инстанса. */
	binariesPresent?: boolean;
	installAvailable: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	installing: boolean;
	routerClock?: string;
}

/** Тело openflux://-ссылки (base64url JSON, имена — флаги upstream). */
export interface OpenFluxLinkPayload {
	v: number;
	role?: 'client' | 'exit';
	transport?: string;
	url?: string;
	maxToken?: string;
	maxUid?: string;
	/** l3|l4 — только у ссылки выхода; клиент бэкенд не выбирает. */
	mode?: string;
	codec?: string;
	key?: string;
	name?: string;
}

/** Ответ ручки ссылки openflux-сервера (проводка → openfluxlink.ShareLink). */
export interface OpenFluxShareLink {
	link: string;
	transport: string;
	url?: string;
	mode?: string;
	codec?: string;
	encrypted: boolean;
	clientCommand: string;
}

// #endregion
