import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { api, ApiGatewayError } from './client';

describe('ApiClient error shape', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	it('attaches status and parsed body to the thrown Error on 422', async () => {
		const fakeBody = {
			sbCheck:
				'FATAL[0000] initialize dns router: dns rule[0]: rule-set not found: geosite-google\n: exit status 1',
		};
		globalThis.fetch = vi.fn().mockResolvedValue(
			new Response(JSON.stringify(fakeBody), {
				status: 422,
				headers: { 'Content-Type': 'application/json' },
			}),
		);

		let caught: unknown;
		try {
			await api.singboxRouterStagingApply();
		} catch (e) {
			caught = e;
		}
		expect(caught).toBeInstanceOf(Error);
		const err = caught as Error & { status?: number; body?: unknown };
		expect(err.status).toBe(422);
		expect(err.body).toEqual(fakeBody);
	});

	it('attaches status and body on a standard envelope error too', async () => {
		const fakeBody = { error: true, message: 'тест', code: 'TEST' };
		globalThis.fetch = vi.fn().mockResolvedValue(
			new Response(JSON.stringify(fakeBody), {
				status: 400,
				headers: { 'Content-Type': 'application/json' },
			}),
		);

		let caught: unknown;
		try {
			await api.singboxRouterStagingApply();
		} catch (e) {
			caught = e;
		}
		const err = caught as Error & { status?: number; body?: unknown };
		expect(err.status).toBe(400);
		expect(err.body).toEqual(fakeBody);
		expect(err.message).toBe('тест');
	});

	it('serializes multi-select log filters as repeated query params', async () => {
		let capturedUrl = '';
		globalThis.fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
			capturedUrl = String(input);
			return new Response(
				JSON.stringify({
					success: true,
					data: {
						enabled: true,
						logs: [],
						total: 0,
						bucket: 'app',
						bufferSize: 0,
						bufferCapacity: 5000,
					},
				}),
				{
					status: 200,
					headers: { 'Content-Type': 'application/json' },
				},
			);
		});

		await api.getLogs({
			bucket: 'singbox',
			groups: ['singbox'],
			subgroups: ['inbound', 'dns'],
			limit: 200,
			offset: 0,
		});

		const url = new URL(capturedUrl, 'http://test.local');
		expect(url.pathname).toBe('/api/logs');
		expect(url.searchParams.get('bucket')).toBe('singbox');
		expect(url.searchParams.getAll('group')).toEqual(['singbox']);
		expect(url.searchParams.getAll('subgroup')).toEqual(['inbound', 'dns']);
		expect(url.searchParams.get('limit')).toBe('200');
		expect(url.searchParams.get('offset')).toBe('0');
	});
});

