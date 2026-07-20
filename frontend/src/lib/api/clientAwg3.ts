import type { Awg3Tunnel } from '$lib/types';
import { FreeturnClient } from './clientFreeturn';

export class Awg3Client extends FreeturnClient {
	// ─────────────────────────────────────────────
	// #region AWG3 imported endpoints
	// ─────────────────────────────────────────────

	async awg3List(): Promise<Awg3Tunnel[]> {
		return this.request<Awg3Tunnel[]>('/awg3-endpoints');
	}

	// config — сырой JSON конфига AWG3 (json.RawMessage на бэке): строка
	// уходит как есть, объект сериализуется, чтобы поле config оставалось
	// сырым JSON, а не JSON-строкой в кавычках.
	async awg3Import(tag: string, config: unknown): Promise<Awg3Tunnel[]> {
		const configJson = typeof config === 'string' ? config : JSON.stringify(config);
		const body = `{"tag":${JSON.stringify(tag)},"config":${configJson}}`;
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
