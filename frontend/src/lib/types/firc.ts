export function stripFircEmojis(text: string | undefined | null): string {
	if (!text) return '';
	const cleaned = text
		.replace(
			/[\u{1F1E6}-\u{1F1FF}\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}\u{FE00}-\u{FE0F}\u{200D}]+/gu,
			'',
		)
		.replace(/\s{2,}/g, ' ')
		.trim();
	return cleaned || text;
}

export interface FircTunnelUplink {
	kind: 'auto' | 'iface' | 'tunnel';
	ref: string;
}

export interface FircTunnelSource {
	id: string;
	kind: 'link' | 'subscription';
	link?: string;
	name?: string;
	url?: string;
	interval?: number;
	headers?: Record<string, string>;
}

export interface FircTunnelAdvanced {
	ca: string;
	insecure: boolean;
	timeout: number;
	kernelTcp: boolean;
	fingerprint: '' | 'chrome' | 'firefox';
}

export interface FircTunnelInbound {
	port: number;
	listen: string;
	node: string;
	user: string;
	password: string;
}

export interface FircTunnelInboundState {
	port: number;
	state: 'up' | 'busy' | 'no-node' | 'no-address' | 'not-lan' | 'starting' | 'off';
	why: string;
	rxBps: number;
	txBps: number;
}

export interface FircTunnel {
	id: string;
	device: string;
	description: string;
	enable: boolean;
	active: number;
	by: 'connection' | 'site' | 'site-client';
	interval: number;
	silence: number;
	filter: string;
	filterExclude?: string;
	order: string[];
	exclude: string[];
	sources: FircTunnelSource[];
	uplink: FircTunnelUplink;
	advanced: FircTunnelAdvanced;
	inbounds: FircTunnelInbound[];
	// Legacy compatibility fields
	name?: string;
	type?: string;
	url?: string;
	ip?: string;
	state?: string;
	current?: string;
}

export type FircTunnelStatus =
	| 'off'
	| 'starting'
	| 'up'
	| 'no_node'
	| 'backoff'
	| 'bad_config'
	| 'waiting'
	| 'uplink_down'
	| 'router';

export const FIRC_TUNNEL_NODE_POOL = '*';

export interface FircTunnelNodeState {
	key: string;
	name: string;
	source: string;
	state: 'active' | 'down' | 'reserve' | 'excluded' | 'missing' | 'skipped';
	since: number;
	skipReason?: string;
	v6?: boolean;
}

export interface FircTunnelSubUsage {
	used: number;
	total: number;
	expire: number;
}

export interface FircTunnelSubState {
	name: string;
	nodes: number;
	fetching: boolean;
	lastOk: number;
	lastTry: number;
	error: string;
	usage?: FircTunnelSubUsage | null;
}

export interface FircTunnelFilteredNode {
	key: string;
	name: string;
	source: string;
}

export interface FircTunnelState {
	id: string;
	device: string;
	status: FircTunnelStatus;
	why?: string;
	since: number;
	backoffS: number;
	lastExit: number;
	uplinkOk: boolean;
	tcpPort: number;
	tcpSkipped: string[];
	tcpOff: '' | 'acl' | 'kernel' | 'refused';
	active: string[];
	groups: string[];
	nodes: FircTunnelNodeState[];
	filteredNodes?: FircTunnelFilteredNode[];
	subscriptions: FircTunnelSubState[];
	rxBps: number;
	txBps: number;
	inbounds: FircTunnelInboundState[];
}

export interface FircTunnelPreviewRow {
	key: string;
	name: string;
	source: string;
	isNew: boolean;
	missing: boolean;
	excluded: boolean;
	overCap: boolean;
	skipReason?: string;
}

export interface FircTunnelPreview {
	total: number;
	matched: number;
	nodes: FircTunnelPreviewRow[];
	filteredNodes?: FircTunnelFilteredNode[];
	subscriptions: { name: string; cached: boolean }[];
}

export interface FircTunnelProbeRow {
	key: string;
	ok: boolean;
	why: string;
	handshakeMs: number | null;
	firstByteMs: number | null;
}

export interface FircTunnelProbeProgress {
	total: number;
	nodes: FircTunnelProbeRow[];
}

