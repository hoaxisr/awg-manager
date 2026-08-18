// GENERATED FILE — do not edit by hand.
// Source: internal/openapi/swagger.yaml (swaggo). Regenerate: npm run gen:api
// Generator: scripts/gen-api-schemas.mjs (см. там же дизайн-решения).
/* eslint-disable */
import * as v from 'valibot';

const api_APIEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.unknown())),
	message: v.optional(v.nullable(v.string())),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ASCParamsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.unknown())),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AWGInterfaceDTO: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	dns: v.optional(v.nullable(v.string())),
	h1: v.optional(v.nullable(v.string())),
	h2: v.optional(v.nullable(v.string())),
	h3: v.optional(v.nullable(v.string())),
	h4: v.optional(v.nullable(v.string())),
	jc: v.optional(v.nullable(v.number())),
	jmax: v.optional(v.nullable(v.number())),
	jmin: v.optional(v.nullable(v.number())),
	mtu: v.optional(v.nullable(v.number())),
	privateKey: v.optional(v.nullable(v.string())),
	s1: v.optional(v.nullable(v.number())),
	s2: v.optional(v.nullable(v.number())),
	s3: v.optional(v.nullable(v.number())),
	s4: v.optional(v.nullable(v.number())),
});

const api_AWGOutboundTagDTO: v.GenericSchema = v.looseObject({
	iface: v.optional(v.nullable(v.string())),
	kind: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_AWGOutboundTagsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_AWGOutboundTagDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AWGPeerDTO: v.GenericSchema = v.looseObject({
	allowedIPs: v.optional(v.nullable(v.array(v.string()))),
	endpoint: v.optional(v.nullable(v.string())),
	persistentKeepalive: v.optional(v.nullable(v.unknown())),
	publicKey: v.optional(v.nullable(v.string())),
});

const api_AWGTunnelDTO: v.GenericSchema = v.looseObject({
	backend: v.optional(v.nullable(v.string())),
	defaultRoute: v.optional(v.nullable(v.boolean())),
	enabled: v.optional(v.nullable(v.boolean())),
	id: v.optional(v.nullable(v.string())),
	interface: v.optional(v.nullable(v.lazy(() => api_AWGInterfaceDTO))),
	interfaceName: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.lazy(() => api_AWGPeerDTO))),
	state: v.optional(v.nullable(v.string())),
	stateInfo: v.optional(v.nullable(v.lazy(() => api_TunnelStateInfoDTO))),
	type: v.optional(v.nullable(v.string())),
});

const api_AccessPoliciesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_AccessPolicyDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AccessPolicyDTO: v.GenericSchema = v.looseObject({
	description: v.optional(v.nullable(v.string())),
	deviceCount: v.optional(v.nullable(v.number())),
	interfaces: v.optional(v.nullable(v.array(v.lazy(() => api_AccessPolicyInterfaceDTO)))),
	isStandard: v.optional(v.nullable(v.boolean())),
	name: v.optional(v.nullable(v.string())),
	standalone: v.optional(v.nullable(v.boolean())),
});

const api_AccessPolicyInterfaceDTO: v.GenericSchema = v.looseObject({
	denied: v.optional(v.nullable(v.boolean())),
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	order: v.optional(v.nullable(v.number())),
});

const api_AccessPolicyResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_AccessPolicyDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ActiveNowResponse: v.GenericSchema = v.looseObject({
	now: v.optional(v.nullable(v.string())),
});

const api_AllInterfacesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_RouterInterfaceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AmneziaPremiumAccountInfoResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.unknown())),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AmneziaPremiumDownloadConfigData: v.GenericSchema = v.looseObject({
	config: v.optional(v.nullable(v.string())),
});

const api_AmneziaPremiumDownloadConfigResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_AmneziaPremiumDownloadConfigData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AmneziaPremiumLoginData: v.GenericSchema = v.looseObject({
	sid: v.optional(v.nullable(v.string())),
});

const api_AmneziaPremiumLoginResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_AmneziaPremiumLoginData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_AuthStatusResponse: v.GenericSchema = v.looseObject({
	authDisabled: v.optional(v.nullable(v.boolean())),
	authenticated: v.optional(v.nullable(v.boolean())),
	entwareAuthEnabled: v.optional(v.nullable(v.boolean())),
	expiresIn: v.optional(v.nullable(v.number())),
	login: v.optional(v.nullable(v.string())),
});

const api_Awg3ListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_Awg3TunnelDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_Awg3TunnelDTO: v.GenericSchema = v.looseObject({
	disableCookies: v.optional(v.nullable(v.boolean())),
	headerProtection: v.optional(v.nullable(v.boolean())),
	host: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	keepaliveTimeout: v.optional(v.nullable(v.string())),
	maxHandshakeAttempts: v.optional(v.nullable(v.string())),
	randomTrailers: v.optional(v.nullable(v.boolean())),
	rejectAfterTime: v.optional(v.nullable(v.string())),
	rekeyAfterTime: v.optional(v.nullable(v.string())),
	rekeyTimeout: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_BackupWarningDTO: v.GenericSchema = v.looseObject({
	interfaceName: v.optional(v.nullable(v.string())),
	message: v.optional(v.nullable(v.string())),
});

const api_BootStatusResponse: v.GenericSchema = v.looseObject({
	initializing: v.optional(v.nullable(v.boolean())),
	instanceId: v.optional(v.nullable(v.string())),
	phase: v.optional(v.nullable(v.string())),
	remainingSeconds: v.optional(v.nullable(v.number())),
});

const api_BypassSetStatusData: v.GenericSchema = v.looseObject({
	available: v.optional(v.nullable(v.boolean())),
	conntrackAvailable: v.optional(v.nullable(v.boolean())),
	entryCount: v.optional(v.nullable(v.number())),
	entryCountOK: v.optional(v.nullable(v.boolean())),
	installing: v.optional(v.nullable(v.boolean())),
	lastError: v.optional(v.nullable(v.string())),
	lastPopulate: v.optional(v.nullable(v.string())),
	missingTags: v.optional(v.nullable(v.array(v.string()))),
});

const api_ChangelogData: v.GenericSchema = v.looseObject({
	entries: v.optional(v.nullable(v.array(v.lazy(() => api_ChangelogEntryDTO)))),
});

const api_ChangelogEntryDTO: v.GenericSchema = v.looseObject({
	date: v.optional(v.nullable(v.string())),
	groups: v.optional(v.nullable(v.array(v.lazy(() => api_ChangelogGroupDTO)))),
	version: v.optional(v.nullable(v.string())),
});

const api_ChangelogGroupDTO: v.GenericSchema = v.looseObject({
	heading: v.optional(v.nullable(v.string())),
	items: v.optional(v.nullable(v.array(v.string()))),
});

const api_ChangelogResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ChangelogData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ClientRouteDTO: v.GenericSchema = v.looseObject({
	clientHostname: v.optional(v.nullable(v.string())),
	clientIp: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	fallback: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
});

const api_ClientRouteToggleData: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	id: v.optional(v.nullable(v.string())),
});

const api_ClientRoutesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_ClientRouteDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ConfigSlotContentResponse: v.GenericSchema = v.looseObject({
	content: v.optional(v.nullable(v.string())),
	filename: v.optional(v.nullable(v.string())),
	hasDraft: v.optional(v.nullable(v.boolean())),
	slot: v.optional(v.nullable(v.string())),
	state: v.optional(v.nullable(v.string())),
});

const api_ConfigSlotInfo: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	filename: v.optional(v.nullable(v.string())),
	hasDraft: v.optional(v.nullable(v.boolean())),
	mtime: v.optional(v.nullable(v.string())),
	ownership: v.optional(v.nullable(v.string())),
	size: v.optional(v.nullable(v.number())),
	slot: v.optional(v.nullable(v.string())),
});

const api_ConfigSlotsResponse: v.GenericSchema = v.looseObject({
	slots: v.optional(v.nullable(v.array(v.lazy(() => api_ConfigSlotInfo)))),
});

const api_ConnectionBucketDTO: v.GenericSchema = v.looseObject({
	bytesIn: v.optional(v.nullable(v.number())),
	bytesOut: v.optional(v.nullable(v.number())),
	count: v.optional(v.nullable(v.number())),
	key: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
});

const api_ConnectionKillData: v.GenericSchema = v.looseObject({
	ok: v.optional(v.nullable(v.boolean())),
});

const api_ConnectionKillEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ConnectionKillData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ConnectionProtocolsDTO: v.GenericSchema = v.looseObject({
	icmp: v.optional(v.nullable(v.number())),
	tcp: v.optional(v.nullable(v.number())),
	udp: v.optional(v.nullable(v.number())),
});

const api_ConnectionRuleHitDTO: v.GenericSchema = v.looseObject({
	fqdn: v.optional(v.nullable(v.string())),
	listId: v.optional(v.nullable(v.string())),
	listName: v.optional(v.nullable(v.string())),
	pattern: v.optional(v.nullable(v.string())),
});

const api_ConnectionStatsDTO: v.GenericSchema = v.looseObject({
	direct: v.optional(v.nullable(v.number())),
	local: v.optional(v.nullable(v.number())),
	protocols: v.optional(v.nullable(v.lazy(() => api_ConnectionProtocolsDTO))),
	singbox: v.optional(v.nullable(v.number())),
	total: v.optional(v.nullable(v.number())),
	tunneled: v.optional(v.nullable(v.number())),
});

const api_ConnectionsData: v.GenericSchema = v.looseObject({
	byClient: v.optional(v.nullable(v.array(v.lazy(() => api_ConnectionBucketDTO)))),
	byDst: v.optional(v.nullable(v.array(v.lazy(() => api_ConnectionBucketDTO)))),
	byTunnel: v.optional(v.nullable(v.array(v.lazy(() => api_ConnectionBucketDTO)))),
	connections: v.optional(v.nullable(v.array(v.lazy(() => api_ConntrackConnectionDTO)))),
	fetchedAt: v.optional(v.nullable(v.string())),
	pagination: v.optional(v.nullable(v.lazy(() => api_ConnectionsPaginationDTO))),
	stats: v.optional(v.nullable(v.lazy(() => api_ConnectionStatsDTO))),
	tunnels: v.optional(v.nullable(v.record(v.string(), v.lazy(() => api_TunnelConnectionInfoDTO)))),
});

const api_ConnectionsPaginationDTO: v.GenericSchema = v.looseObject({
	limit: v.optional(v.nullable(v.number())),
	offset: v.optional(v.nullable(v.number())),
	returned: v.optional(v.nullable(v.number())),
	total: v.optional(v.nullable(v.number())),
});

const api_ConnectionsResponseEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ConnectionsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ConnectivityResultData: v.GenericSchema = v.looseObject({
	connected: v.optional(v.nullable(v.boolean())),
	latency: v.optional(v.nullable(v.number())),
	reason: v.optional(v.nullable(v.string())),
});

const api_ConnectivityResultResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ConnectivityResultData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ConntrackConnectionDTO: v.GenericSchema = v.looseObject({
	bytes: v.optional(v.nullable(v.number())),
	bytesIn: v.optional(v.nullable(v.number())),
	bytesOut: v.optional(v.nullable(v.number())),
	clientMac: v.optional(v.nullable(v.string())),
	clientName: v.optional(v.nullable(v.string())),
	dst: v.optional(v.nullable(v.string())),
	dstPort: v.optional(v.nullable(v.number())),
	interface: v.optional(v.nullable(v.string())),
	packets: v.optional(v.nullable(v.number())),
	protocol: v.optional(v.nullable(v.string())),
	routeClass: v.optional(v.nullable(v.string())),
	rules: v.optional(v.nullable(v.array(v.lazy(() => api_ConnectionRuleHitDTO)))),
	src: v.optional(v.nullable(v.string())),
	srcPort: v.optional(v.nullable(v.number())),
	state: v.optional(v.nullable(v.string())),
	ttl: v.optional(v.nullable(v.number())),
	tunnelId: v.optional(v.nullable(v.string())),
	tunnelName: v.optional(v.nullable(v.string())),
});

const api_DNSRouteSettingsDTO: v.GenericSchema = v.looseObject({
	autoRefreshEnabled: v.optional(v.nullable(v.boolean())),
	refreshDailyTime: v.optional(v.nullable(v.string())),
	refreshIntervalHours: v.optional(v.nullable(v.number())),
	refreshMode: v.optional(v.nullable(v.string())),
});

const api_DecodeLinkResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_LinkPayload))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DeviceProxyAuthDTO: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	password: v.optional(v.nullable(v.string())),
	username: v.optional(v.nullable(v.string())),
});

const api_DeviceProxyConfigData: v.GenericSchema = v.looseObject({
	auth: v.optional(v.nullable(v.lazy(() => api_DeviceProxyAuthDTO))),
	enabled: v.optional(v.nullable(v.boolean())),
	listenAll: v.optional(v.nullable(v.boolean())),
	listenInterface: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.number())),
	selectedOutbound: v.optional(v.nullable(v.string())),
});

const api_DeviceProxyInstanceData: v.GenericSchema = v.looseObject({
	auth: v.optional(v.nullable(v.lazy(() => api_DeviceProxyAuthDTO))),
	enabled: v.optional(v.nullable(v.boolean())),
	id: v.optional(v.nullable(v.string())),
	listenAll: v.optional(v.nullable(v.boolean())),
	listenInterface: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.number())),
	selectedOutbound: v.optional(v.nullable(v.string())),
});

const api_DeviceProxyInstanceIPCheckResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DeviceProxyInstanceIPCheckResultDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DeviceProxyInstanceIPCheckResultDTO: v.GenericSchema = v.looseObject({
	directIp: v.optional(v.nullable(v.string())),
	ipChanged: v.optional(v.nullable(v.boolean())),
	proxyIp: v.optional(v.nullable(v.string())),
	service: v.optional(v.nullable(v.string())),
});

