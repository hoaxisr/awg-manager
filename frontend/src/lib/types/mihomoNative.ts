export type MihomoEnginePreference = 'auto' | 'sing-box' | 'mihomo';
export type MihomoSubscriptionFormat = 'auto' | 'share-links' | 'mihomo-provider';
export type MihomoDiagnosticResourceKind = 'proxy' | 'subscription';

export interface MihomoEngineSupport {
	supported: boolean;
	reason?: string;
}

export interface MihomoProxyBridge {
	listenPort: number;
	proxyIndex: number;
	proxyInterface: string;
	kernelInterface: string;
}

export interface MihomoNativeProxy {
	id: string;
	name: string;
	protocol: string;
	transport: string;
	enginePreference: MihomoEnginePreference;
	selectedEngine: MihomoEnginePreference;
	sourceId?: string;
	rawUri?: string;
	nativeConfig?: Record<string, unknown>;
	compatibility: { 'sing-box': MihomoEngineSupport; mihomo: MihomoEngineSupport };
	enabled: boolean;
	bridge?: MihomoProxyBridge;
	createdAt?: string;
	updatedAt?: string;
}

export interface MihomoNativeSubscription {
	id: string;
	name: string;
	url?: string;
	inline?: string;
	format: MihomoSubscriptionFormat;
	enginePreference: MihomoEnginePreference;
	providerName?: string;
	groupName?: string;
	headers?: Record<string, string[]>;
	refreshHours: number;
	enabled: boolean;
	mode?: string;
	testUrl?: string;
	testInterval?: number;
	testTolerance?: number;
	filterInclude?: string;
	filterExclude?: string;
	bindInterface?: string;
	bridge?: MihomoProxyBridge;
	lastFetched?: string;
	lastError?: string;
	members?: import('./subscriptions').SubscriptionMember[];
	createdAt?: string;
	updatedAt?: string;
}

export interface MihomoNativeList<T> { items: T[] }

export interface MihomoRuntimeProxy {
	name: string;
	type: string;
	now?: string;
	all?: string[];
	alive?: boolean;
	udp?: boolean;
	history?: Array<{ time: string; delay: number }>;
}

export interface MihomoRuntimeProxies { proxies: Record<string, MihomoRuntimeProxy> }

export interface MihomoRuntimeProvider {
	name: string;
	type: string;
	vehicleType?: string;
	updatedAt?: string;
	proxies?: MihomoRuntimeProxy[];
	ruleCount?: number;
	behavior?: string;
	format?: string;
}

export interface MihomoRuntimeProviders {
	providers: Record<string, MihomoRuntimeProvider>;
}

export interface MihomoNativeGroup {
	id: string;
	name: string;
	type: 'select' | 'url-test' | 'fallback' | 'load-balance' | 'relay';
	proxies: string[];
	use: string[];
	url?: string;
	interval?: number;
	lazy: boolean;
	strategy?: 'consistent-hashing' | 'round-robin' | 'sticky-sessions';
	tolerance?: number;
	timeout?: number;
	maxFailedTimes?: number;
	disableUdp?: boolean;
	includeAll?: boolean;
	includeAllProxies?: boolean;
	includeAllProviders?: boolean;
	filter?: string;
	excludeFilter?: string;
	excludeType?: string;
	expectedStatus?: string;
	hidden?: boolean;
	icon?: string;
	enabled: boolean;
}

export interface MihomoNativeRule {
	id: string;
	type: string;
	payload?: string;
	outbound: string;
	noResolve?: boolean;
	enabled: boolean;
}

export interface MihomoNativeRuleProvider {
	id: string;
	name: string;
	type: 'http' | 'file';
	url?: string;
	path?: string;
	behavior: 'classical' | 'domain' | 'ipcidr';
	format: 'yaml' | 'text' | 'mrs';
	interval: number;
	proxy?: string;
	enabled: boolean;
}
