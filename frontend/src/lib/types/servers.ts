// ─────────────────────────────────────────────
// #region Servers — WireGuard, managed server
// ─────────────────────────────────────────────

export interface WireguardServer {
	id: string;
	interfaceName: string;
	description: string;
	status: 'up' | 'down';
	connected: boolean;
	mtu: number;
	address: string;
	mask: string;
	publicKey: string;
	listenPort: number;
	peers: WireguardServerPeer[];
	natEnabled?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	policy?: string;
	keenDnsDomain?: string;
	/** User-configured connect host for client .conf; empty = WAN IP at generation. */
	endpoint?: string;
	builtIn?: boolean;
	/**
	 * False when the backend failed to read NAT mode / policy from NDMS
	 * (e.g. transient router error). When false, natMode/policy are NOT
	 * trustworthy and the UI must show an "unknown" state rather than the
	 * zero-valued 'none'. Absent (legacy/managed) is treated as known.
	 */
	natModeKnown?: boolean;
	policyKnown?: boolean;
	/** NDMS admin intent (conf layer running). Prefer over status/connected for toggles. */
	enabled?: boolean;
	enabledKnown?: boolean;
}

export interface WireguardServerPeer {
	publicKey: string;
	description: string;
	endpoint: string;
	allowedIPs?: string[];
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
	enabled: boolean;
	confAvailable?: boolean;
	// Сигнатура пира (I1-I5) и профиль имитации, которым она сгенерирована.
	// Есть только у пиров с локальным ключом (confAvailable).
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	signatureProfile?: string;
	/** Резолвер пира для .conf (#933). Пусто — бэкенд подставит DNS роутера. */
	dns?: string;
	/** AllowedIPs в .conf клиента (#713); пусто — весь трафик. Только у пиров с записью. */
	clientAllowedIPs?: string;
	/** Сети за клиентом (IPv4 CIDR) — allow-ips пира и маршруты на роутере. */
	remoteSubnets?: string[];
	/** Адрес из записи; у чужих пиров пусто — тогда эвристика по allowedIPs. */
	tunnelIP?: string;
}

export interface WireguardServerConfig {
	publicKey: string;
	listenPort: number;
	mtu: number;
	address: string;
	peers: WireguardServerPeerConfig[];
}

export interface WireguardServerPeerConfig {
	publicKey: string;
	description: string;
	presharedKey: string;
	allowedIPs: string[];
	address: string;
}

export interface ManagedServer {
	interfaceName: string;
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	lanSegments?: string[];
	foreignAcls?: string[];
	policy: string;
	peers: ManagedPeer[];
}

export interface ManagedPeer {
	publicKey: string;
	privateKey: string;
	presharedKey: string;
	description: string;
	tunnelIP: string;
	dns?: string;
	/** AllowedIPs в .conf клиента (#713); пусто — весь трафик. */
	clientAllowedIPs?: string;
	/** Сети за клиентом (IPv4 CIDR) — allow-ips пира и маршруты на роутере. */
	remoteSubnets?: string[];
	enabled: boolean;
	// Сигнатура пира (I1-I5) и профиль имитации, которым она сгенерирована.
	// Профиль пуст у сигнатур, набранных руками или перенесённых с сервера.
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	signatureProfile?: string;
}

export interface ManagedServerStats {
	status: string;
	peers: ManagedPeerStats[];
}

export interface ManagedPeerStats {
	publicKey: string;
	endpoint: string;
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
}

export interface CreateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	description?: string;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	generateAsc?: boolean;
	mode?: '3.1';
}

// UpdateManagedServerRequest matches the Go-side pointer-field semantics:
// - omit a field entirely (do not include it in the body) to PRESERVE the existing value
// - include a field (even with empty string / 0) to SET it (empty string CLEARS)
// Build the payload conditionally on the call site so a value the user
// didn't touch never appears in the request.
export interface UpdateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	description?: string;
	endpoint?: string;
	dns?: string;
	mtu?: number;
}

export interface AddManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
	clientAllowedIPs?: string;
	remoteSubnets?: string[];
}

export interface UpdateManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
	// Омитим — сигнатура не трогается; прислали — заменяет все пять полей и профиль.
	signature?: { profile: string; i1: string; i2: string; i3: string; i4: string; i5: string };
	clientAllowedIPs?: string;
	remoteSubnets?: string[];
}

/** Пресеты поля «AllowedIPs клиента» (#713). */
export interface PeerPresets {
	routerOnly: string;
	exceptRouter: string;
}

