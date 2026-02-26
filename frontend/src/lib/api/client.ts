import type {
	AWGTunnel,
	TunnelListItem,
	TunnelStatus,
	IPResult,
	ConnectivityResult,
	SpeedTestResult,
	SpeedTestInfo,
	IPCheckService,
	SystemInfo,
	Settings,
	AuthStatus,
	LoginResult,
	PingCheckStatus,
	PingLogEntry,
	LogsResponse,
	WANInterface,
	WANStatus,
	ExternalTunnel,
	DeleteResult,
	BootStatus,
	UpdateInfo,
	Policy,
	HotspotClient,
	DiagnosticsStatus,
	KmodVersionsInfo
} from '$lib/types';

interface ApiResponse<T> {
	success?: boolean;
	error?: boolean;
	data?: T;
	message?: string;
	code?: string;
}

export class BootInitializingError extends Error {
	phase: string;
	remainingSeconds: number;

	constructor(phase: string, remainingSeconds: number) {
		super('Система инициализируется');
		this.name = 'BootInitializingError';
		this.phase = phase;
		this.remainingSeconds = remainingSeconds;
	}
}

class ApiClient {
	private baseUrl = '/api';
	private onUnauthorized?: () => void;
	private onBootInitializing?: () => void;
	private onConnectionLost?: () => void;
	private lastBootToast = 0;
	private abortController = new AbortController();

	setUnauthorizedHandler(handler: () => void) {
		this.onUnauthorized = handler;
	}

