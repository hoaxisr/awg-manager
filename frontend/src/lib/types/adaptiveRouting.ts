export type RoutingOwner = 'none' | 'sing-box' | 'mihomo' | 'susanin';

export type EgressKind =
	| 'kernel-tunnel'
	| 'mihomo-proxy'
	| 'mihomo-subscription'
	| 'mihomo-group'
	| 'singbox-subscription'
	| 'singbox-outbound';

export type EgressEngine = 'system' | 'mihomo' | 'sing-box';

export interface EgressRef {
	kind: EgressKind;
	resourceId: string;
	engine: EgressEngine;
}

export interface Capabilities {
	tcp: boolean;
	udp: boolean;
	icmp: boolean;
	ipv4: boolean;
	ipv6: boolean;
}

export interface ResolvedEgress {
	ref: EgressRef;
	displayName: string;
	interface: string;
	capabilities: Capabilities;
	digest: string;
	available: boolean;
	unavailableReason?: string;
}

export type SourceScopeType = 'all_lan' | 'policy' | 'interfaces' | 'server_tunnel';

export interface SourceScope {
	type: SourceScopeType;
	policyId?: string;
	interfaces?: string[];
	tunnelTag?: string;
}

export interface DetectionSettings {
	fastIntervalSeconds: number;
	softIntervalSeconds: number;
	judgeIntervalSeconds: number;
	healthIntervalSeconds: number;
	tcpSynRetries: number;
	lateStallBytes: number;
}

export interface PersistenceConfig {
	okTtlSeconds: number;
	maxEntries: number;
	separateTcpUdp: boolean;
}

export interface AdaptiveDnsSettings {
	enabled: boolean;
	servers: string[];
	routeViaTunnel: boolean;
	interceptPort53: boolean;
}

export interface AdaptiveRoutingSettings {
	enabled: boolean;
	routingTableId: number;
	fwmarkMask: string;
	fwmarkTest: string;
	fwmarkOk: string;
	rulePriorityTest: number;
	rulePriorityOk: number;
	source: SourceScope;
	primaryEgress: EgressRef;
	fallbackEgresses?: EgressRef[];
	failurePolicy: 'direct' | 'block';
	detection: DetectionSettings;
	persistence: PersistenceConfig;
	dns?: AdaptiveDnsSettings;
	alwaysFileEnabled: boolean;
	neverFileEnabled: boolean;
	alwaysEntries?: string[];
	neverEntries?: string[];
}

export interface OperationalState {
	appliedGeneration: string;
	routingOwner: RoutingOwner;
	activeEgress?: ResolvedEgress;
	fallbackActive: boolean;
	status: 'stopped' | 'running' | 'learning' | 'degraded' | 'recovery_required';
	learnedTcpCount: number;
	learnedUdpCount: number;
	testingTcpCount: number;
	testingUdpCount: number;
	alwaysCount: number;
	neverCount: number;
	lastReconcile: string;
	lastError?: string;
	recoveryMarker?: string;
	installed?: boolean;
	version?: string;
	binary?: string;
}

export interface AdaptiveRoutingStatusResponse {
	state: OperationalState;
	settings: AdaptiveRoutingSettings;
}

export interface EgressesResponse {
	items: ResolvedEgress[];
}

export interface PreviewResponse {
	egress: ResolvedEgress;
}

export interface SusaninLogEvent {
	timestamp: string;
	level: string;
	action: string;
	target: string;
	message: string;
	raw: string;
	resourceTitle?: string;
	resourceOrg?: string;
	resourceCountry?: string;
	resourceCc?: string;
	resourceIcon?: string;
}

export interface LearnedDataResponse {
	testTcpCount: number;
	testUdpCount: number;
	okTcpCount: number;
	okUdpCount: number;
	always: string[];
	never: string[];
	okTcp?: string[];
	okUdp?: string[];
	okNet?: string[];
	testTcp?: string[];
	testUdp?: string[];
	knowledge?: Record<
		string,
		{
			title: string;
			description?: string;
			org?: string;
			country?: string;
			cc?: string;
			category?: string;
			icon?: string;
		}
	>;
}