const api_DeviceProxyOutboundDTO: v.GenericSchema = v.looseObject({
	detail: v.optional(v.nullable(v.string())),
	kind: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_DeviceProxyRuntimeData: v.GenericSchema = v.looseObject({
	activeTag: v.optional(v.nullable(v.string())),
	alive: v.optional(v.nullable(v.boolean())),
	defaultTag: v.optional(v.nullable(v.string())),
	degradedOutbound: v.optional(v.nullable(v.string())),
	fallbackTag: v.optional(v.nullable(v.string())),
});

const api_DiagnosticsStatusData: v.GenericSchema = v.looseObject({
	progress: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
});

const api_DiagnosticsStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DiagnosticsStatusData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DnsCheckResultDTO: v.GenericSchema = v.looseObject({
	detail: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	message: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
	title: v.optional(v.nullable(v.string())),
});

const api_DnsCheckStartData: v.GenericSchema = v.looseObject({
	checks: v.optional(v.nullable(v.array(v.lazy(() => api_DnsCheckResultDTO)))),
	clientIP: v.optional(v.nullable(v.string())),
	hostname: v.optional(v.nullable(v.string())),
});

const api_DnsCheckStartResponseEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DnsCheckStartData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DnsProxyInfoData: v.GenericSchema = v.looseObject({
	proxies: v.optional(v.nullable(v.array(v.lazy(() => diagnostics_DNSProxy)))),
});

const api_DnsProxyInfoEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DnsProxyInfoData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DnsRouteDTO: v.GenericSchema = v.looseObject({
	backend: v.optional(v.nullable(v.string())),
	createdAt: v.optional(v.nullable(v.string())),
	domains: v.optional(v.nullable(v.array(v.string()))),
	enabled: v.optional(v.nullable(v.boolean())),
	excludeSubnets: v.optional(v.nullable(v.array(v.string()))),
	excludes: v.optional(v.nullable(v.array(v.string()))),
	excludesText: v.optional(v.nullable(v.string())),
	hrPolicyInterfaces: v.optional(v.nullable(v.array(v.string()))),
	hrPolicyName: v.optional(v.nullable(v.string())),
	hrRouteMode: v.optional(v.nullable(v.string())),
	iconUrl: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	manualDomains: v.optional(v.nullable(v.array(v.string()))),
	manualText: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	routes: v.optional(v.nullable(v.array(v.lazy(() => api_DnsRouteTargetDTO)))),
	subnets: v.optional(v.nullable(v.array(v.string()))),
	subscriptions: v.optional(v.nullable(v.array(v.lazy(() => api_DnsRouteSubscriptionDTO)))),
	updatedAt: v.optional(v.nullable(v.string())),
});

const api_DnsRouteResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DnsRouteDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DnsRouteSubscriptionDTO: v.GenericSchema = v.looseObject({
	lastCount: v.optional(v.nullable(v.number())),
	lastFetched: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const api_DnsRouteTargetDTO: v.GenericSchema = v.looseObject({
	fallback: v.optional(v.nullable(v.string())),
	interface: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
});

const api_DnsRoutesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_DnsRouteDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DownloadOutboundDTO: v.GenericSchema = v.looseObject({
	available: v.optional(v.nullable(v.boolean())),
	detail: v.optional(v.nullable(v.string())),
	kind: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_DownloadOutboundsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_DownloadOutboundDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_DownloadSettingsDTO: v.GenericSchema = v.looseObject({
	routeKind: v.optional(v.nullable(v.string())),
	routeTag: v.optional(v.nullable(v.string())),
});

const api_EnsureWGTunnelResponse: v.GenericSchema = v.looseObject({
	created: v.optional(v.nullable(v.boolean())),
	message: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
	tunnelName: v.optional(v.nullable(v.string())),
});

const api_ExternalTunnelDTO: v.GenericSchema = v.looseObject({
	endpoint: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	isAWG: v.optional(v.nullable(v.boolean())),
	publicKey: v.optional(v.nullable(v.string())),
	rxBytes: v.optional(v.nullable(v.number())),
	tunnelNumber: v.optional(v.nullable(v.number())),
	txBytes: v.optional(v.nullable(v.number())),
});

const api_ExternalTunnelsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_ExternalTunnelDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnAllowlistResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_AllowlistStatus))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnCaptchaStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_CaptchaOverview))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnClientInstanceResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_ClientInstance))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnConfigResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_Config))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnServerInstanceResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_ServerInstance))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_FreeTurnStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => freeturn_Status))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GenerateLinkResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.looseObject({
	clientId: v.optional(v.nullable(v.string())),
	link: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.string())),
}))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GeoExpandData: v.GenericSchema = v.looseObject({
	count: v.optional(v.nullable(v.number())),
	lines: v.optional(v.nullable(v.array(v.string()))),
	path: v.optional(v.nullable(v.string())),
});

const api_GeoFileEntryDTO: v.GenericSchema = v.looseObject({
	external: v.optional(v.nullable(v.boolean())),
	path: v.optional(v.nullable(v.string())),
	size: v.optional(v.nullable(v.number())),
	tagCount: v.optional(v.nullable(v.number())),
	type: v.optional(v.nullable(v.string())),
	updated: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const api_GeoFileResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_GeoFileEntryDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GeoFileSettingsDTO: v.GenericSchema = v.looseObject({
	autoRefreshEnabled: v.optional(v.nullable(v.boolean())),
	refreshDailyTime: v.optional(v.nullable(v.string())),
	refreshIntervalHours: v.optional(v.nullable(v.number())),
	refreshMode: v.optional(v.nullable(v.string())),
});

const api_GeoFileUpdatedData: v.GenericSchema = v.looseObject({
	error: v.optional(v.nullable(v.string())),
	partial: v.optional(v.nullable(v.boolean())),
	updated: v.optional(v.nullable(v.number())),
});

const api_GeoFileUpdatedResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_GeoFileUpdatedData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GeoFilesRescannedData: v.GenericSchema = v.looseObject({
	adopted: v.optional(v.nullable(v.number())),
});

const api_GeoFilesRescannedResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_GeoFilesRescannedData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GeoFilesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_GeoFileEntryDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_GeoTagDTO: v.GenericSchema = v.looseObject({
	count: v.optional(v.nullable(v.number())),
	name: v.optional(v.nullable(v.string())),
});

const api_GeoTagsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_GeoTagDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_HealthData: v.GenericSchema = v.looseObject({
	instanceId: v.optional(v.nullable(v.string())),
	ok: v.optional(v.nullable(v.boolean())),
	version: v.optional(v.nullable(v.string())),
});

const api_HealthResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_HealthData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_HydraRouteConfigData: v.GenericSchema = v.looseObject({
	autoStart: v.optional(v.nullable(v.boolean())),
	cidr: v.optional(v.nullable(v.boolean())),
	clearIPSet: v.optional(v.nullable(v.boolean())),
	conntrackFlush: v.optional(v.nullable(v.boolean())),
	directRouteEnabled: v.optional(v.nullable(v.boolean())),
	geoIPFiles: v.optional(v.nullable(v.array(v.string()))),
	geoSiteFiles: v.optional(v.nullable(v.array(v.string()))),
	globalRouting: v.optional(v.nullable(v.boolean())),
	ipsetEnableTimeout: v.optional(v.nullable(v.boolean())),
	ipsetMaxElem: v.optional(v.nullable(v.number())),
	ipsetTimeout: v.optional(v.nullable(v.number())),
	log: v.optional(v.nullable(v.string())),
	logFile: v.optional(v.nullable(v.string())),
	policyOrder: v.optional(v.nullable(v.array(v.string()))),
});

const api_HydraRouteConfigResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_HydraRouteConfigData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_HydraRouteStatusData: v.GenericSchema = v.looseObject({
	installed: v.optional(v.nullable(v.boolean())),
	lastError: v.optional(v.nullable(v.string())),
	pid: v.optional(v.nullable(v.number())),
	processState: v.optional(v.nullable(v.string())),
	running: v.optional(v.nullable(v.boolean())),
	stalePid: v.optional(v.nullable(v.number())),
	version: v.optional(v.nullable(v.string())),
});

const api_HydraRouteStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_HydraRouteStatusData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_IPCheckServiceDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const api_IPResultData: v.GenericSchema = v.looseObject({
	directIp: v.optional(v.nullable(v.string())),
	endpointIp: v.optional(v.nullable(v.string())),
	ipChanged: v.optional(v.nullable(v.boolean())),
	vpnIp: v.optional(v.nullable(v.string())),
});

const api_IPResultResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_IPResultData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_IPServicesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_IPCheckServiceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_IpsetUsageData: v.GenericSchema = v.looseObject({
	maxElem: v.optional(v.nullable(v.number())),
	usage: v.optional(v.nullable(v.record(v.string(), v.number()))),
});

const api_IpsetUsageResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_IpsetUsageData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_LANSegmentEntryDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	subnet: v.optional(v.nullable(v.string())),
});

const api_LANSegmentsListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_LANSegmentEntryDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_LogEntryDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	group: v.optional(v.nullable(v.string())),
	lastSeen: v.optional(v.nullable(v.string())),
	level: v.optional(v.nullable(v.string())),
	message: v.optional(v.nullable(v.string())),
	repeats: v.optional(v.nullable(v.number())),
	sanitized: v.optional(v.nullable(v.boolean())),
	subgroup: v.optional(v.nullable(v.string())),
	target: v.optional(v.nullable(v.string())),
	timestamp: v.optional(v.nullable(v.string())),
});

const api_LoggingSettingsDTO: v.GenericSchema = v.looseObject({
	appMaxEntries: v.optional(v.nullable(v.number())),
	enabled: v.optional(v.nullable(v.boolean())),
	logLevel: v.optional(v.nullable(v.string())),
	maxAge: v.optional(v.nullable(v.number())),
	singboxLogLevel: v.optional(v.nullable(v.string())),
	singboxMaxEntries: v.optional(v.nullable(v.number())),
});

const api_LoginResponseRaw: v.GenericSchema = v.looseObject({
	login: v.optional(v.nullable(v.string())),
	success: v.optional(v.nullable(v.boolean())),
});

const api_LogsData: v.GenericSchema = v.looseObject({
	bucket: v.optional(v.nullable(v.string())),
	bufferCapacity: v.optional(v.nullable(v.number())),
	bufferSize: v.optional(v.nullable(v.number())),
	enabled: v.optional(v.nullable(v.boolean())),
	logs: v.optional(v.nullable(v.array(v.lazy(() => api_LogEntryDTO)))),
	oldestTimestamp: v.optional(v.nullable(v.string())),
	sanitized: v.optional(v.nullable(v.boolean())),
	total: v.optional(v.nullable(v.number())),
});

const api_LogsResponseEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_LogsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedPeerDTO: v.GenericSchema = v.looseObject({
	description: v.optional(v.nullable(v.string())),
	dns: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	presharedKey: v.optional(v.nullable(v.string())),
	privateKey: v.optional(v.nullable(v.string())),
	publicKey: v.optional(v.nullable(v.string())),
	tunnelIP: v.optional(v.nullable(v.string())),
});

const api_ManagedPeerResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedPeerDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedPeerStatsDTO: v.GenericSchema = v.looseObject({
	endpoint: v.optional(v.nullable(v.string())),
	lastHandshake: v.optional(v.nullable(v.string())),
	online: v.optional(v.nullable(v.boolean())),
	publicKey: v.optional(v.nullable(v.string())),
	rxBytes: v.optional(v.nullable(v.number())),
	txBytes: v.optional(v.nullable(v.number())),
});

const api_ManagedServerBackupDTO: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	asc: v.optional(v.nullable(v.unknown())),
	description: v.optional(v.nullable(v.string())),
	dns: v.optional(v.nullable(v.string())),
	endpoint: v.optional(v.nullable(v.string())),
	i1: v.optional(v.nullable(v.string())),
	i2: v.optional(v.nullable(v.string())),
	i3: v.optional(v.nullable(v.string())),
	i4: v.optional(v.nullable(v.string())),
	i5: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	mask: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	natEnabled: v.optional(v.nullable(v.boolean())),
	peers: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedPeerDTO)))),
	policy: v.optional(v.nullable(v.string())),
	privateKey: v.optional(v.nullable(v.string())),
});

const api_ManagedServerBackupFile: v.GenericSchema = v.looseObject({
	exportedAt: v.optional(v.nullable(v.string())),
	managedServers: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedServerBackupDTO)))),
	type: v.optional(v.nullable(v.string())),
	version: v.optional(v.nullable(v.number())),
	warnings: v.optional(v.nullable(v.array(v.lazy(() => api_BackupWarningDTO)))),
});

const api_ManagedServerDTO: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	dns: v.optional(v.nullable(v.string())),
	endpoint: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	lanSegments: v.optional(v.nullable(v.array(v.string()))),
	listenPort: v.optional(v.nullable(v.number())),
	mask: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	natEnabled: v.optional(v.nullable(v.boolean())),
	natMode: v.optional(v.nullable(v.string())),
	peers: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedPeerDTO)))),
	policy: v.optional(v.nullable(v.string())),
});

const api_ManagedServerDriftEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedServerDriftResponse))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedServerDriftResponse: v.GenericSchema = v.looseObject({
	drift: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedServerBackupDTO)))),
});

const api_ManagedServerExportEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedServerBackupFile))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedServerImportEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedServerRestoreResponse))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedServerResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedServerDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedServerRestoreResponse: v.GenericSchema = v.looseObject({
	outcomes: v.optional(v.nullable(v.array(v.lazy(() => api_RestoreOutcomeDTO)))),
});

const api_ManagedServerStatsDTO: v.GenericSchema = v.looseObject({
	peers: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedPeerStatsDTO)))),
	status: v.optional(v.nullable(v.string())),
});

const api_ManagedServerStatsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ManagedServerStatsDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ManagedServersListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_ManagedServerDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_MonitoringCellDTO: v.GenericSchema = v.looseObject({
	activeForRestart: v.optional(v.nullable(v.boolean())),
	isSelf: v.optional(v.nullable(v.boolean())),
	latencyMs: v.optional(v.nullable(v.number())),
	ok: v.optional(v.nullable(v.boolean())),
	targetId: v.optional(v.nullable(v.string())),
	ts: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
});

const api_MonitoringSnapshotData: v.GenericSchema = v.looseObject({
	cells: v.optional(v.nullable(v.array(v.lazy(() => api_MonitoringCellDTO)))),
	targets: v.optional(v.nullable(v.array(v.lazy(() => api_MonitoringTargetDTO)))),
	tunnels: v.optional(v.nullable(v.array(v.lazy(() => api_MonitoringTunnelDTO)))),
	updatedAt: v.optional(v.nullable(v.string())),
});

const api_MonitoringSnapshotResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_MonitoringSnapshotData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_MonitoringTargetDTO: v.GenericSchema = v.looseObject({
	host: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_MonitoringTunnelDTO: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	ifaceName: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	pingcheckTarget: v.optional(v.nullable(v.string())),
	selfMethod: v.optional(v.nullable(v.string())),
	selfTarget: v.optional(v.nullable(v.string())),
});

const api_NativePingCheckStatusDTO: v.GenericSchema = v.looseObject({
	bound: v.optional(v.nullable(v.boolean())),
	exists: v.optional(v.nullable(v.boolean())),
	failCount: v.optional(v.nullable(v.number())),
	host: v.optional(v.nullable(v.string())),
	interval: v.optional(v.nullable(v.number())),
	maxFails: v.optional(v.nullable(v.number())),
	minSuccess: v.optional(v.nullable(v.number())),
	mode: v.optional(v.nullable(v.string())),
	restart: v.optional(v.nullable(v.boolean())),
	status: v.optional(v.nullable(v.string())),
	successCount: v.optional(v.nullable(v.number())),
	timeout: v.optional(v.nullable(v.number())),
});

const api_NativePingCheckStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_NativePingCheckStatusDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_OkData: v.GenericSchema = v.looseObject({
	ok: v.optional(v.nullable(v.boolean())),
});

const api_OkResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_OkData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_OversizedTagDTO: v.GenericSchema = v.looseObject({
	count: v.optional(v.nullable(v.number())),
	file: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_OversizedTagsData: v.GenericSchema = v.looseObject({
	installed: v.optional(v.nullable(v.boolean())),
	maxelem: v.optional(v.nullable(v.number())),
	tags: v.optional(v.nullable(v.array(v.lazy(() => api_OversizedTagDTO)))),
});

const api_OversizedTagsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_OversizedTagsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PeerConfData: v.GenericSchema = v.looseObject({
	conf: v.optional(v.nullable(v.string())),
});

const api_PeerConfResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_PeerConfData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PingCheckDefaultsDTO: v.GenericSchema = v.looseObject({
	deadInterval: v.optional(v.nullable(v.number())),
	failThreshold: v.optional(v.nullable(v.number())),
	interval: v.optional(v.nullable(v.number())),
	method: v.optional(v.nullable(v.string())),
	target: v.optional(v.nullable(v.string())),
});

const api_PingCheckSettingsDTO: v.GenericSchema = v.looseObject({
	defaults: v.optional(v.nullable(v.lazy(() => api_PingCheckDefaultsDTO))),
	enabled: v.optional(v.nullable(v.boolean())),
});

const api_PingCheckStatusData: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	tunnels: v.optional(v.nullable(v.array(v.lazy(() => api_TunnelPingStatusDTO)))),
});