export type FircRuleType = 'namespace' | 'wildcard' | 'domain' | 'regex' | 'subnet' | 'subnet6';

export const FIRC_RULE_TYPES: { value: FircRuleType; label: string }[] = [
	{ value: 'namespace', label: 'Namespace' },
	{ value: 'wildcard', label: 'Wildcard' },
	{ value: 'domain', label: 'Domain' },
	{ value: 'regex', label: 'Regex' },
	{ value: 'subnet', label: 'IPv4 CIDR' },
	{ value: 'subnet6', label: 'IPv6 CIDR' },
];

export interface FircRule {
	id: string;
	name?: string;
	type: FircRuleType | string;
	rule: string;
	enable: boolean;
	proto?: 'tcp' | 'udp';
	ports?: string;
	comment?: string;
	autolist?: string;
	autolistByDomain?: boolean;
}

export interface FircListRule {
	id: string;
	rule: string;
	type: string;
	enable: boolean;
	proto?: string;
	ports?: string;
}

export interface FircSyncState {
	state: 'idle' | 'queued' | 'fetching' | 'error';
	error: string;
	lastCheck: number;
}

export interface FircGroupList {
	url: string;
	interval?: number;
	lastUpdate?: number;
	rulesTotal?: number;
	rules?: number;
	sync?: FircSyncState;
}

export interface FircGroupResolve {
	tunnel: boolean;
	server: string;
}

export interface FircGroupResolverHealth {
	window: number;
	total: number;
	fallbacks: number;
	last: number;
	reason: string;
	resting: boolean;
	since: number;
}

export interface FircGroupResolver {
	source: 'group' | 'firmware' | 'none' | 'off';
	servers: string[];
	health?: FircGroupResolverHealth;
}

export interface FircDeviceSelector {
	allow: string[];
	deny: string[];
}

export type FircBlockAnswer = 'null' | 'nxdomain' | 'refused';

export interface FircGroupCheck {
	mode: 'none' | 'url' | 'keendns';
	url?: string;
	intervalSec?: number;
	timeoutMs?: number;
	failThreshold?: number;
	recoverThreshold?: number;
}

export interface FircGroup {
	id: string;
	name: string;
	interface: string | string[];
	enable: boolean;
	color?: string;
	devices?: FircDeviceSelector;
	list?: FircGroupList | null;
	rules: FircRule[];
	resolve?: FircGroupResolve;
	check?: FircGroupCheck;
	resolver?: FircGroupResolver;
	block?: FircBlockAnswer;
	cloudflare?: boolean;
}

export interface FircGroupIfaceState {
	name: string;
	state: 'alive' | 'dead' | 'unknown';
	since: number;
	error: string;
}

export interface FircGroupLive {
	live: boolean;
	active?: string;
	reason?: string;
	detail?: string;
	since?: number;
	interfaces?: FircGroupIfaceState[];
	resolver?: FircGroupResolver;
	list?: FircGroupList | null;
}

export interface FircGroupsStateRes {
	groups: Record<string, FircGroupLive>;
	netfilter?: {
		dirty: boolean;
		since?: number;
		error?: string;
	};
}

export interface FircListRulesPage {
	total: number;
	matched: number;
	offset: number;
	rules: FircListRule[];
}

export interface FircListPreviewRes {
	rules: FircListRule[];
	total?: number;
	byType?: Record<string, number>;
	dropped?: number;
	unconstrained?: number;
}

export interface FircCatalogSource {
	id: string;
	url: string;
	names: string[];
}

export interface FircCatalog {
	generated: string;
	fetchedAt: number;
	stale: boolean;
	sources: FircCatalogSource[];
}

export interface FircInterfaceOption {
	id: string;
	name?: string;
	ndmsName?: string;
	role?: 'tunnel' | 'wan' | 'lan' | 'other';
	up?: boolean;
}

export interface FircHost {
	name: string;
	mac: string;
	ip: string;
	ip6: string[];
	online?: boolean;
	active?: boolean;
	segment?: string;
	policy?: string;
}

export interface FircPolicy {
	name: string;
	description?: string;
	devices: number;
}

export interface FircSystemResolver {
	interface: string;
	servers: string[];
}

export type FircSettingValue = string | number | boolean | string[];
export type FircSettingClass = 'live' | 'restart';

