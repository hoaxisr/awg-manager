import type {
	AWGTunnel,
	TunnelListItem,
	IPResult,
	ConnectivityResult,
	SpeedTestResult,
	SpeedTestInfo,
	IPCheckService,
	SystemInfo,
	Settings,
	AuthStatus,
	LoginResult,
	LogsResponse,
	WANInterface,
	RouterInterface,
	WANStatus,
	ExternalTunnel,
	SystemTunnel,
	ASCParams,
	DeleteResult,
	BootStatus,
	ChangelogEntry,
	UpdateInfo,
	DiagnosticsStatus,
	DiagEvent,
	DiagMode,
	DiagRouteMode,
	DnsRoute,
	SignatureCaptureResult,
	StaticRouteList,
	ResolveResult,
	WireguardServerConfig,
	ManagedServer,
	ManagedPeer,
	ManagedServerStats,
	CreateManagedServerRequest,
	UpdateManagedServerRequest,
	AddManagedPeerRequest,
	UpdateManagedPeerRequest,
	NativePingCheckConfig,
	NativePingCheckStatus,
	PingLogEntry,
	TerminalStatus,
	AccessPolicy,
	ClientRoute,
	ConnectionsResponse,
	HydraRouteStatus,
	HydraRouteConfig,
	GeoFileEntry,
	GeoTag,
	HydraRouteOversizedResponse,
	IpsetUsage,
	DnsCheckStartResponse,
	SingboxTunnel,
	SingboxStatus,
	SingboxImportResponse,
	DeviceProxyConfig,
	DeviceProxyOutbound,
	DeviceProxyRuntime,
	AWGTagInfo,
	TunnelReferencedError,
	MonitoringSnapshot,
	MonitoringSample,
	SingboxRouterStatus,
	SingboxRouterSettings,
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterOutbound,
	SingboxRouterPreset,
	RouterPolicy,
	RouterPolicyDevice,
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals
} from '$lib/types';

interface ApiResponse<T> {
	success?: boolean;
	error?: boolean;
	data?: T;
	message?: string;
	code?: string;
}

class ApiClient {
	private baseUrl = '/api';
	private onUnauthorized?: () => void;
	private onConnectionLost?: () => void;
	private abortController = new AbortController();

	setUnauthorizedHandler(handler: () => void) {
		this.onUnauthorized = handler;
	}

	setConnectionLostHandler(handler: () => void) {
		this.onConnectionLost = handler;
	}

	abortAll() {
		this.abortController.abort();
		this.abortController = new AbortController();
	}

	private async request<T>(
		endpoint: string,
		options: RequestInit = {}
	): Promise<T> {
		const url = `${this.baseUrl}${endpoint}`;

		let response: Response;
		try {
			response = await fetch(url, {
				...options,
				credentials: 'same-origin',
				signal: this.abortController.signal,
				headers: {
					'Content-Type': 'application/json',
					...options.headers
				}
			});
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') {
				throw e;
			}
			this.onConnectionLost?.();
			throw new Error('Ошибка сети: не удалось подключиться к серверу');
		}

		// Handle 401 Unauthorized
		if (response.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}

		// Handle 503 Service Unavailable
		if (response.status === 503) {
			throw new Error('Сервер временно недоступен');
		}

		const contentType = response.headers.get('content-type') || '';
		if (!contentType.includes('application/json')) {
			const text = await response.text();
			throw new Error(`Ошибка сервера (${response.status}): ${text.substring(0, 100)}`);
		}

		let data: ApiResponse<T>;
		try {
			data = await response.json();
		} catch {
			throw new Error(`Некорректный ответ сервера (${response.status})`);
		}

		if (!response.ok || data.error) {
			throw new Error(data.message || `Ошибка запроса (${response.status})`);
		}