// #endregion

// #region Managed Server Backup / Restore
// ─────────────────────────────────────────────

/**
 * Single managed server entry as exported to a backup file.
 * Shape mirrors storage.ManagedServer JSON; policy is optional
 * because newly-created servers may not have one assigned yet.
 */
export interface ManagedServerExport {
	interfaceName: string;
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	policy?: string;
	privateKey?: string;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	peers: ManagedPeer[];
}

export interface ManagedServerBackupFile {
	version: number;
	type: string;
	exportedAt: string;
	managedServers: ManagedServerExport[];
	warnings?: Array<{
		interfaceName?: string;
		message: string;
	}>;
}

export interface RestoreOptions {
	allowRenumber: boolean;
}

export interface RestoreOutcome {
	name: string;
	newName?: string;
	action: 'created' | 'merged' | 'renamed' | 'conflict' | 'failed';
	addedPeers?: number;
	conflicts?: string[];
	error?: string;
}

export interface ManagedServerRestoreResponse {
	outcomes: RestoreOutcome[];
}

export interface ManagedServerDriftResponse {
	drift: ManagedServerExport[];
}

// #endregion

// ─────────────────────────────────────────────
// #region Xray Server & Telegram Web Proxy
// ─────────────────────────────────────────────

export interface XrayClient {
	id: string;
	remark: string;
	enabled: boolean;
	created_at: string;
	fp_client_id?: string;
}

export interface XrayEndpointConfig {
	id: string;
	name: string;
	enabled: boolean;
	connection_way?: 'direct' | 'external_https' | string;
	origin_type?: 'public_address' | 'cloud_address' | string;
	public_domain: string;
	public_port?: number;
	origin_host?: string;
	origin_scheme?: 'http' | 'https' | string;
	origin_port?: number;
	path?: string;
	transport?: string;
	mode?: string;
	uplink_method?: string;
	xhttp_tuning?: XHTTPTuning;
}

export interface XrayConfig {
	enabled: boolean;
	listen_address?: string;
	listen_port: number;
	dispatcher_port: number;
	public_domain: string;
	public_port: number;
	connection_way?: 'direct' | 'external_https' | string;
	origin_type?: 'public_address' | 'cloud_address' | string;
	origin_host?: string;
	origin_scheme?: 'http' | 'https' | string;
	origin_port?: number;
	path: string;
	transport?: string;
	mode: string;
	uplink_method: string;
	xmux_max_connections: number;
	outbound_mode?: 'direct' | 'router' | 'interface' | 'outbound' | 'socks' | string;
	outbound_interface?: string;
	outbound_socks_port: number;
	allow_lan?: boolean;
	domain_strategy?: string;
	xhttp_tuning?: XHTTPTuning;
	endpoints?: XrayEndpointConfig[];
	clients: XrayClient[];
}

export interface XrayShareLinks {
	vless_url: string;
	finiteproto_url?: string;
	happ_json: string;
	singbox_json: string;
	mihomo_yaml: string;
	remark: string;
	uuid: string;
}

export type TgNetworkPreset = 'cdn_keendns' | 'cdn_static' | 'cdn_file_https' | 'keendns' | 'static_ip';

export interface WebProxyEndpoint {
	id: string;
	hostname: string;
	listen_port: number;
	admin_port: number;
	description?: string;
	preset?: TgNetworkPreset | string;
	carrier_mode?: string;
	is_cdn: boolean;
	enabled: boolean;
	external_backend?: boolean;
}

/** Secret-free endpoint descriptor returned by GET /servers/tgwebproxy/status. */
export interface WebProxyStatusLink {
	id: string;
	hostname: string;
	description?: string;
	preset?: TgNetworkPreset | string;
	carrier_mode?: string;
	is_cdn: boolean;
}

/** Endpoint with connection links; only returned by POST /servers/tgwebproxy/reveal. */
export interface WebProxyLink extends WebProxyStatusLink {
	tg_url: string;
	tme_url: string;
	bridge_url: string;
}

export interface TelegramUser {
	id: string;
	remark: string;
	secret?: string;
	secret_masked?: string;
	enabled: boolean;
	created_at?: string;
}

export interface TelegramUpstream {
	id: string;
	type: 'direct' | 'socks5';
	device?: string;
	address?: string;
	username?: string;
	/** Write-only: sent to set or change the SOCKS5 password; never returned by the API. */
	password?: string;
	/** A SOCKS5 password is stored; an empty password keeps it for the same id/address/username. */
	password_set?: boolean;
	weight: number;
	enabled: boolean;
}