export interface FircSettingsRes {
	settings: Record<string, FircSettingValue>;
	classes: Record<string, FircSettingClass>;
	boot: string;
	pendingRestart: string[];
	restarting?: boolean;
}

export type FircLogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface FircLogEvent {
	seq: number;
	at: number;
	kind: 'log';
	level: FircLogLevel;
	source: string;
	message: string;
}

export type FircDnsDecision =
	| 'issued'
	| 'not-covered'
	| 'no-match'
	| 'pool-refused'
	| 'passed'
	| 'blocked';

export interface FircDnsEvent {
	seq: number;
	at: number;
	kind: 'dns';
	client: string;
	name: string;
	qtype: string;
	rcode: string;
	decision: FircDnsDecision;
	resolver?: string;
	group?: { id: string; name: string };
	fake?: string;
	reals: string[];
}

export interface FircBypassEvent {
	seq: number;
	at: number;
	kind: 'bypass';
	client: string;
	dst: string;
	port?: number;
	proto: string;
	how: 'addr' | 'sni';
	name: string;
	group?: { id: string; name: string };
	last?: { decision: FircDnsDecision; at: number; fake?: string };
	repeats: number;
}

export type FircJournalEvent = FircLogEvent | FircDnsEvent | FircBypassEvent;

export interface FircEventsRes {
	events: FircJournalEvent[];
	next: number;
	dropped: number;
	boot: string;
}

export interface FircDnsStatsOwner {
	id: string;
	name: string;
	interface: string;
	blocked: boolean;
}

export interface FircDnsStatsRow {
	name: string;
	queries: number;
	blocked: number;
	last: number;
	owner: FircDnsStatsOwner | null;
	clients: { address: string; queries: number }[];
	clientsTotal: number;
}

export interface FircDnsStatsRes {
	enabled: boolean;
	window: number;
	now: number;
	since: number;
	queries: number;
	blocked: number;
	domains: number;
	rows: FircDnsStatsRow[];
	matched: number;
	offset: number;
}

export interface FircCaptureLast {
	startedAt: number;
	endedAt: number;
	ended: 'reached-end' | 'stopped' | 'failed';
	leaks: number;
	summary: string[];
}

export interface FircCaptureRes {
	running: boolean;
	endsAt?: number;
	secondsLeft?: number;
	token?: string;
	last?: FircCaptureLast;
}

export interface FircRecoveryMarker {
	reason?: string;
	transitionId?: string;
	targetEngine?: string;
	previousEngine?: string;
	failedStep?: string;
	lastCompleted?: string;
	timestamp?: number;
}

export interface FircStatus {
	fircdInstalled: boolean;
	tunvlessInstalled: boolean;
	running: boolean;
	selected: boolean;
	version?: string;
	socketPath: string;
	dnsProxyEndpoint: string;
	inboundPort: number;
	dnsPort?: number;
	tunnelsCount: number;
	groupsCount: number;
	lastError?: string;
	recoveryRequired?: boolean;
	recoveryReason?: string;
	recoveryMarker?: FircRecoveryMarker | null;
}

export type FircInstallState =
	| 'not_installed'
	| 'installed'
	| 'running'
	| 'recovery_required'
	| 'installing'
	| 'uninstalling'
	| 'unavailable';

export interface FircInstallStatus {
	state: FircInstallState;
	installedVersion?: string;
	availableVersion?: string;
	updateAvailable?: boolean;
	available: boolean;
	unavailableReason?: string;
	activeEngine: boolean;
	externalPackage?: string;
	externalMigratable?: boolean;
	dependentServices?: string[];
	ndmHookPresent: boolean;
	fircdPresent: boolean;
	tunvlessPresent: boolean;
	initScriptPresent: boolean;
	configPresent: boolean;
	freeDiskBytes?: number;
	requiredDiskBytes?: number;
	busy: boolean;
	busyAction?: string;
	progress?: number;
	error?: string;
}

export interface FircEnginePurgeResult {
	engine: 'sing-box' | 'firc' | string;
	tunnelsRemoved: number;
	groupsRemoved: number;
	subscriptionsRemoved?: number;
	runtimeRemoved: boolean;
	snapshotCreated?: FircSnapshotMeta;
	preservedSnapshots: number;
	savedSingboxPreserved: boolean;
}