const api_PingCheckStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_PingCheckStatusData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PingLogEntryDTO: v.GenericSchema = v.looseObject({
	error: v.optional(v.nullable(v.string())),
	failCount: v.optional(v.nullable(v.number())),
	latency: v.optional(v.nullable(v.number())),
	stateChange: v.optional(v.nullable(v.string())),
	success: v.optional(v.nullable(v.boolean())),
	threshold: v.optional(v.nullable(v.number())),
	timestamp: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
	tunnelName: v.optional(v.nullable(v.string())),
});

const api_PingLogsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_PingLogEntryDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PoliciesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_PolicyOptionDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PolicyDeviceDTO: v.GenericSchema = v.looseObject({
	active: v.optional(v.nullable(v.boolean())),
	hostname: v.optional(v.nullable(v.string())),
	ip: v.optional(v.nullable(v.string())),
	link: v.optional(v.nullable(v.string())),
	mac: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	policy: v.optional(v.nullable(v.string())),
});

const api_PolicyDevicesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_PolicyDeviceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PolicyGlobalInterfaceDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	up: v.optional(v.nullable(v.boolean())),
});

const api_PolicyInterfacesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_PolicyGlobalInterfaceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PolicyOptionDTO: v.GenericSchema = v.looseObject({
	description: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
});

const api_PolicyOrderData: v.GenericSchema = v.looseObject({
	order: v.optional(v.nullable(v.array(v.string()))),
});

const api_PolicyOrderResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_PolicyOrderData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_PresetsListResponse: v.GenericSchema = v.looseObject({
	presets: v.optional(v.nullable(v.array(v.lazy(() => presets_Preset)))),
});

const api_ProxyConfigResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DeviceProxyConfigData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ProxyInstanceResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DeviceProxyInstanceData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ProxyInstancesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_DeviceProxyInstanceData)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ProxyListenChoicesData: v.GenericSchema = v.looseObject({
	lanIP: v.optional(v.nullable(v.string())),
	singboxRunning: v.optional(v.nullable(v.boolean())),
});

const api_ProxyListenChoicesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ProxyListenChoicesData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ProxyOutboundsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_DeviceProxyOutboundDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ProxyRuntimeResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_DeviceProxyRuntimeData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ResolveResponse: v.GenericSchema = v.looseObject({
	domain: v.optional(v.nullable(v.string())),
	error: v.optional(v.nullable(v.string())),
	ips: v.optional(v.nullable(v.array(v.string()))),
});

const api_RestoreOutcomeDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	addedPeers: v.optional(v.nullable(v.number())),
	conflicts: v.optional(v.nullable(v.array(v.string()))),
	error: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	newName: v.optional(v.nullable(v.string())),
});

const api_RouterDetails: v.GenericSchema = v.looseObject({
	architecture: v.optional(v.nullable(v.string())),
	bootSlot: v.optional(v.nullable(v.string())),
	cpuModel: v.optional(v.nullable(v.string())),
	cpuTempC: v.optional(v.nullable(v.number())),
	featureComponents: v.optional(v.nullable(v.array(v.string()))),
	firmwareBuildDate: v.optional(v.nullable(v.string())),
	firmwareRelease: v.optional(v.nullable(v.string())),
	firmwareSandbox: v.optional(v.nullable(v.string())),
	firmwareTitle: v.optional(v.nullable(v.string())),
	hardwareId: v.optional(v.nullable(v.string())),
	loadAverage: v.optional(v.nullable(v.string())),
	memoryTotalMB: v.optional(v.nullable(v.number())),
	memoryUsedMB: v.optional(v.nullable(v.number())),
	memoryUsedPercent: v.optional(v.nullable(v.number())),
	meshMembers: v.optional(v.nullable(v.array(v.string()))),
	model: v.optional(v.nullable(v.string())),
	modelDisplay: v.optional(v.nullable(v.string())),
	opkgStorage: v.optional(v.nullable(v.string())),
	portedBuild: v.optional(v.nullable(v.boolean())),
	region: v.optional(v.nullable(v.string())),
	storageComponents: v.optional(v.nullable(v.array(v.string()))),
	uptimeHuman: v.optional(v.nullable(v.string())),
	vpnComponents: v.optional(v.nullable(v.array(v.string()))),
	wifi5TempC: v.optional(v.nullable(v.number())),
	wifi24TempC: v.optional(v.nullable(v.number())),
});

const api_RouterInterfaceDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	up: v.optional(v.nullable(v.boolean())),
});

const api_RouterStagingStatusResponse: v.GenericSchema = v.looseObject({
	draftedAt: v.optional(v.nullable(v.string())),
	hasDraft: v.optional(v.nullable(v.boolean())),
	validation: v.optional(v.nullable(v.lazy(() => api_RouterValidationDTO))),
});

const api_RouterValidationDTO: v.GenericSchema = v.looseObject({
	errors: v.optional(v.nullable(v.array(v.lazy(() => api_RouterValidationErrorDTO)))),
});

const api_RouterValidationErrorDTO: v.GenericSchema = v.looseObject({
	inRule: v.optional(v.nullable(v.string())),
	kind: v.optional(v.nullable(v.string())),
	message: v.optional(v.nullable(v.string())),
	slot: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_RoutingRefreshData: v.GenericSchema = v.looseObject({
	missing: v.optional(v.nullable(v.array(v.string()))),
});

const api_RoutingRefreshResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_RoutingRefreshData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_RoutingTunnelDTO: v.GenericSchema = v.looseObject({
	available: v.optional(v.nullable(v.boolean())),
	id: v.optional(v.nullable(v.string())),
	iface: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
});

const api_RoutingTunnelsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_RoutingTunnelDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SaveStatusDTO: v.GenericSchema = v.looseObject({
	lastError: v.optional(v.nullable(v.string())),
	lastSaveAt: v.optional(v.nullable(v.string())),
	pendingCount: v.optional(v.nullable(v.number())),
	state: v.optional(v.nullable(v.string())),
});

const api_ServerListenChangeData: v.GenericSchema = v.looseObject({
	boundAddrs: v.optional(v.nullable(v.array(v.string()))),
	confirmDeadline: v.optional(v.nullable(v.string())),
	confirmToken: v.optional(v.nullable(v.string())),
});

const api_ServerListenChangeResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ServerListenChangeData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ServerListenStateData: v.GenericSchema = v.looseObject({
	boundAddrs: v.optional(v.nullable(v.array(v.string()))),
	confirmDeadline: v.optional(v.nullable(v.string())),
	interfaces: v.optional(v.nullable(v.array(v.string()))),
	pendingConfirm: v.optional(v.nullable(v.boolean())),
	port: v.optional(v.nullable(v.number())),
});

const api_ServerListenStateResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ServerListenStateData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_ServerSettingsDTO: v.GenericSchema = v.looseObject({
	interface: v.optional(v.nullable(v.string())),
	interfaces: v.optional(v.nullable(v.array(v.string()))),
	port: v.optional(v.nullable(v.number())),
});

const api_ServersAllData: v.GenericSchema = v.looseObject({
	managedStats: v.optional(v.nullable(v.lazy(() => api_ManagedServerStatsDTO))),
	servers: v.optional(v.nullable(v.array(v.lazy(() => api_WireguardServerDTO)))),
	wanIP: v.optional(v.nullable(v.string())),
});

const api_ServersAllResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ServersAllData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SettingsData: v.GenericSchema = v.looseObject({
	authEnabled: v.optional(v.nullable(v.boolean())),
	connectivityCheckUrl: v.optional(v.nullable(v.string())),
	disableMemorySaving: v.optional(v.nullable(v.boolean())),
	dnsRoute: v.optional(v.nullable(v.lazy(() => api_DNSRouteSettingsDTO))),
	download: v.optional(v.nullable(v.lazy(() => api_DownloadSettingsDTO))),
	entwareAuthEnabled: v.optional(v.nullable(v.boolean())),
	geoFile: v.optional(v.nullable(v.lazy(() => api_GeoFileSettingsDTO))),
	logging: v.optional(v.nullable(v.lazy(() => api_LoggingSettingsDTO))),
	monitoringExcludedTunnels: v.optional(v.nullable(v.array(v.string()))),
	pingCheck: v.optional(v.nullable(v.lazy(() => api_PingCheckSettingsDTO))),
	schemaVersion: v.optional(v.nullable(v.number())),
	server: v.optional(v.nullable(v.lazy(() => api_ServerSettingsDTO))),
	sessionTtlHours: v.optional(v.nullable(v.number())),
	updates: v.optional(v.nullable(v.lazy(() => api_UpdateSettingsDTO))),
	usageLevel: v.optional(v.nullable(v.string())),
});

const api_SettingsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SettingsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SignatureCaptureData: v.GenericSchema = v.looseObject({
	ok: v.optional(v.nullable(v.boolean())),
	packets: v.optional(v.nullable(v.lazy(() => api_SignaturePacketsDTO))),
	source: v.optional(v.nullable(v.string())),
	warning: v.optional(v.nullable(v.string())),
});

const api_SignatureCaptureResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SignatureCaptureData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SignatureGenerateData: v.GenericSchema = v.looseObject({
	byteSize: v.optional(v.nullable(v.number())),
	ok: v.optional(v.nullable(v.boolean())),
	packets: v.optional(v.nullable(v.lazy(() => api_SignaturePacketsDTO))),
	protocol: v.optional(v.nullable(v.string())),
	source: v.optional(v.nullable(v.string())),
});

const api_SignatureGenerateResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SignatureGenerateData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SignaturePacketsDTO: v.GenericSchema = v.looseObject({
	i1: v.optional(v.nullable(v.string())),
	i2: v.optional(v.nullable(v.string())),
	i3: v.optional(v.nullable(v.string())),
	i4: v.optional(v.nullable(v.string())),
	i5: v.optional(v.nullable(v.string())),
});

const api_SingboxConfigPreviewResponse: v.GenericSchema = v.looseObject({
	json: v.optional(v.nullable(v.string())),
});

const api_SingboxConnectionsClientsData: v.GenericSchema = v.looseObject({
	clientsByIP: v.optional(v.nullable(v.record(v.string(), v.string()))),
});

const api_SingboxConnectionsClientsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxConnectionsClientsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSCertificateDTO: v.GenericSchema = v.looseObject({
	certificate_public_key_sha256: v.optional(v.nullable(v.string())),
	issuer: v.optional(v.nullable(v.string())),
	not_after: v.optional(v.nullable(v.string())),
	subject: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSChainPresetData: v.GenericSchema = v.looseObject({
	directServer: v.optional(v.nullable(v.string())),
	mode: v.optional(v.nullable(v.string())),
	poisonCidrs: v.optional(v.nullable(v.array(v.string()))),
	proxyServer: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSChainPresetResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxDNSChainPresetData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSClientTLSOptionsDTO: v.GenericSchema = v.looseObject({
	alpn: v.optional(v.nullable(v.array(v.string()))),
	certificate_public_key_sha256: v.optional(v.nullable(v.array(v.string()))),
	insecure: v.optional(v.nullable(v.boolean())),
	max_version: v.optional(v.nullable(v.string())),
	min_version: v.optional(v.nullable(v.string())),
	server_name: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSGlobalsData: v.GenericSchema = v.looseObject({
	final: v.optional(v.nullable(v.string())),
	strategy: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSGlobalsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxDNSGlobalsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSLookupData: v.GenericSchema = v.looseObject({
	certificate_public_key_sha256: v.optional(v.nullable(v.array(v.string()))),
	certificates: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxDNSCertificateDTO)))),
	ips: v.optional(v.nullable(v.array(v.string()))),
});

const api_SingboxDNSLookupResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxDNSLookupData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSRewriteDTO: v.GenericSchema = v.looseObject({
	ips: v.optional(v.nullable(v.array(v.string()))),
	managed: v.optional(v.nullable(v.string())),
	pattern: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSRewritesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxDNSRewriteDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSRuleDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	domain: v.optional(v.nullable(v.array(v.string()))),
	domain_keyword: v.optional(v.nullable(v.array(v.string()))),
	domain_suffix: v.optional(v.nullable(v.array(v.string()))),
	ip_cidr: v.optional(v.nullable(v.array(v.string()))),
	match_response: v.optional(v.nullable(v.unknown())),
	query_type: v.optional(v.nullable(v.array(v.string()))),
	race: v.optional(v.nullable(v.boolean())),
	response_answer: v.optional(v.nullable(v.array(v.string()))),
	response_extra: v.optional(v.nullable(v.array(v.string()))),
	response_ns: v.optional(v.nullable(v.array(v.string()))),
	response_rcode: v.optional(v.nullable(v.string())),
	rule_set: v.optional(v.nullable(v.array(v.string()))),
	server: v.optional(v.nullable(v.string())),
	speculative: v.optional(v.nullable(v.boolean())),
	tag: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSRulesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxDNSRuleDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDNSServerDTO: v.GenericSchema = v.looseObject({
	detour: v.optional(v.nullable(v.string())),
	domain_resolver: v.optional(v.nullable(v.lazy(() => api_SingboxDomainResolverDTO))),
	domain_strategy: v.optional(v.nullable(v.string())),
	path: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
	server_port: v.optional(v.nullable(v.number())),
	tag: v.optional(v.nullable(v.string())),
	tls: v.optional(v.nullable(v.lazy(() => api_SingboxDNSClientTLSOptionsDTO))),
	type: v.optional(v.nullable(v.string())),
});

const api_SingboxDNSServersListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxDNSServerDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxDomainResolverDTO: v.GenericSchema = v.looseObject({
	server: v.optional(v.nullable(v.string())),
	strategy: v.optional(v.nullable(v.string())),
});

const api_SingboxGeositesData: v.GenericSchema = v.looseObject({
	baseUrl: v.optional(v.nullable(v.string())),
	fetchedAt: v.optional(v.nullable(v.string())),
	names: v.optional(v.nullable(v.array(v.string()))),
	stale: v.optional(v.nullable(v.boolean())),
});

const api_SingboxInboundEntry: v.GenericSchema = v.looseObject({
	idle: v.optional(v.nullable(v.boolean())),
	idleReason: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	ownerLabel: v.optional(v.nullable(v.string())),
	slot: v.optional(v.nullable(v.string())),
	source: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
});

const api_SingboxInboundsResponse: v.GenericSchema = v.looseObject({
	inbounds: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxInboundEntry)))),
	warnings: v.optional(v.nullable(v.array(v.string()))),
});

const api_SingboxProxiesListResponse: v.GenericSchema = v.looseObject({
	groups: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxProxyGroup)))),
});

const api_SingboxProxiesTestResponse: v.GenericSchema = v.looseObject({
	delays: v.optional(v.nullable(v.record(v.string(), v.number()))),
});

const api_SingboxProxyGroup: v.GenericSchema = v.looseObject({
	members: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxProxyMember)))),
	now: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
});

