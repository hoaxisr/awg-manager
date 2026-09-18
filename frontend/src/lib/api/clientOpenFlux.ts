import type {
	OpenFluxClientConfig,
	OpenFluxClientInstance,
	OpenFluxConfig,
	OpenFluxLinkPayload,
	OpenFluxServerConfig,
	OpenFluxServerInstance,
	OpenFluxShareLink,
	OpenFluxStatus
} from '$lib/types';
import { WdttClient } from './clientWdtt';
import {
	instancePath,
	toOpenFluxClientConfig,
	toOpenFluxClientPatch,
	toOpenFluxConfig,
	toOpenFluxServerConfig,
	toOpenFluxServerPatch,
	toOpenFluxStatus
} from './proxyInstances';

/**
 * Клиент подсистемы OpenFlux: SOCKS5-выход на роутере (exit-вкладка) и
 * выходная нода с транспортами-релеями (share-вкладка). Поверхность та же,
 что у freeturn: список инстансов одна на все роли, разница — в kind'ах и
 * decode-ручке своей ссылки.
 */
export class OpenFluxClient extends WdttClient {
	async getOpenFluxConfig(): Promise<OpenFluxConfig> {
		return toOpenFluxConfig(await this.proxyList());
	}

	async getOpenFluxStatus(): Promise<OpenFluxStatus> {
		const [list, install] = await Promise.all([
			this.proxyList(),
			this.proxyInstallStatus('openflux')
		]);
		return toOpenFluxStatus(list, install);
	}

	async createOpenFluxClient(name?: string): Promise<OpenFluxClientInstance> {
		const view = await this.proxyCreate('openflux-client', name);
		return { id: view.id, name: view.name, config: toOpenFluxClientConfig(view) };
	}

	async createOpenFluxServer(name?: string): Promise<OpenFluxServerInstance> {
		const view = await this.proxyCreate('openflux-server', name);
		return { id: view.id, name: view.name, config: toOpenFluxServerConfig(view) };
	}

	async updateOpenFluxClientInstance(
		id: string,
		config: OpenFluxClientConfig
	): Promise<OpenFluxClientConfig> {
		const view = await this.proxyPatch('openflux-client', id, {
			enabled: config.enabled,
			config: toOpenFluxClientPatch(config)
		});
		return toOpenFluxClientConfig(view);
	}

	async updateOpenFluxServerInstance(
		id: string,
		config: OpenFluxServerConfig
	): Promise<OpenFluxServerConfig> {
		const view = await this.proxyPatch('openflux-server', id, {
			enabled: config.enabled,
			config: toOpenFluxServerPatch(config)
		});
		return toOpenFluxServerConfig(view);
	}

	async renameOpenFluxClient(id: string, name: string): Promise<void> {
		await this.proxyPatch('openflux-client', id, { name });
	}

	async renameOpenFluxServer(id: string, name: string): Promise<void> {
		await this.proxyPatch('openflux-server', id, { name });
	}

	async deleteOpenFluxClient(id: string): Promise<void> {
		await this.proxyDelete('openflux-client', id);
	}

	async deleteOpenFluxServer(id: string): Promise<void> {
		await this.proxyDelete('openflux-server', id);
	}

	async startOpenFluxClient(id: string): Promise<void> {
		await this.proxyPatch('openflux-client', id, { enabled: true });
	}

	async stopOpenFluxClient(id: string): Promise<void> {
		await this.proxyPatch('openflux-client', id, { enabled: false });
	}

	async startOpenFluxServer(id: string): Promise<void> {
		await this.proxyPatch('openflux-server', id, { enabled: true });
	}

	async stopOpenFluxServer(id: string): Promise<void> {
		await this.proxyPatch('openflux-server', id, { enabled: false });
	}

	async installOpenFlux(): Promise<void> {
		await this.proxyInstall('openflux');
	}

	/** Ссылка абоненту openflux-сервера: openflux:// + готовая команда клиента. */
	async generateOpenFluxLink(serverId: string): Promise<OpenFluxShareLink> {
		return this.request<OpenFluxShareLink>(
			instancePath('openflux-server', serverId, '/link'),
			{ method: 'POST', body: JSON.stringify({}) }
		);
	}

	async decodeOpenFluxLink(link: string): Promise<OpenFluxLinkPayload> {
		return this.request<OpenFluxLinkPayload>('/proxyrt/openflux/link/decode', {
			method: 'POST',
			body: JSON.stringify({ link })
		});
	}
}
