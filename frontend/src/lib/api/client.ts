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
	DownloadRoute,
	DownloadOutbound,
	GeoTag,
	HydraRouteOversizedResponse,
	IpsetUsage,
	DnsCheckStartResponse,
	PolicyDevice,
	SingboxTunnel,
	SingboxStatus,
	SingboxImportResponse,
	SingboxConfigPreview,
	DeviceProxyConfig,
	DeviceProxyInstance,
	DeviceProxyOutbound,
	DeviceProxyRuntime,
	DeviceProxyInstanceIPCheckResult,
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
	SingboxRouterWANInterface,
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
	SingboxRouterDNSRewrite,
	SingboxRouterInspectRequest,
	SingboxRouterInspectResult,
	SingboxRouterInspectProgress,
	SingboxProxiesListResponse,
	SingboxProxiesSelectRequest,
	SingboxProxiesTestRequest,
	SingboxProxiesTestResponse,
	Subscription,
	SubscriptionHeader,
	SubscriptionRefreshResult,
	SubscriptionActiveNowResponse,
	CreateSubscriptionInput,
	UpdateSubscriptionInput,
	RouterStagingStatusResponse,
	AmneziaPremiumAccountInfo,
	ManagedServerBackupFile,
	ManagedServerDriftResponse,
	ManagedServerRestoreResponse,
	RestoreOptions,
	DnsProxyInfo,
	CatalogPreset,
} from '$lib/types';
import { isMockDevMode } from '$lib/env';

export type TrafficPeriod = '5m' | '10m' | '30m' | '1h' | '3h' | '6h' | '12h' | '24h';

const DIAGNOSTICS_SANITIZE_STORAGE_KEY = 'awgm.diagnostics.sanitizeLogs';