const api_SingboxProxyMember: v.GenericSchema = v.looseObject({
	lastDelay: v.optional(v.nullable(v.number())),
	tag: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterBulkUpdatedData: v.GenericSchema = v.looseObject({
	updated: v.optional(v.nullable(v.number())),
});

const api_SingboxRouterBulkUpdatedResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterBulkUpdatedData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterDatRuleSetURLData: v.GenericSchema = v.looseObject({
	url: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterDatRuleSetURLResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterDatRuleSetURLData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterInspectDNSData: v.GenericSchema = v.looseObject({
	classification: v.optional(v.nullable(v.string())),
	final: v.optional(v.nullable(v.string())),
	input: v.optional(v.nullable(v.string())),
	inputType: v.optional(v.nullable(v.string())),
	matchedRule: v.optional(v.nullable(v.number())),
	matches: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterInspectDNSMatchDTO)))),
	note: v.optional(v.nullable(v.string())),
	pool: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterInspectDNSMatchDTO: v.GenericSchema = v.looseObject({
	conditions: v.optional(v.nullable(v.array(v.string()))),
	index: v.optional(v.nullable(v.number())),
	matched: v.optional(v.nullable(v.boolean())),
	reason: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterInspectDNSResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterInspectDNSData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterInspectData: v.GenericSchema = v.looseObject({
	destination: v.optional(v.nullable(v.string())),
	final: v.optional(v.nullable(v.string())),
	input: v.optional(v.nullable(v.string())),
	inputType: v.optional(v.nullable(v.string())),
	matchedRule: v.optional(v.nullable(v.number())),
	matches: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterInspectMatchDTO)))),
	note: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterInspectMatchDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	conditions: v.optional(v.nullable(v.array(v.string()))),
	index: v.optional(v.nullable(v.number())),
	matched: v.optional(v.nullable(v.boolean())),
	outbound: v.optional(v.nullable(v.string())),
	reason: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterInspectResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterInspectData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterIssueDTO: v.GenericSchema = v.looseObject({
	kind: v.optional(v.nullable(v.string())),
	message: v.optional(v.nullable(v.string())),
	ruleIndex: v.optional(v.nullable(v.number())),
	severity: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterNATEgressDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterNATPreviewData: v.GenericSchema = v.looseObject({
	egresses: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterNATEgressDTO)))),
	segments: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterNATSegmentDTO)))),
});

const api_SingboxRouterNATPreviewResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterNATPreviewData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterNATSegmentDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	mode: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	staticWan: v.optional(v.nullable(v.string())),
	subnet: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterOutboundDTO: v.GenericSchema = v.looseObject({
	bind_interface: v.optional(v.nullable(v.string())),
	default: v.optional(v.nullable(v.string())),
	interval: v.optional(v.nullable(v.string())),
	outbounds: v.optional(v.nullable(v.array(v.string()))),
	source: v.optional(v.nullable(v.string())),
	strategy: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
	tolerance: v.optional(v.nullable(v.number())),
	type: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterOutboundsListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterOutboundDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterPoliciesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterPolicyInfoDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterPolicyDeviceDTO: v.GenericSchema = v.looseObject({
	bound: v.optional(v.nullable(v.boolean())),
	ip: v.optional(v.nullable(v.string())),
	mac: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterPolicyDevicesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterPolicyDeviceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterPolicyInfoDTO: v.GenericSchema = v.looseObject({
	description: v.optional(v.nullable(v.string())),
	deviceCount: v.optional(v.nullable(v.number())),
	isOurDefault: v.optional(v.nullable(v.boolean())),
	mark: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterPolicyResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterPolicyInfoDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterPresetDTO: v.GenericSchema = v.looseObject({
	category: v.optional(v.nullable(v.string())),
	covers: v.optional(v.nullable(v.array(v.string()))),
	featured: v.optional(v.nullable(v.boolean())),
	iconSlug: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	notice: v.optional(v.nullable(v.string())),
	ruleSets: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterPresetRuleRefDTO)))),
	rules: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterPresetRuleLinkDTO)))),
	sensitive: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterPresetRuleLinkDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	domain_suffix: v.optional(v.nullable(v.array(v.string()))),
	rule_set: v.optional(v.nullable(v.array(v.string()))),
});

const api_SingboxRouterPresetRuleRefDTO: v.GenericSchema = v.looseObject({
	tag: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterPresetsListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterPresetDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterQoSClassDTO: v.GenericSchema = v.looseObject({
	dscp: v.optional(v.nullable(v.number())),
	enabled: v.optional(v.nullable(v.boolean())),
	name: v.optional(v.nullable(v.string())),
	outbound: v.optional(v.nullable(v.string())),
	slot: v.optional(v.nullable(v.number())),
});

const api_SingboxRouterRuleDTO: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	domain_suffix: v.optional(v.nullable(v.array(v.string()))),
	inbound: v.optional(v.nullable(v.array(v.string()))),
	ip_cidr: v.optional(v.nullable(v.array(v.string()))),
	outbound: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.array(v.number()))),
	protocol: v.optional(v.nullable(v.string())),
	rule_set: v.optional(v.nullable(v.array(v.string()))),
	source_ip_cidr: v.optional(v.nullable(v.array(v.string()))),
});

const api_SingboxRouterRuleSetDTO: v.GenericSchema = v.looseObject({
	download_detour: v.optional(v.nullable(v.string())),
	format: v.optional(v.nullable(v.string())),
	materialized_srs: v.optional(v.nullable(v.boolean())),
	path: v.optional(v.nullable(v.string())),
	rules: v.optional(v.nullable(v.array(v.record(v.string(), v.unknown())))),
	tag: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
	update_interval: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterRuleSetsListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterRuleSetDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterRulesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterRuleDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterSettingsData: v.GenericSchema = v.looseObject({
	bypassExtraPorts: v.optional(v.nullable(v.string())),
	bypassExtraSubnets: v.optional(v.nullable(v.string())),
	bypassGeoipTags: v.optional(v.nullable(v.array(v.string()))),
	bypassPresets: v.optional(v.nullable(v.array(v.string()))),
	deviceMode: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	fakeipMtu: v.optional(v.nullable(v.number())),
	fakeipPool4: v.optional(v.nullable(v.string())),
	fakeipPool6: v.optional(v.nullable(v.string())),
	fakeipRealServer: v.optional(v.nullable(v.string())),
	fakeipStack: v.optional(v.nullable(v.string())),
	ingressInterfaces: v.optional(v.nullable(v.array(v.string()))),
	policyName: v.optional(v.nullable(v.string())),
	policyTunNatSegments: v.optional(v.nullable(v.array(v.string()))),
	policyTunSourcePreserve: v.optional(v.nullable(v.boolean())),
	qosClasses: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterQoSClassDTO)))),
	routingMode: v.optional(v.nullable(v.string())),
	snifferEnabled: v.optional(v.nullable(v.boolean())),
	udpTimeout: v.optional(v.nullable(v.string())),
	wanAutoDetect: v.optional(v.nullable(v.boolean())),
	wanInterface: v.optional(v.nullable(v.string())),
});

const api_SingboxRouterSettingsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterSettingsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterStatusData: v.GenericSchema = v.looseObject({
	active: v.optional(v.nullable(v.boolean())),
	crashCount: v.optional(v.nullable(v.number())),
	deviceCount: v.optional(v.nullable(v.number())),
	deviceMode: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	fakeipDns: v.optional(v.nullable(v.string())),
	fakeipIface: v.optional(v.nullable(v.string())),
	fakeipTunAddr: v.optional(v.nullable(v.string())),
	final: v.optional(v.nullable(v.string())),
	installed: v.optional(v.nullable(v.boolean())),
	issues: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterIssueDTO)))),
	lastCrashReason: v.optional(v.nullable(v.string())),
	lastError: v.optional(v.nullable(v.string())),
	netfilterAvailable: v.optional(v.nullable(v.boolean())),
	netfilterComponentName: v.optional(v.nullable(v.string())),
	outboundAwgCount: v.optional(v.nullable(v.number())),
	outboundCompositeCount: v.optional(v.nullable(v.number())),
	policyExists: v.optional(v.nullable(v.boolean())),
	policyMark: v.optional(v.nullable(v.string())),
	policyName: v.optional(v.nullable(v.string())),
	policyTunIface: v.optional(v.nullable(v.string())),
	policyTunNdmsName: v.optional(v.nullable(v.string())),
	policyTunSourcePreserve: v.optional(v.nullable(v.boolean())),
	restartSuppressedUntil: v.optional(v.nullable(v.string())),
	ruleCount: v.optional(v.nullable(v.number())),
	ruleSetCount: v.optional(v.nullable(v.number())),
	snifferEnabled: v.optional(v.nullable(v.boolean())),
	tproxyTargetAvailable: v.optional(v.nullable(v.boolean())),
	xtDscpAvailable: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxRouterStatusData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterWANInterfaceDTO: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	priority: v.optional(v.nullable(v.number())),
	up: v.optional(v.nullable(v.boolean())),
});

const api_SingboxRouterWANInterfacesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxRouterWANInterfaceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxStatusData: v.GenericSchema = v.looseObject({
	currentSha256: v.optional(v.nullable(v.string())),
	currentVersion: v.optional(v.nullable(v.string())),
	features: v.optional(v.nullable(v.array(v.string()))),
	freeBytes: v.optional(v.nullable(v.number())),
	installState: v.optional(v.nullable(v.string())),
	installed: v.optional(v.nullable(v.boolean())),
	lastError: v.optional(v.nullable(v.string())),
	ndmsProxyEnabled: v.optional(v.nullable(v.boolean())),
	pid: v.optional(v.nullable(v.number())),
	proxyComponent: v.optional(v.nullable(v.boolean())),
	requiredBytes: v.optional(v.nullable(v.number())),
	requiredSha256: v.optional(v.nullable(v.string())),
	requiredVersion: v.optional(v.nullable(v.string())),
	running: v.optional(v.nullable(v.boolean())),
	tunnelCount: v.optional(v.nullable(v.number())),
	updateAvailable: v.optional(v.nullable(v.boolean())),
	version: v.optional(v.nullable(v.string())),
});

const api_SingboxStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxStatusData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxTunnelConnectivity: v.GenericSchema = v.looseObject({
	connected: v.optional(v.nullable(v.boolean())),
	latency: v.optional(v.nullable(v.number())),
});

const api_SingboxTunnelDTO: v.GenericSchema = v.looseObject({
	connectivity: v.optional(v.nullable(v.lazy(() => api_SingboxTunnelConnectivity))),
	fingerprint: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	port: v.optional(v.nullable(v.number())),
	protocol: v.optional(v.nullable(v.string())),
	proxyInterface: v.optional(v.nullable(v.string())),
	running: v.optional(v.nullable(v.boolean())),
	security: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
	sni: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
	transport: v.optional(v.nullable(v.string())),
});

const api_SingboxTunnelGetData: v.GenericSchema = v.looseObject({
	outbound: v.optional(v.nullable(v.record(v.string(), v.unknown()))),
	tag: v.optional(v.nullable(v.string())),
});

const api_SingboxTunnelGetResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxTunnelGetData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SingboxTunnelsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SingboxTunnelDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SpeedTestInfoData: v.GenericSchema = v.looseObject({
	available: v.optional(v.nullable(v.boolean())),
	servers: v.optional(v.nullable(v.array(v.lazy(() => api_SpeedTestServerDTO)))),
});

const api_SpeedTestInfoResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SpeedTestInfoData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SpeedTestResultData: v.GenericSchema = v.looseObject({
	bandwidth: v.optional(v.nullable(v.number())),
	bytes: v.optional(v.nullable(v.number())),
	direction: v.optional(v.nullable(v.string())),
	duration: v.optional(v.nullable(v.number())),
	retransmits: v.optional(v.nullable(v.number())),
	server: v.optional(v.nullable(v.string())),
});

const api_SpeedTestResultResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SpeedTestResultData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SpeedTestServerDTO: v.GenericSchema = v.looseObject({
	host: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.number())),
});

const api_StaticRouteDTO: v.GenericSchema = v.looseObject({
	createdAt: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	fallback: v.optional(v.nullable(v.string())),
	iconUrl: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	subnets: v.optional(v.nullable(v.array(v.string()))),
	tunnelID: v.optional(v.nullable(v.string())),
	updatedAt: v.optional(v.nullable(v.string())),
});

const api_StaticRoutesListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_StaticRouteDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubgroupsData: v.GenericSchema = v.looseObject({
	group: v.optional(v.nullable(v.string())),
	subgroups: v.optional(v.nullable(v.array(v.string()))),
});

const api_SubgroupsResponseEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SubgroupsData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubscriptionDTO: v.GenericSchema = v.looseObject({
	activeMember: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	excludedMembers: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionMemberDTO)))),
	excludedTags: v.optional(v.nullable(v.array(v.string()))),
	filterExclude: v.optional(v.nullable(v.string())),
	filterInclude: v.optional(v.nullable(v.string())),
	filteredMembers: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionMemberDTO)))),
	headers: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionHeader)))),
	id: v.optional(v.nullable(v.string())),
	inboundTag: v.optional(v.nullable(v.string())),
	infoItems: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionInfoItemDTO)))),
	isInline: v.optional(v.nullable(v.boolean())),
	label: v.optional(v.nullable(v.string())),
	lastError: v.optional(v.nullable(v.string())),
	lastFetched: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	memberTags: v.optional(v.nullable(v.array(v.string()))),
	members: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionMemberDTO)))),
	mode: v.optional(v.nullable(v.string())),
	orphanTags: v.optional(v.nullable(v.array(v.string()))),
	proxyIndex: v.optional(v.nullable(v.number())),
	refreshHours: v.optional(v.nullable(v.number())),
	rejectedMembers: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionRejectedDTO)))),
	selectorTag: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
	urlTest: v.optional(v.nullable(v.lazy(() => api_SubscriptionURLTestDTO))),
});

const api_SubscriptionGroupDTO: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	filterExclude: v.optional(v.nullable(v.string())),
	filterInclude: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	inboundTag: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	memberCount: v.optional(v.nullable(v.number())),
	members: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionGroupMemberDTO)))),
	mode: v.optional(v.nullable(v.string())),
	proxyIndex: v.optional(v.nullable(v.number())),
	tag: v.optional(v.nullable(v.string())),
	urlTest: v.optional(v.nullable(v.lazy(() => api_SubscriptionURLTestDTO))),
	useSubscriptionIds: v.optional(v.nullable(v.array(v.string()))),
});

const api_SubscriptionGroupListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionGroupDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubscriptionGroupMemberDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_SubscriptionGroupResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SubscriptionGroupDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubscriptionHeader: v.GenericSchema = v.looseObject({
	name: v.optional(v.nullable(v.string())),
	value: v.optional(v.nullable(v.string())),
});

const api_SubscriptionInfoItemDTO: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	label: v.optional(v.nullable(v.string())),
	source: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_SubscriptionListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SubscriptionDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubscriptionMemberDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.number())),
	protocol: v.optional(v.nullable(v.string())),
	security: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
	sni: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
	transport: v.optional(v.nullable(v.string())),
});

const api_SubscriptionRefreshData: v.GenericSchema = v.looseObject({
	added: v.optional(v.nullable(v.number())),
	orphaned: v.optional(v.nullable(v.number())),
	parseErrors: v.optional(v.nullable(v.array(v.string()))),
	skippedDuplicate: v.optional(v.nullable(v.number())),
	skippedOther: v.optional(v.nullable(v.number())),
	skippedVmess: v.optional(v.nullable(v.number())),
	updated: v.optional(v.nullable(v.number())),
	when: v.optional(v.nullable(v.string())),
});

const api_SubscriptionRejectedDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	port: v.optional(v.nullable(v.number())),
	protocol: v.optional(v.nullable(v.string())),
	reason: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.string())),
	tag: v.optional(v.nullable(v.string())),
});

