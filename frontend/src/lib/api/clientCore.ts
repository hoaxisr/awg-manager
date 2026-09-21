// Ядро API-клиента: транспорт (request), обработчики 401/сети, gateway-
// классификация. Доменные методы живут в client*-слоях (см. client.ts).
import { validateApiResponse } from './validate';
import { isMockDevMode as envIsMockDevMode } from '$lib/env';
import type { Subscription, SingboxTunnel } from '$lib/types';


export type TrafficPeriod = '5m' | '10m' | '30m' | '1h' | '3h' | '6h' | '12h' | '24h';

const DIAGNOSTICS_SANITIZE_STORAGE_KEY = 'awgm.diagnostics.sanitizeLogs';

export function readDiagnosticsSanitizedPreference(): boolean {
	if (typeof localStorage === 'undefined') {
		return true;
	}
	// Missing key means safe default. The diagnostics privacy store persists
	// enabled as "1" and disabled/raw reveal as "0".
	return localStorage.getItem(DIAGNOSTICS_SANITIZE_STORAGE_KEY) !== '0';
}

export interface ApiResponse<T> {
	success?: boolean;
	error?: boolean;
	data?: T;
	message?: string;
	code?: string;
}

/**
 * Ошибка уровня шлюза (nginx перед awg-manager): 502/503/504 без JSON-тела.
 * Отдельный класс, чтобы вызывающие могли отличить «шлюз не дождался ответа»
 * (операция может продолжаться в фоне) от настоящей ошибки приложения.
 */
export class ApiGatewayError extends Error {
	readonly code = 'GATEWAY_ERROR';
	readonly status: number;

	constructor(message: string, status: number) {
		super(message);
		this.name = 'ApiGatewayError';
		this.status = status;
	}
}

const GATEWAY_MESSAGES: Record<number, string> = {
	502: 'Шлюз не смог получить ответ от роутера (502). Операция может продолжаться в фоне.',
	503: 'Сервер временно недоступен (503). Операция может продолжаться в фоне.',
	504: 'Шлюз не дождался ответа от роутера (504). Операция может продолжаться в фоне.',
};


export class CoreClient {
	protected baseUrl = '/api';
	protected onUnauthorized?: () => void;
	protected onConnectionLost?: () => void;
	protected abortController = new AbortController();

	setUnauthorizedHandler(handler: () => void) {
		this.onUnauthorized = handler;
	}

	setConnectionLostHandler(handler: () => void) {
		this.onConnectionLost = handler;
	}

	abortAll() {
		this.abortController.abort();
		this.abortController = new AbortController();
	}

	protected async request<T>(
		endpoint: string,
		options: RequestInit = {}
	): Promise<T> {
		const url = `${this.baseUrl}${endpoint}`;

		let response: Response;
		try {
			response = await fetch(url, {
				...options,
				credentials: 'same-origin',
				signal: this.abortController.signal,
				headers: {
					'Content-Type': 'application/json',
					...options.headers
				}
			});
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') {
				throw e;
			}
			this.onConnectionLost?.();
			throw new Error('Ошибка сети: не удалось подключиться к серверу');
		}

		// Handle 401 Unauthorized
		if (response.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}

		const contentType = response.headers.get('content-type') || '';

		// Handle 503 Service Unavailable — preserve server message when present.
		if (response.status === 503) {
			if (contentType.includes('application/json')) {
				let message = 'Сервер временно недоступен';
				try {
					const body = (await response.json()) as ApiResponse<unknown>;
					if (body.message) message = body.message;
				} catch {
					// keep default
				}
				throw new Error(message);
			}
			// Non-JSON 503 — страница шлюза (nginx), разметку не показываем.
			throw new ApiGatewayError(GATEWAY_MESSAGES[503], 503);
		}

		if (!contentType.includes('application/json')) {
			const text = await response.text();
			// Gateway-ошибки (nginx перед awg-manager) отдают HTML-страницы —
			// классифицируем по статусу и никогда не показываем разметку.
			if (response.status in GATEWAY_MESSAGES) {
				throw new ApiGatewayError(GATEWAY_MESSAGES[response.status], response.status);
			}
			const looksLikeHtml =
				contentType.includes('text/html') || text.trimStart().startsWith('<');
			if (looksLikeHtml) {
				throw new Error(`Ошибка сервера (${response.status})`);
			}
			throw new Error(`Ошибка сервера (${response.status}): ${text.substring(0, 100)}`);
		}

		let data: ApiResponse<T>;
		try {
			data = await response.json();
		} catch {
			throw new Error(`Некорректный ответ сервера (${response.status})`);
		}

		if (!response.ok || data.error) {
			const err: Error & { status?: number; body?: unknown } = new Error(
				data.message || `Ошибка запроса (${response.status})`
			);
			err.status = response.status;
			err.body = data;
			throw err;
		}