export interface TgUserRevealData {
	id: string;
	remark: string;
	enabled: boolean;
	secret: string;
	tg_url: string;
	tme_url: string;
	bridge_url: string;
	mtproxy_secret: string;
	mtproxy_url: string;
	mtproxy_tme_url: string;
	web_proxies?: WebProxyLink[];
}

export interface TgWebProxyConfig {
	schema_version?: number;
	enabled: boolean;
	scenario?: string;
	network_preset?: TgNetworkPreset | string;
	listen_port: number;
	admin_port: number;
	public_hostname: string;
	direct_host?: string;
	direct_port?: number;
	secret: string;
	legacy_secret?: string;
	legacy_expires_at?: string;
	backend: string;
	carrier_mode: string;
	upstream_device?: string;
	upstreams?: TelegramUpstream[];
	tls_domain?: string;
	endpoints?: WebProxyEndpoint[];
	users?: TelegramUser[];
}

export interface TgWebProxyStatus {
	installed: boolean;
	installed_telemt?: boolean;
	installed_tproxy?: boolean;
	running: boolean;
	pid: number;
	port: number;
	direct_online?: boolean;
	raw_online?: boolean;
	webproxy_online?: boolean;
	backend_online: boolean;
	backend_addr: string;
	upstream_status?: 'ok' | 'degraded' | 'interface_down';
	upstreams?: TelegramUpstream[];
	network_preset?: TgNetworkPreset | string;
	carrier_mode: string;
	public_host: string;
	direct_host?: string;
	direct_port?: number;
	tls_domain?: string;
	secret_masked?: string;
	legacy_secret_masked?: string;
	legacy_active?: boolean;
	legacy_expires_at?: string;
	legacy_expired_pending_reconcile?: boolean;
	/** Сбой правила WAN-файрвола прямого порта MTProto. */
	direct_wan_error?: string;
	/** Connection links are intentionally absent; use revealTgWebProxySecret(). */
	web_proxies?: WebProxyStatusLink[];
	users?: TelegramUser[];
}

export interface TgWebProxyRevealData {
	user_id?: string;
	user_remark?: string;
	secret: string;
	legacy_secret?: string;
	direct_host: string;
	direct_port: number;
	tls_domain: string;
	tg_url: string;
	tme_url: string;
	bridge_url: string;
	mtproxy_secret: string;
	mtproxy_url: string;
	mtproxy_tme_url: string;
	web_proxies?: WebProxyLink[];
	users?: TgUserRevealData[];
}

export interface CDNRouteRule {
	id: string;
	domain: string;
	description: string;
	route_xray: boolean;
	route_telegram: boolean;
	enabled: boolean;
}

export interface CDNDispatcherConfig {
	enabled: boolean;
	running?: boolean;
	/** Ответ на сохранение: маршруты сохранены, но что-то рядом не обновилось. */
	warning?: string;
	listen_addr: string;
	xray_target: string;
	tg_target: string;
	routes: CDNRouteRule[];
	default_action?: '404' | 'reject';
	public_hostname?: string;
	xray_public_host?: string;
	tg_public_host?: string;
	xray_path_prefix?: string;
}

export type XHTTPPreset = 'live_low_latency' | 'cdn_get' | 'balanced' | 'custom';

export interface XHTTPTuning {
	preset: XHTTPPreset | string;
	uplink_method: string;
	min_posts_interval_ms: string;
	max_buffered_posts: number;
	sc_max_each_post_bytes?: number;
	stream_up_server_secs: string;
	stream_path_suffix: string;
	enable_xmux: boolean;
	x_padding_bytes: string;
	x_padding_header: string;
	x_padding_placement: string;
	x_padding_key?: string;
	x_padding_method?: string;
	x_padding_obfs_mode?: boolean;
	session_placement?: string;
	session_key?: string;
	seq_placement?: string;
	seq_key?: string;
	fp_chunk_bytes?: number;
	fp_max_ways?: number;
}

export interface ServerResetOptions {
	delete_keendns?: boolean;
}

export interface ServerResetResult {
	server: string;
	reset: boolean;
	dispatcher_reset: boolean;
	keendns_deleted?: string[];
	keendns_skipped?: string[];
	warnings?: string[];
}

// #endregion