function readDiagnosticsSanitizedPreference(): boolean {
	if (typeof localStorage === 'undefined') {
		return true;
	}
	// Missing key means safe default. The diagnostics privacy store persists
	// enabled as "1" and disabled/raw reveal as "0".
	return localStorage.getItem(DIAGNOSTICS_SANITIZE_STORAGE_KEY) !== '0';
}

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
			const err: Error & { status?: number; body?: unknown } = new Error(
				data.message || `Ошибка запроса (${response.status})`
			);
			err.status = response.status;
			err.body = data;
			throw err;
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
		period: TrafficPeriod
	): Promise<{
		points: { t: number; rx: number; tx: number }[];
		stats: {
			points: number;
			peakRate: number;
			avgRx: number;
			avgTx: number;
			currentRx: number;
			currentTx: number;
			volumeRx?: number;
			volumeTx?: number;
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
				routerRules: [],
				routerOther: []
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

	async amneziaPremiumLogin(vpnKey: string): Promise<{ sid: string }> {
		return this.request('/amnezia-premium/login', {
			method: 'POST',
			body: JSON.stringify({ vpnKey: vpnKey.trim(), remember: true })
		});
	}

	async amneziaPremiumAccountInfo(sid: string): Promise<AmneziaPremiumAccountInfo> {
		return this.request('/amnezia-premium/account-info', {
			method: 'POST',
			body: JSON.stringify({ sid })
		});
	}

	async amneziaPremiumDownloadConfig(
		sid: string,
		countryCode: string
	): Promise<{ config: string }> {
		return this.request('/amnezia-premium/download-config', {
			method: 'POST',
			body: JSON.stringify({ sid, countryCode })
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

	async listDownloadOutbounds(): Promise<DownloadOutbound[]> {
		return this.request('/download/outbounds');
	}

	async addGeoFile(type: 'geosite' | 'geoip', url: string, route?: DownloadRoute): Promise<GeoFileEntry> {
		return this.request('/hydraroute/geo-files/add', {
			method: 'POST',
			body: JSON.stringify({ type, url, route }),
		});
	}

	async deleteGeoFile(path: string): Promise<void> {
		await this.request(`/hydraroute/geo-files/delete?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
	}

	async updateGeoFile(path?: string, route?: DownloadRoute): Promise<{ updated: number; partial?: boolean; error?: string }> {
		return this.request('/hydraroute/geo-files/update', {
			method: 'POST',
			body: JSON.stringify({ path: path || '', route }),
		});
	}

	async takeGeoFileControl(path: string): Promise<GeoFileEntry> {
		return this.request('/hydraroute/geo-files/take-control', {
			method: 'POST',
			body: JSON.stringify({ path }),
		});
	}

	async rescanGeoFiles(): Promise<{ adopted: number }> {
		return this.request('/hydraroute/geo-files/rescan', { method: 'POST' });
	}

	async getGeoTags(path: string): Promise<GeoTag[]> {
		return this.request(`/hydraroute/geo-tags?path=${encodeURIComponent(path)}`);
	}

	async expandGeoTag(
		kind: 'geosite' | 'geoip',
		tag: string,
	): Promise<{ lines: string[]; path: string; count: number }> {
		const q = new URLSearchParams({ kind, tag });
		return this.request(`/hydraroute/geo-expand?${q.toString()}`);
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
		// Omit `from` for the current minor line up to `to` (2.11.0…2.11.2 on 2.11.2+r70).
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

	async updateSettings(settings: Partial<Settings>): Promise<Settings> {
		const updated = await this.request<Settings>('/settings/update', {
			method: 'POST',
			body: JSON.stringify(settings)
		});
		// Prism mock is stateless: it often returns schema examples instead of
		// echoing persisted values. In mock-dev mode keep UI controls usable by
		// merging the patch into current settings snapshot.
		if (this.isMockDevMode()) {
			const current = await this.getSettings().catch(() => ({} as Settings));
			return { ...current, ...settings };
		}
		return updated;
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
		bucket?: 'app' | 'singbox';
		group?: string;
		subgroup?: string;
		groups?: string[];
		subgroups?: string[];
		level?: string;
		since?: number;
		limit?: number;
		sanitize?: boolean;
		offset?: number;
	}): Promise<LogsResponse> {
		const query = new URLSearchParams();
		if (params?.bucket) query.set('bucket', params.bucket);
		if (params?.group) query.append('group', params.group);
		for (const g of params?.groups ?? []) {
			if (g) query.append('group', g);
		}
		if (params?.subgroup) query.append('subgroup', params.subgroup);
		for (const s of params?.subgroups ?? []) {
			if (s) query.append('subgroup', s);
		}
		if (params?.level) query.set('level', params.level);
		if (params?.since != null && params.since > 0) query.set('since', String(params.since));
		if (params?.limit) query.set('limit', String(params.limit));
		const sanitize = params?.sanitize ?? readDiagnosticsSanitizedPreference();
		query.set('sanitize', String(sanitize));
		if (params?.offset != null && params.offset >= 0) query.set('offset', String(params.offset));
		const qs = query.toString();
		return this.request(`/logs${qs ? '?' + qs : ''}`);
	}

	async clearLogs(bucket: 'app' | 'singbox' = 'app'): Promise<void> {
		await this.request(`/logs/clear?bucket=${bucket}`, { method: 'POST' });
	}

	async getLogsSubgroups(group: string): Promise<{ group: string; subgroups: string[] }> {
		return this.request(`/logs/subgroups?group=${encodeURIComponent(group)}`);
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

	async restartManagedServer(serverId: string): Promise<{ id: string; accepted: boolean }> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/restart`, {
			method: 'POST'
		});
	}

	async restartWireguardServer(name: string): Promise<{ id: string; accepted: boolean }> {
		return this.request(`/servers/restart?name=${encodeURIComponent(name)}`, {
			method: 'POST'
		});
	}

	async setWireguardServerEnabled(
		name: string,
		enabled: boolean
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/enabled?name=${encodeURIComponent(name)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
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

	async downloadDiagnosticsReport(environment?: unknown): Promise<void> {
		const response = await fetch('/api/diagnostics/result', { credentials: 'same-origin' });
		if (!response.ok) throw new Error('Report not available');
		const filename = response.headers.get('Content-Disposition')
			?.match(/filename="(.+)"/)?.[1] || 'diagnostics.json';
		const text = await response.text();
		let payloadText = text;
		try {
			const report = JSON.parse(text);
			const merged = environment ? { ...report, environment } : report;
			payloadText = JSON.stringify(merged, null, 2);
		} catch {
			// keep original payload text if parsing fails
		}
		const blob = new Blob([payloadText], {
			type: 'application/json;charset=utf-8'
		});
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		a.click();
		URL.revokeObjectURL(url);
	}

	streamDiagnostics(
		restart: boolean,
		onEvent: (event: DiagEvent) => void,
		onError: (error: Event) => void,
		tunnelId?: string,
	): EventSource {
		const params = new URLSearchParams({ restart: String(restart) });
		if (tunnelId) params.set('tunnelId', tunnelId);
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
		es.addEventListener('error', (e: Event) => {
			// Named SSE event `error` carries JSON in MessageEvent.data; connection faults do not.
			if (e instanceof MessageEvent && typeof e.data === 'string' && e.data.length > 0) {
				handleEvent(e);
				return;
			}
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

	async setManagedServerNATMode(serverId: string, mode: 'full' | 'internet-only' | 'none'): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/nat`, {
			method: 'POST',
			body: JSON.stringify({ mode })
		});
	}

	async setManagedServerLANSegments(serverId: string, segments: string[]): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/lan-segments`, {
			method: 'POST',
			body: JSON.stringify({ segments })
		});
	}

	async listManagedLANSegments(): Promise<{ name: string; label: string; subnet: string }[]> {
		return this.request('/managed-servers/lan-segments');
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

	async listPolicyDevices(): Promise<PolicyDevice[]> {
		return this.request<PolicyDevice[]>('/routing/policy-devices');
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

	/** Client IP, hostname, and policy only — no full DNS diagnostic suite. */
	async getDnsCheckClient(): Promise<DnsCheckStartResponse> {
		return this.request('/dns-check/client');
	}

	async getDnsProxyInfo(): Promise<DnsProxyInfo> {
		return this.request('/diagnostics/dns-proxy');
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Sing-box
	// ─────────────────────────────────────────────

	async singboxGetStatus(): Promise<SingboxStatus> {
		return this.request('/singbox/status');
	}

	async singboxGetClientsByIP(): Promise<{ clientsByIP: Record<string, string> }> {
		return this.request('/singbox/connections/clients');
	}

	// Kill a single sing-box connection by Clash UUID. Bypasses request()
	// because ClashProxy returns 204 with no JSON envelope. Returns true on
	// success so callers can decide whether to roll back optimistic UI.
	async singboxKillConnection(id: string): Promise<boolean> {
		const url = `${this.baseUrl}/singbox/clash/connections/${encodeURIComponent(id)}`;
		try {
			const r = await fetch(url, {
				method: 'DELETE',
				credentials: 'same-origin',
				signal: this.abortController.signal,
			});
			return r.ok;
		} catch {
			return false;
		}
	}

	// Bulk-kill: returns counts so the caller can surface partial failure.
	async singboxKillConnections(ids: string[]): Promise<{ ok: number; total: number }> {
		const results = await Promise.all(ids.map((id) => this.singboxKillConnection(id)));
		const ok = results.filter(Boolean).length;
		return { ok, total: ids.length };
	}

	async singboxInstall(): Promise<SingboxStatus> {
		return this.request('/singbox/install', { method: 'POST' });
	}

	async singboxUpdate(): Promise<SingboxStatus> {
		return this.request('/singbox/update', { method: 'POST' });
	}

	async singboxControl(action: 'start' | 'stop' | 'restart'): Promise<SingboxStatus> {
		return this.request('/singbox/control', {
			method: 'POST',
			body: JSON.stringify({ action }),
		});
	}

	async singboxToggleNDMSProxy(enabled: boolean): Promise<{ enabled: boolean; migrated: boolean }> {
		return this.request('/singbox/ndms-proxy', {
			method: 'POST',
			body: JSON.stringify({ enabled }),
		});
	}

	private isMockDevMode(): boolean {
		return isMockDevMode();
	}

	private ensureMockSubscriptionMembers(sub: Subscription): Subscription {
		if (!this.isMockDevMode()) return sub;
		const baseMembers = Array.isArray(sub.members) ? [...sub.members] : [];
		const normalized: Subscription = {
			...sub,
			id: sub.id || 'sub-demo',
			label: sub.label || 'Demo Provider',
			selectorTag: sub.selectorTag || 'sub-demo',
			inboundTag: sub.inboundTag || 'sub-demo-in',
			listenPort: sub.listenPort || 11000,
			enabled: sub.enabled ?? true,
			lastError: '',
		};
		if (baseMembers.length >= 3) {
			const memberTags = baseMembers.map((m) => m.tag).filter(Boolean);
			const activeMember = normalized.activeMember && memberTags.includes(normalized.activeMember)
				? normalized.activeMember
				: memberTags[0] || '';
			return {
				...normalized,
				memberTags,
				members: baseMembers,
				activeMember,
				enabled: normalized.enabled,
			};
		}

		const seed = baseMembers[0] ?? {
			tag: `${normalized.selectorTag || 'sub-demo'}-001`,
			label: 'DE vless-tcp-reality #1',
			protocol: 'vless',
			server: 'demo-1.example.com',
			port: 443,
			sni: 'cdn.example.com',
			transport: 'tcp',
			security: 'reality',
		};

		for (let i = baseMembers.length; i < 3; i++) {
			const n = i + 1;
			const tag = `${normalized.selectorTag || 'sub-demo'}-${String(n).padStart(3, '0')}`;
			baseMembers.push({
				...seed,
				tag,
				label: `DE vless-tcp-reality #${n}`,
				server: `demo-${n}.example.com`,
				port: 443 + i,
			});
		}

		const memberTags = baseMembers.map((m) => m.tag).filter(Boolean);
		const activeMember = normalized.activeMember && memberTags.includes(normalized.activeMember)
			? normalized.activeMember
			: memberTags[0] || '';

		return {
			...normalized,
			memberTags,
			members: baseMembers,
			activeMember,
			enabled: normalized.enabled,
		};
	}

	async singboxGetConfigPreview(): Promise<SingboxConfigPreview> {
		return this.request<SingboxConfigPreview>('/singbox/config-preview');
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
		const isMockDev = this.isMockDevMode();
		try {
			const raw = await this.request<unknown>(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`);
			// Normal backend shape.
			if (raw && typeof raw === 'object' && 'outbound' in raw && 'tag' in raw) {
				const obj = raw as { tag: string; outbound: unknown };
				if (obj.outbound) return obj;
			}
			// Prism may return a SingboxTunnel-like item directly instead of {tag,outbound}.
			if (isMockDev && raw && typeof raw === 'object' && 'tag' in raw) {
				const t = raw as SingboxTunnel;
				return { tag: t.tag, outbound: this.buildMockOutboundFromTunnel(t) };
			}
		} catch (err) {
			if (!isMockDev) throw err;
		}

		if (isMockDev) {
			const tunnels = await this.singboxListTunnels();
			const found = tunnels.find((t) => t.tag === tag) ?? tunnels[0];
			if (found) {
				return { tag: found.tag, outbound: this.buildMockOutboundFromTunnel(found) };
			}
		}
		throw new Error('Туннель не найден');
	}

	private buildMockOutboundFromTunnel(t: SingboxTunnel): Record<string, unknown> {
		const outbound: Record<string, unknown> = {
			type: t.protocol,
			tag: t.tag,
			server: t.server,
			server_port: t.port,
		};

		if (t.protocol === 'vless') {
			const tls: Record<string, unknown> = {};
			if (t.sni) tls.server_name = t.sni;
			if (t.fingerprint) tls.utls = { enabled: true, fingerprint: t.fingerprint };
			if (t.security === 'reality') {
				tls.enabled = true;
				tls.reality = { enabled: true, public_key: 'EXAMPLE_PUBLIC_KEY', short_id: 'abcd1234' };
			} else if (t.security === 'tls') {
				tls.enabled = true;
			}
			outbound.transport = { type: t.transport || 'tcp' };
			if (Object.keys(tls).length > 0) outbound.tls = tls;
		} else if (t.protocol === 'mieru') {
			outbound.transport = (t.transport || 'TCP').toUpperCase();
			outbound.username = t.username || '';
			outbound.password = '';
		}

		return outbound;
	}

	async singboxUpdateTunnel(tag: string, outbound: unknown): Promise<SingboxTunnel[]> {
		return this.request(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`, {
			method: 'PUT',
			body: JSON.stringify({ outbound })
		});
	}

	async singboxRenameTunnel(oldTag: string, newTag: string): Promise<SingboxTunnel[]> {
		return this.request('/singbox/tunnels/rename', {
			method: 'PATCH',
			body: JSON.stringify({ oldTag, newTag })
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

	async singboxCheckConnectivity(tag: string, iface?: string): Promise<ConnectivityResult> {
		let url = `/singbox/tunnels/test/connectivity?tag=${encodeURIComponent(tag)}`;
		if (iface) url += `&iface=${encodeURIComponent(iface)}`;
		return this.request(url);
	}

	async singboxCheckIP(tag: string, serviceURL?: string, iface?: string): Promise<IPResult> {
		let url = `/singbox/tunnels/test/ip?tag=${encodeURIComponent(tag)}`;
		if (serviceURL) url += `&service=${encodeURIComponent(serviceURL)}`;
		if (iface) url += `&iface=${encodeURIComponent(iface)}`;
		return this.request(url);
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
		iface?: string,
	): EventSource {
		const ifaceParam = iface ? `&iface=${encodeURIComponent(iface)}` : '';
		const url = `${this.baseUrl}/singbox/tunnels/test/speed/stream?tag=${encodeURIComponent(tag)}&server=${encodeURIComponent(server)}&port=${port}${ifaceParam}`;
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

	// ─────────────────────────────────────────────
	// #region Device Proxy — multi-instance
	// ─────────────────────────────────────────────

	async listDeviceProxyInstances(): Promise<DeviceProxyInstance[]> {
		return this.request<DeviceProxyInstance[]>('/proxy/instances');
	}

	async getDeviceProxyInstance(id: string): Promise<DeviceProxyInstance> {
		return this.request<DeviceProxyInstance>(`/proxy/instance?id=${encodeURIComponent(id)}`);
	}

	async saveDeviceProxyInstance(instance: DeviceProxyInstance): Promise<DeviceProxyInstance> {
		return this.request<DeviceProxyInstance>('/proxy/instance', {
			method: 'PUT',
			body: JSON.stringify(instance)
		});
	}

	async deleteDeviceProxyInstance(id: string): Promise<{ deleted: boolean }> {
		return this.request<{ deleted: boolean }>(`/proxy/instance?id=${encodeURIComponent(id)}`, {
			method: 'DELETE'
		});
	}

	async applyDeviceProxyInstances(): Promise<{ applied: boolean }> {
		return this.request<{ applied: boolean }>('/proxy/instances/apply', {
			method: 'POST'
		});
	}

	async getDeviceProxyInstanceRuntime(id: string): Promise<DeviceProxyRuntime> {
		return this.request<DeviceProxyRuntime>(`/proxy/instance/runtime?id=${encodeURIComponent(id)}`);
	}

	async selectDeviceProxyInstanceRuntime(id: string, tag: string): Promise<{ active: string }> {
		return this.request<{ active: string }>(`/proxy/instance/runtime/select?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ tag })
		});
	}

	async checkDeviceProxyInstanceExternalIP(
		id: string,
		serviceURL?: string
	): Promise<DeviceProxyInstanceIPCheckResult> {
		let endpoint = `/proxy/instance/check-ip?id=${encodeURIComponent(id)}`;
		if (serviceURL) endpoint += `&service=${encodeURIComponent(serviceURL)}`;
		return this.request<DeviceProxyInstanceIPCheckResult>(endpoint);
	}

	// #endregion

	// #endregion

	// ─────────────────────────────────────────────
	// #region Monitoring (Phase 3)
	// ─────────────────────────────────────────────

	async getMonitoringMatrix(opts?: { force?: boolean }): Promise<MonitoringSnapshot> {
		const path = opts?.force ? '/monitoring/matrix?force=1' : '/monitoring/matrix';
		return this.request<MonitoringSnapshot>(path);
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

	async singboxRouterDatRuleSetURL(kind: 'geosite' | 'geoip', tags: string[]): Promise<{ url: string }> {
		const q = new URLSearchParams({ kind });
		for (const t of tags) {
			q.append('tag', t);
		}
		return this.request(`/singbox/router/rulesets/dat-url?${q.toString()}`);
	}

	async singboxRouterAddRuleSet(rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/router/rulesets/add', {
			method: 'POST',
			body: JSON.stringify(rs),
		});
	}

	async singboxRouterUpdateRuleSet(tag: string, rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/router/rulesets/update', {
			method: 'POST',
			body: JSON.stringify({ tag, ruleSet: rs }),
		});
	}

	async singboxRouterDeleteRuleSet(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/rulesets/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
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

	async singboxRouterListProxies(): Promise<SingboxProxiesListResponse> {
		return this.request<SingboxProxiesListResponse>('/singbox/router/proxies/list');
	}

	async singboxRouterSelectProxy(req: SingboxProxiesSelectRequest): Promise<void> {
		await this.request<unknown>('/singbox/router/proxies/select', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async singboxRouterTestProxy(req: SingboxProxiesTestRequest): Promise<SingboxProxiesTestResponse> {
		return this.request<SingboxProxiesTestResponse>('/singbox/router/proxies/test', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async singboxRouterListPresets(): Promise<SingboxRouterPreset[]> {
		return this.request('/singbox/router/presets/list');
	}

	async listPresets(): Promise<{ presets: CatalogPreset[] }> {
		const payload = await this.request<{ presets?: CatalogPreset[] } | undefined>('/presets');
		return {
			presets: Array.isArray(payload?.presets) ? payload.presets : [],
		};
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

	async singboxRouterListWANInterfaces(): Promise<SingboxRouterWANInterface[]> {
		return this.request<SingboxRouterWANInterface[]>('/singbox/router/wan-interfaces');
	}

	async singboxRouterListBindableInterfaces(): Promise<SingboxRouterWANInterface[]> {
		return this.request<SingboxRouterWANInterface[]>('/singbox/router/bindable-interfaces');
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

	async singboxRouterListDNSRewrites(): Promise<SingboxRouterDNSRewrite[]> {
		return this.request('/singbox/router/dns/rewrites/list');
	}

	async singboxRouterAddDNSRewrite(rewrite: SingboxRouterDNSRewrite): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/add', {
			method: 'POST',
			body: JSON.stringify(rewrite),
		});
	}

	async singboxRouterUpdateDNSRewrite(index: number, rewrite: SingboxRouterDNSRewrite): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/update', {
			method: 'POST',
			body: JSON.stringify({ index, rewrite }),
		});
	}

	async singboxRouterDeleteDNSRewrite(index: number): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveDNSRewrite(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/move', {
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

	async singboxRouterPutRouteFinal(final: string): Promise<void> {
		await this.request('/singbox/router/route/final', {
			method: 'POST',
			body: JSON.stringify({ final }),
		});
	}

	async singboxRouterInspectRoute(
		req: SingboxRouterInspectRequest,
	): Promise<SingboxRouterInspectResult> {
		return this.request('/singbox/router/inspect', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	singboxRouterInspectRouteStream(
		req: SingboxRouterInspectRequest,
		handlers: {
			onProgress: (progress: SingboxRouterInspectProgress) => void;
			onResult: (result: SingboxRouterInspectResult) => void;
			onInspectError: (message: string) => void;
			onError: (message: string) => void;
		},
	): EventSource {
		const qs = new URLSearchParams();
		qs.set('domain', req.domain);
		if (typeof req.port === 'number') qs.set('port', String(req.port));
		if (req.protocol) qs.set('protocol', req.protocol);
		const es = new EventSource(`${this.baseUrl}/singbox/router/inspect/stream?${qs.toString()}`);
		es.addEventListener('progress', (e) => {
			try {
				const payload = JSON.parse((e as MessageEvent).data);
				if (payload?.progress) handlers.onProgress(payload.progress as SingboxRouterInspectProgress);
			} catch {}
		});
		es.addEventListener('result', (e) => {
			try {
				const payload = JSON.parse((e as MessageEvent).data);
				if (payload?.result) handlers.onResult(payload.result as SingboxRouterInspectResult);
			} catch (err) {
				handlers.onError(err instanceof Error ? err.message : 'Invalid stream result');
			}
			es.close();
		});
		es.addEventListener('inspect-error', (e) => {
			try {
				const payload = JSON.parse((e as MessageEvent).data);
				handlers.onInspectError(String(payload?.error ?? 'Inspect failed'));
			} catch {
				handlers.onInspectError('Inspect failed');
			}
			es.close();
		});
		es.addEventListener('error', () => {
			handlers.onError('Stream connection lost');
			es.close();
		});
		return es;
	}

	async singboxRouterStagingStatus(): Promise<RouterStagingStatusResponse> {
		return this.request('/singbox/router/staging');
	}

	async singboxRouterStagingApply(): Promise<void> {
		await this.request('/singbox/router/staging/apply', {
			method: 'POST',
		});
	}

	async singboxRouterStagingDiscard(): Promise<void> {
		await this.request('/singbox/router/staging/discard', {
			method: 'POST',
		});
	}

	// #endregion

	// #region Subscriptions

	async listSubscriptions(): Promise<Subscription[]> {
		const subs = await this.request<Subscription[]>('/singbox/subscriptions');
		return this.isMockDevMode() ? subs.map((s) => this.ensureMockSubscriptionMembers(s)) : subs;
	}

	async createSubscription(in_: CreateSubscriptionInput): Promise<Subscription> {
		return this.request<Subscription>('/singbox/subscriptions/create', {
			method: 'POST',
			body: JSON.stringify(in_),
		});
	}

	async getSubscription(id: string): Promise<Subscription> {
		const sub = await this.request<Subscription>(
			`/singbox/subscriptions/get?id=${encodeURIComponent(id)}`,
		);
		return this.ensureMockSubscriptionMembers(sub);
	}

	async updateSubscription(
		id: string,
		patch: UpdateSubscriptionInput,
	): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/update?id=${encodeURIComponent(id)}`,
			{
				method: 'PUT',
				body: JSON.stringify(patch),
			},
		);
	}

	async deleteSubscription(id: string): Promise<void> {
		const url = `/singbox/subscriptions/delete?id=${encodeURIComponent(id)}`;
		await this.request(url, { method: 'DELETE' });
	}

	async refreshSubscription(id: string): Promise<SubscriptionRefreshResult> {
		return this.request<SubscriptionRefreshResult>(
			`/singbox/subscriptions/refresh?id=${encodeURIComponent(id)}`,
			{ method: 'POST' },
		);
	}

	async getSubscriptionActiveNow(id: string): Promise<SubscriptionActiveNowResponse> {
		return this.request<SubscriptionActiveNowResponse>(
			`/singbox/subscriptions/active-now?id=${encodeURIComponent(id)}`,
		);
	}

	async setSubscriptionActiveMember(id: string, memberTag: string): Promise<void> {
		await this.request(
			`/singbox/subscriptions/active-member?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ memberTag }),
			},
		);
	}

	async moveSubscriptionRejectedToInfo(id: string, memberTag: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/rejected/to-info?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ memberTag }) },
		);
	}

	async removeSubscriptionInfoItem(id: string, itemId: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/info/remove?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ itemId }) },
		);
	}

	async deleteSubscriptionOrphans(id: string): Promise<void> {
		await this.request(
			`/singbox/subscriptions/orphans/delete?id=${encodeURIComponent(id)}`,
			{ method: 'POST' },
		);
	}

	async addSubscriptionMember(id: string, shareLink: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/members/add?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ shareLink }),
			},
		);
	}

	/**
	 * Remove one member from an inline subscription. Returns the updated
	 * subscription, or null when removing the last member tore down the
	 * whole subscription (the caller should navigate away in that case).
	 */
	async removeSubscriptionMember(id: string, memberTag: string): Promise<Subscription | null> {
		const data = await this.request<{ deleted: boolean; subscription?: Subscription }>(
			`/singbox/subscriptions/members/remove?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ memberTag }),
			},
		);
		return data.deleted ? null : (data.subscription ?? null);
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Managed Server Backup / Restore
	// ─────────────────────────────────────────────

	async managedServerExport(): Promise<ManagedServerBackupFile> {
		return this.request<ManagedServerBackupFile>('/managed/export');
	}

	async managedServerImport(
		payload: ManagedServerBackupFile & { options: RestoreOptions },
	): Promise<ManagedServerRestoreResponse> {
		return this.request<ManagedServerRestoreResponse>('/managed/import', {
			method: 'POST',
			body: JSON.stringify(payload),
		});
	}

	async managedServerDrift(): Promise<ManagedServerDriftResponse> {
		return this.request<ManagedServerDriftResponse>('/managed/drift');
	}

	async managedServerRestoreDrift(opts: RestoreOptions): Promise<ManagedServerRestoreResponse> {
		return this.request<ManagedServerRestoreResponse>('/managed/restore-drift', {
			method: 'POST',
			body: JSON.stringify({ options: opts }),
		});
	}

	// #endregion
}

export const api = new ApiClient();
