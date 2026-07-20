import type { Awg3Tunnel } from '$lib/types';
import { FreeturnClient } from './clientFreeturn';

export class Awg3Client extends FreeturnClient {
	// ─────────────────────────────────────────────
	// #region AWG3 imported endpoints
	// ─────────────────────────────────────────────

	async awg3List(): Promise<Awg3Tunnel[]> {
		return this.request<Awg3Tunnel[]>('/awg3-endpoints');
	}

	// config — сырой JSON конфига AWG3 (json.RawMessage на бэке). Строку
	// парсим в объект, чтобы поле config уходило как настоящий JSON, а не
	// JSON-строка в кавычках. JSON.parse на некорректной строке бросит
	// SyntaxError — это ок: модалка pre-валидирует и передаёт объект.
	async awg3Import(tag: string, config: unknown): Promise<Awg3Tunnel[]> {
		const body = JSON.stringify({
			tag,
			config: typeof config === 'string' ? JSON.parse(config) : config,
		});
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
