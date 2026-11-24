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
	PeerPresets,
	RestoreOptions,
	UpdateManagedPeerRequest,
	UpdateManagedServerRequest,
	WireguardServerConfig,
	XrayClient,
	XrayConfig,
	XrayStatus,
	XrayShareLinks,
	XrayRealityState,
	XrayRealityUpdate,
	XrayRealityDestCheck,
	XrayRealityQuickSetupResponse,
	TgWebProxyConfig,
	TgWebProxyStatus,
	TgWebProxyRevealData,
	CDNDispatcherConfig,
	XHTTPTuning,
	ServerResetOptions,
	ServerResetResult,
} from '$lib/types';
import type {
	WizardKind,
	CapabilitiesResponse,
	KeenDNSDiscovery,
	PreflightResponse,
	WizardPlanRequest,
	ServerPlanRecord,
	JobStatusResponse,
	RevealCredentials,
	VerificationReport,
} from '$lib/types/serverWizard';
import { SystemClient } from './clientSystem';

let cachedCsrfToken: string | null = null;
let csrfTokenPromise: Promise<string> | null = null;

export function clearTgWebProxyCSRFCache(): void {
	cachedCsrfToken = null;
	csrfTokenPromise = null;
}

export class ServersClient extends SystemClient {
	override async logout(): Promise<void> {
		clearTgWebProxyCSRFCache();
		await super.logout();
	}

	async getTgWebProxyCSRF(forceRefresh = false): Promise<string> {
		if (!forceRefresh && cachedCsrfToken) {
			return cachedCsrfToken;
		}
		if (csrfTokenPromise) {
			return csrfTokenPromise;
		}
		csrfTokenPromise = (async () => {
			try {
				const res = await this.request<{ csrf_token: string }>('/servers/tgwebproxy/csrf', {
					method: 'GET'
				});
				cachedCsrfToken = res.csrf_token;
				return res.csrf_token;
			} finally {
				csrfTokenPromise = null;
			}
		})();
		return csrfTokenPromise;
	}

