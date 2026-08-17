import type {
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
	WdttGenerateLinkResult,
	WdttImportPayload,
	WdttLinkDecodeResult,
	WdttServerConfig,
	WdttServerInstance,
	WdttStatus
} from '$lib/types';
import { FreeturnClient } from './clientFreeturn';

export type WdttDeleteClientResult = {
	message: string;
	deletedTunnels?: string[];
	tunnelErrors?: string[];
};

export type WdttEnsureWgResult = {
	created: boolean;
	tunnelId?: string;
	tunnelName?: string;
	message?: string;
};

export type WdttSaveClientResult = {
	config: WdttClientConfig;
	deletedTunnels?: string[];
	tunnelErrors?: string[];
};

export type WdttSaveServerResult = {
	config: WdttServerConfig;
};

export class WdttClient extends FreeturnClient {
	async getWdttConfig(): Promise<WdttConfig> {
		return this.request<WdttConfig>('/wdtt/config');
	}

	async updateWdttClientConfig(config: WdttClientConfig): Promise<WdttSaveClientResult> {
		return this.request<WdttSaveClientResult>('/wdtt/client/config', {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async updateWdttClientInstance(id: string, config: WdttClientConfig): Promise<WdttSaveClientResult> {
		return this.request<WdttSaveClientResult>(`/wdtt/clients/${encodeURIComponent(id)}`, {
			method: 'PUT',
			body: JSON.stringify(config)
		});
	}

	async createWdttClient(name?: string, config?: WdttClientConfig): Promise<WdttClientInstance> {
		return this.request<WdttClientInstance>('/wdtt/clients', {
			method: 'POST',
			body: JSON.stringify({ name, config })
		});
	}

	async deleteWdttClient(id: string): Promise<WdttDeleteClientResult> {
		return this.request<WdttDeleteClientResult>(`/wdtt/clients/${encodeURIComponent(id)}`, {
			method: 'DELETE'
		});
	}

	async renameWdttClient(id: string, name: string): Promise<{ message: string }> {
		return this.request(`/wdtt/clients/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		});
	}

	async getWdttStatus(): Promise<WdttStatus> {
		return this.request<WdttStatus>('/wdtt/status');
	}

	async startWdttClientInstance(id: string): Promise<void> {
		await this.request(`/wdtt/clients/${encodeURIComponent(id)}/start`, { method: 'POST' });
	}

	async stopWdttClientInstance(id: string): Promise<void> {
		await this.request(`/wdtt/clients/${encodeURIComponent(id)}/stop`, { method: 'POST' });
	}

	async decodeWdttLink(link: string): Promise<WdttLinkDecodeResult> {
		return this.request<WdttLinkDecodeResult>('/wdtt/link/decode', {
			method: 'POST',
			body: JSON.stringify({ link })
		});
	}

	async installWdttClient(): Promise<void> {
		await this.request('/wdtt/install', { method: 'POST' });
	}

	async ensureWdttWgTunnel(id: string): Promise<WdttEnsureWgResult> {
		return this.request<WdttEnsureWgResult>(`/wdtt/clients/${encodeURIComponent(id)}/ensure-wg-tunnel`, {
			method: 'POST'
		});
	}

	async ensureWdttRawTunnel(id: string): Promise<WdttEnsureWgResult> {
		return this.request<WdttEnsureWgResult>(`/wdtt/clients/${encodeURIComponent(id)}/ensure-raw-tunnel`, {
			method: 'POST'
		});
	}

	async refreshWdttSubscription(id: string): Promise<{
		instance: WdttClientInstance;
		payload: WdttImportPayload;
		message: string;
	}> {
		return this.request(`/wdtt/clients/${encodeURIComponent(id)}/subscription/refresh`, {
			method: 'POST'
		});
	}

	async updateWdttServerInstance(id: string, config: WdttServerConfig): Promise<WdttSaveServerResult> {
		const res = await this.request<{ config: WdttServerConfig }>(
			`/wdtt/servers/${encodeURIComponent(id)}`,
			{ method: 'PUT', body: JSON.stringify(config) }
		);
		return { config: res.config };
	}

	async setWdttServerNATMode(id: string, mode: 'full' | 'internet-only' | 'none'): Promise<WdttSaveServerResult> {
		const res = await this.request<{ config: WdttServerConfig }>(
			`/wdtt/servers/${encodeURIComponent(id)}/nat`,
			{ method: 'POST', body: JSON.stringify({ mode }) }
		);
		return { config: res.config };
	}

	async setWdttServerPolicy(id: string, policy: string): Promise<WdttSaveServerResult> {
		const res = await this.request<{ config: WdttServerConfig }>(
			`/wdtt/servers/${encodeURIComponent(id)}/policy`,
			{ method: 'POST', body: JSON.stringify({ policy }) }
		);
		return { config: res.config };
	}

	async setWdttServerLANSegments(id: string, segments: string[]): Promise<WdttSaveServerResult> {
		const res = await this.request<{ config: WdttServerConfig }>(
			`/wdtt/servers/${encodeURIComponent(id)}/lan-segments`,
			{ method: 'POST', body: JSON.stringify({ segments }) }
		);
		return { config: res.config };
	}

	async createWdttServer(name?: string, config?: WdttServerConfig): Promise<WdttServerInstance> {
		return this.request<WdttServerInstance>('/wdtt/servers', {
			method: 'POST',
			body: JSON.stringify({ name, config })
		});
	}

	async deleteWdttServer(id: string): Promise<{ message: string }> {
		return this.request(`/wdtt/servers/${encodeURIComponent(id)}`, { method: 'DELETE' });
	}

	async renameWdttServer(id: string, name: string): Promise<{ message: string }> {
		return this.request(`/wdtt/servers/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		});
	}

	async startWdttServerInstance(id: string): Promise<void> {
		await this.request(`/wdtt/servers/${encodeURIComponent(id)}/start`, { method: 'POST' });
	}

	async stopWdttServerInstance(id: string): Promise<void> {
		await this.request(`/wdtt/servers/${encodeURIComponent(id)}/stop`, { method: 'POST' });
	}

	async generateWdttServerLink(
		id: string,
		opts?: { peer?: string; vkHashes?: string[]; name?: string; password?: string }
	): Promise<WdttGenerateLinkResult> {
		return this.request<WdttGenerateLinkResult>(`/wdtt/servers/${encodeURIComponent(id)}/link`, {
			method: 'POST',
			body: JSON.stringify(opts ?? {})
		});
	}

	async getWdttServerPanelUsers(serverId: string): Promise<import('$lib/types').WdttPanelUsersStatus> {
		return this.request(`/wdtt/servers/${encodeURIComponent(serverId)}/users`);
	}

	async addWdttServerPanelUser(
		serverId: string,
		opts: { password?: string; comment?: string; vkHash?: string; mainPassword?: string }
	): Promise<import('$lib/types').WdttPanelUsersStatus> {
		return this.request(`/wdtt/servers/${encodeURIComponent(serverId)}/users`, {
			method: 'POST',
			body: JSON.stringify(opts)
		});
	}

	/** Переименование абонента: имя ложится в comment записи, состав не меняется. */
	async renameWdttServerPanelUser(
		serverId: string,
		password: string,
		name: string
	): Promise<import('$lib/types').WdttPanelUsersStatus> {
		return this.request(
			`/wdtt/servers/${encodeURIComponent(serverId)}/users/${encodeURIComponent(password)}`,
			{ method: 'PATCH', body: JSON.stringify({ name }) }
		);
	}

	async removeWdttServerPanelUser(
		serverId: string,
		password: string
	): Promise<import('$lib/types').WdttPanelUsersStatus> {
		return this.request(
			`/wdtt/servers/${encodeURIComponent(serverId)}/users/${encodeURIComponent(password)}`,
			{ method: 'DELETE' }
		);
	}
}
