import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { api } from './client';

function mockFetch(body: unknown, status = 200) {
	return vi.fn(async (_input?: RequestInfo | URL, _init?: RequestInit) =>
		new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
	);
}

describe('MCP key API', () => {
	const realFetch = globalThis.fetch;
	beforeEach(() => void 0);
	afterEach(() => {
		globalThis.fetch = realFetch;
	});

	it('getMcpKeys unwraps data.keys', async () => {
		const f = mockFetch({ success: true, data: { keys: [{ id: 'k1', name: 'laptop', createdAt: '2026-09-02T10:00:00Z' }] } });
		globalThis.fetch = f;
		const keys = await api.getMcpKeys();
		expect(keys).toEqual([{ id: 'k1', name: 'laptop', createdAt: '2026-09-02T10:00:00Z' }]);
		expect(f.mock.calls[0][0]).toBe('/api/mcp/keys');
	});

	it('createMcpKey posts the name and returns the plaintext once', async () => {
		const f = mockFetch({ success: true, data: { id: 'k2', name: 'phone', createdAt: '2026-09-02T10:00:00Z', key: 'awgm_abc' } });
		globalThis.fetch = f;
		const created = await api.createMcpKey('phone');
		expect(created.key).toBe('awgm_abc');
		const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe('/api/mcp/keys/create');
		expect(init.method).toBe('POST');
		expect(JSON.parse(String(init.body))).toEqual({ name: 'phone' });
	});

	it('revokeMcpKey posts the id', async () => {
		const f = mockFetch({ success: true, data: { revoked: true } });
		globalThis.fetch = f;
		await api.revokeMcpKey('k2');
		const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe('/api/mcp/keys/revoke');
		expect(JSON.parse(String(init.body))).toEqual({ id: 'k2' });
	});
});
