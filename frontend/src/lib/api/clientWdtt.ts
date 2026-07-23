import type {
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
	WdttImportPayload,
	WdttLinkDecodeResult,
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

	async refreshWdttSubscription(id: string): Promise<{
		instance: WdttClientInstance;
		payload: WdttImportPayload;
		message: string;
	}> {
		return this.request(`/wdtt/clients/${encodeURIComponent(id)}/subscription/refresh`, {
			method: 'POST'
		});
	}
}
