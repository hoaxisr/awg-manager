import { FircClient } from './clientFirc';
import type {
	OpenFluxClientConfig,
	OpenFluxClientStatus,
	OpenFluxConfig,
	OpenFluxParsedLink,
	OpenFluxStatus,
} from '$lib/types';

export class OpenFluxClient extends FircClient {
	async openfluxStatus(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/status');
	}

	async openfluxSaveConfig(config: OpenFluxConfig): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/config', {
			method: 'POST',
			body: JSON.stringify(config),
		});
	}

	async openfluxInstall(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/install', {
			method: 'POST',
		});
	}

	async openfluxStart(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/start', {
			method: 'POST',
		});
	}

	async openfluxStop(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/stop', {
			method: 'POST',
		});
	}

	async openfluxRestart(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/restart', {
			method: 'POST',
		});
	}

	async openfluxGetLink(): Promise<{ link: string }> {
		return this.request<{ link: string }>('/openflux/link');
	}

	async openfluxGetLogs(lines = 100): Promise<{ logs: string }> {
		return this.request<{ logs: string }>(`/openflux/logs?lines=${lines}`);
	}

	async openfluxClientStatus(): Promise<OpenFluxClientStatus> {
		return this.request<OpenFluxClientStatus>('/openflux/client/status');
	}

	async openfluxSaveClientConfig(config: OpenFluxClientConfig): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/client/config', {
			method: 'POST',
			body: JSON.stringify(config),
		});
	}

	async openfluxClientStart(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/client/start', {
			method: 'POST',
		});
	}

	async openfluxClientStop(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/client/stop', {
			method: 'POST',
		});
	}

	async openfluxClientRestart(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/client/restart', {
			method: 'POST',
		});
	}

	async openfluxClientDelete(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/client/delete', {
			method: 'POST',
		});
	}

	async openfluxParseLink(link: string): Promise<OpenFluxParsedLink> {
		return this.request<OpenFluxParsedLink>('/openflux/client/parse-link', {
			method: 'POST',
			body: JSON.stringify({ link }),
		});
	}

	async openfluxGetClientLogs(lines = 100): Promise<{ logs: string }> {
		return this.request<{ logs: string }>(`/openflux/client/logs?lines=${lines}`);
	}
}