	private async requestWithCSRF<T>(
		endpoint: string,
		options: RequestInit = {}
	): Promise<T> {
		let token = await this.getTgWebProxyCSRF();
		const doReq = (t: string) => {
			return this.request<T>(endpoint, {
				...options,
				headers: {
					...(options.headers || {}),
					'X-CSRF-Token': t,
					'X-Requested-With': 'XMLHttpRequest',
				}
			});
		};

		try {
			return await doReq(token);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err ?? '');
			if (msg.includes('CSRF') || msg.includes('csrf') || msg.includes('Forbidden') || msg.includes('403')) {
				clearTgWebProxyCSRFCache();
				token = await this.getTgWebProxyCSRF(true);
				return await doReq(token);
			}
			throw err;
		}
	}
	// ─────────────────────────────────────────────
	// #region VPN Servers — list, config, mark
	// ─────────────────────────────────────────────

	async getServerConfig(name: string): Promise<WireguardServerConfig> {
		return this.request(`/servers/config?name=${encodeURIComponent(name)}`);
	}

	// Снимок /servers/all разово, без подписки на polling-стор: страницам,
	// которым нужен разовый ответ (кто из интерфейсов — сервер), интервал
	// опроса не нужен.
	async getAllServers(): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request('/servers/all');
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
		// dns — резолвер пира для .conf (#933); пусто = LAN-адрес роутера.
		// clientAllowedIPs/remoteSubnets — сети клиента (#713).
		data: {
			description: string;
			tunnelIP: string;
			dns?: string;
			clientAllowedIPs?: string;
			remoteSubnets?: string[];
		}
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers`, {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async updateSystemServerPeer(
		serverId: string,
		pubkey: string,
		// signature омитим — сигнатура пира не трогается; прислали — заменяет
		// все пять полей и профиль.
		data: {
			description: string;
			tunnelIP: string;
			dns?: string;
			signature?: { profile: string; i1: string; i2: string; i3: string; i4: string; i5: string };
			clientAllowedIPs?: string;
			remoteSubnets?: string[];
		}
	): Promise<import('$lib/stores/servers').ServersSnapshot> {
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers/${encodeURIComponent(pubkey)}`, {
			method: 'PUT',
			body: JSON.stringify(data)
		});
	}

	/** Пресеты поля «AllowedIPs клиента»; dns — как введён в форме (пусто — умолчание бэкенда). */
	async getSystemServerPeerPresets(serverId: string, dns: string): Promise<PeerPresets> {
		const q = dns ? `?dns=${encodeURIComponent(dns)}` : '';
		return this.request(`/servers/${encodeURIComponent(serverId)}/peers/presets${q}`);
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

	async getManagedPeerPresets(serverId: string, dns: string): Promise<PeerPresets> {
		const q = dns ? `?dns=${encodeURIComponent(dns)}` : '';
		return this.request(`/managed-servers/${encodeURIComponent(serverId)}/peers/presets${q}`);
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

	// ─────────────────────────────────────────────
	// #region VLESS Server (awgm-xhttpd) & Telegram Web Proxy
	// ─────────────────────────────────────────────

	async getXrayServerConfig(): Promise<XrayConfig> {
		return this.request<XrayConfig>('/servers/vless');
	}

	async getXrayConfig(): Promise<XrayConfig> {
		return this.getXrayServerConfig();
	}

	async updateXrayServerConfig(cfg: Partial<XrayConfig>): Promise<XrayConfig> {
		return this.request<XrayConfig>('/servers/vless', {
			method: 'PUT',
			body: JSON.stringify(cfg),
		});
	}

	async getXrayServerStatus(): Promise<XrayStatus> {
		return this.request<XrayStatus>('/servers/vless/status');
	}

	async getXrayStatus(): Promise<XrayStatus> {
		return this.getXrayServerStatus();
	}

	async xrayServerAction(action: 'start' | 'stop' | 'restart'): Promise<XrayStatus> {
		return this.request<XrayStatus>('/servers/vless/action', {
			method: 'POST',
			body: JSON.stringify({ action }),
		});
	}

	async addXrayClient(remark: string): Promise<XrayClient> {
		return this.request<XrayClient>('/servers/vless/clients', {
			method: 'POST',
			body: JSON.stringify({ remark }),
		});
	}

	async deleteXrayClient(id: string): Promise<{ success: boolean }> {
		return this.request<{ success: boolean }>(`/servers/vless/clients/${encodeURIComponent(id)}`, {
			method: 'DELETE',
		});
	}

	async toggleXrayClient(id: string, enabled: boolean): Promise<{ success: boolean }> {
		return this.request<{ success: boolean }>(`/servers/vless/clients/${encodeURIComponent(id)}/toggle`, {
			method: 'POST',
			body: JSON.stringify({ enabled }),
		});
	}

	async rotateXrayClientFP(id: string): Promise<XrayClient> {
		return this.request<XrayClient>(`/servers/vless/clients/${encodeURIComponent(id)}/rotate-fp`, {
			method: 'POST',
		});
	}

	async getXrayClientLinks(id: string, domain?: string, endpointId?: string): Promise<XrayShareLinks> {
		const params = new URLSearchParams();
		if (domain) params.set('domain', domain);
		if (endpointId) params.set('endpoint_id', endpointId);
		const qs = params.toString();
		return this.request<XrayShareLinks>(`/servers/vless/clients/${encodeURIComponent(id)}/link${qs ? `?${qs}` : ''}`);
	}

	// Direct VLESS + REALITY entry (white-IP routers)
	async getXrayReality(refreshNetwork = false): Promise<XrayRealityState> {
		return this.request<XrayRealityState>(`/servers/vless/reality${refreshNetwork ? '?refresh=1' : ''}`);
	}

	async updateXrayReality(update: XrayRealityUpdate): Promise<XrayRealityState> {
		return this.requestWithCSRF<XrayRealityState>('/servers/vless/reality', {
			method: 'POST',
			body: JSON.stringify(update),
		});
	}

	async checkXrayRealityDest(dest: string, serverName?: string): Promise<XrayRealityDestCheck> {
		return this.requestWithCSRF<XrayRealityDestCheck>('/servers/vless/reality/check-dest', {
			method: 'POST',
			body: JSON.stringify({ dest, server_name: serverName }),
		});
	}

	async xrayRealityQuickSetup(
		clientRemark: string,
		reality: XrayRealityUpdate,
		options?: {
			preserve_existing_clients?: boolean;
			outbound_mode?: string;
			outbound_interface?: string;
		}
	): Promise<XrayRealityQuickSetupResponse> {
		return this.requestWithCSRF<XrayRealityQuickSetupResponse>('/servers/vless/reality/quick-setup', {
			method: 'POST',
			body: JSON.stringify({
				client_remark: clientRemark,
				reality,
				...(options ?? {}),
			}),
		});
	}

	async getTgWebProxyConfig(): Promise<TgWebProxyConfig> {
		return this.request<TgWebProxyConfig>('/servers/tgwebproxy');
	}

	async updateTgWebProxyConfig(cfg: Partial<TgWebProxyConfig>): Promise<TgWebProxyConfig> {
		return this.requestWithCSRF<TgWebProxyConfig>('/servers/tgwebproxy', {
			method: 'PUT',
			body: JSON.stringify(cfg),
		});
	}

	async getTgWebProxyStatus(): Promise<TgWebProxyStatus> {
		return this.request<TgWebProxyStatus>('/servers/tgwebproxy/status');
	}

	async revealTgWebProxySecret(userId?: string): Promise<TgWebProxyRevealData> {
		const query = userId ? `?user_id=${encodeURIComponent(userId)}` : '';
		return this.requestWithCSRF<TgWebProxyRevealData>(`/servers/tgwebproxy/reveal${query}`, {
			method: 'POST',
			body: userId ? JSON.stringify({ user_id: userId }) : undefined,
		});
	}

	async tgWebProxyAction(
		action:
			| 'start'
			| 'stop'
			| 'restart'
			| 'rotate_secret'
			| 'revoke_legacy'
			| 'clear_scanner_cache'
			| 'create_user'
			| 'update_user'
			| 'delete_user',
		payload?: {
			user_id?: string;
			remark?: string;
			enabled?: boolean;
			secret?: string;
			regenerate_secret?: boolean;
		}
	): Promise<TgWebProxyStatus> {
		return this.requestWithCSRF<TgWebProxyStatus>('/servers/tgwebproxy/action', {
			method: 'POST',
			body: JSON.stringify({ action, ...payload }),
		});
	}

	async getDispatcherConfig(): Promise<CDNDispatcherConfig> {
		return this.request<CDNDispatcherConfig>('/servers/ingress/dispatcher');
	}

	async getCDNDispatcherConfig(): Promise<CDNDispatcherConfig> {
		return this.getDispatcherConfig();
	}

	async updateDispatcherConfig(cfg: CDNDispatcherConfig): Promise<CDNDispatcherConfig> {
		return this.requestWithCSRF<CDNDispatcherConfig>('/servers/ingress/dispatcher', {
			method: 'PUT',
			body: JSON.stringify(cfg),
		});
	}

	async getXrayTuning(): Promise<XHTTPTuning> {
		return this.request<XHTTPTuning>('/servers/vless/tuning');
	}

	async updateXrayTuning(tuning: XHTTPTuning): Promise<XHTTPTuning> {
		return this.requestWithCSRF<XHTTPTuning>('/servers/vless/tuning', {
			method: 'PUT',
			body: JSON.stringify(tuning),
		});
	}

	async xrayResolveRecovery(strategy = 'clear'): Promise<{ success: boolean }> {
		return this.request<{ success: boolean }>('/servers/vless/recovery/resolve', {
			method: 'POST',
			body: JSON.stringify({ strategy }),
		});
	}

	async resetVlessServer(opts: ServerResetOptions = {}): Promise<ServerResetResult> {
		return this.request<ServerResetResult>('/servers/vless/reset', {
			method: 'POST',
			body: JSON.stringify({ delete_keendns: Boolean(opts.delete_keendns) }),
		});
	}

	async resetTgWebProxy(opts: ServerResetOptions = {}): Promise<ServerResetResult> {
		try {
			return await this.requestWithCSRF<ServerResetResult>('/servers/tgwebproxy/reset', {
				method: 'POST',
				body: JSON.stringify({ delete_keendns: Boolean(opts.delete_keendns) }),
			});
		} finally {
			clearTgWebProxyCSRFCache();
		}
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region Server First-Run Wizards
	// ─────────────────────────────────────────────

	async getWizardCSRF(kind: WizardKind): Promise<string> {
		const res = await this.request<{ token: string }>(`/servers/${kind}/wizard/csrf`, {
			method: 'GET'
		});
		return res.token;
	}

	private normalizeKeenDNSDiscovery(raw: unknown): KeenDNSDiscovery {
		const rawRec =
			raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : {};
		const kd =
			rawRec.keendns && typeof rawRec.keendns === 'object'
				? (rawRec.keendns as Record<string, unknown>)
				: rawRec;
		const str = (v: unknown, fallback = ''): string => (typeof v === 'string' && v ? v : fallback);
		const domain = str(kd.domain) || str(kd.base_fqdn) || str(kd.base_domain);
		const rawMode = str(kd.mode) || str(kd.access_mode);
		const mode: KeenDNSDiscovery['mode'] =
			rawMode === 'cloud' || rawMode === 'direct' || rawMode === 'disabled'
				? rawMode
				: kd.direct_accessible
					? 'direct'
					: domain
						? 'cloud'
						: 'unknown';
		const rawState = str(kd.state);
		const state = (rawState || (domain ? 'supported' : 'unconfigured')) as KeenDNSDiscovery['state'];
		const enabled =
			kd.enabled !== undefined
				? Boolean(kd.enabled)
				: Boolean(domain) && state !== 'unconfigured' && state !== 'unsupported' && mode !== 'disabled';
		// Never infer per-FQDN certificate validity from generic ssl_enabled or acme_v2 flags.
		const certOk = kd.cert_ok !== undefined ? Boolean(kd.cert_ok) : false;
		const rawPubs: unknown[] = Array.isArray(kd.publications)
			? kd.publications
			: Array.isArray(kd.existing_publications)
				? kd.existing_publications
				: [];
		const publications = rawPubs.map((item: unknown) => {
			const p = item && typeof item === 'object' ? (item as Record<string, unknown>) : {};
			const upHost = str(p.upstream_host) || str(p.target_host) || '127.0.0.1';
			const upPort =
				typeof p.upstream_port === 'number' && p.upstream_port > 0
					? p.upstream_port
					: typeof p.target_port === 'number' && p.target_port > 0
						? p.target_port
						: 9009;
			const upProto = str(p.upstream_proto) || 'http';
			return {
				label: str(p.label),
				fqdn: str(p.fqdn),
				upstream: str(p.upstream) || `${upProto}://${upHost}:${upPort}`,
				public: p.public !== undefined ? Boolean(p.public) : p.access === 'public',
				auth: Boolean(p.auth),
				domain_ndns: p.domain_ndns !== undefined ? Boolean(p.domain_ndns) : true,
				static: Boolean(p.static),
				active_in_runtime:
					p.active_in_runtime !== undefined
						? Boolean(p.active_in_runtime)
						: Boolean(p.runtime_active ?? p.runtime_https),
				compatible:
					p.compatible !== undefined
						? Boolean(p.compatible)
						: Boolean(p.compatible_with_ingress ?? p.reusable),
				conflict_reason: str(p.conflict_reason),
			};
		});

		return {
			state,
			enabled,
			domain,
			mode,
			cert_ok: certOk,
			cert_info: str(kd.cert_info),
			error_detail: str(kd.error_detail) || str(kd.status_message),
			publications,
			wan_ip: str(kd.wan_ip) || str(kd.wan_address),
			wan_ip_private:
				kd.wan_ip_private !== undefined ? Boolean(kd.wan_ip_private) : Boolean(kd.wan_private),
		};
	}

	async getWizardCapabilities(kind: WizardKind): Promise<CapabilitiesResponse> {
		const res = await this.request<CapabilitiesResponse>(`/servers/${kind}/wizard/capabilities`, {
			method: 'GET'
		});
		return {
			...res,
			kind: res?.kind || kind,
			profiles: Array.isArray(res?.profiles) ? res.profiles : [],
			egress_options: Array.isArray(res?.egress_options) ? res.egress_options : [],
			modes: Array.isArray(res?.modes) ? res.modes : [],
			scenarios: Array.isArray(res?.scenarios) ? res.scenarios : [],
			configured: Boolean(res?.configured),
			running: Boolean(res?.running),
			recovery_required: Boolean(res?.recovery_required),
			keendns: res?.keendns ? this.normalizeKeenDNSDiscovery(res.keendns) : undefined,
		};
	}

	async getWizardKeenDNS(kind: WizardKind): Promise<KeenDNSDiscovery> {
		const res = await this.request<{ keendns?: KeenDNSDiscovery } & Partial<KeenDNSDiscovery>>(
			`/servers/${kind}/wizard/keendns`,
			{ method: 'GET' }
		);
		return this.normalizeKeenDNSDiscovery(res);
	}

	async runWizardPreflight(kind: WizardKind, req: WizardPlanRequest): Promise<PreflightResponse> {
		return this.request<PreflightResponse>(`/servers/${kind}/wizard/preflight`, {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async createWizardPlan(kind: WizardKind, req: WizardPlanRequest): Promise<ServerPlanRecord> {
		return this.request<ServerPlanRecord>(`/servers/${kind}/wizard/plan`, {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async applyWizardPlan(kind: WizardKind, planId: string): Promise<{ job_id: string }> {
		const token = await this.getWizardCSRF(kind);
		return this.request<{ job_id: string }>(`/servers/${kind}/wizard/apply`, {
			method: 'POST',
			headers: {
				'X-CSRF-Token': token,
				'X-Requested-With': 'XMLHttpRequest',
			},
			body: JSON.stringify({ plan_id: planId })
		});
	}

	async getWizardJob(kind: WizardKind, jobId: string): Promise<JobStatusResponse> {
		return this.request<JobStatusResponse>(`/servers/${kind}/wizard/jobs/${jobId}`, {
			method: 'GET'
		});
	}

	async cancelWizardJob(kind: WizardKind, jobId: string): Promise<{ status: string }> {
		const token = await this.getWizardCSRF(kind);
		return this.request<{ status: string }>(`/servers/${kind}/wizard/jobs/${jobId}/cancel`, {
			method: 'POST',
			headers: {
				'X-CSRF-Token': token,
				'X-Requested-With': 'XMLHttpRequest',
			}
		});
	}

	async revealWizardCredentials(kind: WizardKind, jobId: string): Promise<RevealCredentials> {
		const token = await this.getWizardCSRF(kind);
		return this.request<RevealCredentials>(`/servers/${kind}/wizard/jobs/${jobId}/reveal`, {
			method: 'POST',
			headers: {
				'X-CSRF-Token': token,
				'X-Requested-With': 'XMLHttpRequest',
			}
		});
	}

	async verifyWizard(kind: WizardKind): Promise<VerificationReport> {
		return this.request<VerificationReport>(`/servers/${kind}/wizard/verify`, {
			method: 'POST'
		});
	}

	// #endregion
}
