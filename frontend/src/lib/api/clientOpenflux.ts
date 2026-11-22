import { FircClient } from './clientFirc';
import type {
	OpenFluxClientConfig,
	OpenFluxClientStatus,
	OpenFluxConfig,
	OpenFluxParsedLink,
	OpenFluxStatus,
} from '$lib/types';

let cachedOpenfluxCsrfToken: string | null = null;
let openfluxCsrfPromise: Promise<string> | null = null;

export function clearOpenFluxCSRFCache(): void {
	cachedOpenfluxCsrfToken = null;
	openfluxCsrfPromise = null;
}

export class OpenFluxClient extends FircClient {
	override async logout(): Promise<void> {
		clearOpenFluxCSRFCache();
		await super.logout();
	}

	async openfluxCsrfToken(forceRefresh = false): Promise<string> {
		if (!forceRefresh && cachedOpenfluxCsrfToken) {
			return cachedOpenfluxCsrfToken;
		}
		if (openfluxCsrfPromise) {
			return openfluxCsrfPromise;
		}
		openfluxCsrfPromise = (async () => {
			try {
				const res = await this.request<{ token: string }>('/openflux/csrf', {
					method: 'GET',
				});
				cachedOpenfluxCsrfToken = res.token;
				return res.token;
			} finally {
				openfluxCsrfPromise = null;
			}
		})();
		return openfluxCsrfPromise;
	}

	private async openfluxMutate<T>(endpoint: string, body?: unknown): Promise<T> {
		let token = await this.openfluxCsrfToken();
		const send = (t: string) =>
			this.request<T>(endpoint, {
				method: 'POST',
				headers: {
					'X-CSRF-Token': t,
					'X-Requested-With': 'XMLHttpRequest',
				},
				body: body !== undefined ? JSON.stringify(body) : undefined,
			});

		try {
			return await send(token);
		} catch (err: unknown) {
			const status = (err as { status?: number } | null)?.status;
			const msg = err instanceof Error ? err.message : String(err ?? '');
			if (status === 403 || msg.includes('CSRF')) {
				clearOpenFluxCSRFCache();
				token = await this.openfluxCsrfToken(true);
				return await send(token);
			}
			throw err;
		}
	}

	async openfluxStatus(): Promise<OpenFluxStatus> {
		return this.request<OpenFluxStatus>('/openflux/status');
	}

	async openfluxSaveConfig(config: OpenFluxConfig): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/config', config);
	}

	async openfluxInstall(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/install');
	}

	async openfluxStart(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/start');
	}

	async openfluxStop(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/stop');
	}

	async openfluxRestart(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/restart');
	}

	async openfluxGetLink(): Promise<{ link: string }> {
		return this.openfluxMutate<{ link: string }>('/openflux/link');
	}

	async openfluxGetLogs(lines = 100): Promise<{ logs: string }> {
		return this.request<{ logs: string }>(`/openflux/logs?lines=${lines}`);
	}

	async openfluxClientStatus(): Promise<OpenFluxClientStatus> {
		return this.request<OpenFluxClientStatus>('/openflux/client/status');
	}

	async openfluxSaveClientConfig(config: OpenFluxClientConfig): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/client/config', config);
	}

	async openfluxClientStart(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/client/start');
	}

	async openfluxClientStop(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/client/stop');
	}

	async openfluxClientRestart(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/client/restart');
	}

	async openfluxClientDelete(): Promise<OpenFluxStatus> {
		return this.openfluxMutate<OpenFluxStatus>('/openflux/client/delete');
	}

	async openfluxParseLink(link: string): Promise<OpenFluxParsedLink> {
		return this.openfluxMutate<OpenFluxParsedLink>('/openflux/client/parse-link', { link });
	}

	async openfluxGetClientLogs(lines = 100): Promise<{ logs: string }> {
		return this.request<{ logs: string }>(`/openflux/client/logs?lines=${lines}`);
	}
}