export interface FircMigrationOptions {
	mode?: 'merge' | 'replace';
	routerSource?: 'auto' | 'tproxy' | 'fakeip' | 'both';
	includeTunnels?: boolean;
	includeSubscriptions?: boolean;
	includeGroups?: boolean;
	includeClientRoutes?: boolean;
	includeFakeIPConfig?: boolean;
	selectedTunnelIds?: string[];
	selectedGroupIds?: string[];
	postSingboxAction?: 'switch_standby' | 'switch_and_stop' | 'switch_and_uninstall' | 'keep_current';
	createSnapshot?: boolean;
}

export interface FircFakeIPMigrationInfo {
	detected: boolean;
	sourceFile?: string;
	slotFile?: string;
	inet4Range?: string;
	inet6Range?: string;
	realDns?: string;
	routeFinal?: string;
	finalOutbound?: string;
	dnsRulesCount?: number;
	applied?: boolean;
}

export interface FircSnapshotMeta {
	id: string;
	createdAt: number;
	label: string;
	trigger: 'auto-pre-migrate' | 'auto-pre-reverse' | 'auto-pre-purge' | 'manual' | string;
	activeEngine: string;
	singboxTunnelsCount: number;
	singboxSubscriptionsCount: number;
	singboxGroupsCount: number;
	fircTunnelsCount: number;
	fircGroupsCount: number;
	filesCount: number;
	postActionApplied?: string;
	postActionError?: string;
	recoveryRequired?: boolean;
}

export interface FircSavedSingboxStatus {
	available: boolean;
	verificationStatus?: 'verified' | 'validator_unavailable' | 'invalid' | 'missing' | string;
	source?: 'saved_bundle' | 'snapshot' | string;
	snapshotId?: string;
	createdAt?: number;
	label?: string;
	tunnelsCount: number;
	subscriptionsCount: number;
	groupsCount: number;
	filesCount: number;
	unavailableReason?: string;
}

export interface FircSavedSingboxRestoreResult {
	source: string;
	restoredFiles: number;
	snapshotCreated?: FircSnapshotMeta;
	postActionApplied?: string;
	postActionError?: string;
	removedEngine?: FircEnginePurgeResult;
	recoveryRequired?: boolean;
}

export interface FircMigrationWizardState {
	dismissedAutoPopup: boolean;
	dismissedAt?: number;
	lastMigratedAt?: number;
	lastDirection?: string;
	lastSnapshotId?: string;
	lastPostAction?: string;
}

export interface FircReverseMigrationOptions {
	mode?: 'merge' | 'replace';
	includeTunnels?: boolean;
	includeGroups?: boolean;
	selectedTunnelIds?: string[];
	selectedGroupIds?: string[];
	postFircAction?: 'switch_to_singbox' | 'switch_and_stop_firc' | 'switch_and_uninstall_firc' | 'keep_current';
	createSnapshot?: boolean;
}

export interface FircReverseMigratedTunnelItem {
	id: string;
	name: string;
	device: string;
	kind: 'subscription' | 'outbound' | string;
	linksCount: number;
	subUrl?: string;
	targetTag: string;
}

export interface FircReverseMigratedGroupItem {
	id: string;
	name: string;
	outboundTag: string;
	rulesCount: number;
	ruleSetUrl?: string;
	devices?: string[];
}

export interface FircReverseMigrationSummary {
	tunnels?: FircReverseMigratedTunnelItem[];
	groups?: FircReverseMigratedGroupItem[];
	outboundsCreated?: number;
	subscriptionsMigrated?: number;
	rulesMigrated?: number;
	ruleSetsMigrated?: number;
	clientRoutesMigrated?: number;
	skipped?: string[];
	snapshotCreated?: FircSnapshotMeta;
	postActionApplied?: string;
	postActionError?: string;
	removedEngine?: FircEnginePurgeResult;
	recoveryRequired?: boolean;
}

