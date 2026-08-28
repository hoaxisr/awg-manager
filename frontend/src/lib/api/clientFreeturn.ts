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
import {
	instancePath,
	toCaptchaOverview,
	toFreeTurnClientConfig,
	toFreeTurnClientPatch,
	toFreeTurnConfig,
	toFreeTurnServerConfig,
	toFreeTurnServerPatch,
	toFreeTurnStatus,
	type ProxyInstallStatus,
	type ProxyInstanceView,
	type ProxyKind,
	type ProxyListData,
	type ProxySeedView
} from './proxyInstances';

/** Очистка связанных AWG-туннелей — прежняя форма ответа ручки. */
export interface ProxyLinkedClearResult {
	deletedTunnels?: string[];
	tunnelErrors?: string[];
	message?: string;
}

export class FreeturnClient extends SubscriptionsClient {
	// ─────────────────────────────────────────────
	// #region Прокси-рантайм — общая поверхность /api/proxyrt
	// ─────────────────────────────────────────────

	/**
	 * Список инстансов всех ролей. Запрос дедуплицируется, пока он в полёте:
	 * страница читает статусы и конфиги обоих протоколов одним `Promise.all`,
	 * и без дедупликации один и тот же список ехал бы по сети четырежды.
	 */
	private proxyListInFlight?: Promise<ProxyListData>;

	private proxyListRaw(): Promise<ProxyListData> {
		if (!this.proxyListInFlight) {
			this.proxyListInFlight = this.request<ProxyListData>('/proxyrt/instances').finally(() => {
				this.proxyListInFlight = undefined;
			});
		}
		return this.proxyListInFlight;
	}

	protected async proxyList(): Promise<ProxyListData> {
		const list = await this.proxyListRaw();
		// Посев не состоялся — список ПУСТ по построению, а причина лежит в
		// блоке seed. Отдать пустой список молча значило бы показать
		// «инстансов нет» вместо «подсистема не поднялась».
		if (!list.seed?.seeded) {
			throw new Error(list.seed?.error || 'Прокси-подсистема не загружена');
		}
		return list;
	}

	/**
	 * Состояние посева: `seeded` — подсистема поднялась, `certified` — посев
	 * подтверждён реестру и уборка разрешена. Признака два, и «гейт заперт»
	 * (seeded без certified) обязано быть видно.
	 */
	async getProxySeed(): Promise<ProxySeedView> {
		return (await this.proxyListRaw()).seed;
	}

	/**
	 * Признать уведомления о переезде listen-порта прочитанными. Без этого
	 * плашка висит вечно: посев не повторяется и отметку никто не перепишет.
	 */
	async ackProxyListenMoves(): Promise<void> {
		await this.request('/proxyrt/seed/listen-moves', { method: 'DELETE' });
	}

	protected async proxyInstallStatus(subsystem: 'wdtt' | 'freeturn'): Promise<ProxyInstallStatus> {
		return this.request<ProxyInstallStatus>(`/proxyrt/install/status?subsystem=${subsystem}`);
	}

	protected async proxyInstall(subsystem: 'wdtt' | 'freeturn'): Promise<void> {
		await this.request('/proxyrt/install', {
			method: 'POST',
			body: JSON.stringify({ subsystem })
		});
	}

	protected async proxyCreate(
		kind: ProxyKind,
		name?: string,
		config?: Record<string, unknown>
	): Promise<ProxyInstanceView> {
		return this.request<ProxyInstanceView>('/proxyrt/instances', {
			method: 'POST',
			body: JSON.stringify({ kind, name: name ?? '', enabled: false, config: config ?? {} })
		});
	}

	protected async proxyPatch(
		kind: ProxyKind,
		id: string,
		body: {
			name?: string;
			enabled?: boolean;
			config?: Record<string, unknown>;
			/** Поля ЗАПИСИ, а не конфига роли: подписка и режим журнала статистики. */
			sub?: string;
			statsLog?: string;
		}
	): Promise<ProxyInstanceView> {
		return this.request<ProxyInstanceView>(instancePath(kind, id), {
			method: 'PATCH',
			body: JSON.stringify(body)
		});
	}

	protected async proxyDelete(kind: ProxyKind, id: string): Promise<void> {
		await this.request(instancePath(kind, id), { method: 'DELETE' });
	}

	/**
	 * Снос AWG-туннелей, связанных с клиентским инстансом. Отказ ручки не
	 * роняет удаление инстанса, а уезжает в `tunnelErrors`: в старом мире
	 * удаление сносило и связи, и инстанс, и об ошибках туннелей отчитывалось
	 * списком — молча потерять их нельзя, но и запирать удаление из-за них
	 * пользователь не просил.
	 */
	protected async proxyClearLinkedTunnels(
		kind: ProxyKind,
		id: string
	): Promise<ProxyLinkedClearResult> {
		try {
			return await this.request<ProxyLinkedClearResult>(
				instancePath(kind, id, '/linked-tunnels/clear'),
				{ method: 'POST' }
			);
		} catch (e) {
			return { tunnelErrors: [e instanceof Error ? e.message : String(e)] };
		}
	}

	// #endregion

	// ─────────────────────────────────────────────
	// #region FreeTurn — TURN-tunnel client + server
	// ─────────────────────────────────────────────

	async getFreeTurnConfig(): Promise<FreeTurnConfig> {
		return toFreeTurnConfig(await this.proxyList());
	}