		return data.data as T;
	}

	// ─────────────────────────────────────────────
	// #region Tunnels — CRUD, export, traffic
	// ─────────────────────────────────────────────

	async listTunnels(): Promise<TunnelListItem[]> {
		return this.request('/tunnels/list');
	}

	async getTunnelsAll(): Promise<import('$lib/stores/tunnels').TunnelsSnapshot> {
		return this.request('/tunnels/all');
	}

	async getTunnel(id: string): Promise<AWGTunnel> {
		return this.request(`/tunnels/get?id=${encodeURIComponent(id)}`);
	}

	async updateTunnel(id: string, tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		return this.request(`/tunnels/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(tunnel)
		});
	}

	async getTraffic(
		id: string,
		period: '1h' | '24h'
	): Promise<{
		points: { t: number; rx: number; tx: number }[];
		stats: {
			points: number;
			peakRate: number;
			avgRx: number;
			avgTx: number;
			currentRx: number;
			currentTx: number;
		};
	}> {
		return this.request(
			`/tunnels/traffic?id=${encodeURIComponent(id)}&period=${encodeURIComponent(period)}`
		);
	}

	async deleteTunnel(id: string): Promise<DeleteResult> {
		// Direct fetch (not via this.request) so we can inspect HTTP 409
		// for the structured TunnelReferencedError payload before the
		// generic error handler swallows it.
		const url = `${this.baseUrl}/tunnels/delete?id=${encodeURIComponent(id)}`;
		let res: Response;
		try {
			res = await fetch(url, {
				method: 'POST',
				credentials: 'same-origin',
				signal: this.abortController.signal,
				headers: { 'Content-Type': 'application/json' }
			});
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') throw e;
			this.onConnectionLost?.();
			throw new Error('Ошибка сети: не удалось подключиться к серверу');
		}
		if (res.status === 409) {
			const body = await res.json().catch(() => ({}));
			const details: TunnelReferencedError = body?.details ?? {
				tunnelId: id,
				deviceProxy: false,
				routerRules: []
			};
			const err = new Error('tunnel_referenced') as Error & {
				details: TunnelReferencedError;
			};
			err.details = details;
			throw err;
		}
		if (res.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}
		if (!res.ok) {
			const text = await res.text().catch(() => '');
			throw new Error(`Ошибка удаления (${res.status}): ${text.substring(0, 100)}`);
		}
		const data = (await res.json()) as ApiResponse<DeleteResult>;
		if (data.error) throw new Error(data.message || 'Ошибка удаления');
		return data.data as DeleteResult;
	}

	async getAWGTags(): Promise<AWGTagInfo[]> {
		return this.request<AWGTagInfo[]>('/singbox/awg-outbounds/tags');
	}

	async exportTunnel(id: string): Promise<Blob> {
		const url = `${this.baseUrl}/tunnels/export?id=${encodeURIComponent(id)}`;
		const res = await fetch(url, { credentials: 'same-origin', signal: this.abortController.signal });
		if (!res.ok) throw new Error(`Export failed: ${res.status}`);
		return res.blob();
	}

	async exportAllTunnels(): Promise<Blob> {
		const url = `${this.baseUrl}/tunnels/export-all`;
		const res = await fetch(url, { credentials: 'same-origin', signal: this.abortController.signal });
		if (!res.ok) throw new Error(`Export failed: ${res.status}`);
		return res.blob();
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Control — start, stop, restart, toggle
	// ─────────────────────────────────────────────

	async startTunnel(id: string): Promise<{ id: string; status: string }> {
		return this.request(`/control/start?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async stopTunnel(id: string): Promise<{ id: string; status: string }> {
		return this.request(`/control/stop?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async restartTunnel(id: string): Promise<{ id: string; status: string }> {
		return this.request(`/control/restart?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async toggleDefaultRoute(id: string): Promise<{ id: string; defaultRoute: boolean }> {
		return this.request(`/control/toggle-default-route?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Import
	// ─────────────────────────────────────────────

	async importConfig(content: string, name?: string, backend?: string): Promise<AWGTunnel> {
		return this.request('/import/conf', {
			method: 'POST',
			body: JSON.stringify({ content, name, backend })
		});
	}

	async replaceConfig(id: string, content: string, name?: string): Promise<AWGTunnel> {
		return this.request(`/tunnels/replace?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ content, name: name || '' })
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Testing — IP check, connectivity, speed
	// ─────────────────────────────────────────────

	async checkIP(id: string, serviceURL?: string): Promise<IPResult> {
		let url = `/test/ip?id=${encodeURIComponent(id)}`;
		if (serviceURL) url += `&service=${encodeURIComponent(serviceURL)}`;
		return this.request(url);
	}

	async getIPCheckServices(): Promise<IPCheckService[]> {
		return this.request('/test/ip/services');
	}

	async checkConnectivity(id: string): Promise<ConnectivityResult> {
		return this.request(`/test/connectivity?id=${encodeURIComponent(id)}`);
	}

	async getSpeedTestInfo(): Promise<SpeedTestInfo> {
		return this.request('/test/speed/servers');
	}

	async speedTest(id: string, server: string, port: number, direction: 'download' | 'upload'): Promise<SpeedTestResult> {
		return this.request(`/test/speed?id=${encodeURIComponent(id)}&server=${encodeURIComponent(server)}&port=${port}&direction=${direction}`);
	}

	speedTestStream(
		id: string, server: string, port: number, direction: 'download' | 'upload',
		onInterval: (data: { second: number; bandwidth: number }) => void,
		onResult: (result: SpeedTestResult) => void,
		onError: (error: string) => void
	): EventSource {
		const url = `${this.baseUrl}/test/speed/stream?id=${encodeURIComponent(id)}&server=${encodeURIComponent(server)}&port=${port}&direction=${direction}`;
		const es = new EventSource(url);
		es.addEventListener('interval', (e) => { onInterval(JSON.parse(e.data)); });
		es.addEventListener('result', (e) => { onResult(JSON.parse(e.data)); es.close(); });
		es.addEventListener('error', (e) => {
			if (e instanceof MessageEvent) {
				onError(e.data);
			} else {
				onError('Соединение потеряно');
			}
			es.close();
		});
		return es;
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region System — info, WAN, interfaces
	// ─────────────────────────────────────────────

	async getSystemInfo(): Promise<SystemInfo> {
		return this.request('/system/info');
	}

	async restartDaemon(): Promise<void> {
		await this.request('/system/restart', { method: 'POST' });
	}

	async getHydraRouteStatus(): Promise<HydraRouteStatus> {
		return this.request('/system/hydraroute-status');
	}

	async controlHydraRoute(action: 'start' | 'stop' | 'restart'): Promise<HydraRouteStatus> {
		return this.request('/system/hydraroute-control', {
			method: 'POST',
			body: JSON.stringify({ action }),
		});
	}

	async getHydraRouteConfig(): Promise<HydraRouteConfig> {
		return this.request('/hydraroute/config');
	}

	async updateHydraRouteConfig(config: HydraRouteConfig): Promise<HydraRouteConfig> {
		return this.request('/hydraroute/config/update', {
			method: 'PUT',
			body: JSON.stringify(config),
		});
	}

	async getGeoFiles(): Promise<GeoFileEntry[]> {
		return this.request('/hydraroute/geo-files');
	}

	async addGeoFile(type: 'geosite' | 'geoip', url: string): Promise<GeoFileEntry> {
		return this.request('/hydraroute/geo-files/add', {
			method: 'POST',
			body: JSON.stringify({ type, url }),
		});
	}

	async deleteGeoFile(path: string): Promise<void> {
		await this.request(`/hydraroute/geo-files/delete?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
	}

	async updateGeoFile(path?: string): Promise<unknown> {
		return this.request('/hydraroute/geo-files/update', {
			method: 'POST',
			body: JSON.stringify({ path: path || '' }),
		});
	}

	async getGeoTags(path: string): Promise<GeoTag[]> {
		return this.request(`/hydraroute/geo-tags?path=${encodeURIComponent(path)}`);
	}

	async getIpsetUsage(): Promise<IpsetUsage> {
		return this.request('/hydraroute/ipset-usage');
	}

	async getHydraRouteOversizedTags(): Promise<HydraRouteOversizedResponse> {
		return this.request('/hydraroute/oversized-tags');
	}

	async importNativeHydraRouteRules(): Promise<{ imported: number }> {
		return this.request('/hydraroute/import-native', { method: 'POST' });
	}

	async setPolicyOrder(order: string[]): Promise<{ order: string[] }> {
		return this.request('/hydraroute/policy-order', {
			method: 'POST',
			body: JSON.stringify({ order }),
		});
	}

	async getWANInterfaces(): Promise<WANInterface[]> {
		return this.request('/system/wan-interfaces');
	}

	async getAllInterfaces(): Promise<RouterInterface[]> {
		return this.request('/system/all-interfaces');
	}

	async getWANStatus(): Promise<WANStatus> {
		return this.request('/wan/status');
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Updates
	// ─────────────────────────────────────────────

	async checkUpdate(force = false): Promise<UpdateInfo> {
		const query = force ? '?force=true' : '';
		return this.request(`/system/update/check${query}`);
	}

	async applyUpdate(): Promise<{ status: string }> {
		return this.request('/system/update/apply', { method: 'POST' });
	}

	async getUpdateChangelog(from: string, to: string): Promise<{ entries: ChangelogEntry[] }> {
		const parts = [`to=${encodeURIComponent(to)}`];
		// Omit `from` to request the single-version view for `to` — backend
		// returns just that one entry (used by "Что нового" when no update
		// is pending).
		if (from) parts.push(`from=${encodeURIComponent(from)}`);
		return this.request(`/system/update/changelog?${parts.join('&')}`);
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Settings
	// ─────────────────────────────────────────────

	async getSettings(): Promise<Settings> {
		return this.request('/settings/get');
	}

	async updateSettings(settings: Settings): Promise<Settings> {
		return this.request('/settings/update', {
			method: 'POST',
			body: JSON.stringify(settings)
		});
	}

	async regenerateApiKey(): Promise<Settings> {
		return this.request('/settings/regenerate-api-key', { method: 'POST' });
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Auth — login, logout, status
	// ─────────────────────────────────────────────

	async login(login: string, password: string): Promise<LoginResult> {
		const url = `${this.baseUrl}/auth/login`;
		const response = await fetch(url, {
			method: 'POST',
			credentials: 'same-origin',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ login, password })
		});

		const data = await response.json();
		if (!response.ok || data.error) {
			throw new Error(data.message || 'Ошибка авторизации');
		}
		return data;
	}

	async logout(): Promise<void> {
		await fetch(`${this.baseUrl}/auth/logout`, {
			method: 'POST',
			credentials: 'same-origin'
		});
	}

	async getAuthStatus(): Promise<AuthStatus> {
		const response = await fetch(`${this.baseUrl}/auth/status`, {
			credentials: 'same-origin'
		});
		if (!response.ok) {
			return { authenticated: false };
		}
		return response.json();
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Boot status (public, direct JSON)
	// ─────────────────────────────────────────────

	async getBootStatus(): Promise<BootStatus> {
		const response = await fetch(`${this.baseUrl}/boot-status`);
		if (!response.ok) {
			throw new Error('Boot status unavailable');
		}
		return response.json();
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Ping Check — status, logs, native
	// ─────────────────────────────────────────────

	async triggerPingCheck(): Promise<{ message: string }> {
		return this.request('/pingcheck/check-now', { method: 'POST' });
	}

	async getPingCheckLogs(tunnelId?: string): Promise<PingLogEntry[]> {
		const qs = tunnelId ? `?tunnelId=${encodeURIComponent(tunnelId)}` : '';
		return this.request<PingLogEntry[]>(`/pingcheck/logs${qs}`);
	}

	async clearPingCheckLogs(): Promise<{ message: string }> {
		return this.request('/pingcheck/logs/clear', { method: 'POST' });
	}

	// Per-tunnel NativeWG ping-check
	async getNativePingCheckStatus(tunnelId: string): Promise<NativePingCheckStatus> {
		return this.request(`/tunnels/pingcheck?id=${encodeURIComponent(tunnelId)}`);
	}

	async configureNativePingCheck(tunnelId: string, config: NativePingCheckConfig): Promise<void> {
		await this.request(`/tunnels/pingcheck?id=${encodeURIComponent(tunnelId)}`, {
			method: 'POST',
			body: JSON.stringify(config)
		});
	}

	async removeNativePingCheck(tunnelId: string): Promise<void> {
		await this.request(`/tunnels/pingcheck/remove?id=${encodeURIComponent(tunnelId)}`, {
			method: 'POST'
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Logging
	// ─────────────────────────────────────────────

	async getLogs(params?: {
		group?: string;
		subgroup?: string;
		level?: string;
		since?: number;
		limit?: number;
		offset?: number;
	}): Promise<LogsResponse> {
		const query = new URLSearchParams();
		if (params?.group) query.set('group', params.group);
		if (params?.subgroup) query.set('subgroup', params.subgroup);
		if (params?.level) query.set('level', params.level);
		if (params?.since != null && params.since > 0) query.set('since', String(params.since));
		if (params?.limit) query.set('limit', String(params.limit));
		if (params?.offset) query.set('offset', String(params.offset));
		const qs = query.toString();
		return this.request(`/logs${qs ? '?' + qs : ''}`);
	}

	async clearLogs(): Promise<void> {
		await this.request('/logs/clear', { method: 'POST' });
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region External Tunnels — list, adopt
	// ─────────────────────────────────────────────

	async listExternalTunnels(): Promise<ExternalTunnel[]> {
		return this.request('/external-tunnels');
	}

	async adoptExternalTunnel(interfaceName: string, content: string, name?: string): Promise<AWGTunnel> {
		return this.request(`/external-tunnels/adopt?interface=${encodeURIComponent(interfaceName)}`, {
			method: 'POST',
			body: JSON.stringify({ content, name })
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region System Tunnels — CRUD, ASC, testing
	// ─────────────────────────────────────────────

	async listSystemTunnels(): Promise<SystemTunnel[]> {
		return this.request('/system-tunnels');
	}

	async getSystemTunnel(name: string): Promise<SystemTunnel> {
		return this.request(`/system-tunnels/get?name=${encodeURIComponent(name)}`);
	}

	async getASCParams(name: string): Promise<ASCParams> {
		return this.request(`/system-tunnels/asc?name=${encodeURIComponent(name)}`);
	}

	async setASCParams(name: string, params: ASCParams): Promise<void> {
		return this.request(`/system-tunnels/asc?name=${encodeURIComponent(name)}`, {
			method: 'POST',
			body: JSON.stringify(params)
		});
	}

	async checkSystemTunnelConnectivity(name: string): Promise<ConnectivityResult> {
		return this.request(`/system-tunnels/test-connectivity?name=${encodeURIComponent(name)}`);
	}

	async checkSystemTunnelIP(name: string, serviceURL?: string): Promise<IPResult> {
		let url = `/system-tunnels/test-ip?name=${encodeURIComponent(name)}`;
		if (serviceURL) url += `&service=${encodeURIComponent(serviceURL)}`;
		return this.request(url);
	}

	systemTunnelSpeedTestStream(
		name: string, server: string, port: number, direction: 'download' | 'upload',
		onInterval: (data: { second: number; bandwidth: number }) => void,
		onResult: (result: SpeedTestResult) => void,
		onError: (error: string) => void
	): EventSource {
		const url = `${this.baseUrl}/system-tunnels/test-speed?name=${encodeURIComponent(name)}&server=${encodeURIComponent(server)}&port=${port}&direction=${direction}`;
		const es = new EventSource(url);
		es.addEventListener('interval', (e) => { onInterval(JSON.parse(e.data)); });
		es.addEventListener('result', (e) => { onResult(JSON.parse(e.data)); es.close(); });
		es.addEventListener('error', (e) => {
			if (e instanceof MessageEvent) {
				onError(e.data);
			} else {
				onError('Соединение потеряно');
			}
			es.close();
		});
		return es;
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region VPN Servers — list, config, mark
	// ─────────────────────────────────────────────

	async getServerConfig(name: string): Promise<WireguardServerConfig> {
		return this.request(`/servers/config?name=${encodeURIComponent(name)}`);
	}

	async markServerInterface(name: string): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/mark?name=${encodeURIComponent(name)}`, {
			method: 'POST'
		});
	}

	async unmarkServerInterface(name: string): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/mark?name=${encodeURIComponent(name)}`, {
			method: 'DELETE'
		});
	}

	async getMarkedServerInterfaces(): Promise<string[]> {
		return this.request('/servers/marked');
	}

	async getWANIP(): Promise<string> {
		const res = await this.request<{ ip: string }>('/servers/wan-ip');
		return res.ip;
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Static IP Routes
	// ─────────────────────────────────────────────

	async createStaticRoute(rl: Partial<StaticRouteList>): Promise<StaticRouteList> {
		return this.request('/static-routes/create', {
			method: 'POST',
			body: JSON.stringify(rl)
		});
	}

	async updateStaticRoute(rl: StaticRouteList): Promise<StaticRouteList> {
		return this.request('/static-routes/update', {
			method: 'POST',
			body: JSON.stringify(rl)
		});
	}

	async deleteStaticRoute(id: string): Promise<void> {
		return this.request(`/static-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async setStaticRouteEnabled(id: string, enabled: boolean): Promise<void> {
		return this.request(`/static-routes/set-enabled?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async importStaticRoutes(tunnelID: string, name: string, content: string): Promise<StaticRouteList> {
		return this.request('/static-routes/import', {
			method: 'POST',
			body: JSON.stringify({ tunnelID, name, content })
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Routing — resolve, tunnels
	// ─────────────────────────────────────────────

	async resolveDomain(domain: string): Promise<ResolveResult> {
		return this.request(`/routing/resolve?domain=${encodeURIComponent(domain)}`);
	}

	async refreshRouting(): Promise<{ missing: string[] }> {
		return this.request('/routing/refresh', { method: 'POST' });
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region DNS Routes — CRUD, batch, subscriptions
	// ─────────────────────────────────────────────

	async getDnsRoute(id: string): Promise<DnsRoute> {
		return this.request(`/dns-routes/get?id=${encodeURIComponent(id)}`);
	}

	async createDnsRoute(route: Partial<DnsRoute>): Promise<DnsRoute> {
		return this.request('/dns-routes/create', {
			method: 'POST',
			body: JSON.stringify(route)
		});
	}

	async updateDnsRoute(id: string, route: Partial<DnsRoute>): Promise<DnsRoute> {
		return this.request(`/dns-routes/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(route)
		});
	}

	async deleteDnsRoute(id: string): Promise<DnsRoute[]> {
		return this.request(`/dns-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async setDnsRouteEnabled(id: string, enabled: boolean): Promise<DnsRoute[]> {
		return this.request(`/dns-routes/set-enabled?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async createDnsRouteBatch(lists: Array<Partial<DnsRoute>>): Promise<{ created: number; lists: DnsRoute[] }> {
		return this.request('/dns-routes/create-batch', {
			method: 'POST',
			body: JSON.stringify(lists)
		});
	}

	async deleteDnsRouteBatch(ids: string[]): Promise<DnsRoute[]> {
		return this.request('/dns-routes/delete-batch', {
			method: 'POST',
			body: JSON.stringify({ ids })
		});
	}

	async refreshDnsRouteSubscriptions(id?: string): Promise<DnsRoute[]> {
		const endpoint = id
			? `/dns-routes/refresh?id=${encodeURIComponent(id)}`
			: '/dns-routes/refresh';
		return this.request(endpoint, { method: 'POST' });
	}

	async bulkDnsRouteBackend(listIDs: string[], backend: 'ndms' | 'hydraroute'): Promise<DnsRoute[]> {
		return this.request('/dns-routes/bulk-backend', {
			method: 'POST',
			body: JSON.stringify({ listIDs, backend }),
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Diagnostics — run, status, stream
	// ─────────────────────────────────────────────

	async runDiagnostics(): Promise<{ status: string }> {
		return this.request('/diagnostics/run', { method: 'POST' });
	}

	async getDiagnosticsStatus(): Promise<DiagnosticsStatus> {
		return this.request('/diagnostics/status');
	}

	async downloadDiagnosticsReport(): Promise<void> {
		const response = await fetch('/api/diagnostics/result');
		if (!response.ok) throw new Error('Report not available');
		const blob = await response.blob();
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = response.headers.get('Content-Disposition')
			?.match(/filename="(.+)"/)?.[1] || 'diagnostics.json';
		a.click();
		URL.revokeObjectURL(url);
	}

	streamDiagnostics(
		mode: DiagMode,
		restart: boolean,
		routeMode: DiagRouteMode,
		routeTunnelId: string,
		onEvent: (event: DiagEvent) => void,
		onError: (error: Event) => void
	): EventSource {
		const params = new URLSearchParams({
			mode,
			restart: String(restart),
			route: routeMode
		});
		if (routeMode === 'tunnel' && routeTunnelId) {
			params.set('tunnelId', routeTunnelId);
		}
		const es = new EventSource(`/api/diagnostics/stream?${params}`);

		const handleEvent = (e: MessageEvent) => {
			try {
				const data = JSON.parse(e.data) as DiagEvent;
				data.type = e.type as DiagEvent['type'];
				onEvent(data);
			} catch { /* ignore parse errors */ }
		};

		es.addEventListener('phase', handleEvent);
		es.addEventListener('test', handleEvent);
		es.addEventListener('done', handleEvent);
		es.addEventListener('error', (e) => {
			if (es.readyState === EventSource.CLOSED) return;
			onError(e);
		});

		return es;
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Managed WireGuard Server — CRUD, peers, ASC
	// ─────────────────────────────────────────────

	async getManagedServers(): Promise<ManagedServer[]> {
		return this.request('/managed-servers');
	}

	async getManagedServer(serverId: string): Promise<ManagedServer> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}`);
	}

	async createManagedServer(req: CreateManagedServerRequest): Promise<ManagedServer> {
		return this.request('/managed-servers', {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async suggestManagedServerAddress(): Promise<{ address: string; mask: string }> {
		return this.request('/managed-servers/suggest-address');
	}

	async getManagedServerPolicies(): Promise<{ id: string; description: string }[]> {
		return this.request('/managed-servers/policies');
	}

	async getManagedServerStats(serverId: string): Promise<ManagedServerStats> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/stats`);
	}

	async setManagedServerPolicy(serverId: string, policy: string): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/policy`, {
			method: 'POST',
			body: JSON.stringify({ policy })
		});
	}

	async updateManagedServer(serverId: string, req: UpdateManagedServerRequest): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}`, {
			method: 'PUT',
			body: JSON.stringify(req)
		});
	}

	async setManagedServerEnabled(serverId: string, enabled: boolean): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/enabled`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async setManagedServerNAT(serverId: string, enabled: boolean): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/nat`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async deleteManagedServer(serverId: string): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}`, {
			method: 'DELETE'
		});
	}

	async addManagedPeer(serverId: string, req: AddManagedPeerRequest): Promise<ManagedPeer> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/peers`, {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async updateManagedPeer(serverId: string, pubkey: string, req: UpdateManagedPeerRequest): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}`, {
			method: 'PUT',
			body: JSON.stringify(req)
		});
	}

	async deleteManagedPeer(serverId: string, pubkey: string): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}`, {
			method: 'DELETE'
		});
	}

	async toggleManagedPeer(serverId: string, publicKey: string, enabled: boolean): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(publicKey)}/toggle`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async getManagedPeerConf(serverId: string, pubkey: string): Promise<string> {
		const res = await this.request<{ conf: string }>(`/managed-servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}/conf`);
		return res.conf;
	}

	async getManagedServerASC(serverId: string): Promise<ASCParams> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/asc`);
	}

	async setManagedServerASC(serverId: string, params: ASCParams): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/asc`, {
			method: 'PUT',
			body: JSON.stringify(params)
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Terminal
	// ─────────────────────────────────────────────

	async terminalStatus(): Promise<TerminalStatus> {
		return this.request('/terminal/status');
	}

	async terminalInstall(): Promise<void> {
		return this.request('/terminal/install', { method: 'POST' });
	}

	async terminalStart(): Promise<{ port: number }> {
		return this.request('/terminal/start', { method: 'POST' });
	}

	async terminalStop(): Promise<void> {
		return this.request('/terminal/stop', { method: 'POST' });
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Signature capture
	// ─────────────────────────────────────────────

	async captureSignature(domain: string): Promise<SignatureCaptureResult> {
		return this.request(`/signature/capture?domain=${encodeURIComponent(domain)}`);
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Access Policies — CRUD, devices, interfaces
	// ─────────────────────────────────────────────

	async createAccessPolicy(description: string): Promise<AccessPolicy> {
		return this.request('/access-policies/create', {
			method: 'POST',
			body: JSON.stringify({ description }),
		});
	}

	async deleteAccessPolicy(name: string): Promise<void> {
		return this.request(`/access-policies/delete?name=${encodeURIComponent(name)}`, {
			method: 'DELETE',
		});
	}

	async setAccessPolicyDescription(name: string, description: string): Promise<void> {
		return this.request('/access-policies/description', {
			method: 'POST',
			body: JSON.stringify({ name, description }),
		});
	}

	async setAccessPolicyStandalone(name: string, enabled: boolean): Promise<void> {
		return this.request('/access-policies/standalone', {
			method: 'POST',
			body: JSON.stringify({ name, enabled }),
		});
	}

	async permitPolicyInterface(name: string, iface: string, order: number): Promise<void> {
		return this.request('/access-policies/permit', {
			method: 'POST',
			body: JSON.stringify({ name, interface: iface, order }),
		});
	}

	async denyPolicyInterface(name: string, iface: string): Promise<void> {
		return this.request(`/access-policies/permit?name=${encodeURIComponent(name)}&interface=${encodeURIComponent(iface)}`, {
			method: 'DELETE',
		});
	}

	async assignDeviceToPolicy(mac: string, policy: string): Promise<void> {
		return this.request('/access-policies/assign', {
			method: 'POST',
			body: JSON.stringify({ mac, policy }),
		});
	}

	async unassignDeviceFromPolicy(mac: string): Promise<void> {
		return this.request(`/access-policies/assign?mac=${encodeURIComponent(mac)}`, {
			method: 'DELETE',
		});
	}

	async setPolicyInterfaceUp(name: string, up: boolean): Promise<void> {
		return this.request('/access-policies/interface-up', {
			method: 'POST',
			body: JSON.stringify({ name, up }),
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Client Routes
	// ─────────────────────────────────────────────

	async createClientRoute(data: Partial<ClientRoute>): Promise<ClientRoute> {
		return this.request('/client-routes/create', {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async updateClientRoute(id: string, data: Partial<ClientRoute>): Promise<ClientRoute> {
		return this.request(`/client-routes/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async deleteClientRoute(id: string): Promise<void> {
		return this.request(`/client-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async toggleClientRoute(id: string, enabled: boolean): Promise<void> {
		return this.request(`/client-routes/toggle?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Connections — conntrack viewer
	// ─────────────────────────────────────────────

	async getConnections(params: {
		tunnel?: string;
		protocol?: string;
		search?: string;
		offset?: number;
		limit?: number;
		sortBy?: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes';
		sortDir?: 'asc' | 'desc';
	} = {}): Promise<ConnectionsResponse> {
		const sp = new URLSearchParams();
		if (params.tunnel && params.tunnel !== 'all') sp.set('tunnel', params.tunnel);
		if (params.protocol && params.protocol !== 'all') sp.set('protocol', params.protocol);
		if (params.search) sp.set('search', params.search);
		if (params.offset) sp.set('offset', String(params.offset));
		if (params.limit) sp.set('limit', String(params.limit));
		if (params.sortBy) sp.set('sortBy', params.sortBy);
		if (params.sortDir) sp.set('sortDir', params.sortDir);
		const qs = sp.toString();
		return this.request<ConnectionsResponse>(`/connections${qs ? '?' + qs : ''}`);
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region DNS Check
	// ─────────────────────────────────────────────

	async startDnsCheck(): Promise<DnsCheckStartResponse> {
		return this.request('/dns-check/start', { method: 'POST' });
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Sing-box
	// ─────────────────────────────────────────────

	async singboxGetStatus(): Promise<SingboxStatus> {
		return this.request('/singbox/status');
	}

	async singboxInstall(): Promise<SingboxStatus> {
		return this.request('/singbox/install', { method: 'POST' });
	}

	async singboxControl(action: 'start' | 'stop' | 'restart'): Promise<SingboxStatus> {
		return this.request('/singbox/control', {
			method: 'POST',
			body: JSON.stringify({ action }),
		});
	}

	async singboxListTunnels(): Promise<SingboxTunnel[]> {
		return this.request('/singbox/tunnels');
	}

	async singboxImportLinks(links: string): Promise<SingboxImportResponse> {
		return this.request('/singbox/tunnels', {
			method: 'POST',
			body: JSON.stringify({ links })
		});
	}

	async singboxGetTunnel(tag: string): Promise<{ tag: string; outbound: unknown }> {
		return this.request(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`);
	}

	async singboxUpdateTunnel(tag: string, outbound: unknown): Promise<SingboxTunnel[]> {
		return this.request(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`, {
			method: 'PUT',
			body: JSON.stringify({ outbound })
		});
	}

	async singboxDeleteTunnel(tag: string): Promise<SingboxTunnel[]> {
		return this.request(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`, {
			method: 'DELETE'
		});
	}

	async singboxDelayCheck(tag: string): Promise<{ tag: string; delay: number }> {
		return this.request(`/singbox/tunnels/delay-check?tag=${encodeURIComponent(tag)}`, {
			method: 'POST',
		});
	}

	singboxSpeedTestStream(
		tag: string,
		server: string,
		port: number,
		onPhase: (phase: 'download' | 'upload') => void,
		onInterval: (data: { phase: string; second: number; bandwidth: number }) => void,
		onResult: (data: { phase: string; bandwidth: number; bytes: number; duration: number }) => void,
		onDone: () => void,
		onError: (error: string) => void,
	): EventSource {
		const url = `${this.baseUrl}/singbox/tunnels/test/speed/stream?tag=${encodeURIComponent(tag)}&server=${encodeURIComponent(server)}&port=${port}`;
		const es = new EventSource(url);
		es.addEventListener('phase', (e) => {
			try { onPhase(JSON.parse((e as MessageEvent).data).phase); } catch { /* ignore */ }
		});
		es.addEventListener('interval', (e) => {
			try { onInterval(JSON.parse((e as MessageEvent).data)); } catch { /* ignore */ }
		});
		es.addEventListener('result', (e) => {
			try { onResult(JSON.parse((e as MessageEvent).data)); } catch { /* ignore */ }
		});
		es.addEventListener('done', () => { onDone(); es.close(); });
		es.addEventListener('error', (e) => {
			const msg = e instanceof MessageEvent ? String(e.data) : 'Соединение потеряно';
			onError(msg);
			es.close();
		});
		return es;
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Device Proxy
	// ─────────────────────────────────────────────

	async getDeviceProxyConfig(): Promise<DeviceProxyConfig> {
		return this.request('/proxy/config');
	}

	async saveDeviceProxyConfig(cfg: DeviceProxyConfig): Promise<DeviceProxyConfig> {
		return this.request('/proxy/config', {
			method: 'PUT',
			body: JSON.stringify(cfg),
		});
	}

	async getDeviceProxyRuntime(): Promise<DeviceProxyRuntime> {
		return this.request('/proxy/runtime');
	}

	async selectDeviceProxyRuntime(tag: string): Promise<{ active: string }> {
		return this.request('/proxy/runtime/select', {
			method: 'POST',
			body: JSON.stringify({ tag }),
		});
	}

	async applyDeviceProxy(): Promise<{ applied: boolean }> {
		return this.request('/proxy/apply', { method: 'POST' });
	}

	async listDeviceProxyOutbounds(): Promise<DeviceProxyOutbound[]> {
		return this.request('/proxy/outbounds');
	}

	async getDeviceProxyListenChoices(): Promise<{
		lanIP: string;
		bridges: { id: string; label: string; ip: string }[];
		singboxRunning: boolean;
	}> {
		return this.request('/proxy/listen-choices');
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Monitoring (Phase 3)
	// ─────────────────────────────────────────────

	async getMonitoringMatrix(): Promise<MonitoringSnapshot> {
		return this.request<MonitoringSnapshot>('/monitoring/matrix');
	}

	async getMonitoringHistory(params: {
		target: string;
		tunnelId: string;
		limit?: number;
	}): Promise<MonitoringSample[]> {
		const qs = new URLSearchParams({
			target: params.target,
			tunnelId: params.tunnelId,
		});
		if (params.limit && params.limit > 0) qs.set('limit', String(params.limit));
		return this.request<MonitoringSample[]>(`/monitoring/history?${qs.toString()}`);
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Sing-box Router (TProxy routing engine)
	// ─────────────────────────────────────────────

	async singboxRouterStatus(): Promise<SingboxRouterStatus> {
		return this.request('/singbox/router/status');
	}

	async singboxRouterEnable(): Promise<void> {
		await this.request('/singbox/router/enable', { method: 'POST' });
	}

	async singboxRouterDisable(): Promise<void> {
		await this.request('/singbox/router/disable', { method: 'POST' });
	}

	async singboxRouterGetSettings(): Promise<SingboxRouterSettings> {
		return this.request('/singbox/router/settings');
	}

	async singboxRouterPutSettings(settings: SingboxRouterSettings): Promise<void> {
		await this.request('/singbox/router/settings', {
			method: 'PUT',
			body: JSON.stringify(settings),
		});
	}

	async singboxRouterListRules(): Promise<SingboxRouterRule[]> {
		return this.request('/singbox/router/rules/list');
	}

	async singboxRouterAddRule(rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/router/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxRouterUpdateRule(index: number, rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/router/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxRouterDeleteRule(index: number): Promise<void> {
		await this.request('/singbox/router/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterListRuleSets(): Promise<SingboxRouterRuleSet[]> {
		return this.request('/singbox/router/rulesets/list');
	}

	async singboxRouterAddRuleSet(rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/router/rulesets/add', {
			method: 'POST',
			body: JSON.stringify(rs),
		});
	}

	async singboxRouterDeleteRuleSet(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/rulesets/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterRefreshRuleSet(tag: string): Promise<void> {
		await this.request('/singbox/router/rulesets/refresh', {
			method: 'POST',
			body: JSON.stringify({ tag }),
		});
	}

	async singboxRouterListOutbounds(): Promise<SingboxRouterOutbound[]> {
		return this.request('/singbox/router/outbounds/list');
	}

	async singboxRouterAddOutbound(o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/router/outbounds/add', {
			method: 'POST',
			body: JSON.stringify(o),
		});
	}

	async singboxRouterUpdateOutbound(tag: string, o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/router/outbounds/update', {
			method: 'POST',
			body: JSON.stringify({ tag, outbound: o }),
		});
	}

	async singboxRouterDeleteOutbound(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/outbounds/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterListPresets(): Promise<SingboxRouterPreset[]> {
		return this.request('/singbox/router/presets/list');
	}

	async singboxRouterApplyPreset(id: string, outbound: string): Promise<void> {
		await this.request('/singbox/router/presets/apply', {
			method: 'POST',
			body: JSON.stringify({ id, outbound }),
		});
	}

	async singboxRouterListPolicies(): Promise<RouterPolicy[]> {
		return this.request<RouterPolicy[]>('/singbox/router/policies');
	}

	async singboxRouterCreatePolicy(description?: string): Promise<RouterPolicy> {
		return this.request<RouterPolicy>('/singbox/router/policies', {
			method: 'POST',
			body: JSON.stringify({ description: description ?? 'awgm-router' }),
		});
	}

	async singboxRouterPolicyDevices(policyName: string): Promise<RouterPolicyDevice[]> {
		return this.request<RouterPolicyDevice[]>(
			`/singbox/router/policy-devices?name=${encodeURIComponent(policyName)}`
		);
	}

	async singboxRouterPolicyBind(mac: string, policyName: string): Promise<void> {
		await this.request('/singbox/router/policy-devices/bind', {
			method: 'POST',
			body: JSON.stringify({ mac, policyName }),
		});
	}

	async singboxRouterPolicyUnbind(mac: string): Promise<void> {
		await this.request('/singbox/router/policy-devices/unbind', {
			method: 'POST',
			body: JSON.stringify({ mac }),
		});
	}

	async singboxRouterListDNSServers(): Promise<SingboxRouterDNSServer[]> {
		return this.request('/singbox/router/dns/servers/list');
	}

	async singboxRouterAddDNSServer(server: SingboxRouterDNSServer): Promise<void> {
		await this.request('/singbox/router/dns/servers/add', {
			method: 'POST',
			body: JSON.stringify(server),
		});
	}

	async singboxRouterUpdateDNSServer(tag: string, server: SingboxRouterDNSServer): Promise<void> {
		await this.request('/singbox/router/dns/servers/update', {
			method: 'POST',
			body: JSON.stringify({ tag, server }),
		});
	}

	async singboxRouterDeleteDNSServer(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/dns/servers/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterListDNSRules(): Promise<SingboxRouterDNSRule[]> {
		return this.request('/singbox/router/dns/rules/list');
	}

	async singboxRouterAddDNSRule(rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/router/dns/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxRouterUpdateDNSRule(index: number, rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/router/dns/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxRouterDeleteDNSRule(index: number): Promise<void> {
		await this.request('/singbox/router/dns/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveDNSRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/dns/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterGetDNSGlobals(): Promise<SingboxRouterDNSGlobals> {
		return this.request('/singbox/router/dns/globals');
	}

	async singboxRouterPutDNSGlobals(globals: SingboxRouterDNSGlobals): Promise<void> {
		await this.request('/singbox/router/dns/globals', {
			method: 'PUT',
			body: JSON.stringify(globals),
		});
	}

	// #endregion
}

export const api = new ApiClient();
