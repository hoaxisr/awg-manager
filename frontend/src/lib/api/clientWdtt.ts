import type {
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
	WdttGenerateLinkResult,
	WdttImportPayload,
	WdttLinkDecodeResult,
	WdttPanelUsersStatus,
	WdttServerConfig,
	WdttServerInstance,
	WdttStatus
} from '$lib/types';
import { FreeturnClient } from './clientFreeturn';
import {
	instancePath,
	toWdttClientConfig,
	toWdttClientPatch,
	toWdttConfig,
	toWdttServerConfig,
	toWdttServerPatch,
	toWdttStatus
} from './proxyInstances';

export type WdttDeleteClientResult = {
	message?: string;
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

/**
 * Конфиг связанного WG-туннеля ещё не приехал от сервера: ручка отвечает
 * КОДОМ отказа, а не успехом с признаком. Автоэффект детали зовёт её сам, и
 * глушить надо именно этот код — «ошибка вообще» скрыла бы настоящие сбои.
 */
export const WDTT_WG_NOT_READY = 'WDTT_WG_NOT_READY';

export class WdttClient extends FreeturnClient {
	async getWdttConfig(): Promise<WdttConfig> {
		return toWdttConfig(await this.proxyList());
	}

	/**
	 * `sub` едет ОТДЕЛЬНЫМ полем тела, а не внутри конфига: URL подписки живёт
	 * на самой записи (у freeturn-клиента он поле роли, у wdtt — нет).
	 */
	async updateWdttClientInstance(id: string, config: WdttClientConfig): Promise<WdttSaveClientResult> {
		const view = await this.proxyPatch('wdtt-client', id, {
			enabled: config.enabled,
			sub: config.sub ?? '',
			config: toWdttClientPatch(config)
		});
		return { config: toWdttClientConfig(view) };
	}

	async createWdttClient(name?: string, config?: WdttClientConfig): Promise<WdttClientInstance> {
		const view = await this.proxyCreate(
			'wdtt-client',
			name,
			config ? toWdttClientPatch(config) : undefined
		);
		return { id: view.id, name: view.name, config: toWdttClientConfig(view) };
	}

	/** Удаление клиента: связанные AWG-туннели уносит бэкенд тем же запросом. */
	async deleteWdttClient(id: string): Promise<WdttDeleteClientResult> {
		const res = await this.proxyDelete('wdtt-client', id);
		return { deletedTunnels: res.deletedTunnels, tunnelErrors: res.tunnelErrors };
	}

	async renameWdttClient(id: string, name: string): Promise<void> {
		await this.proxyPatch('wdtt-client', id, { name });
	}

	async getWdttStatus(): Promise<WdttStatus> {
		const [list, install] = await Promise.all([
			this.proxyList(),
			this.proxyInstallStatus('wdtt')
		]);
		return toWdttStatus(list, install);
	}

	async startWdttClientInstance(id: string): Promise<void> {
		await this.proxyPatch('wdtt-client', id, { enabled: true });
	}

	async stopWdttClientInstance(id: string): Promise<void> {
		await this.proxyPatch('wdtt-client', id, { enabled: false });
	}

	async decodeWdttLink(link: string): Promise<WdttLinkDecodeResult> {
		return this.request<WdttLinkDecodeResult>('/proxyrt/wdtt/link/decode', {
			method: 'POST',
			body: JSON.stringify({ link })
		});
	}

	async installWdttClient(): Promise<void> {
		await this.proxyInstall('wdtt');
	}

	async ensureWdttWgTunnel(id: string): Promise<WdttEnsureWgResult> {
		return this.request<WdttEnsureWgResult>(
			instancePath('wdtt-client', id, '/ensure-wg-tunnel'),
			{ method: 'POST' }
		);
	}

	async refreshWdttSubscription(id: string): Promise<{
		key: string;
		payload: WdttImportPayload;
		message: string;
	}> {
		return this.request(instancePath('wdtt-client', id, '/subscription/refresh'), {
			method: 'POST'
		});
	}

	/**
	 * `statsLog` едет ОТДЕЛЬНЫМ полем тела, а не внутри конфига: режим журнала
	 * статистики живёт на самой записи. Пустая строка — законное значение
	 * (дефолт ram, журнал в tmpfs).
	 */
	async updateWdttServerInstance(id: string, config: WdttServerConfig): Promise<WdttSaveServerResult> {
		const view = await this.proxyPatch('wdtt-server', id, {
			enabled: config.enabled,
			statsLog: config.statsLog ?? '',
			config: toWdttServerPatch(config)
		});
		return { config: toWdttServerConfig(view) };
	}

	/**
	 * Режим NAT, политика и сегменты LAN правятся тем же PATCH, что и прочий
	 * конфиг: своих ручек у них больше нет. Ответ — принятое НАМЕРЕНИЕ, а не
	 * факт применения; применение доводит движок.
	 */
	async setWdttServerNATMode(id: string, mode: 'full' | 'internet-only' | 'none'): Promise<WdttSaveServerResult> {
		const view = await this.proxyPatch('wdtt-server', id, { config: { natMode: mode } });
		return { config: toWdttServerConfig(view) };
	}

	async setWdttServerPolicy(id: string, policy: string): Promise<WdttSaveServerResult> {
		const view = await this.proxyPatch('wdtt-server', id, { config: { policy } });
		return { config: toWdttServerConfig(view) };
	}

	async setWdttServerLANSegments(id: string, segments: string[]): Promise<WdttSaveServerResult> {
		const view = await this.proxyPatch('wdtt-server', id, { config: { lanSegments: segments } });
		return { config: toWdttServerConfig(view) };
	}

	async createWdttServer(name?: string, config?: WdttServerConfig): Promise<WdttServerInstance> {
		const view = await this.proxyCreate(
			'wdtt-server',
			name,
			config ? toWdttServerPatch(config) : undefined
		);
		return { id: view.id, name: view.name, config: toWdttServerConfig(view) };
	}

	async deleteWdttServer(id: string): Promise<void> {
		await this.proxyDelete('wdtt-server', id);
	}

	async renameWdttServer(id: string, name: string): Promise<void> {
		await this.proxyPatch('wdtt-server', id, { name });
	}

	async startWdttServerInstance(id: string): Promise<void> {
		await this.proxyPatch('wdtt-server', id, { enabled: true });
	}

	async stopWdttServerInstance(id: string): Promise<void> {
		await this.proxyPatch('wdtt-server', id, { enabled: false });
	}

	/** `mode` — режим ссылки (§11); пусто — режим записи (`relayMode`). */
	async generateWdttServerLink(
		id: string,
		opts?: { peer?: string; vkHashes?: string[]; name?: string; password?: string; mode?: 'wg' | 'raw' }
	): Promise<WdttGenerateLinkResult> {
		return this.request<WdttGenerateLinkResult>(instancePath('wdtt-server', id, '/link'), {
			method: 'POST',
			body: JSON.stringify(opts ?? {})
		});
	}

	async getWdttServerPanelUsers(serverId: string): Promise<WdttPanelUsersStatus> {
		return this.request(instancePath('wdtt-server', serverId, '/users'));
	}

	async addWdttServerPanelUser(
		serverId: string,
		opts: { password?: string; comment?: string; vkHash?: string }
	): Promise<WdttPanelUsersStatus> {
		return this.request(instancePath('wdtt-server', serverId, '/users'), {
			method: 'POST',
			body: JSON.stringify(opts)
		});
	}

	/** Переименование абонента: имя ложится в comment записи, состав не меняется. */
	async renameWdttServerPanelUser(
		serverId: string,
		password: string,
		name: string
	): Promise<WdttPanelUsersStatus> {
		return this.request(
			instancePath('wdtt-server', serverId, `/users/${encodeURIComponent(password)}`),
			{ method: 'PATCH', body: JSON.stringify({ name }) }
		);
	}

	async removeWdttServerPanelUser(
		serverId: string,
		password: string
	): Promise<WdttPanelUsersStatus> {
		return this.request(
			instancePath('wdtt-server', serverId, `/users/${encodeURIComponent(password)}`),
			{ method: 'DELETE' }
		);
	}
}