const api_SubscriptionResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SubscriptionDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SubscriptionURLTestDTO: v.GenericSchema = v.looseObject({
	intervalSec: v.optional(v.nullable(v.number())),
	toleranceMs: v.optional(v.nullable(v.number())),
	url: v.optional(v.nullable(v.string())),
});

const api_SuggestAddressData: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	mask: v.optional(v.nullable(v.string())),
});

const api_SuggestAddressResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SuggestAddressData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SystemInfoBackendAvailability: v.GenericSchema = v.looseObject({
	kernel: v.optional(v.nullable(v.boolean())),
	nativewg: v.optional(v.nullable(v.boolean())),
});

const api_SystemInfoData: v.GenericSchema = v.looseObject({
	activeBackend: v.optional(v.nullable(v.string())),
	backendAvailability: v.optional(v.nullable(v.lazy(() => api_SystemInfoBackendAvailability))),
	bootInProgress: v.optional(v.nullable(v.boolean())),
	disableMemorySaving: v.optional(v.nullable(v.boolean())),
	firmwareVersion: v.optional(v.nullable(v.string())),
	gcMemLimit: v.optional(v.nullable(v.string())),
	goArch: v.optional(v.nullable(v.string())),
	goOS: v.optional(v.nullable(v.string())),
	goVersion: v.optional(v.nullable(v.string())),
	gogc: v.optional(v.nullable(v.string())),
	isAarch64: v.optional(v.nullable(v.boolean())),
	isLowMemory: v.optional(v.nullable(v.boolean())),
	isOS5: v.optional(v.nullable(v.boolean())),
	keeneticOS: v.optional(v.nullable(v.string())),
	kernelModuleExists: v.optional(v.nullable(v.boolean())),
	kernelModuleLoaded: v.optional(v.nullable(v.boolean())),
	kernelModuleLoadedVersion: v.optional(v.nullable(v.string())),
	kernelModuleModel: v.optional(v.nullable(v.string())),
	kernelModuleVersion: v.optional(v.nullable(v.string())),
	routerDetails: v.optional(v.nullable(v.lazy(() => api_RouterDetails))),
	routerIP: v.optional(v.nullable(v.string())),
	routerTime: v.optional(v.nullable(v.string())),
	routerTimezone: v.optional(v.nullable(v.string())),
	routerTimezoneOffsetMinutes: v.optional(v.nullable(v.number())),
	singbox: v.optional(v.nullable(v.lazy(() => api_SystemInfoSingbox))),
	slowRequestThresholdMs: v.optional(v.nullable(v.number())),
	supportsExtendedASC: v.optional(v.nullable(v.boolean())),
	supportsHRanges: v.optional(v.nullable(v.boolean())),
	supportsPingCheck: v.optional(v.nullable(v.boolean())),
	totalMemoryMB: v.optional(v.nullable(v.number())),
	version: v.optional(v.nullable(v.string())),
});

const api_SystemInfoResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SystemInfoData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_SystemInfoSingbox: v.GenericSchema = v.looseObject({
	installed: v.optional(v.nullable(v.boolean())),
	version: v.optional(v.nullable(v.string())),
});

const api_SystemTunnelDTO: v.GenericSchema = v.looseObject({
	connected: v.optional(v.nullable(v.boolean())),
	description: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	peer: v.optional(v.nullable(v.lazy(() => api_SystemTunnelPeerDTO))),
	status: v.optional(v.nullable(v.string())),
});

const api_SystemTunnelPeerDTO: v.GenericSchema = v.looseObject({
	endpoint: v.optional(v.nullable(v.string())),
	lastHandshake: v.optional(v.nullable(v.string())),
	online: v.optional(v.nullable(v.boolean())),
	publicKey: v.optional(v.nullable(v.string())),
	rxBytes: v.optional(v.nullable(v.number())),
	txBytes: v.optional(v.nullable(v.number())),
});

const api_SystemTunnelsResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_SystemTunnelDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TerminalInstallData: v.GenericSchema = v.looseObject({
	installed: v.optional(v.nullable(v.boolean())),
});

const api_TerminalStartData: v.GenericSchema = v.looseObject({
	port: v.optional(v.nullable(v.number())),
});

const api_TerminalStartResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TerminalStartData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TerminalStatusResponse: v.GenericSchema = v.looseObject({
	installed: v.optional(v.nullable(v.boolean())),
	running: v.optional(v.nullable(v.boolean())),
	sessionActive: v.optional(v.nullable(v.boolean())),
});

const api_TunnelConnectionInfoDTO: v.GenericSchema = v.looseObject({
	count: v.optional(v.nullable(v.number())),
	interface: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const api_TunnelControlData: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
});

const api_TunnelControlResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TunnelControlData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelDeleteResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TunnelDeleteResultData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelDeleteResultData: v.GenericSchema = v.looseObject({
	success: v.optional(v.nullable(v.boolean())),
	tunnelId: v.optional(v.nullable(v.string())),
	verified: v.optional(v.nullable(v.boolean())),
});

const api_TunnelDetailResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_AWGTunnelDTO))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelListItemDTO: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	awgVersion: v.optional(v.nullable(v.string())),
	backend: v.optional(v.nullable(v.string())),
	defaultRoute: v.optional(v.nullable(v.boolean())),
	enabled: v.optional(v.nullable(v.boolean())),
	endpoint: v.optional(v.nullable(v.string())),
	hasAddressConflict: v.optional(v.nullable(v.boolean())),
	id: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	ispInterface: v.optional(v.nullable(v.string())),
	ispInterfaceLabel: v.optional(v.nullable(v.string())),
	lastHandshake: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	name: v.optional(v.nullable(v.string())),
	ndmsName: v.optional(v.nullable(v.string())),
	pingCheck: v.optional(v.nullable(v.lazy(() => api_TunnelPingCheckStatus))),
	resolvedIspInterface: v.optional(v.nullable(v.string())),
	resolvedIspInterfaceLabel: v.optional(v.nullable(v.string())),
	rxBytes: v.optional(v.nullable(v.number())),
	startedAt: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
	txBytes: v.optional(v.nullable(v.number())),
	type: v.optional(v.nullable(v.string())),
});

const api_TunnelListResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_TunnelListItemDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelPingCheckStatus: v.GenericSchema = v.looseObject({
	failCount: v.optional(v.nullable(v.number())),
	failThreshold: v.optional(v.nullable(v.number())),
	restartCount: v.optional(v.nullable(v.number())),
	status: v.optional(v.nullable(v.string())),
});

const api_TunnelPingStatusDTO: v.GenericSchema = v.looseObject({
	backend: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	failCount: v.optional(v.nullable(v.number())),
	failThreshold: v.optional(v.nullable(v.number())),
	lastLatency: v.optional(v.nullable(v.number())),
	latencyNote: v.optional(v.nullable(v.string())),
	method: v.optional(v.nullable(v.string())),
	restartCount: v.optional(v.nullable(v.number())),
	status: v.optional(v.nullable(v.string())),
	tunnelId: v.optional(v.nullable(v.string())),
	tunnelName: v.optional(v.nullable(v.string())),
});

const api_TunnelStateInfoDTO: v.GenericSchema = v.looseObject({
	hasHandshake: v.optional(v.nullable(v.boolean())),
	interfaceUp: v.optional(v.nullable(v.boolean())),
	lastHandshake: v.optional(v.nullable(v.string())),
	processRunning: v.optional(v.nullable(v.boolean())),
	rxBytes: v.optional(v.nullable(v.number())),
	state: v.optional(v.nullable(v.number())),
	txBytes: v.optional(v.nullable(v.number())),
});

const api_TunnelTrafficData: v.GenericSchema = v.looseObject({
	points: v.optional(v.nullable(v.array(v.lazy(() => api_TunnelTrafficPoint)))),
	stats: v.optional(v.nullable(v.lazy(() => api_TunnelTrafficStats))),
});

const api_TunnelTrafficPoint: v.GenericSchema = v.looseObject({
	rx: v.optional(v.nullable(v.number())),
	t: v.optional(v.nullable(v.number())),
	tx: v.optional(v.nullable(v.number())),
});

const api_TunnelTrafficResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TunnelTrafficData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelTrafficStats: v.GenericSchema = v.looseObject({
	avgRx: v.optional(v.nullable(v.number())),
	avgTx: v.optional(v.nullable(v.number())),
	currentRx: v.optional(v.nullable(v.number())),
	currentTx: v.optional(v.nullable(v.number())),
	peakRate: v.optional(v.nullable(v.number())),
	points: v.optional(v.nullable(v.number())),
	volumeRx: v.optional(v.nullable(v.number())),
	volumeTx: v.optional(v.nullable(v.number())),
});

const api_TunnelsAllResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TunnelsAllSnapshotData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_TunnelsAllSnapshotData: v.GenericSchema = v.looseObject({
	external: v.optional(v.nullable(v.array(v.lazy(() => api_ExternalTunnelDTO)))),
	system: v.optional(v.nullable(v.array(v.lazy(() => api_SystemTunnelDTO)))),
	tunnels: v.optional(v.nullable(v.array(v.lazy(() => api_TunnelListItemDTO)))),
});

const api_UpdateApplyData: v.GenericSchema = v.looseObject({
	status: v.optional(v.nullable(v.string())),
});

const api_UpdateApplyResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_UpdateApplyData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_UpdateCheckResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_UpdateInfoData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_UpdateInfoData: v.GenericSchema = v.looseObject({
	available: v.optional(v.nullable(v.boolean())),
	checkedAt: v.optional(v.nullable(v.string())),
	checking: v.optional(v.nullable(v.boolean())),
	currentVersion: v.optional(v.nullable(v.string())),
	lastAutoInstallAt: v.optional(v.nullable(v.string())),
	latestVersion: v.optional(v.nullable(v.string())),
	nextAutoInstallAt: v.optional(v.nullable(v.string())),
});

const api_UpdateSettingsDTO: v.GenericSchema = v.looseObject({
	autoInstallEnabled: v.optional(v.nullable(v.boolean())),
	autoInstallIntervalDays: v.optional(v.nullable(v.number())),
	autoInstallTime: v.optional(v.nullable(v.string())),
	channel: v.optional(v.nullable(v.string())),
	checkEnabled: v.optional(v.nullable(v.boolean())),
});

const api_UserConfigApplyResponse: v.GenericSchema = v.looseObject({
	ok: v.optional(v.nullable(v.boolean())),
	warnings: v.optional(v.nullable(v.array(v.lazy(() => api_RouterValidationErrorDTO)))),
});

const api_UserConfigCheckResponse: v.GenericSchema = v.looseObject({
	errors: v.optional(v.nullable(v.array(v.lazy(() => api_RouterValidationErrorDTO)))),
	ok: v.optional(v.nullable(v.boolean())),
	warnings: v.optional(v.nullable(v.array(v.lazy(() => api_RouterValidationErrorDTO)))),
});

const api_WANIPData: v.GenericSchema = v.looseObject({
	ip: v.optional(v.nullable(v.string())),
});

const api_WANIPResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_WANIPData))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WANInterfaceDTO: v.GenericSchema = v.looseObject({
	label: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	state: v.optional(v.nullable(v.string())),
});

const api_WANInterfacesResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.array(v.lazy(() => api_WANInterfaceDTO)))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WANStatusEnvelope: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.looseObject({
	anyWANUp: v.optional(v.nullable(v.boolean())),
}))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WdttClientInstanceResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => wdtt_ClientInstance))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WdttConfigResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => wdtt_Config))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WdttDecodeLinkResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => wdtt_LinkDecodeResult))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WdttEnsureWGTunnelResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_EnsureWGTunnelResponse))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WdttStatusResponse: v.GenericSchema = v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => wdtt_Status))),
	success: v.optional(v.nullable(v.boolean())),
});

const api_WireguardServerDTO: v.GenericSchema = v.looseObject({
	address: v.optional(v.nullable(v.string())),
	builtIn: v.optional(v.nullable(v.boolean())),
	connected: v.optional(v.nullable(v.boolean())),
	description: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	enabledKnown: v.optional(v.nullable(v.boolean())),
	endpoint: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	interfaceName: v.optional(v.nullable(v.string())),
	keenDnsDomain: v.optional(v.nullable(v.string())),
	listenPort: v.optional(v.nullable(v.number())),
	mask: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	natEnabled: v.optional(v.nullable(v.boolean())),
	natMode: v.optional(v.nullable(v.string())),
	natModeKnown: v.optional(v.nullable(v.boolean())),
	peers: v.optional(v.nullable(v.array(v.lazy(() => api_WireguardServerPeerDTO)))),
	policy: v.optional(v.nullable(v.string())),
	policyKnown: v.optional(v.nullable(v.boolean())),
	publicKey: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.string())),
});

const api_WireguardServerPeerDTO: v.GenericSchema = v.looseObject({
	allowedIPs: v.optional(v.nullable(v.array(v.string()))),
	confAvailable: v.optional(v.nullable(v.boolean())),
	description: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	endpoint: v.optional(v.nullable(v.string())),
	lastHandshake: v.optional(v.nullable(v.string())),
	online: v.optional(v.nullable(v.boolean())),
	publicKey: v.optional(v.nullable(v.string())),
	rxBytes: v.optional(v.nullable(v.number())),
	txBytes: v.optional(v.nullable(v.number())),
});

const diagnostics_DNSProxy: v.GenericSchema = v.looseObject({
	displayName: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	rebind: v.optional(v.nullable(v.lazy(() => diagnostics_DNSRebind))),
	stat: v.optional(v.nullable(v.lazy(() => diagnostics_DNSProxyStat))),
	staticRecords: v.optional(v.nullable(v.array(v.lazy(() => diagnostics_DNSStaticRecord)))),
	tcpPort: v.optional(v.nullable(v.number())),
	udpPort: v.optional(v.nullable(v.number())),
	upstreams: v.optional(v.nullable(v.array(v.lazy(() => diagnostics_DNSUpstream)))),
});

const diagnostics_DNSProxyStat: v.GenericSchema = v.looseObject({
	cacheHitRatio: v.optional(v.nullable(v.number())),
	cacheHits: v.optional(v.nullable(v.number())),
	memory: v.optional(v.nullable(v.string())),
	proxyRequestsSent: v.optional(v.nullable(v.number())),
	totalRequests: v.optional(v.nullable(v.number())),
});

const diagnostics_DNSRebind: v.GenericSchema = v.looseObject({
	enabled: v.optional(v.nullable(v.boolean())),
	excludes: v.optional(v.nullable(v.array(v.string()))),
	nets: v.optional(v.nullable(v.array(v.string()))),
});

const diagnostics_DNSStaticRecord: v.GenericSchema = v.looseObject({
	flag: v.optional(v.nullable(v.number())),
	host: v.optional(v.nullable(v.string())),
	type: v.optional(v.nullable(v.string())),
	value: v.optional(v.nullable(v.string())),
});

const diagnostics_DNSUpstream: v.GenericSchema = v.looseObject({
	aRcvd: v.optional(v.nullable(v.number())),
	address: v.optional(v.nullable(v.string())),
	avgResp: v.optional(v.nullable(v.string())),
	encryption: v.optional(v.nullable(v.string())),
	medResp: v.optional(v.nullable(v.string())),
	nxRcvd: v.optional(v.nullable(v.number())),
	port: v.optional(v.nullable(v.number())),
	rSent: v.optional(v.nullable(v.number())),
	rank: v.optional(v.nullable(v.number())),
	scope: v.optional(v.nullable(v.string())),
	sni: v.optional(v.nullable(v.string())),
});

