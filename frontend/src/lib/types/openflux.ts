export type OpenFluxTransport =
	| 'direct'
	| 'webdav'
	| 'mailru'
	| 'yandex'
	| 'vyandex'
	| 'boards'
	| 'avito'
	| 'cupsonline'
	| 'telemost'
	| 'oneme';

export type OpenFluxMode = 'l4' | 'l3';
export type OpenFluxTransportMode = 'session' | 'single';
export type OpenFluxCodec = 'batched' | 'legacy';
export type OpenFluxNatMode = 'full' | 'internet-only' | 'none';

export interface OpenFluxConfig {
	enabled: boolean;
	mode: OpenFluxMode | string;
	transportMode: OpenFluxTransportMode | string;
	transports: string[];
	transportPriorities?: Record<string, number>;
	webdavUrl?: string;
	webdavUser?: string;
	webdavPassword?: string;
	mailruUrl?: string;
	mailruCookies?: string;
	yandexUrl?: string;
	vyandexUrl?: string;
	boardsUrl?: string;
	avitoUrl?: string;
	avitoCookies?: string;
	cupsonlineUrl?: string;
	telemostUrl?: string;
	onemeToken?: string;
	onemeUid?: string;
	directListen?: string;
	directHost?: string;
	secret?: string;
	codec?: OpenFluxCodec | string;
	upstreamProxy?: string;
	lowMemory?: boolean;
	lanes?: number;
	nativeScriptsDir?: string;
	openFirewall: boolean;
	debug: number;
	name?: string;

	// Routing, NAT and Egress matching WDTT server
	natMode?: OpenFluxNatMode | string;
	natStaticWan?: string;
	natStaticWans?: string[];
	policy?: string;
	lanSegments?: string[];
	exposeToPolicies?: boolean;
	egressInterface?: string;
	ndmsIface?: string;
	tunIface?: string;
	tunMtu?: number;

	// Redacted DTO presence indicators (returned by server) and explicit clear flags (sent on save).
	secretSet?: boolean;
	webdavPasswordSet?: boolean;
	mailruCookiesSet?: boolean;
	avitoCookiesSet?: boolean;
	onemeTokenSet?: boolean;
	clearSecret?: boolean;
	clearWebdavPassword?: boolean;
	clearMailruCookies?: boolean;
	clearAvitoCookies?: boolean;
	clearOnemeToken?: boolean;
}

export interface OpenFluxClientConfig {
	enabled: boolean;
	name?: string;
	inboundMode?: 'tun' | 'socks5' | string;
	ndmsIface?: string;
	tunIface?: string;
	tunAddr?: string;
	tunMtu?: number;
	policy?: string;
	exposeToPolicies?: boolean;
	defaultRoute?: boolean;
	transportMode: 'session' | 'single' | 'stream' | string;
	transports: string[];
	directDial?: string;
	webdavUrl?: string;
	webdavUser?: string;
	webdavPassword?: string;
	mailruUrl?: string;
	mailruCookies?: string;
	yandexUrl?: string;
	vyandexUrl?: string;
	boardsUrl?: string;
	avitoUrl?: string;
	avitoCookies?: string;
	cupsonlineUrl?: string;
	telemostUrl?: string;
	onemeToken?: string;
	onemeUid?: string;
	secret?: string;
	context?: string;
	codec?: OpenFluxCodec | string;
	lowMemory?: boolean;
	lanes?: number;
	nativeScriptsDir?: string;
	socks5Listen?: string;
	httpProxyListen?: string;
	autoReconnect: boolean;
	debug: number;

	secretSet?: boolean;
	webdavPasswordSet?: boolean;
	mailruCookiesSet?: boolean;
	avitoCookiesSet?: boolean;
	onemeTokenSet?: boolean;
	clearSecret?: boolean;
	clearWebdavPassword?: boolean;
	clearMailruCookies?: boolean;
	clearAvitoCookies?: boolean;
	clearOnemeToken?: boolean;
}

export interface OpenFluxClientStatus {
	installed: boolean;
	running: boolean;
	pid?: number;
	uptimeS?: number;
	inboundMode?: string;
	ndmsIface?: string;
	tunIface?: string;
	tunAddr?: string;
	policy?: string;
	socks5Port: number;
	socks5Uri?: string;
	httpUri?: string;
	error?: string;
	config?: OpenFluxClientConfig;
	logTail?: string;
}

export interface OpenFluxStatus {
	installed: boolean;
	running: boolean;
	pid?: number;
	uptimeS?: number;
	version?: string;
	latestVersion?: string;
	archSupported: boolean;
	binary: string;
	arch: string;
	linkAvailable?: boolean;
	error?: string;
	firewallError?: string;
	config?: OpenFluxConfig;
	logTail?: string;
	client?: OpenFluxClientStatus;
}

export interface OpenFluxShareTransport {
	type: string;
	name?: string;
	url?: string;
	priority?: number;
	dial?: string;
	user?: string;
	password?: string;
}

export interface OpenFluxShareConfig {
	name?: string;
	negotiate?: boolean;
	codec?: string;
	secret?: string;
	context?: string;
	mode?: string;
	transports: OpenFluxShareTransport[];
}

export interface OpenFluxParsedLink {
	share: OpenFluxShareConfig;
	config: OpenFluxClientConfig;
}
