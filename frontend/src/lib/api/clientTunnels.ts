import type {
	ASCParams,
	AWGTagInfo,
	AWGTunnel,
	AmneziaPremiumAccountInfo,
	ConnectivityResult,
	DeleteResult,
	ExternalTunnel,
	IPResult,
	NativePingCheckConfig,
	NativePingCheckStatus,
	PingLogEntry,
	SpeedTestResult,
	SystemTunnel,
	TunnelListItem,
	TunnelReferencedError
} from '$lib/types';
import type { TrafficPeriod } from './clientCore';
import { CoreClient } from './clientCore';
import type { ApiResponse } from './clientCore';
import { validateApiResponse } from './validate';

export class TunnelsClient extends CoreClient {
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

	protected throwTunnelReferencedFrom409(body: unknown, fallbackId: string): never {
		const details: TunnelReferencedError =
			(body as { details?: TunnelReferencedError })?.details ?? {
				tunnelId: fallbackId,
				deviceProxy: false,
				routerRules: [],
				routerOther: [],
			};
		const err = new Error('tunnel_referenced') as Error & {
			details: TunnelReferencedError;
		};
		err.details = details;
		throw err;
	}

	protected async fetchDelete<T>(url: string, options: RequestInit, fallbackId: string): Promise<T> {
		let res: Response;
		try {
			res = await fetch(url, {
				...options,
				credentials: 'same-origin',
				signal: this.abortController.signal,
				headers: {
					'Content-Type': 'application/json',
					...options.headers,
				},
			});
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') throw e;
			this.onConnectionLost?.();
			throw new Error('Ошибка сети: не удалось подключиться к серверу');
		}
		if (res.status === 409) {
			// Тело "null" — валидный JSON, catch не сработает, поэтому ?.
			const body = (await res.json().catch(() => null)) as {
				error?: unknown;
				message?: string;
			} | null;
			// 409 на удалении означает две разные вещи: туннель на кого-то
			// завязан (error:"tunnel_referenced") либо замок туннеля занят
			// другой операцией (error:true + message). Второе — ретраибельно,
			// модалку «используется» показывать нельзя.
			if (body?.error === 'tunnel_referenced') {
				this.throwTunnelReferencedFrom409(body, fallbackId);
			}
			// Нейтральный fallback: fetchDelete общий для всех ручек удаления,
			// подставлять сюда причину одной из них нельзя.
			throw new Error(body?.message || 'Конфликт: операция отклонена (409)');
		}
		if (res.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}
		if (!res.ok) {
			const text = await res.text().catch(() => '');
			throw new Error(`Ошибка удаления (${res.status}): ${text.substring(0, 100)}`);
		}
		const data = (await res.json()) as ApiResponse<T>;
		if (data.error) throw new Error(data.message || 'Ошибка удаления');
		validateApiResponse(
			options.method ?? 'POST',
			url.startsWith(this.baseUrl) ? url.slice(this.baseUrl.length) : url,
			data,
		);
		return data.data as T;
	}

	async deleteTunnel(id: string): Promise<DeleteResult> {
		return this.fetchDelete<DeleteResult>(
			`${this.baseUrl}/tunnels/delete?id=${encodeURIComponent(id)}`,
			{ method: 'POST' },
			id,
		);
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

	async importConfig(
		content: string,
		name?: string,
		backend?: string,
		freeTurnClientId?: string,
		wdttClientId?: string
	): Promise<AWGTunnel> {
		return this.request('/import/conf', {
			method: 'POST',
			body: JSON.stringify({ content, name, backend, freeTurnClientId, wdttClientId })
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


}