const freeturn_AllowlistEntry: v.GenericSchema = v.looseObject({
	clientId: v.optional(v.nullable(v.string())),
	comment: v.optional(v.nullable(v.string())),
});

const freeturn_AllowlistStatus: v.GenericSchema = v.looseObject({
	clients: v.optional(v.nullable(v.array(v.lazy(() => freeturn_AllowlistEntry)))),
	clientsFile: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
});

const freeturn_CaptchaClientStatus: v.GenericSchema = v.looseObject({
	active: v.optional(v.nullable(v.boolean())),
	canOpen: v.optional(v.nullable(v.boolean())),
	captchaSession: v.optional(v.nullable(v.number())),
	clientId: v.optional(v.nullable(v.string())),
	clientName: v.optional(v.nullable(v.string())),
	pendingStreams: v.optional(v.nullable(v.number())),
	portContention: v.optional(v.nullable(v.boolean())),
	queued: v.optional(v.nullable(v.boolean())),
	url: v.optional(v.nullable(v.string())),
	waiting: v.optional(v.nullable(v.boolean())),
});

const freeturn_CaptchaOverview: v.GenericSchema = v.looseObject({
	clients: v.optional(v.nullable(v.array(v.lazy(() => freeturn_CaptchaClientStatus)))),
	ownerClientId: v.optional(v.nullable(v.string())),
	ownerName: v.optional(v.nullable(v.string())),
	portOpen: v.optional(v.nullable(v.boolean())),
});

const freeturn_ClientConfig: v.GenericSchema = v.looseObject({
	bond: v.optional(v.nullable(v.boolean())),
	clientId: v.optional(v.nullable(v.string())),
	debug: v.optional(v.nullable(v.boolean())),
	dnsMode: v.optional(v.nullable(v.string())),
	dnsServers: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	links: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	mode: v.optional(v.nullable(v.string())),
	obfKey: v.optional(v.nullable(v.string())),
	obfProfile: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.string())),
	platform: v.optional(v.nullable(v.string())),
	provider: v.optional(v.nullable(v.string())),
	streams: v.optional(v.nullable(v.number())),
	streamsPerCred: v.optional(v.nullable(v.number())),
	sub: v.optional(v.nullable(v.string())),
	transport: v.optional(v.nullable(v.string())),
	turnHost: v.optional(v.nullable(v.string())),
	turnPort: v.optional(v.nullable(v.number())),
});

const freeturn_ClientInstance: v.GenericSchema = v.looseObject({
	config: v.optional(v.nullable(v.lazy(() => freeturn_ClientConfig))),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const freeturn_Config: v.GenericSchema = v.looseObject({
	client: v.optional(v.nullable(v.lazy(() => freeturn_ClientConfig))),
	clients: v.optional(v.nullable(v.array(v.lazy(() => freeturn_ClientInstance)))),
	server: v.optional(v.nullable(v.lazy(() => freeturn_ServerConfig))),
	servers: v.optional(v.nullable(v.array(v.lazy(() => freeturn_ServerInstance)))),
	version: v.optional(v.nullable(v.number())),
});

const freeturn_InstanceStatus: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.lazy(() => freeturn_ProcessStatus))),
});

const freeturn_LinkPayload: v.GenericSchema = v.looseObject({
	bond: v.optional(v.nullable(v.boolean())),
	cid: v.optional(v.nullable(v.string())),
	dns: v.optional(v.nullable(v.string())),
	dnss: v.optional(v.nullable(v.string())),
	key: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	mcap: v.optional(v.nullable(v.boolean())),
	mode: v.optional(v.nullable(v.string())),
	mtu: v.optional(v.nullable(v.number())),
	n: v.optional(v.nullable(v.number())),
	name: v.optional(v.nullable(v.string())),
	obf: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.string())),
	provider: v.optional(v.nullable(v.string())),
	spc: v.optional(v.nullable(v.number())),
	transport: v.optional(v.nullable(v.string())),
	v: v.optional(v.nullable(v.number())),
	wg: v.optional(v.nullable(v.string())),
});

const freeturn_ProcessStatus: v.GenericSchema = v.looseObject({
	binary: v.optional(v.nullable(v.string())),
	binaryPresent: v.optional(v.nullable(v.boolean())),
	dtlsConnections: v.optional(v.nullable(v.number())),
	lastError: v.optional(v.nullable(v.string())),
	log: v.optional(v.nullable(v.string())),
	pid: v.optional(v.nullable(v.number())),
	running: v.optional(v.nullable(v.boolean())),
	startedAt: v.optional(v.nullable(v.string())),
});

const freeturn_ServerConfig: v.GenericSchema = v.looseObject({
	clientsFile: v.optional(v.nullable(v.string())),
	connect: v.optional(v.nullable(v.string())),
	debug: v.optional(v.nullable(v.boolean())),
	enabled: v.optional(v.nullable(v.boolean())),
	listen: v.optional(v.nullable(v.string())),
	mode: v.optional(v.nullable(v.string())),
	obfKey: v.optional(v.nullable(v.string())),
	obfProfile: v.optional(v.nullable(v.string())),
	openFirewall: v.optional(v.nullable(v.boolean())),
});

const freeturn_ServerInstance: v.GenericSchema = v.looseObject({
	config: v.optional(v.nullable(v.lazy(() => freeturn_ServerConfig))),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const freeturn_Status: v.GenericSchema = v.looseObject({
	client: v.optional(v.nullable(v.lazy(() => freeturn_ProcessStatus))),
	clients: v.optional(v.nullable(v.array(v.lazy(() => freeturn_InstanceStatus)))),
	installAvailable: v.optional(v.nullable(v.boolean())),
	installVersion: v.optional(v.nullable(v.string())),
	installedVersion: v.optional(v.nullable(v.string())),
	installing: v.optional(v.nullable(v.boolean())),
	routerClock: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.lazy(() => freeturn_ProcessStatus))),
	servers: v.optional(v.nullable(v.array(v.lazy(() => freeturn_InstanceStatus)))),
	updateAvailable: v.optional(v.nullable(v.boolean())),
});

const presets_DNSEngine: v.GenericSchema = v.looseObject({
	domains: v.optional(v.nullable(v.array(v.string()))),
	subnets: v.optional(v.nullable(v.array(v.string()))),
	subscriptionUrl: v.optional(v.nullable(v.string())),
});

const presets_Engines: v.GenericSchema = v.looseObject({
	dns: v.optional(v.nullable(v.lazy(() => presets_DNSEngine))),
	hydraroute: v.optional(v.nullable(v.lazy(() => presets_HydraRouteEngine))),
	singbox: v.optional(v.nullable(v.lazy(() => presets_SingboxEngine))),
});

const presets_HydraRouteEngine: v.GenericSchema = v.looseObject({
	geoTags: v.optional(v.nullable(v.array(v.string()))),
});

const presets_Origin: v.GenericSchema = v.string();

const presets_Preset: v.GenericSchema = v.looseObject({
	category: v.optional(v.nullable(v.string())),
	covers: v.optional(v.nullable(v.array(v.string()))),
	engines: v.optional(v.nullable(v.lazy(() => presets_Engines))),
	featured: v.optional(v.nullable(v.boolean())),
	iconSlug: v.optional(v.nullable(v.string())),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	notice: v.optional(v.nullable(v.string())),
	origin: v.optional(v.nullable(v.lazy(() => presets_Origin))),
	sensitive: v.optional(v.nullable(v.boolean())),
});

const presets_RuleRef: v.GenericSchema = v.looseObject({
	tag: v.optional(v.nullable(v.string())),
	url: v.optional(v.nullable(v.string())),
});

const presets_SingboxEngine: v.GenericSchema = v.looseObject({
	action: v.optional(v.nullable(v.string())),
	ruleSets: v.optional(v.nullable(v.array(v.lazy(() => presets_RuleRef)))),
});

const wdtt_ClientConfig: v.GenericSchema = v.looseObject({
	captchaMode: v.optional(v.nullable(v.string())),
	connMode: v.optional(v.nullable(v.string())),
	debug: v.optional(v.nullable(v.boolean())),
	deviceId: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	fingerprint: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	ndmsIface: v.optional(v.nullable(v.string())),
	obfs: v.optional(v.nullable(v.string())),
	password: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.string())),
	peerRaw: v.optional(v.nullable(v.string())),
	peerWg: v.optional(v.nullable(v.string())),
	policyPermits: v.optional(v.nullable(v.array(v.lazy(() => wdtt_OpkgPolicyPermit)))),
	rawClientIp: v.optional(v.nullable(v.string())),
	rawClientMTU: v.optional(v.nullable(v.number())),
	rawIface: v.optional(v.nullable(v.string())),
	sub: v.optional(v.nullable(v.string())),
	vkAuthMode: v.optional(v.nullable(v.string())),
	vkHashes: v.optional(v.nullable(v.string())),
	workers: v.optional(v.nullable(v.number())),
});

