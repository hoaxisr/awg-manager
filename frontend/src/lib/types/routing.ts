// ─────────────────────────────────────────────
// #region Routing — DNS routes, static routes, tunnels
// ─────────────────────────────────────────────

export interface DnsRouteSubscription {
	url: string;
	name: string;
	lastFetched?: string;
	lastCount?: number;
	lastError?: string;
}

export interface DnsRouteTarget {
	interface: string;
	tunnelId: string;
	fallback?: 'auto' | 'reject' | '';
}

export interface DedupeItem {
	domain: string;
	reason: 'exact' | 'wildcard' | 'subnet_covered';
	coveredBy: string;
	listId: string;
	listName: string;
}

export interface DedupeReport {
	totalInput: number;
	totalKept: number;
	totalRemoved: number;
	exactDupes: number;
	wildcardDupes: number;
	items?: DedupeItem[];
}

export interface DnsRoute {
	id: string;
	name: string;
	domains: string[];
	excludes?: string[];
	/** Raw excludes editor text. Preserves comments and blank lines; active excludes are derived from it. */
	excludesText?: string;
	excludeSubnets?: string[];
	subnets?: string[];
	manualDomains: string[];
	/** Raw manual editor text. Preserves comments and blank lines; active entries are derived from it. */
	manualText?: string;
	subscriptions?: DnsRouteSubscription[];
	routes: DnsRouteTarget[];
	enabled: boolean;
	createdAt: string;
	updatedAt: string;
	lastDedupeReport?: DedupeReport;
	backend?: 'ndms' | 'hydraroute';
	hrRouteMode?: 'interface' | 'policy';
	hrPolicyName?: string;
	/**
	 * Tunnel IDs permitted in a newly-created HR policy, in priority order.
	 * Only honored when hrRouteMode === 'policy' and the policy is new.
	 * Absent for existing-policy and interface-mode flows.
	 */
	hrPolicyInterfaces?: string[];
	/** Optional URL for a custom icon (e.g. Qure CDN PNG or user-supplied URL). */
	iconUrl?: string;
}

export interface StaticRouteList {
	id: string;
	name: string;
	tunnelID: string;
	subnets: string[];
	fallback?: '' | 'reject';
	enabled: boolean;
	createdAt: string;
	updatedAt: string;
	/** Optional URL for a custom icon (e.g. Qure CDN PNG or user-supplied URL). */
	iconUrl?: string;
	/** Включён, но записи туннеля ещё нет: маршруты встанут при его старте. */
	pending?: boolean;
}

export interface RoutingTunnel {
	id: string;
	name: string;
	iface?: string; // kernel interface name ("nwg0", "opkgtun10", "ppp0"); у system-записей — NDMS-id (см. hrTargetTunnel)
	type: 'managed' | 'system' | 'wan';
	status: string;
	available: boolean;
	/** Предупреждение каталога, напр. «нет адреса в NDMS» (issue #935). */
	warning?: string;
	/** Системный WireGuard-сервер (managed, помеченный или встроенный), F503. */
	server?: boolean;
}

export interface ResolveResult {
	domain: string;
	ips: string[];
	error?: string;
}

// #endregion

// ─────────────────────────────────────────────
// #region Access Policies — ip policy
// ─────────────────────────────────────────────

export interface AccessPolicy {
	name: string;
	description: string;
	standalone: boolean;
	interfaces: AccessPolicyInterface[];
	deviceCount: number;
	/** true for Policy0..PolicyN; false for HydraRoute Neo and other custom NDMS profiles */
	isStandard?: boolean;
}

export interface AccessPolicyInterface {
	name: string;
	label?: string;
	order: number;
	denied?: boolean;
}

export interface PolicyDevice {
	mac: string;
	ip: string;
	name: string;
	hostname: string;
	active: boolean;
	link: string;
	policy: string;
}

export interface PolicyGlobalInterface {
	name: string;
	label: string;
	up: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region Client Routes — per-device VPN routing
// ─────────────────────────────────────────────

export interface ClientRoute {
	id: string;
	clientIp: string;
	clientHostname: string;
	tunnelId: string;
	fallback: 'drop' | 'bypass';
	enabled: boolean;
}

// #endregion