export interface FircMigrationOverview {
	shouldShowPopup: boolean;
	wizardState: FircMigrationWizardState;
	activeEngine: string;
	fircRunning: boolean;
	fircInstalled?: boolean;
	singboxRunning: boolean;
	singboxInstalled?: boolean;
	singboxTunnelsCount?: number;
	singboxSubscriptionsCount?: number;
	fircTunnelsCount?: number;
	fircGroupsCount?: number;
	snapshots: FircSnapshotMeta[];
	savedSingbox?: FircSavedSingboxStatus;
	forwardPreview?: FircMigrationSummary;
	reversePreview?: FircReverseMigrationSummary;
}

export interface FircMigrationSummary {
	tunnels?: FircTunnel[];
	groups?: FircGroup[];
	skipped?: string[];
	tunnelsCreated?: number;
	subscriptionsMigrated?: number;
	groupsCreated?: number;
	rulesMigrated?: number;
	ruleSetsMigrated?: number;
	clientRoutesMigrated?: number;
	routerSourcesUsed?: string[];
	fakeip?: FircFakeIPMigrationInfo;
	snapshotCreated?: FircSnapshotMeta;
	configMigrated?: boolean;
	fircStarted?: boolean;
	engineSwitched?: boolean;
	startError?: string;
	switchError?: string;
	postActionApplied?: string;
	postActionError?: string;
	removedEngine?: FircEnginePurgeResult;
	recoveryRequired?: boolean;
	warnings?: string[];
}

export interface FircProxyGroup {
	id: string;
	name: string;
	interfaces: string[];
}

export interface FircInterfaceProbeResult {
	interface: string;
	ok: boolean;
	latencyMs: number;
	httpCode?: number;
	error?: string;
	checkedAt?: number;
}

export interface FircInterfacesProbeRes {
	url: string;
	checkedAt: number;
	results: Record<string, FircInterfaceProbeResult>;
}

export interface FircKeenDNSCloudConfig {
	enabled: boolean;
	cloudHealthCheck?: boolean;
	proxyGroupId?: string;
	interfaces?: string[];
	lastSyncError?: string;
	lastSyncAt?: number;
}

export interface FircInboundServerItem {
	id: string;
	name: string;
	category: 'awgm' | 'system';
	running: boolean;
	detail: string;
}

export interface FircServerRoutingConfig {
	enabledServers: string[];
	awgServer?: boolean;
	wdttServer?: boolean;
	freeTurnServer?: boolean;
	vlessXhttpServer?: boolean;
}

export interface FircExtConfig {
	globalTarget?: string;
	proxyGroups: FircProxyGroup[];
	keenDnsCloud: FircKeenDNSCloudConfig;
	serverRouting: FircServerRoutingConfig;
	availableServers?: FircInboundServerItem[];
}

export interface FircMonitorDomainStat {
	domain: string;
	queries: number;
	lastAt?: number;
}

export interface FircMonitorClientStat {
	ip: string;
	sourceId?: string;
	sourceName?: string;
	conns: number;
	txBytes: number;
	rxBytes: number;
	groups?: string[];
}

export interface FircMonitorServerRow {
	id: string;
	name: string;
	category: 'awgm' | 'system' | 'lan';
	detail: string;
	running: boolean;
	routingEnabled: boolean;
	dnsPackets: number;
	dnsBytes: number;
	matchedQueries: number;
	activeConns: number;
	activeClients?: number;
	clients?: FircMonitorClientStat[];
	txBytes: number;
	rxBytes: number;
	lastDomain?: string;
	lastGroup?: string;
	lastOutbound?: string;
	lastOutboundLabel?: string;
	lastAt?: number;
}

export interface FircMonitorGroupRow {
	id: string;
	name: string;
	activeOutbound: string;
	activeOutboundLabel?: string;
	live: boolean;
	mark?: string;
	fakeSubnet?: string;
	rulesTotal?: number;
	subnetRules?: number;
	matchedPackets: number;
	matchedBytes: number;
	activeConns: number;
	activeClients?: number;
	clients?: FircMonitorClientStat[];
	connTxBytes: number;
	connRxBytes: number;
	matchedQueries: number;
	uniqueDomains?: number;
	topDomains?: FircMonitorDomainStat[];
	lastDomain?: string;
	lastAt?: number;
}

export interface FircMonitorMatchEvent {
	seq: number;
	at: number;
	clientIp: string;
	sourceId: string;
	sourceName: string;
	domain: string;
	fakeIp?: string;
	realIp?: string;
	groupId: string;
	groupName: string;
	outbound: string;
	outboundLabel?: string;
}