const wdtt_ClientInstance: v.GenericSchema = v.looseObject({
	config: v.optional(v.nullable(v.lazy(() => wdtt_ClientConfig))),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const wdtt_Config: v.GenericSchema = v.looseObject({
	clients: v.optional(v.nullable(v.array(v.lazy(() => wdtt_ClientInstance)))),
	servers: v.optional(v.nullable(v.array(v.lazy(() => wdtt_ServerInstance)))),
	version: v.optional(v.nullable(v.number())),
});

const wdtt_ImportPayload: v.GenericSchema = v.looseObject({
	connMode: v.optional(v.nullable(v.string())),
	deviceId: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	password: v.optional(v.nullable(v.string())),
	peer: v.optional(v.nullable(v.string())),
	subUrl: v.optional(v.nullable(v.string())),
	vkHashes: v.optional(v.nullable(v.array(v.string()))),
	wg: v.optional(v.nullable(v.string())),
	workers: v.optional(v.nullable(v.number())),
});

const wdtt_InstanceStatus: v.GenericSchema = v.looseObject({
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	status: v.optional(v.nullable(v.lazy(() => wdtt_ProcessStatus))),
});

const wdtt_LinkDecodeResult: v.GenericSchema = v.looseObject({
	profile: v.optional(v.nullable(v.lazy(() => wdtt_ImportPayload))),
	subscription: v.optional(v.nullable(v.lazy(() => wdtt_SubscriptionPreview))),
});

const wdtt_OpkgPolicyPermit: v.GenericSchema = v.looseObject({
	name: v.optional(v.nullable(v.string())),
	order: v.optional(v.nullable(v.number())),
});

const wdtt_ProcessStatus: v.GenericSchema = v.looseObject({
	binary: v.optional(v.nullable(v.string())),
	binaryPresent: v.optional(v.nullable(v.boolean())),
	dtlsConnections: v.optional(v.nullable(v.number())),
	lastError: v.optional(v.nullable(v.string())),
	log: v.optional(v.nullable(v.string())),
	ndmsIface: v.optional(v.nullable(v.string())),
	pid: v.optional(v.nullable(v.number())),
	rawClientIp: v.optional(v.nullable(v.string())),
	rawIface: v.optional(v.nullable(v.string())),
	running: v.optional(v.nullable(v.boolean())),
	startedAt: v.optional(v.nullable(v.string())),
	wgConfig: v.optional(v.nullable(v.string())),
});

const wdtt_ServerClient: v.GenericSchema = v.looseObject({
	comment: v.optional(v.nullable(v.string())),
	password: v.optional(v.nullable(v.string())),
	vkHash: v.optional(v.nullable(v.string())),
});

const wdtt_ServerConfig: v.GenericSchema = v.looseObject({
	adminId: v.optional(v.nullable(v.string())),
	botToken: v.optional(v.nullable(v.string())),
	clients: v.optional(v.nullable(v.array(v.lazy(() => wdtt_ServerClient)))),
	configDir: v.optional(v.nullable(v.string())),
	debug: v.optional(v.nullable(v.boolean())),
	directListen: v.optional(v.nullable(v.string())),
	enabled: v.optional(v.nullable(v.boolean())),
	ingressEnabled: v.optional(v.nullable(v.boolean())),
	lanSegments: v.optional(v.nullable(v.array(v.string()))),
	linkPeer: v.optional(v.nullable(v.string())),
	linkVkHashes: v.optional(v.nullable(v.string())),
	listen: v.optional(v.nullable(v.string())),
	natIface: v.optional(v.nullable(v.string())),
	natMode: v.optional(v.nullable(v.string())),
	natStaticWan: v.optional(v.nullable(v.string())),
	natStaticWans: v.optional(v.nullable(v.array(v.string()))),
	ndmsIface: v.optional(v.nullable(v.string())),
	openFirewall: v.optional(v.nullable(v.boolean())),
	password: v.optional(v.nullable(v.string())),
	policy: v.optional(v.nullable(v.string())),
	rawListen: v.optional(v.nullable(v.string())),
	relayMode: v.optional(v.nullable(v.string())),
	statsLog: v.optional(v.nullable(v.string())),
	wgIface: v.optional(v.nullable(v.string())),
	wgPort: v.optional(v.nullable(v.number())),
});

const wdtt_ServerInstance: v.GenericSchema = v.looseObject({
	config: v.optional(v.nullable(v.lazy(() => wdtt_ServerConfig))),
	id: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
});

const wdtt_Status: v.GenericSchema = v.looseObject({
	client: v.optional(v.nullable(v.lazy(() => wdtt_ProcessStatus))),
	clients: v.optional(v.nullable(v.array(v.lazy(() => wdtt_InstanceStatus)))),
	installAvailable: v.optional(v.nullable(v.boolean())),
	installVersion: v.optional(v.nullable(v.string())),
	installedVersion: v.optional(v.nullable(v.string())),
	installing: v.optional(v.nullable(v.boolean())),
	routerClock: v.optional(v.nullable(v.string())),
	server: v.optional(v.nullable(v.lazy(() => wdtt_ProcessStatus))),
	serverSupported: v.optional(v.nullable(v.boolean())),
	servers: v.optional(v.nullable(v.array(v.lazy(() => wdtt_InstanceStatus)))),
	updateAvailable: v.optional(v.nullable(v.boolean())),
});

const wdtt_SubscriptionPreview: v.GenericSchema = v.looseObject({
	description: v.optional(v.nullable(v.string())),
	name: v.optional(v.nullable(v.string())),
	profiles: v.optional(v.nullable(v.array(v.lazy(() => wdtt_ImportPayload)))),
	subUrl: v.optional(v.nullable(v.string())),
	trafficLimitMb: v.optional(v.nullable(v.number())),
	trafficUsedMb: v.optional(v.nullable(v.number())),
	updatedAt: v.optional(v.nullable(v.string())),
});

/**
 * 2xx response envelope schema per "METHOD /path" (path как в swagger,
 * без basePath /api; шаблонные сегменты — {param}).
 */
export const RESPONSE_SCHEMAS: Record<string, v.GenericSchema> = {
	"DELETE /access-policies/assign": v.lazy(() => api_OkResponse),
	"DELETE /access-policies/delete": v.lazy(() => api_OkResponse),
	"DELETE /access-policies/permit": v.lazy(() => api_OkResponse),
	"DELETE /awg3-endpoints/{id}": v.lazy(() => api_Awg3ListResponse),
	"DELETE /connections": v.lazy(() => api_ConnectionKillEnvelope),
	"DELETE /freeturn/clients/{id}": v.lazy(() => api_APIEnvelope),
	"DELETE /freeturn/servers/{id}": v.lazy(() => api_APIEnvelope),
	"DELETE /freeturn/servers/{id}/allowlist": v.lazy(() => api_FreeTurnAllowlistResponse),
	"DELETE /freeturn/servers/{id}/allowlist/{clientId}": v.lazy(() => api_FreeTurnAllowlistResponse),
	"DELETE /hydraroute/geo-files/delete": v.lazy(() => api_OkResponse),
	"DELETE /managed-servers/{id}": v.lazy(() => api_ServersAllResponse),
	"DELETE /managed-servers/{id}/peers/{pubkey}": v.lazy(() => api_ServersAllResponse),
	"DELETE /proxy/instance": v.lazy(() => api_APIEnvelope),
	"DELETE /servers/{name}/peers/{pubkey}": v.lazy(() => api_ServersAllResponse),
	"DELETE /servers/mark": v.lazy(() => api_ServersAllResponse),
	"DELETE /singbox/subscriptions/delete": v.lazy(() => api_APIEnvelope),
	"DELETE /singbox/tunnels": v.lazy(() => api_SingboxTunnelsResponse),
	"DELETE /wdtt/clients/{id}": v.lazy(() => api_APIEnvelope),
	"DELETE /wdtt/servers/{id}": v.lazy(() => api_APIEnvelope),
	"DELETE /wdtt/servers/{id}/users/{password}": v.lazy(() => api_APIEnvelope),
	"GET /access-policies": v.lazy(() => api_AccessPoliciesListResponse),
	"GET /access-policies/devices": v.lazy(() => api_PolicyDevicesListResponse),
	"GET /access-policies/interfaces": v.lazy(() => api_PolicyInterfacesListResponse),
	"GET /auth/status": v.lazy(() => api_AuthStatusResponse),
	"GET /awg3-endpoints": v.lazy(() => api_Awg3ListResponse),
	"GET /boot-status": v.lazy(() => api_BootStatusResponse),
	"GET /client-routes": v.lazy(() => api_ClientRoutesListResponse),
	"GET /connections": v.lazy(() => api_ConnectionsResponseEnvelope),
	"GET /diagnostics/dns-proxy": v.lazy(() => api_DnsProxyInfoEnvelope),
	"GET /diagnostics/status": v.lazy(() => api_DiagnosticsStatusResponse),
	"GET /dns-check/client": v.lazy(() => api_DnsCheckStartResponseEnvelope),
	"GET /dns-check/probe": v.lazy(() => api_APIEnvelope),
	"GET /dns-routes/get": v.lazy(() => api_DnsRouteResponse),
	"GET /dns-routes/list": v.lazy(() => api_DnsRoutesListResponse),
	"GET /download/outbounds": v.lazy(() => api_DownloadOutboundsResponse),
	"GET /external-tunnels": v.lazy(() => api_ExternalTunnelsResponse),
	"GET /freeturn/captcha/status": v.lazy(() => api_FreeTurnCaptchaStatusResponse),
	"GET /freeturn/config": v.lazy(() => api_FreeTurnConfigResponse),
	"GET /freeturn/servers/{id}/allowlist": v.lazy(() => api_FreeTurnAllowlistResponse),
	"GET /freeturn/status": v.lazy(() => api_FreeTurnStatusResponse),
	"GET /health": v.lazy(() => api_HealthResponse),
	"GET /hydraroute/config": v.lazy(() => api_HydraRouteConfigResponse),
	"GET /hydraroute/geo-expand": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_GeoExpandData))),
})]),
	"GET /hydraroute/geo-files": v.lazy(() => api_GeoFilesResponse),
	"GET /hydraroute/geo-tags": v.lazy(() => api_GeoTagsResponse),
	"GET /hydraroute/ipset-usage": v.lazy(() => api_IpsetUsageResponse),
	"GET /hydraroute/oversized-tags": v.lazy(() => api_OversizedTagsResponse),
	"GET /logs": v.lazy(() => api_LogsResponseEnvelope),
	"GET /logs/subgroups": v.lazy(() => api_SubgroupsResponseEnvelope),
	"GET /managed-servers": v.lazy(() => api_ManagedServersListResponse),
	"GET /managed-servers/{id}": v.lazy(() => api_ManagedServerResponse),
	"GET /managed-servers/{id}/asc": v.lazy(() => api_ASCParamsResponse),
	"GET /managed-servers/{id}/peers/{pubkey}/conf": v.lazy(() => api_PeerConfResponse),
	"GET /managed-servers/{id}/stats": v.lazy(() => api_ManagedServerStatsResponse),
	"GET /managed-servers/lan-segments": v.lazy(() => api_LANSegmentsListResponse),
	"GET /managed-servers/policies": v.lazy(() => api_PoliciesListResponse),
	"GET /managed-servers/suggest-address": v.lazy(() => api_SuggestAddressResponse),
	"GET /managed/drift": v.lazy(() => api_ManagedServerDriftEnvelope),
	"GET /managed/export": v.lazy(() => api_ManagedServerExportEnvelope),
	"GET /monitoring/matrix": v.lazy(() => api_MonitoringSnapshotResponse),
	"GET /ndms/save-status": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SaveStatusDTO))),
})]),
	"GET /pingcheck/logs": v.lazy(() => api_PingLogsResponse),
	"GET /pingcheck/status": v.lazy(() => api_PingCheckStatusResponse),
	"GET /presets": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_PresetsListResponse))),
})]),
	"GET /proxy/config": v.lazy(() => api_ProxyConfigResponse),
	"GET /proxy/instance": v.lazy(() => api_ProxyInstanceResponse),
	"GET /proxy/instance/check-ip": v.lazy(() => api_DeviceProxyInstanceIPCheckResponse),
	"GET /proxy/instance/runtime": v.lazy(() => api_ProxyRuntimeResponse),
	"GET /proxy/instances": v.lazy(() => api_ProxyInstancesResponse),
	"GET /proxy/listen-choices": v.lazy(() => api_ProxyListenChoicesResponse),
	"GET /proxy/listener": v.lazy(() => api_APIEnvelope),
	"GET /proxy/outbounds": v.lazy(() => api_ProxyOutboundsResponse),
	"GET /proxy/runtime": v.lazy(() => api_ProxyRuntimeResponse),
	"GET /routing/access-policies": v.lazy(() => api_AccessPoliciesListResponse),
	"GET /routing/client-routes": v.lazy(() => api_ClientRoutesListResponse),
	"GET /routing/dns-routes": v.lazy(() => api_DnsRoutesListResponse),
	"GET /routing/policy-devices": v.lazy(() => api_PolicyDevicesListResponse),
	"GET /routing/policy-interfaces": v.lazy(() => api_PolicyInterfacesListResponse),
	"GET /routing/resolve": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ResolveResponse))),
})]),
	"GET /routing/static-routes": v.lazy(() => api_StaticRoutesListResponse),
	"GET /routing/tunnels": v.lazy(() => api_RoutingTunnelsResponse),
	"GET /server/listen": v.lazy(() => api_ServerListenStateResponse),
	"GET /servers/{name}/peers/{pubkey}/conf": v.lazy(() => api_PeerConfResponse),
	"GET /servers/all": v.lazy(() => api_ServersAllResponse),
	"GET /servers/marked": v.lazy(() => api_APIEnvelope),
	"GET /servers/wan-ip": v.lazy(() => api_WANIPResponse),
	"GET /settings/get": v.lazy(() => api_SettingsResponse),
	"GET /signature/capture": v.lazy(() => api_SignatureCaptureResponse),
	"GET /singbox/awg-outbounds/tags": v.lazy(() => api_AWGOutboundTagsResponse),
	"GET /singbox/config-preview": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxConfigPreviewResponse))),
})]),
	"GET /singbox/config/slot": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ConfigSlotContentResponse))),
})]),
	"GET /singbox/config/slots": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ConfigSlotsResponse))),
})]),
	"GET /singbox/connections/clients": v.lazy(() => api_SingboxConnectionsClientsResponse),
	"GET /singbox/fakeip/config/dns/globals": v.lazy(() => api_SingboxDNSGlobalsResponse),
	"GET /singbox/fakeip/config/dns/rules/list": v.lazy(() => api_SingboxDNSRulesListResponse),
	"GET /singbox/fakeip/config/dns/servers/list": v.lazy(() => api_SingboxDNSServersListResponse),
	"GET /singbox/fakeip/config/outbounds/list": v.lazy(() => api_SingboxRouterOutboundsListResponse),
	"GET /singbox/fakeip/config/rules/list": v.lazy(() => api_SingboxRouterRulesListResponse),
	"GET /singbox/fakeip/config/rulesets/list": v.lazy(() => api_SingboxRouterRuleSetsListResponse),
	"GET /singbox/inbounds": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxInboundsResponse))),
})]),
	"GET /singbox/router/bindable-interfaces": v.lazy(() => api_SingboxRouterWANInterfacesListResponse),
	"GET /singbox/router/bypass-set/status": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_BypassSetStatusData))),
})]),
	"GET /singbox/router/dns/chain-preset": v.lazy(() => api_SingboxDNSChainPresetResponse),
	"GET /singbox/router/dns/globals": v.lazy(() => api_SingboxDNSGlobalsResponse),
	"GET /singbox/router/dns/rewrites/list": v.lazy(() => api_SingboxDNSRewritesListResponse),
	"GET /singbox/router/dns/rules/list": v.lazy(() => api_SingboxDNSRulesListResponse),
	"GET /singbox/router/dns/servers/list": v.lazy(() => api_SingboxDNSServersListResponse),
	"GET /singbox/router/dns/servers/lookup": v.lazy(() => api_SingboxDNSLookupResponse),
	"GET /singbox/router/geosites/list": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxGeositesData))),
})]),
	"GET /singbox/router/ingress-eligible-interfaces": v.lazy(() => api_SingboxRouterWANInterfacesListResponse),
	"GET /singbox/router/outbounds/list": v.lazy(() => api_SingboxRouterOutboundsListResponse),
	"GET /singbox/router/policies": v.lazy(() => api_SingboxRouterPoliciesListResponse),
	"GET /singbox/router/policy-devices": v.lazy(() => api_SingboxRouterPolicyDevicesListResponse),
	"GET /singbox/router/policy-tun/nat-preview": v.lazy(() => api_SingboxRouterNATPreviewResponse),
	"GET /singbox/router/presets/list": v.lazy(() => api_SingboxRouterPresetsListResponse),
	"GET /singbox/router/proxies/list": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxProxiesListResponse))),
})]),
	"GET /singbox/router/rules/list": v.lazy(() => api_SingboxRouterRulesListResponse),
	"GET /singbox/router/rulesets/dat-url": v.lazy(() => api_SingboxRouterDatRuleSetURLResponse),
	"GET /singbox/router/rulesets/list": v.lazy(() => api_SingboxRouterRuleSetsListResponse),
	"GET /singbox/router/settings": v.lazy(() => api_SingboxRouterSettingsResponse),
	"GET /singbox/router/staging": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_RouterStagingStatusResponse))),
})]),
	"GET /singbox/router/status": v.lazy(() => api_SingboxRouterStatusResponse),
	"GET /singbox/router/wan-interfaces": v.lazy(() => api_SingboxRouterWANInterfacesListResponse),
	"GET /singbox/status": v.lazy(() => api_SingboxStatusResponse),
	"GET /singbox/subscriptions": v.lazy(() => api_SubscriptionListResponse),
	"GET /singbox/subscriptions/active-now": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ActiveNowResponse))),
})]),
	"GET /singbox/subscriptions/get": v.lazy(() => api_SubscriptionResponse),
	"GET /singbox/subscriptions/groups": v.lazy(() => api_SubscriptionGroupListResponse),
	"GET /singbox/tunnels": v.lazy(() => api_SingboxTunnelsResponse),
	"GET /singbox/tunnels/get": v.lazy(() => api_SingboxTunnelGetResponse),
	"GET /singbox/tunnels/test/connectivity": v.lazy(() => api_APIEnvelope),
	"GET /singbox/tunnels/test/ip": v.lazy(() => api_APIEnvelope),
	"GET /static-routes/list": v.lazy(() => api_StaticRoutesListResponse),
	"GET /system-tunnels": v.lazy(() => api_SystemTunnelsResponse),
	"GET /system-tunnels/asc": v.lazy(() => api_ASCParamsResponse),
	"GET /system/all-interfaces": v.lazy(() => api_AllInterfacesResponse),
	"GET /system/hydraroute-status": v.lazy(() => api_HydraRouteStatusResponse),
	"GET /system/info": v.lazy(() => api_SystemInfoResponse),
	"GET /system/update/changelog": v.lazy(() => api_ChangelogResponse),
	"GET /system/update/check": v.lazy(() => api_UpdateCheckResponse),
	"GET /system/wan-interfaces": v.lazy(() => api_WANInterfacesResponse),
	"GET /terminal/status": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TerminalStatusResponse))),
})]),
	"GET /test/connectivity": v.lazy(() => api_ConnectivityResultResponse),
	"GET /test/ip": v.lazy(() => api_IPResultResponse),
	"GET /test/ip/services": v.lazy(() => api_IPServicesResponse),
	"GET /test/speed": v.lazy(() => api_SpeedTestResultResponse),
	"GET /test/speed/servers": v.lazy(() => api_SpeedTestInfoResponse),
	"GET /tunnels/all": v.lazy(() => api_TunnelsAllResponse),
	"GET /tunnels/get": v.lazy(() => api_TunnelDetailResponse),
	"GET /tunnels/list": v.lazy(() => api_TunnelListResponse),
	"GET /tunnels/pingcheck": v.lazy(() => api_NativePingCheckStatusResponse),
	"GET /tunnels/traffic": v.lazy(() => api_TunnelTrafficResponse),
	"GET /wan/status": v.lazy(() => api_WANStatusEnvelope),
	"GET /wdtt/config": v.lazy(() => api_WdttConfigResponse),
	"GET /wdtt/servers/{id}/users": v.lazy(() => api_APIEnvelope),
	"GET /wdtt/status": v.lazy(() => api_WdttStatusResponse),
	"PATCH /awg3-endpoints/{id}": v.lazy(() => api_Awg3ListResponse),
	"PATCH /freeturn/clients/{id}": v.lazy(() => api_APIEnvelope),
	"PATCH /freeturn/servers/{id}": v.lazy(() => api_APIEnvelope),
	"PATCH /singbox/tunnels/rename": v.lazy(() => api_SingboxTunnelsResponse),
	"PATCH /wdtt/clients/{id}": v.lazy(() => api_APIEnvelope),
	"PATCH /wdtt/servers/{id}": v.lazy(() => api_APIEnvelope),
	"POST /access-policies/assign": v.lazy(() => api_OkResponse),
	"POST /access-policies/create": v.lazy(() => api_AccessPolicyResponse),
	"POST /access-policies/description": v.lazy(() => api_OkResponse),
	"POST /access-policies/interface-up": v.lazy(() => api_OkResponse),
	"POST /access-policies/permit": v.lazy(() => api_OkResponse),
	"POST /access-policies/standalone": v.lazy(() => api_OkResponse),
	"POST /amnezia-premium/account-info": v.lazy(() => api_AmneziaPremiumAccountInfoResponse),
	"POST /amnezia-premium/download-config": v.lazy(() => api_AmneziaPremiumDownloadConfigResponse),
	"POST /amnezia-premium/login": v.lazy(() => api_AmneziaPremiumLoginResponse),
	"POST /auth/login": v.lazy(() => api_LoginResponseRaw),
	"POST /auth/logout": v.lazy(() => api_APIEnvelope),
	"POST /awg3-endpoints": v.lazy(() => api_Awg3ListResponse),
	"POST /client-routes/create": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ClientRouteDTO))),
})]),
	"POST /client-routes/delete": v.lazy(() => api_OkResponse),
	"POST /client-routes/toggle": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ClientRouteToggleData))),
})]),
	"POST /client-routes/update": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_ClientRouteDTO))),
})]),
	"POST /control/restart": v.lazy(() => api_TunnelControlResponse),
	"POST /control/restart-all": v.lazy(() => api_APIEnvelope),
	"POST /control/start": v.lazy(() => api_TunnelControlResponse),
	"POST /control/stop": v.lazy(() => api_TunnelControlResponse),
	"POST /control/toggle-default-route": v.lazy(() => api_APIEnvelope),
	"POST /control/toggle-enabled": v.lazy(() => api_APIEnvelope),
	"POST /diagnostics/run": v.lazy(() => api_APIEnvelope),
	"POST /dns-check/start": v.lazy(() => api_DnsCheckStartResponseEnvelope),
	"POST /dns-routes/bulk-backend": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/create": v.lazy(() => api_DnsRouteResponse),
	"POST /dns-routes/create-batch": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/delete": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/delete-batch": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/refresh": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/set-enabled": v.lazy(() => api_APIEnvelope),
	"POST /dns-routes/update": v.lazy(() => api_DnsRouteResponse),
	"POST /external-tunnels/adopt": v.lazy(() => api_APIEnvelope),
	"POST /freeturn/clients": v.lazy(() => api_FreeTurnClientInstanceResponse),
	"POST /freeturn/clients/{id}/start": v.lazy(() => api_APIEnvelope),
	"POST /freeturn/clients/{id}/stop": v.lazy(() => api_APIEnvelope),
	"POST /freeturn/install": v.lazy(() => api_APIEnvelope),
	"POST /freeturn/link/decode": v.lazy(() => api_DecodeLinkResponse),
	"POST /freeturn/server/link": v.lazy(() => api_GenerateLinkResponse),
	"POST /freeturn/servers": v.lazy(() => api_FreeTurnServerInstanceResponse),
	"POST /freeturn/servers/{id}/allowlist": v.lazy(() => api_FreeTurnAllowlistResponse),
	"POST /freeturn/servers/{id}/link": v.lazy(() => api_GenerateLinkResponse),
	"POST /freeturn/servers/{id}/start": v.lazy(() => api_APIEnvelope),
	"POST /freeturn/servers/{id}/stop": v.lazy(() => api_APIEnvelope),
	"POST /hook/ndms": v.lazy(() => api_APIEnvelope),
	"POST /hydraroute/geo-files/add": v.lazy(() => api_GeoFileResponse),
	"POST /hydraroute/geo-files/rescan": v.lazy(() => api_GeoFilesRescannedResponse),
	"POST /hydraroute/geo-files/take-control": v.lazy(() => api_GeoFileResponse),
	"POST /hydraroute/geo-files/update": v.lazy(() => api_GeoFileUpdatedResponse),
	"POST /hydraroute/policy-order": v.lazy(() => api_PolicyOrderResponse),
	"POST /import/conf": v.lazy(() => api_APIEnvelope),
	"POST /logs/clear": v.lazy(() => api_APIEnvelope),
	"POST /managed-servers": v.lazy(() => api_ManagedServerResponse),
	"POST /managed-servers/{id}/enabled": v.lazy(() => api_ServersAllResponse),
	"POST /managed-servers/{id}/lan-segments": v.lazy(() => api_ServersAllResponse),
	"POST /managed-servers/{id}/nat": v.lazy(() => api_ServersAllResponse),
	"POST /managed-servers/{id}/peers": v.lazy(() => api_ManagedPeerResponse),
	"POST /managed-servers/{id}/peers/{pubkey}/toggle": v.lazy(() => api_ServersAllResponse),
	"POST /managed-servers/{id}/policy": v.lazy(() => api_ServersAllResponse),
	"POST /managed-servers/{id}/restart": v.lazy(() => api_APIEnvelope),
	"POST /managed/import": v.lazy(() => api_ManagedServerImportEnvelope),
	"POST /managed/restore-drift": v.lazy(() => api_ManagedServerImportEnvelope),
	"POST /pingcheck/check-now": v.lazy(() => api_APIEnvelope),
	"POST /pingcheck/logs/clear": v.lazy(() => api_APIEnvelope),
	"POST /proxy/apply": v.lazy(() => api_APIEnvelope),
	"POST /proxy/instance/runtime/select": v.lazy(() => api_ProxyRuntimeResponse),
	"POST /proxy/instances/apply": v.lazy(() => api_APIEnvelope),
	"POST /proxy/kill-listener": v.lazy(() => api_APIEnvelope),
	"POST /proxy/runtime/select": v.lazy(() => api_ProxyRuntimeResponse),
	"POST /routing/refresh": v.lazy(() => api_RoutingRefreshResponse),
	"POST /server/listen/change": v.lazy(() => api_ServerListenChangeResponse),
	"POST /server/listen/confirm": v.lazy(() => api_OkResponse),
	"POST /servers/{name}/endpoint": v.lazy(() => api_ServersAllResponse),
	"POST /servers/{name}/nat": v.lazy(() => api_ServersAllResponse),
	"POST /servers/{name}/peers": v.lazy(() => api_ServersAllResponse),
	"POST /servers/{name}/peers/{pubkey}/toggle": v.lazy(() => api_ServersAllResponse),
	"POST /servers/{name}/policy": v.lazy(() => api_ServersAllResponse),
	"POST /servers/enabled": v.lazy(() => api_ServersAllResponse),
	"POST /servers/mark": v.lazy(() => api_ServersAllResponse),
	"POST /servers/restart": v.lazy(() => api_APIEnvelope),
	"POST /settings/regenerate-api-key": v.lazy(() => api_SettingsResponse),
	"POST /settings/update": v.lazy(() => api_SettingsResponse),
	"POST /signature/generate": v.lazy(() => api_SignatureGenerateResponse),
	"POST /singbox/config/user/apply": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_UserConfigApplyResponse))),
})]),
	"POST /singbox/config/user/check": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_UserConfigCheckResponse))),
})]),
	"POST /singbox/config/user/discard": v.lazy(() => api_OkResponse),
	"POST /singbox/config/user/enable": v.lazy(() => api_OkResponse),
	"POST /singbox/control": v.lazy(() => api_SingboxStatusResponse),
	"POST /singbox/fakeip/config/dns/globals": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/rules/add": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/rules/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/rules/move": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/rules/update": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/servers/add": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/servers/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/servers/move": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/dns/servers/update": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/outbounds/add": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/outbounds/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/outbounds/update": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/route/final": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rules/add": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rules/bulk-outbound": v.lazy(() => api_SingboxRouterBulkUpdatedResponse),
	"POST /singbox/fakeip/config/rules/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rules/move": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rules/update": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rulesets/add": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rulesets/bulk-detour": v.lazy(() => api_SingboxRouterBulkUpdatedResponse),
	"POST /singbox/fakeip/config/rulesets/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/fakeip/config/rulesets/update": v.lazy(() => api_OkResponse),
	"POST /singbox/install": v.lazy(() => api_APIEnvelope),
	"POST /singbox/ndms-proxy": v.lazy(() => api_APIEnvelope),
	"POST /singbox/router/bypass-set/install-conntrack": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_BypassSetStatusData))),
})]),
	"POST /singbox/router/bypass-set/install-deps": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_BypassSetStatusData))),
})]),
	"POST /singbox/router/dns/chain-preset": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/globals": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rewrites/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rewrites/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rewrites/move": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rewrites/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rules/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rules/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rules/move": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/rules/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/servers/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/servers/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/servers/move": v.lazy(() => api_OkResponse),
	"POST /singbox/router/dns/servers/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/inspect": v.lazy(() => api_SingboxRouterInspectResponse),
	"POST /singbox/router/inspect-dns": v.lazy(() => api_SingboxRouterInspectDNSResponse),
	"POST /singbox/router/mode": v.lazy(() => api_OkResponse),
	"POST /singbox/router/outbounds/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/outbounds/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/outbounds/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/policies": v.lazy(() => api_SingboxRouterPolicyResponse),
	"POST /singbox/router/policy-devices/bind": v.lazy(() => api_OkResponse),
	"POST /singbox/router/policy-devices/unbind": v.lazy(() => api_OkResponse),
	"POST /singbox/router/presets/apply": v.lazy(() => api_OkResponse),
	"POST /singbox/router/proxies/select": v.lazy(() => api_OkResponse),
	"POST /singbox/router/proxies/test": v.intersect([v.lazy(() => api_OkResponse), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SingboxProxiesTestResponse))),
})]),
	"POST /singbox/router/route/final": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rules/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rules/bulk-outbound": v.lazy(() => api_SingboxRouterBulkUpdatedResponse),
	"POST /singbox/router/rules/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rules/move": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rules/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rulesets/add": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rulesets/bulk-detour": v.lazy(() => api_SingboxRouterBulkUpdatedResponse),
	"POST /singbox/router/rulesets/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/router/rulesets/update": v.lazy(() => api_OkResponse),
	"POST /singbox/router/settings": v.lazy(() => api_OkResponse),
	"POST /singbox/router/staging/apply": v.lazy(() => api_OkResponse),
	"POST /singbox/router/staging/discard": v.lazy(() => api_OkResponse),
	"POST /singbox/subscriptions/active-member": v.lazy(() => api_OkResponse),
	"POST /singbox/subscriptions/create": v.lazy(() => api_SubscriptionResponse),
	"POST /singbox/subscriptions/groups/create": v.lazy(() => api_SubscriptionGroupResponse),
	"POST /singbox/subscriptions/groups/delete": v.lazy(() => api_APIEnvelope),
	"POST /singbox/subscriptions/members/add": v.lazy(() => api_SubscriptionResponse),
	"POST /singbox/subscriptions/members/exclude": v.lazy(() => api_SubscriptionResponse),
	"POST /singbox/subscriptions/members/remove": v.lazy(() => api_APIEnvelope),
	"POST /singbox/subscriptions/members/restore": v.lazy(() => api_SubscriptionResponse),
	"POST /singbox/subscriptions/orphans/delete": v.lazy(() => api_OkResponse),
	"POST /singbox/subscriptions/preview": v.lazy(() => api_APIEnvelope),
	"POST /singbox/subscriptions/refresh": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_SubscriptionRefreshData))),
})]),
	"POST /singbox/tunnels": v.lazy(() => api_APIEnvelope),
	"POST /singbox/tunnels/delay-check": v.lazy(() => api_APIEnvelope),
	"POST /singbox/tunnels/share-link": v.lazy(() => api_APIEnvelope),
	"POST /singbox/update": v.lazy(() => api_SingboxStatusResponse),
	"POST /static-routes/create": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_StaticRouteDTO))),
})]),
	"POST /static-routes/delete": v.lazy(() => api_APIEnvelope),
	"POST /static-routes/import": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_StaticRouteDTO))),
})]),
	"POST /static-routes/set-enabled": v.lazy(() => api_APIEnvelope),
	"POST /static-routes/update": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_StaticRouteDTO))),
})]),
	"POST /system-tunnels/asc": v.lazy(() => api_OkResponse),
	"POST /system/backup/import": v.lazy(() => api_APIEnvelope),
	"POST /system/hydraroute-control": v.lazy(() => api_APIEnvelope),
	"POST /system/restart": v.lazy(() => api_APIEnvelope),
	"POST /system/update/apply": v.lazy(() => api_UpdateApplyResponse),
	"POST /terminal/install": v.intersect([v.lazy(() => api_APIEnvelope), v.looseObject({
	data: v.optional(v.nullable(v.lazy(() => api_TerminalInstallData))),
})]),
	"POST /terminal/start": v.lazy(() => api_TerminalStartResponse),
	"POST /terminal/stop": v.lazy(() => api_APIEnvelope),
	"POST /tunnels/create": v.lazy(() => api_APIEnvelope),
	"POST /tunnels/delete": v.lazy(() => api_TunnelDeleteResponse),
	"POST /tunnels/pingcheck": v.lazy(() => api_APIEnvelope),
	"POST /tunnels/pingcheck/remove": v.lazy(() => api_APIEnvelope),
	"POST /tunnels/replace": v.lazy(() => api_APIEnvelope),
	"POST /tunnels/update": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/client/start": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/client/stop": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/clients": v.lazy(() => api_WdttClientInstanceResponse),
	"POST /wdtt/clients/{id}/ensure-wg-tunnel": v.lazy(() => api_WdttEnsureWGTunnelResponse),
	"POST /wdtt/clients/{id}/import": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/clients/{id}/linked-tunnels/clear": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/clients/{id}/start": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/clients/{id}/stop": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/clients/{id}/subscription/refresh": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/install": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/link/decode": v.lazy(() => api_WdttDecodeLinkResponse),
	"POST /wdtt/link/import": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/server/config": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/server/start": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/server/stop": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/lan-segments": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/link": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/nat": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/policy": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/start": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/stop": v.lazy(() => api_APIEnvelope),
	"POST /wdtt/servers/{id}/users": v.lazy(() => api_APIEnvelope),
	"PUT /freeturn/client/config": v.lazy(() => api_FreeTurnConfigResponse),
	"PUT /freeturn/clients/{id}": v.lazy(() => api_APIEnvelope),
	"PUT /freeturn/server/config": v.lazy(() => api_FreeTurnConfigResponse),
	"PUT /freeturn/servers/{id}": v.lazy(() => api_APIEnvelope),
	"PUT /hydraroute/config/update": v.lazy(() => api_HydraRouteConfigResponse),
	"PUT /managed-servers/{id}": v.lazy(() => api_ServersAllResponse),
	"PUT /managed-servers/{id}/asc": v.lazy(() => api_ASCParamsResponse),
	"PUT /managed-servers/{id}/peers/{pubkey}": v.lazy(() => api_ServersAllResponse),
	"PUT /proxy/config": v.lazy(() => api_ProxyConfigResponse),
	"PUT /proxy/instance": v.lazy(() => api_ProxyInstanceResponse),
	"PUT /servers/{name}/peers/{pubkey}": v.lazy(() => api_ServersAllResponse),
	"PUT /singbox/config/user": v.lazy(() => api_OkResponse),
	"PUT /singbox/fakeip/config/dns/globals": v.lazy(() => api_OkResponse),
	"PUT /singbox/router/dns/globals": v.lazy(() => api_OkResponse),
	"PUT /singbox/router/settings": v.lazy(() => api_OkResponse),
	"PUT /singbox/subscriptions/groups/update": v.lazy(() => api_SubscriptionGroupResponse),
	"PUT /singbox/subscriptions/update": v.lazy(() => api_SubscriptionResponse),
	"PUT /singbox/tunnels": v.lazy(() => api_SingboxTunnelsResponse),
	"PUT /wdtt/client/config": v.lazy(() => api_APIEnvelope),
	"PUT /wdtt/clients/{id}": v.lazy(() => api_APIEnvelope),
	"PUT /wdtt/server/config": v.lazy(() => api_APIEnvelope),
	"PUT /wdtt/servers/{id}": v.lazy(() => api_APIEnvelope),
};
