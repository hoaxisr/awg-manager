import type {
	ASCParams,
	AddManagedPeerRequest,
	CreateManagedServerRequest,
	ManagedPeer,
	ManagedServer,
	ManagedServerBackupFile,
	ManagedServerDriftResponse,
	ManagedServerRestoreResponse,
	ManagedServerStats,
	RestoreOptions,
	UpdateManagedPeerRequest,
	UpdateManagedServerRequest,
	WireguardServerConfig
} from '$lib/types';
import { SystemClient } from './clientSystem';

export class ServersClient extends SystemClient {
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

	async setWireguardServerNATMode(
		name: string,
		mode: 'full' | 'internet-only' | 'none'
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(name)}/nat`, {
			method: 'POST',
			body: JSON.stringify({ mode })
		});
	}

	async setWireguardServerNATEnabled(
		name: string,
		enabled: boolean
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(name)}/nat`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async setWireguardServerPolicy(
		name: string,
		policy: string
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(name)}/policy`, {
			method: 'POST',
			body: JSON.stringify({ policy })
		});
	}

	async setWireguardServerEndpoint(
		name: string,
		endpoint: string
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(name)}/endpoint`, {
			method: 'POST',
			body: JSON.stringify({ endpoint })
		});
	}

	async addSystemServerPeer(
		serverId: string,
		data: { description: string; tunnelIP: string }
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers`, {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async updateSystemServerPeer(
		serverId: string,
		pubkey: string,
		data: { description: string; tunnelIP: string }
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}`, {
			method: 'PUT',
			body: JSON.stringify(data)
		});
	}

	async deleteSystemServerPeer(
		serverId: string,
		pubkey: string
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}`, {
			method: 'DELETE'
		});
	}

	async toggleSystemServerPeer(
		serverId: string,
		publicKey: string,
		enabled: boolean
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(publicKey)}/toggle`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	/** endpointHost — хост Endpoint вместо WAN/KeenDNS (прокси-обвязки шлют 127.0.0.1). */
	async getSystemServerPeerConf(serverId: string, pubkey: string, endpointHost = ''): Promise<string> {
		const q = endpointHost ? `?endpoint=${encodeURIComponent(endpointHost)}` : '';
		const res = await this.request<{ conf: string }>(
			`/servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}/conf${q}`
		);
		return res.conf;
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

	/** endpointHost — хост Endpoint вместо WAN/KeenDNS (прокси-обвязки шлют 127.0.0.1). */
	async getManagedPeerConf(serverId: string, pubkey: string, endpointHost = ''): Promise<string> {
		const q = endpointHost ? `?endpoint=${encodeURIComponent(endpointHost)}` : '';
		const res = await this.request<{ conf: string }>(`/managed-servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}/conf${q}`);
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