	setBootInitializingHandler(handler: () => void) {
		this.onBootInitializing = handler;
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

		// Handle 503 Boot Initializing
		if (response.status === 503) {
			try {
				const body = await response.json();
				if (body.code === 'BOOT_INITIALIZING') {
					const now = Date.now();
					if (now - this.lastBootToast > 10_000) {
						this.lastBootToast = now;
						this.onBootInitializing?.();
					}
					throw new BootInitializingError(body.phase, body.remainingSeconds);
				}
			} catch (e) {
				if (e instanceof BootInitializingError) throw e;
			}
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

	// Tunnels
	async listTunnels(): Promise<TunnelListItem[]> {
		return this.request('/tunnels/list');
	}

	async getTunnel(id: string): Promise<AWGTunnel> {
		return this.request(`/tunnels/get?id=${encodeURIComponent(id)}`);
	}

	async createTunnel(tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		return this.request('/tunnels/create', {
			method: 'POST',
			body: JSON.stringify(tunnel)
		});
	}

	async updateTunnel(id: string, tunnel: Partial<AWGTunnel>): Promise<AWGTunnel> {
		return this.request(`/tunnels/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(tunnel)
		});
	}

	async getTrafficHistory(id: string, period: string): Promise<{ t: number; rx: number; tx: number }[]> {
		return this.request(`/tunnels/traffic-history?id=${encodeURIComponent(id)}&period=${encodeURIComponent(period)}`);
	}

	async deleteTunnel(id: string): Promise<DeleteResult> {
		return this.request(`/tunnels/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	// Control
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

	async toggleEnabled(id: string): Promise<{ id: string; enabled: boolean }> {
		return this.request(`/control/toggle-enabled?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async toggleDefaultRoute(id: string): Promise<{ id: string; defaultRoute: boolean }> {
		return this.request(`/control/toggle-default-route?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	// Status
	async getStatus(id: string): Promise<TunnelStatus> {
		return this.request(`/status/get?id=${encodeURIComponent(id)}`);
	}

	async getAllStatus(): Promise<TunnelStatus[]> {
		return this.request('/status/all');
	}

	// Import
	async importConfig(content: string, name?: string): Promise<AWGTunnel> {
		return this.request('/import/conf', {
			method: 'POST',
			body: JSON.stringify({ content, name })
		});
	}

	// Testing
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

	// System
	async getSystemInfo(): Promise<SystemInfo> {
		return this.request('/system/info');
	}

	async getWANInterfaces(): Promise<WANInterface[]> {
		return this.request('/system/wan-interfaces');
	}

	async getWANStatus(): Promise<WANStatus> {
		return this.request('/wan/status');
	}

	// Updates
	async checkUpdate(force = false): Promise<UpdateInfo> {
		const query = force ? '?force=true' : '';
		return this.request(`/system/update/check${query}`);
	}

	async applyUpdate(): Promise<{ status: string }> {
		return this.request('/system/update/apply', { method: 'POST' });
	}

	// Settings
	async getSettings(): Promise<Settings> {
		return this.request('/settings/get');
	}

	async updateSettings(settings: Settings): Promise<Settings> {
		return this.request('/settings/update', {
			method: 'POST',
			body: JSON.stringify(settings)
		});
	}

	// Auth
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

	// Boot status (public, direct JSON response)
	async getBootStatus(): Promise<BootStatus> {
		const response = await fetch(`${this.baseUrl}/boot-status`);
		if (!response.ok) {
			throw new Error('Boot status unavailable');
		}
		return response.json();
	}

	// Ping Check
	async getPingCheckStatus(): Promise<PingCheckStatus> {
		return this.request('/pingcheck/status');
	}

	async getPingCheckLogs(tunnelId?: string): Promise<PingLogEntry[]> {
		const params = tunnelId ? `?tunnelId=${encodeURIComponent(tunnelId)}` : '';
		return this.request(`/pingcheck/logs${params}`);
	}

	async triggerPingCheck(): Promise<{ message: string }> {
		return this.request('/pingcheck/check-now', { method: 'POST' });
	}

	async clearPingCheckLogs(): Promise<{ message: string }> {
		return this.request('/pingcheck/logs/clear', { method: 'POST' });
	}

	// Logging
	async getLogs(category?: string, level?: string): Promise<LogsResponse> {
		const params = new URLSearchParams();
		if (category) params.set('category', category);
		if (level) params.set('level', level);
		const queryString = params.toString();
		return this.request(`/logs${queryString ? '?' + queryString : ''}`);
	}

	async clearLogs(): Promise<void> {
		await this.request('/logs/clear', { method: 'POST' });
	}

	// System actions
	async changeBackend(mode: 'auto' | 'kernel' | 'userspace'): Promise<void> {
		await this.request('/system/change-backend', {
			method: 'POST',
			body: JSON.stringify({ mode })
		});
	}

	async downloadKmod(): Promise<void> {
		await this.request('/system/kmod/download', { method: 'POST' });
	}

	async getKmodVersions(): Promise<KmodVersionsInfo> {
		return this.request('/system/kmod/versions');
	}

	async swapKmod(version: string): Promise<void> {
		await this.request('/system/kmod/swap', {
			method: 'POST',
			body: JSON.stringify({ version })
		});
	}

	// External Tunnels
	async listExternalTunnels(): Promise<ExternalTunnel[]> {
		return this.request('/external-tunnels');
	}

	async adoptExternalTunnel(interfaceName: string, content: string, name?: string): Promise<AWGTunnel> {
		return this.request(`/external-tunnels/adopt?interface=${encodeURIComponent(interfaceName)}`, {
			method: 'POST',
			body: JSON.stringify({ content, name })
		});
	}

	// Policies
	async listPolicies(): Promise<Policy[]> {
		return this.request('/policies/list');
	}

	async createPolicy(p: Partial<Policy>): Promise<Policy> {
		return this.request('/policies/create', {
			method: 'POST',
			body: JSON.stringify(p)
		});
	}

	async updatePolicy(p: Policy): Promise<Policy> {
		return this.request('/policies/update', {
			method: 'POST',
			body: JSON.stringify(p)
		});
	}

	async deletePolicy(id: string): Promise<void> {
		return this.request(`/policies/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async getHotspotClients(): Promise<HotspotClient[]> {
		return this.request('/hotspot');
	}

	// Diagnostics
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
}

export const api = new ApiClient();