	async updateFreeTurnClientInstance(
		id: string,
		config: FreeTurnClientConfig
	): Promise<FreeTurnClientConfig> {
		const view = await this.proxyPatch('freeturn-client', id, {
			enabled: config.enabled,
			config: toFreeTurnClientPatch(config)
		});
		return toFreeTurnClientConfig(view);
	}

	async updateFreeTurnServerInstance(
		id: string,
		config: FreeTurnServerConfig
	): Promise<FreeTurnServerConfig> {
		const view = await this.proxyPatch('freeturn-server', id, {
			enabled: config.enabled,
			config: toFreeTurnServerPatch(config)
		});
		return toFreeTurnServerConfig(view);
	}

	async createFreeTurnClient(name?: string): Promise<FreeTurnClientInstance> {
		const view = await this.proxyCreate('freeturn-client', name);
		return { id: view.id, name: view.name, config: toFreeTurnClientConfig(view) };
	}

	async createFreeTurnServer(name?: string): Promise<FreeTurnServerInstance> {
		const view = await this.proxyCreate('freeturn-server', name);
		return { id: view.id, name: view.name, config: toFreeTurnServerConfig(view) };
	}

	/**
	 * Удаление клиента: связанные AWG-туннели сносит своя ручка, удаление
	 * инстанса их не трогает. Порядок «сначала связи, потом инстанс» —
	 * уборщик ищет туннели по id ЖИВОЙ записи.
	 */
	async deleteFreeTurnClient(id: string): Promise<FreeTurnDeleteClientResult> {
		const cleared = await this.proxyClearLinkedTunnels('freeturn-client', id);
		await this.proxyDelete('freeturn-client', id);
		return {
			message: cleared.message,
			deletedTunnels: cleared.deletedTunnels,
			tunnelErrors: cleared.tunnelErrors
		};
	}

	async deleteFreeTurnServer(id: string): Promise<void> {
		await this.proxyDelete('freeturn-server', id);
	}

	async renameFreeTurnClient(id: string, name: string): Promise<void> {
		await this.proxyPatch('freeturn-client', id, { name });
	}

	async renameFreeTurnServer(id: string, name: string): Promise<void> {
		await this.proxyPatch('freeturn-server', id, { name });
	}

	async getFreeTurnStatus(): Promise<FreeTurnStatus> {
		const [list, install] = await Promise.all([
			this.proxyList(),
			this.proxyInstallStatus('freeturn')
		]);
		return toFreeTurnStatus(list, install);
	}

	async getFreeTurnCaptchaStatus(): Promise<FreeTurnCaptchaOverview> {
		return toCaptchaOverview(
			await this.request<FreeTurnCaptchaOverview>('/proxyrt/freeturn/captcha/status')
		);
	}

	async startFreeTurnClient(id = 'default'): Promise<void> {
		await this.proxyPatch('freeturn-client', id, { enabled: true });
	}

	async stopFreeTurnClient(id = 'default'): Promise<void> {
		await this.proxyPatch('freeturn-client', id, { enabled: false });
	}

	async startFreeTurnServer(id = 'default'): Promise<void> {
		await this.proxyPatch('freeturn-server', id, { enabled: true });
	}

	async stopFreeTurnServer(id = 'default'): Promise<void> {
		await this.proxyPatch('freeturn-server', id, { enabled: false });
	}

	async generateFreeTurnLink(
		req: FreeTurnGenerateLinkRequest = {}
	): Promise<FreeTurnGenerateLinkResult> {
		const serverId = req.serverId?.trim() || 'default';
		return this.request<FreeTurnGenerateLinkResult>(
			instancePath('freeturn-server', serverId, '/link'),
			{ method: 'POST', body: JSON.stringify(req) }
		);
	}

	async decodeFreeTurnLink(link: string): Promise<FreeTurnLinkPayload> {
		return this.request<FreeTurnLinkPayload>('/proxyrt/freeturn/link/decode', {
			method: 'POST',
			body: JSON.stringify({ link })
		});
	}

	async installFreeTurn(): Promise<void> {
		await this.proxyInstall('freeturn');
	}

	async getFreeTurnServerAllowlist(serverId: string): Promise<FreeTurnAllowlistStatus> {
		return this.request<FreeTurnAllowlistStatus>(
			instancePath('freeturn-server', serverId, '/allowlist')
		);
	}

	async addFreeTurnServerAllowlistClient(
		serverId: string,
		clientId: string,
		comment: string
	): Promise<FreeTurnAllowlistAddResult> {
		return this.request<FreeTurnAllowlistAddResult>(
			instancePath('freeturn-server', serverId, '/allowlist'),
			{ method: 'POST', body: JSON.stringify({ clientId, comment }) }
		);
	}

	async removeFreeTurnServerAllowlistClient(serverId: string, clientId: string): Promise<void> {
		await this.request(
			instancePath('freeturn-server', serverId, `/allowlist/${encodeURIComponent(clientId)}`),
			{ method: 'DELETE' }
		);
	}

	/** needsRestart — список реально выключился этой ручкой (TS-24). */
	async disableFreeTurnServerAllowlist(
		serverId: string
	): Promise<{ needsRestart?: boolean }> {
		return this.request<{ needsRestart?: boolean }>(
			instancePath('freeturn-server', serverId, '/allowlist'),
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
