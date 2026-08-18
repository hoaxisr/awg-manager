import type {
	FreeTurnAllowlistAddResult,
	FreeTurnAllowlistStatus,
	FreeTurnCaptchaOverview,
	FreeTurnClientConfig,
	FreeTurnClientInstance,
	FreeTurnConfig,
	FreeTurnDeleteClientResult,
	FreeTurnGenerateLinkRequest,
	FreeTurnGenerateLinkResult,
	FreeTurnLinkPayload,
	FreeTurnServerConfig,
	FreeTurnServerInstance,
	FreeTurnStatus
} from '$lib/types';
import { SubscriptionsClient } from './clientSubscriptions';

export class FreeturnClient extends SubscriptionsClient {
	// ─────────────────────────────────────────────
	// #region FreeTurn — TURN-tunnel client + server
	// ─────────────────────────────────────────────

	async getFreeTurnConfig(): Promise<FreeTurnConfig> {
		return this.request<FreeTurnConfig>('/freeturn/config');
	}

	async updateFreeTurnClientConfig(config: FreeTurnClientConfig): Promise<FreeTurnClientConfig> {
		return this.request<FreeTurnClientConfig>('/freeturn/client/config', {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async updateFreeTurnServerConfig(config: FreeTurnServerConfig): Promise<FreeTurnServerConfig> {
		return this.request<FreeTurnServerConfig>('/freeturn/server/config', {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async updateFreeTurnClientInstance(
		id: string,
		config: FreeTurnClientConfig
	): Promise<FreeTurnClientConfig> {
		return this.request<FreeTurnClientConfig>(`/freeturn/clients/${encodeURIComponent(id)}`, {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async updateFreeTurnServerInstance(
		id: string,
		config: FreeTurnServerConfig
	): Promise<FreeTurnServerConfig> {
		return this.request<FreeTurnServerConfig>(`/freeturn/servers/${encodeURIComponent(id)}`, {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async createFreeTurnClient(name?: string): Promise<FreeTurnClientInstance> {
		return this.request<FreeTurnClientInstance>('/freeturn/clients', {
			method: 'POST',
			body: JSON.stringify(name ? { name } : {})
		});
	}

	async createFreeTurnServer(name?: string): Promise<FreeTurnServerInstance> {
		return this.request<FreeTurnServerInstance>('/freeturn/servers', {
			method: 'POST',
			body: JSON.stringify(name ? { name } : {})
		});
	}

	async deleteFreeTurnClient(id: string): Promise<FreeTurnDeleteClientResult> {
		return this.request<FreeTurnDeleteClientResult>(
			`/freeturn/clients/${encodeURIComponent(id)}`,
			{ method: 'DELETE' }
		);
	}

	async deleteFreeTurnServer(id: string): Promise<void> {
		await this.request(`/freeturn/servers/${encodeURIComponent(id)}`, { method: 'DELETE' });
	}

	async renameFreeTurnClient(id: string, name: string): Promise<void> {
		await this.request(`/freeturn/clients/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		});
	}

	async renameFreeTurnServer(id: string, name: string): Promise<void> {
		await this.request(`/freeturn/servers/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		});
	}

	async getFreeTurnStatus(): Promise<FreeTurnStatus> {
		return this.request<FreeTurnStatus>('/freeturn/status');
	}

	async getFreeTurnCaptchaStatus(): Promise<FreeTurnCaptchaOverview> {
		return this.request<FreeTurnCaptchaOverview>('/freeturn/captcha/status');
	}

	async startFreeTurnClient(id = 'default'): Promise<{ message: string }> {
		if (id === 'default') {
			return this.request('/freeturn/client/start', { method: 'POST' });
		}
		return this.request(`/freeturn/clients/${encodeURIComponent(id)}/start`, { method: 'POST' });
	}

	async stopFreeTurnClient(id = 'default'): Promise<{ message: string }> {
		if (id === 'default') {
			return this.request('/freeturn/client/stop', { method: 'POST' });
		}
		return this.request(`/freeturn/clients/${encodeURIComponent(id)}/stop`, { method: 'POST' });
	}

	async startFreeTurnServer(id = 'default'): Promise<{ message: string }> {
		if (id === 'default') {
			return this.request('/freeturn/server/start', { method: 'POST' });
		}
		return this.request(`/freeturn/servers/${encodeURIComponent(id)}/start`, { method: 'POST' });
	}

	async stopFreeTurnServer(id = 'default'): Promise<{ message: string }> {
		if (id === 'default') {
			return this.request('/freeturn/server/stop', { method: 'POST' });
		}
		return this.request(`/freeturn/servers/${encodeURIComponent(id)}/stop`, { method: 'POST' });
	}

	async generateFreeTurnLink(
		req: FreeTurnGenerateLinkRequest = {}
	): Promise<FreeTurnGenerateLinkResult> {
		const serverId = req.serverId?.trim();
		if (serverId && serverId !== 'default') {
			return this.request<FreeTurnGenerateLinkResult>(
				`/freeturn/servers/${encodeURIComponent(serverId)}/link`,
				{
					method: 'POST',
					body: JSON.stringify(req)
				}
			);
		}
		return this.request<FreeTurnGenerateLinkResult>('/freeturn/server/link', {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async decodeFreeTurnLink(link: string): Promise<FreeTurnLinkPayload> {
		return this.request<FreeTurnLinkPayload>('/freeturn/link/decode', {
			method: 'POST',
			body: JSON.stringify({ link })
		});
	}

	async installFreeTurn(): Promise<void> {
		await this.request<{ message: string }>('/freeturn/install', { method: 'POST' });
	}

	async getFreeTurnServerAllowlist(serverId: string): Promise<FreeTurnAllowlistStatus> {
		return this.request<FreeTurnAllowlistStatus>(
			`/freeturn/servers/${encodeURIComponent(serverId)}/allowlist`
		);
	}

	async addFreeTurnServerAllowlistClient(
		serverId: string,
		clientId: string,
		comment: string
	): Promise<FreeTurnAllowlistAddResult> {
		return this.request<FreeTurnAllowlistAddResult>(
			`/freeturn/servers/${encodeURIComponent(serverId)}/allowlist`,
			{
				method: 'POST',
				body: JSON.stringify({ clientId, comment })
			}
		);
	}

	async removeFreeTurnServerAllowlistClient(serverId: string, clientId: string): Promise<void> {
		await this.request(
			`/freeturn/servers/${encodeURIComponent(serverId)}/allowlist/${encodeURIComponent(clientId)}`,
			{ method: 'DELETE' }
		);
	}

	/** needsRestart — список реально выключился этой ручкой (TS-24). */
	async disableFreeTurnServerAllowlist(
		serverId: string
	): Promise<{ needsRestart?: boolean }> {
		return this.request<{ needsRestart?: boolean }>(
			`/freeturn/servers/${encodeURIComponent(serverId)}/allowlist`,
			{ method: 'DELETE' }
		);
	}

	async lookupProxyListener(
		host: string,
		port: number,
		proto: 'udp' | 'tcp' = 'udp'
	): Promise<ProxyListenerInfo> {
		const q = new URLSearchParams({
			host,
			port: String(port),
			proto
		});
		return this.request<ProxyListenerInfo>(`/proxy/listener?${q}`);
	}

	async killProxyListener(
		host: string,
		port: number,
		proto: 'udp' | 'tcp' = 'udp'
	): Promise<{ message?: string; pid?: number; comm?: string }> {
		return this.request(`/proxy/kill-listener`, {
			method: 'POST',
			body: JSON.stringify({ host, port, proto })
		});
	}

	// #endregion
}

export interface ProxyListenerInfo {
	open: boolean;
	pid?: number;
	comm?: string;
	proto: string;
	host: string;
	port: number;
}
