import type { Awg3Tunnel } from '$lib/types';
import { WdttClient } from './clientWdtt';

export class Awg3Client extends WdttClient {
	// ─────────────────────────────────────────────
	// #region AWG3 imported endpoints
	// ─────────────────────────────────────────────

	async awg3List(): Promise<Awg3Tunnel[]> {
		return this.request<Awg3Tunnel[]>('/awg3-endpoints');
	}

	// config — либо JSON-объект endpoint'а, либо строка с текстом .conf.
	// Объект уходит как настоящий JSON; строка — как JSON-строка в кавычках,
	// backend детектит её по первому байту и парсит как .conf.
	async awg3Import(tag: string, config: unknown): Promise<Awg3Tunnel[]> {
		const body = JSON.stringify({ tag, config });
		return this.request<Awg3Tunnel[]>('/awg3-endpoints', { method: 'POST', body });
	}

	async awg3Delete(id: string): Promise<Awg3Tunnel[]> {
		return this.request<Awg3Tunnel[]>(`/awg3-endpoints/${encodeURIComponent(id)}`, {
			method: 'DELETE',
		});
	}

	async awg3Rename(id: string, tag: string): Promise<Awg3Tunnel[]> {
		return this.request<Awg3Tunnel[]>(`/awg3-endpoints/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ tag }),
		});
	}

	// Переиспользует singbox delay-check: AWG3-endpoint виден sing-box'у как
	// обычный outbound по своему тегу.
	async awg3DelayCheck(tag: string): Promise<{ tag: string; delay: number }> {
		return this.request(`/singbox/tunnels/delay-check?tag=${encodeURIComponent(tag)}`, {
			method: 'POST',
		});
	}

	// #endregion
}
