import { Awg3Client } from './clientAwg3';
import type {
	WarpAccount,
	WarpApplyRequest,
	WarpApplyResult,
	WarpConfig,
	WarpScanRequest,
	WarpScanStatus,
	WarpScoutState,
	WarpTunnelRef,
} from '$lib/types/warpscout';

// «WARP Сканер» (вкладка страницы диагностики): учётная запись Cloudflare
// WARP, ручной скан узлов, применение узла и профили автопилота.
export class WarpScoutClient extends Awg3Client {
	async warpscoutState(): Promise<WarpScoutState> {
		return this.request<WarpScoutState>('/warpscout/state');
	}

	async warpscoutTargets(): Promise<WarpTunnelRef[]> {
		const list = await this.request<WarpTunnelRef[] | null>('/warpscout/targets');
		return list ?? [];
	}

	async warpscoutRegister(): Promise<WarpAccount> {
		return this.request<WarpAccount>('/warpscout/account/register', {
			method: 'POST',
			body: JSON.stringify({}),
		});
	}

	async warpscoutLicense(license: string): Promise<WarpAccount> {
		return this.request<WarpAccount>('/warpscout/account/license', {
			method: 'POST',
			body: JSON.stringify({ license }),
		});
	}

	async warpscoutDeleteAccount(): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/warpscout/account/delete', {
			method: 'POST',
			body: JSON.stringify({}),
		});
	}

	async warpscoutScanStart(req: WarpScanRequest): Promise<WarpScanStatus> {
		return this.request<WarpScanStatus>('/warpscout/scan/start', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async warpscoutScanStop(): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/warpscout/scan/stop', {
			method: 'POST',
			body: JSON.stringify({}),
		});
	}

	async warpscoutApply(req: WarpApplyRequest): Promise<WarpApplyResult> {
		return this.request<WarpApplyResult>('/warpscout/apply', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async warpscoutSaveConfig(cfg: WarpConfig): Promise<WarpConfig> {
		return this.request<WarpConfig>('/warpscout/config', {
			method: 'POST',
			body: JSON.stringify(cfg),
		});
	}

	async warpscoutResetStats(profileId = ''): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/warpscout/stats/reset', {
			method: 'POST',
			body: JSON.stringify({ profileId }),
		});
	}
}