describe('ApiClient gateway/HTML error classification', () => {
	const originalFetch = globalThis.fetch;

	const NGINX_504 =
		'<html>\r\n<head><title>504 Gateway Time-out</title></head>\r\n' +
		'<body>\r\n<center><h1>504 Gateway Time-out</h1></center>\r\n' +
		'<hr><center>nginx</center>\r\n</body>\r\n</html>';

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	function mockResponse(status: number, body: string, contentType: string) {
		globalThis.fetch = vi.fn().mockResolvedValue(
			new Response(body, { status, headers: { 'Content-Type': contentType } }),
		);
	}

	async function catchFrom(promise: Promise<unknown>): Promise<unknown> {
		try {
			await promise;
		} catch (e) {
			return e;
		}
		return undefined;
	}

	it('classifies nginx 504 HTML as ApiGatewayError without leaking markup', async () => {
		mockResponse(504, NGINX_504, 'text/html');
		const err = await catchFrom(api.singboxRouterStagingApply());
		expect(err).toBeInstanceOf(ApiGatewayError);
		const gw = err as ApiGatewayError;
		expect(gw.status).toBe(504);
		expect(gw.code).toBe('GATEWAY_ERROR');
		expect(gw.message).toBe(
			'Шлюз не дождался ответа от роутера (504). Операция может продолжаться в фоне.',
		);
		expect(gw.message).not.toContain('<');
	});

	it('classifies 502 HTML as ApiGatewayError', async () => {
		mockResponse(502, '<html><body>502 Bad Gateway</body></html>', 'text/html');
		const err = await catchFrom(api.singboxRouterStagingApply());
		expect(err).toBeInstanceOf(ApiGatewayError);
		expect((err as ApiGatewayError).status).toBe(502);
		expect((err as ApiGatewayError).message).not.toContain('<');
	});

	it('classifies non-JSON 503 as ApiGatewayError, keeps JSON 503 message intact', async () => {
		mockResponse(503, '<html><body>503</body></html>', 'text/html');
		const gw = await catchFrom(api.singboxRouterStagingApply());
		expect(gw).toBeInstanceOf(ApiGatewayError);
		expect((gw as ApiGatewayError).status).toBe(503);

		mockResponse(503, JSON.stringify({ error: true, message: 'идёт операция' }), 'application/json');
		const jsonErr = await catchFrom(api.singboxRouterStagingApply());
		expect(jsonErr).toBeInstanceOf(Error);
		expect(jsonErr).not.toBeInstanceOf(ApiGatewayError);
		expect((jsonErr as Error).message).toBe('идёт операция');
	});

	it('never includes HTML body for non-gateway statuses', async () => {
		mockResponse(500, '<html><body>Internal Server Error</body></html>', 'text/html');
		const err = await catchFrom(api.singboxRouterStagingApply());
		expect(err).toBeInstanceOf(Error);
		expect(err).not.toBeInstanceOf(ApiGatewayError);
		expect((err as Error).message).toBe('Ошибка сервера (500)');
	});

	it('keeps plain-text bodies in the message for non-gateway statuses', async () => {
		mockResponse(500, 'boom from upstream', 'text/plain');
		const err = await catchFrom(api.singboxRouterStagingApply());
		expect((err as Error).message).toBe('Ошибка сервера (500): boom from upstream');
	});
});

// Issue #795: 409 на удалении несёт две несовместимые формы тела. Раньше
// любой 409 считался «туннель используется» и открывал модалку со списком
// зависимостей — для занятого замка она пустая и врёт.
describe('deleteTunnel: два смысла 409', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	const reply = (body: unknown) =>
		vi.fn().mockResolvedValue(
			new Response(JSON.stringify(body), {
				status: 409,
				headers: { 'Content-Type': 'application/json' },
			}),
		);

	it('tunnel_referenced по-прежнему даёт ошибку с details для модалки', async () => {
		globalThis.fetch = reply({
			error: 'tunnel_referenced',
			details: { tunnelId: 'awg11', deviceProxy: true, routerRules: [], routerOther: [] },
		});

		const err = await api.deleteTunnel('awg11').catch((e: unknown) => e);
		expect((err as Error).message).toBe('tunnel_referenced');
		expect((err as Error & { details?: { deviceProxy?: boolean } }).details?.deviceProxy).toBe(
			true,
		);
	});

	it('занятый замок даёт сообщение бэкенда и НЕ выдаёт себя за tunnel_referenced', async () => {
		globalThis.fetch = reply({
			error: true,
			message: 'операция с туннелем уже выполняется — дождитесь завершения предыдущей (awg11)',
			code: 'OPERATION_IN_PROGRESS',
		});

		const err = await api.deleteTunnel('awg11').catch((e: unknown) => e);
		expect((err as Error).message).toContain('уже выполняется');
		expect((err as Error & { details?: unknown }).details).toBeUndefined();
	});

	it('тело null не роняет клиент TypeError-ом', async () => {
		globalThis.fetch = vi.fn().mockResolvedValue(
			new Response('null', {
				status: 409,
				headers: { 'Content-Type': 'application/json' },
			}),
		);

		const err = await api.deleteTunnel('awg11').catch((e: unknown) => e);
		expect((err as Error).message).toBe('Конфликт: операция отклонена (409)');
	});
});
