import type {
	AdaptiveRoutingSettings,
	AdaptiveRoutingStatusResponse,
	EgressRef,
	EgressesResponse,
	LearnedDataResponse,
	OperationalState,
	PreviewResponse,
	SusaninLogEvent,
} from '$lib/types/adaptiveRouting';
import { Awg3Client } from './clientAwg3';

export class AdaptiveRoutingClient extends Awg3Client {
	// ─────────────────────────────────────────────
	// #region Adaptive Routing (Susanin) endpoints
	// ─────────────────────────────────────────────

	async getAdaptiveRoutingStatus(): Promise<AdaptiveRoutingStatusResponse> {
		return this.request<AdaptiveRoutingStatusResponse>('/adaptive-routing/status');
	}

	async getAdaptiveRoutingSettings(): Promise<AdaptiveRoutingSettings> {
		return this.request<AdaptiveRoutingSettings>('/adaptive-routing/settings');
	}

	async updateAdaptiveRoutingSettings(
		settings: AdaptiveRoutingSettings
	): Promise<AdaptiveRoutingStatusResponse> {
		return this.request<AdaptiveRoutingStatusResponse>('/adaptive-routing/settings', {
			method: 'PUT',
			body: JSON.stringify(settings),
		});
	}

	async getAdaptiveRoutingEgresses(): Promise<EgressesResponse> {
		return this.request<EgressesResponse>('/adaptive-routing/egresses');
	}

	async previewAdaptiveRouting(settings: AdaptiveRoutingSettings): Promise<PreviewResponse> {
		return this.request<PreviewResponse>('/adaptive-routing/preview', {
			method: 'POST',
			body: JSON.stringify(settings),
		});
	}

	async applyAdaptiveRouting(
		settings: AdaptiveRoutingSettings
	): Promise<AdaptiveRoutingStatusResponse> {
		return this.request<AdaptiveRoutingStatusResponse>('/adaptive-routing/apply', {
			method: 'POST',
			body: JSON.stringify(settings),
		});
	}

	async startAdaptiveRouting(): Promise<{ state: OperationalState }> {
		return this.request<{ state: OperationalState }>('/adaptive-routing/start', {
			method: 'POST',
		});
	}

	async stopAdaptiveRouting(): Promise<{ state: OperationalState }> {
		return this.request<{ state: OperationalState }>('/adaptive-routing/stop', {
			method: 'POST',
		});
	}

	async restartAdaptiveRouting(): Promise<{ state: OperationalState }> {
		return this.request<{ state: OperationalState }>('/adaptive-routing/restart', {
			method: 'POST',
		});
	}

	async installAdaptiveRouting(): Promise<AdaptiveRoutingStatusResponse> {
		return this.request<AdaptiveRoutingStatusResponse>('/adaptive-routing/install', {
			method: 'POST',
		});
	}

	async uninstallAdaptiveRouting(): Promise<AdaptiveRoutingStatusResponse> {
		return this.request<AdaptiveRoutingStatusResponse>('/adaptive-routing/uninstall', {
			method: 'POST',
		});
	}

	async testAdaptiveRoutingEgress(
		target: EgressRef
	): Promise<{ available: boolean; interface: string; reason?: string }> {
		return this.request<{ available: boolean; interface: string; reason?: string }>(
			'/adaptive-routing/test-egress',
			{
				method: 'POST',
				body: JSON.stringify({ target }),
			}
		);
	}

	async getAdaptiveRoutingLearned(): Promise<LearnedDataResponse> {
		return this.request<LearnedDataResponse>('/adaptive-routing/learned');
	}

	async forgetAdaptiveRoute(
		target: string,
		protocol = 'all'
	): Promise<{ forgotten: string; protocol: string }> {
		return this.request<{ forgotten: string; protocol: string }>('/adaptive-routing/forget', {
			method: 'POST',
			body: JSON.stringify({ target, protocol }),
		});
	}

	async clearAdaptiveRoutingCache(): Promise<{ cleared: boolean }> {
		return this.request<{ cleared: boolean }>('/adaptive-routing/cache/clear', {
			method: 'POST',
		});
	}

	async getAdaptiveRoutingLogs(limit = 50): Promise<{ events: SusaninLogEvent[] }> {
		return this.request<{ events: SusaninLogEvent[] }>(`/adaptive-routing/logs?limit=${limit}`);
	}

	// #endregion
}