export interface FircMonitorSnapshot {
	timestamp: number;
	running: boolean;
	totalConns: number;
	totalClients?: number;
	clients?: FircMonitorClientStat[];
	totalTxBytes?: number;
	totalRxBytes?: number;
	totalDns: number;
	totalMatched: number;
	servers: FircMonitorServerRow[];
	groups: FircMonitorGroupRow[];
	recentMatches: FircMonitorMatchEvent[];
}

export interface FircDomainCheckResult {
	domain: string;
	routed: boolean;
	fakeIp?: string;
	realIp?: string;
	groupId?: string;
	groupName?: string;
	outbound: string;
	outboundLabel?: string;
	latencyMs: number;
	error?: string;
}

export interface FircAutolistAddressGroup {
	id: string;
	name: string;
}

export interface FircAutolistAddress {
	address: string;
	outcome: string;
	how: string;
	group: string;
	proto: string;
	port: number;
	name: string;
	ptr: string;
	ptrState: string;
	why: string;
	fails: number;
	clients: string[];
	firstAt: number;
	lastAt: number;
	groups: FircAutolistAddressGroup[];
}

export interface FircAutolistCandidate {
	domain: string;
	host: string;
	why: string;
	fails: number;
	clients: string[];
	lastAt: number;
	state: string;
	notes: string;
}

export interface FircAutolistProbeResult {
	host: string;
	at: number;
	verdict: string;
	why: string;
	bytes?: number;
	error?: string;
	candidate?: string;
	resolver?: string;
	address?: string;
	httpStatus?: number;
}

export interface FircAutolistProbes {
	usedHour: number;
	queued: number;
	running: boolean | string | null;
	results: FircAutolistProbeResult[];
}

export interface FircAutolistStatus {
	enabled: boolean;
	running?: boolean;
	failures?: number;
	listed: number;
	whyOff?: string;
	checking?: boolean;
	checkedAt?: number;
	switchedOff?: number;
	switchedOn?: number;
	mode: string;
	group?: string;
	groupId?: string;
	groupName?: string;
	interface?: string;
	failuresDay: number;
	ambiguousDay?: number;
	wanWarning?: boolean;
	addedHour?: number;
	addresses: FircAutolistAddress[];
	candidates: FircAutolistCandidate[];
	probes?: FircAutolistProbes;
}

export interface FircExtractedDomains {
	domains: string[];
	lines: string[];
	text: string;
	skippedRulesCount: number;
	skippedRegexOrCidr: number;
	listGroupsCount: number;
	skippedRemoteLists: number;
}

/**
 * Extracts enabled domain/wildcard/namespace rules from selected FIRC groups (or all groups),
 * deduplicating them in order while separating regex, CIDR, and external rule-list counts.
 */
export function extractFircGroupDomainLines(
	groups: FircGroup[],
	selectedGroupIds?: Iterable<string>,
): FircExtractedDomains {
	const arrIds = selectedGroupIds ? Array.from(selectedGroupIds) : [];
	const selectedSet = arrIds.length > 0 ? new Set(arrIds) : null;
	const seen = new Set<string>();
	const domains: string[] = [];
	let skippedRulesCount = 0;
	let listGroupsCount = 0;

	for (const g of groups ?? []) {
		if (selectedSet ? !selectedSet.has(g.id) : !g.enable) continue;
		if (g.list?.url?.trim()) {
			listGroupsCount++;
		}
		for (const r of g.rules ?? []) {
			if (!r.enable) continue;
			const val = (r.rule ?? '').trim();
			if (!val) continue;
			const t = (r.type ?? 'domain').trim();
			if (
				t === 'domain' ||
				t === 'wild' ||
				t === 'DotWild' ||
				t === 'wildcard' ||
				t === 'namespace'
			) {
				if (!seen.has(val)) {
					seen.add(val);
					domains.push(val);
				}
			} else {
				skippedRulesCount++;
			}
		}
	}

	return {
		domains,
		lines: domains,
		text: domains.join('\n'),
		skippedRulesCount,
		skippedRegexOrCidr: skippedRulesCount,
		listGroupsCount,
		skippedRemoteLists: listGroupsCount,
	};
}