		// Runtime-проверка конверта по схеме из swagger: каст `as T` ниже
		// подкреплён фактической валидацией, а не доверием.
		validateApiResponse(options.method ?? 'GET', endpoint, data);

		return data.data as T;
	}

	protected isMockDevMode(): boolean {
		return envIsMockDevMode();
	}

	protected ensureMockSubscriptionMembers(sub: Subscription): Subscription {
		if (!this.isMockDevMode()) return sub;
		const baseMembers = Array.isArray(sub.members) ? [...sub.members] : [];
		const normalized: Subscription = {
			...sub,
			id: sub.id || 'sub-demo',
			label: sub.label || 'Demo Provider',
			selectorTag: sub.selectorTag || 'sub-demo',
			inboundTag: sub.inboundTag || 'sub-demo-in',
			listenPort: sub.listenPort || 11000,
			enabled: sub.enabled ?? true,
			lastError: '',
		};
		if (baseMembers.length >= 3) {
			const memberTags = baseMembers.map((m) => m.tag).filter(Boolean);
			const activeMember = normalized.activeMember && memberTags.includes(normalized.activeMember)
				? normalized.activeMember
				: memberTags[0] || '';
			return {
				...normalized,
				memberTags,
				members: baseMembers,
				activeMember,
				enabled: normalized.enabled,
			};
		}

		const seed = baseMembers[0] ?? {
			tag: `${normalized.selectorTag || 'sub-demo'}-001`,
			label: 'DE vless-tcp-reality #1',
			protocol: 'vless',
			server: 'demo-1.example.com',
			port: 443,
			sni: 'cdn.example.com',
			transport: 'tcp',
			security: 'reality',
		};

		for (let i = baseMembers.length; i < 3; i++) {
			const n = i + 1;
			const tag = `${normalized.selectorTag || 'sub-demo'}-${String(n).padStart(3, '0')}`;
			baseMembers.push({
				...seed,
				tag,
				label: `DE vless-tcp-reality #${n}`,
				server: `demo-${n}.example.com`,
				port: 443 + i,
			});
		}

		const memberTags = baseMembers.map((m) => m.tag).filter(Boolean);
		const activeMember = normalized.activeMember && memberTags.includes(normalized.activeMember)
			? normalized.activeMember
			: memberTags[0] || '';

		return {
			...normalized,
			memberTags,
			members: baseMembers,
			activeMember,
			enabled: normalized.enabled,
		};
	}

	protected buildMockOutboundFromTunnel(t: SingboxTunnel): Record<string, unknown> {
		const outbound: Record<string, unknown> = {
			type: t.protocol,
			tag: t.tag,
			server: t.server,
			server_port: t.port,
		};

		if (t.protocol === 'vless') {
			outbound.uuid = '00000000-0000-4000-8000-000000000001';
			const tls: Record<string, unknown> = {};
			if (t.sni) tls.server_name = t.sni;
			if (t.fingerprint) tls.utls = { enabled: true, fingerprint: t.fingerprint };
			if (t.security === 'reality') {
				tls.enabled = true;
				tls.reality = { enabled: true, public_key: 'EXAMPLE_PUBLIC_KEY', short_id: 'abcd1234' };
			} else if (t.security === 'tls') {
				tls.enabled = true;
			}
			const transport: Record<string, unknown> = { type: t.transport || 'tcp' };
			if (t.transport === 'grpc') transport.service_name = 'demo-service';
			outbound.transport = transport;
			if (Object.keys(tls).length > 0) outbound.tls = tls;
		} else if (t.protocol === 'hysteria2') {
			outbound.password = 'demo-password';
			outbound.tls = { enabled: true, server_name: t.sni || t.server };
		} else if (t.protocol === 'naive') {
			outbound.username = t.username || 'demo-user';
			outbound.password = 'demo-password';
			outbound.tls = { enabled: true, server_name: t.sni || t.server };
		} else if (t.protocol === 'trojan') {
			outbound.password = 'demo-password';
			outbound.tls = { enabled: true, server_name: t.sni || t.server };
			if (t.transport && t.transport !== 'tcp') {
				outbound.transport = { type: t.transport };
			}
		} else if (t.protocol === 'shadowsocks') {
			outbound.method = 'aes-256-gcm';
			outbound.password = 'demo-password';
		} else if (t.protocol === 'mieru') {
			outbound.transport = (t.transport || 'tcp').toUpperCase() === 'UDP' ? 'UDP' : 'TCP';
			outbound.username = t.username || 'demo-user';
			outbound.password = 'demo-password';
			outbound.server_ports = ['8443', '1000:2000'];
			outbound.multiplexing = 'MULTIPLEXING_LOW';
		} else if (t.protocol === 'trusttunnel') {
			outbound.username = t.username || 'demo-user';
			outbound.password = 'demo-password';
			outbound.quic = false;
			outbound.tls = { enabled: true, server_name: t.sni || t.server };
		}

		return outbound;
	}

}
