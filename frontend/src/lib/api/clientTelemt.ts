import type { TelemtConfig, TelemtStatus } from '$lib/types';
import { WarpScoutClient } from './clientWarpScout';

export class TelemtClient extends WarpScoutClient {
	// ─────────────────────────────────────────────
	// #region Telegram MTProto Proxy (telemt)
	// ─────────────────────────────────────────────

	async telemtStatus(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/status');
	}

	async telemtConfig(): Promise<TelemtConfig> {
		return this.request<TelemtConfig>('/telemt/config');
	}

	async telemtSaveConfig(cfg: TelemtConfig): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/config', {
			method: 'POST',
			body: JSON.stringify(cfg),
		});
	}

	async telemtInstall(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/install', { method: 'POST' });
	}

	async telemtUpdate(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/update', { method: 'POST' });
	}

	async telemtStart(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/start', { method: 'POST' });
	}

	async telemtStop(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/stop', { method: 'POST' });
	}

	async telemtRestart(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/restart', { method: 'POST' });
	}

	async telemtUninstall(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/uninstall', { method: 'POST' });
	}

	// #endregion
}
