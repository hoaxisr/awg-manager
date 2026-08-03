import type {
	AuthStatus,
	BootStatus,
	ChangelogEntry,
	ConnectionsResponse,
	ConnectivityResult,
	DiagEvent,
	DiagnosticsStatus,
	DnsCheckStartResponse,
	DnsProxyInfo,
	DownloadOutbound,
	DownloadRoute,
	GeoFileEntry,
	GeoTag,
	HydraRouteConfig,
	HydraRouteOversizedResponse,
	HydraRouteStatus,
	IPCheckService,
	IPResult,
	IpsetUsage,
	LoginResult,
	LogsResponse,
	MonitoringSnapshot,
	RouterInterface,
	ServerListenChangeResult,
	ServerListenState,
	Settings,
	SignatureCaptureResult,
	SignatureGenerateResult,
	SpeedTestInfo,
	SpeedTestResult,
	SystemInfo,
	TerminalStatus,
	UpdateInfo,
	WANInterface,
	WANStatus
} from '$lib/types';
import { readDiagnosticsSanitizedPreference } from './clientCore';
import { TunnelsClient } from './clientTunnels';

export class SystemClient extends TunnelsClient {
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

	// ── HTTP-listen (живая смена порта/интерфейсов веб-интерфейса) ──

	async serverListenState(): Promise<ServerListenState> {
		return this.request('/server/listen');
	}

	async serverListenChange(port: number, interfaces: string[]): Promise<ServerListenChangeResult> {
		return this.request('/server/listen/change', {
			method: 'POST',
			body: JSON.stringify({ port, interfaces }),
		});
	}

	async serverListenConfirm(token: string): Promise<void> {
		await this.request('/server/listen/confirm', {
			method: 'POST',
			body: JSON.stringify({ token }),
		});
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

		let data: (LoginResult & { error?: unknown; message?: string }) | null = null;
		try {
			data = await response.json();
		} catch {
			// Non-JSON body (gateway page etc.) — fall through to status-based message.
		}
		if (!response.ok || !data || data.error) {
			// Показываем сообщение бэкенда как есть (401 «неверный логин…»,
			// 429 «слишком много попыток…»), не подменяя его generic-текстом.
			const fallback =
				response.status === 429
					? 'Слишком много попыток входа. Попробуйте позже.'
					: 'Ошибка авторизации';
			throw new Error(data?.message || fallback);
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

	async generateSignature(protocol: string, mtu?: number): Promise<SignatureGenerateResult> {
		return this.request('/signature/generate', {
			method: 'POST',
			body: JSON.stringify(mtu ? { protocol, mtu } : { protocol }),
		});
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Connections — conntrack viewer
	// ─────────────────────────────────────────────

	async getConnections(params: {
		tunnel?: string;
		protocol?: string;
		state?: string;
		search?: string;
		offset?: number;
		limit?: number;
		sortBy?: 'proto' | 'src' | 'dst' | 'iface' | 'state' | 'bytes';
		sortDir?: 'asc' | 'desc';
	} = {}): Promise<ConnectionsResponse> {
		const sp = new URLSearchParams();
		if (params.tunnel && params.tunnel !== 'all') sp.set('tunnel', params.tunnel);
		if (params.protocol && params.protocol !== 'all') sp.set('protocol', params.protocol);
		if (params.state && params.state !== 'all') sp.set('state', params.state);
		if (params.search) sp.set('search', params.search);
		if (params.offset) sp.set('offset', String(params.offset));
		if (params.limit) sp.set('limit', String(params.limit));
		if (params.sortBy) sp.set('sortBy', params.sortBy);
		if (params.sortDir) sp.set('sortDir', params.sortDir);
		const qs = sp.toString();
		return this.request<ConnectionsResponse>(`/connections${qs ? '?' + qs : ''}`);
	}

	/** Сброс conntrack-записи. true = удалено (или уже истекло), false = ошибка. */
	async killConnection(c: {
		src: string;
		dst: string;
		srcPort: number;
		dstPort: number;
		protocol: string;
	}): Promise<boolean> {
		const sp = new URLSearchParams({
			src: c.src,
			dst: c.dst,
			srcPort: String(c.srcPort),
			dstPort: String(c.dstPort),
			protocol: c.protocol,
		});
		try {
			await this.request(`/connections?${sp}`, { method: 'DELETE' });
			return true;
		} catch {
			return false;
		}
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
	// #region Monitoring (Phase 3)
	// ─────────────────────────────────────────────

	async getMonitoringMatrix(opts?: { force?: boolean }): Promise<MonitoringSnapshot> {
		const path = opts?.force ? '/monitoring/matrix?force=1' : '/monitoring/matrix';
		return this.request<MonitoringSnapshot>(path);
	}

	async exportFullBackup(): Promise<Blob> {
		const res = await fetch(`${this.baseUrl}/system/backup/export`, {
			credentials: 'same-origin',
			signal: this.abortController.signal
		});
		if (res.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}
		const contentType = res.headers.get('content-type') || '';
		if (!res.ok) {
			if (contentType.includes('application/json')) {
				const body = (await res.json()) as { message?: string };
				throw new Error(body.message || `Ошибка экспорта (${res.status})`);
			}
			throw new Error(`Ошибка экспорта (${res.status})`);
		}
		return res.blob();
	}

	async importFullBackup(file: File): Promise<{ message: string }> {
		const form = new FormData();
		form.append('file', file);
		const res = await fetch(`${this.baseUrl}/system/backup/import`, {
			method: 'POST',
			credentials: 'same-origin',
			body: form,
			signal: this.abortController.signal
		});
		if (res.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}
		const contentType = res.headers.get('content-type') || '';
		if (!contentType.includes('application/json')) {
			throw new Error(`Неожиданный ответ сервера (${res.status})`);
		}
		const body = (await res.json()) as { success?: boolean; message?: string; data?: { message?: string } };
		if (!res.ok || body.success === false) {
			throw new Error(body.message || 'Не удалось восстановить резервную копию');
		}
		return { message: body.data?.message || body.message || 'OK' };
	}

	// #endregion


}
