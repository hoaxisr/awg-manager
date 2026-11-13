import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './client';
import type { AutolistStatus } from '$lib/types';

const originalFetch = globalThis.fetch;

beforeEach(() => {
	vi.restoreAllMocks();
});

afterEach(() => {
	globalThis.fetch = originalFetch;
});

function reply(body: unknown, status = 200) {
	return vi.fn().mockResolvedValue(
		new Response(JSON.stringify(body), {
			status,
			headers: { 'Content-Type': 'application/json' },
		}),
	);
}

describe('Autolist client', () => {
	it('returns the status from the envelope and tolerates a missing payload', async () => {
		const full: AutolistStatus = {
			running: true,
			installed: true,
			enabled: true,
			captureActive: true,
			capturePaused: false,
			activeChecks: 1,
			queuedChecks: 0,
			addsUsedHour: 0,
			addsLimit: 50,
			candidates: [],
			history: [],
		};
		globalThis.fetch = reply({ success: true, data: { autolistStatus: full } });
		await expect(api.getAutolistStatus()).resolves.toEqual(full);

		globalThis.fetch = reply({ success: true, data: {} });
		await expect(api.getAutolistStatus()).resolves.toEqual({
			running: false,
			installed: false,
			enabled: false,
			captureActive: false,
			capturePaused: false,
			activeChecks: 0,
			queuedChecks: 0,
			addsUsedHour: 0,
			addsLimit: 0,
			candidates: [],
			history: [],
		});
	});

	it('sends the satellite proxy envelope { path, body } for config reads and writes', async () => {
		let capturedBody = '';
		globalThis.fetch = vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
			capturedBody = String(init?.body ?? '');
			return new Response(JSON.stringify({ success: true, data: { autoAdd: false, tunnelProbe: true } }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			});
		});

		await api.getAutolistConfig();
		expect(JSON.parse(capturedBody)).toEqual({ path: '/config' });

		await api.setAutolistConfig({
			autoAdd: false,
			targetInterface: 'nwg0',
			resolveVia: '',
			wanCheckUrl: 'https://ya.ru/robots.txt',
			wanMonitorEnabled: false,
			tunnelProbe: true,
			excludeDomains: [],
			excludeRuleSets: [],
			udpEnabled: false,
			verbose: false,
		});
		const written = JSON.parse(capturedBody);
		expect(written.path).toBe('/config');
		expect(written.body.autoAdd).toBe(false);
		expect(written.body.targetInterface).toBe('nwg0');
	});

	it('sends candidate actions as { path, body: { id } }', async () => {
		const bodies: unknown[] = [];
		globalThis.fetch = vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
			const payload = JSON.parse(String(init?.body ?? '')) as { body?: { id?: string }; id?: string };
			bodies.push(payload);
			const id = payload.body?.id ?? payload.id ?? '';
			return new Response(JSON.stringify({ success: true, data: { results: { [id]: null } } }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			});
		});

		await api.addAutolistCandidate('domain|example.com|tcp|443');
		await api.forgetAutolistCandidate('domain|example.com|tcp|443');
		await api.restoreAutolistCandidate('domain|example.com|tcp|443');
		await api.removeAutolistCandidate('domain|example.com|tcp|443');
		await api.dismissAutolistCandidate('domain|example.com|tcp|443');

		// dismiss — отдельная ручка AWGM без конверта {path}; остальные через /autolist/proxy.
		expect(bodies.slice(0, 4)).toEqual([
			{ path: '/add', body: { id: 'domain|example.com|tcp|443' } },
			{ path: '/forget', body: { id: 'domain|example.com|tcp|443' } },
			{ path: '/restore', body: { id: 'domain|example.com|tcp|443' } },
			{ path: '/remove', body: { id: 'domain|example.com|tcp|443' } },
		]);
		expect(bodies[4]).toEqual({ id: 'domain|example.com|tcp|443' });
	});

	it('sends batch actions as { path, body: { ids } } and dismiss batches without the proxy envelope', async () => {
		const bodies: unknown[] = [];
		globalThis.fetch = vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
			const payload = JSON.parse(String(init?.body ?? '')) as { body?: { ids?: string[] }; ids?: string[] };
			bodies.push(payload);
			const ids = payload.body?.ids ?? payload.ids ?? [];
			const results = Object.fromEntries(ids.map((id) => [id, null]));
			return new Response(JSON.stringify({ success: true, data: { results } }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			});
		});

		const ids = ['domain|a.example|tcp|443', 'ip|198.51.100.7'];
		const add = await api.addAutolistCandidates(ids);
		const forget = await api.forgetAutolistCandidates(ids);
		const hide = await api.dismissAutolistCandidates(ids);

		// add/forget идут через прокси-конверт, dismiss — отдельной ручкой AWGM.
		expect(bodies[0]).toEqual({ path: '/add', body: { ids } });
		expect(bodies[1]).toEqual({ path: '/forget', body: { ids } });
		expect(bodies[2]).toEqual({ ids });
		expect(add).toEqual({ ok: ids, failed: [] });
		expect(forget).toEqual({ ok: ids, failed: [] });
		expect(hide).toEqual({ ok: ids, failed: [] });
	});

	it('returns per-ID failures from a batch instead of throwing on HTTP 200', async () => {
		const ids = ['domain|a.example|tcp|443', 'ip|198.51.100.7'];
		globalThis.fetch = reply({
			success: true,
			data: { results: { 'domain|a.example|tcp|443': null, 'ip|198.51.100.7': 'apply failed after content update' } },
		});

		const result = await api.addAutolistCandidates(ids);
		expect(result.ok).toEqual(['domain|a.example|tcp|443']);
		expect(result.failed).toEqual([{ id: 'ip|198.51.100.7', error: 'apply failed after content update' }]);
	});

	it('rejects a batch response without the results envelope', async () => {
		globalThis.fetch = reply({ success: true, data: {} });
		await expect(api.addAutolistCandidates(['domain|a.example|tcp|443'])).rejects.toThrow('missing candidate results');
	});

	it('skips the request for an empty batch', async () => {
		const fetchMock = vi.fn();
		globalThis.fetch = fetchMock;
		await expect(api.addAutolistCandidates([])).resolves.toEqual({ ok: [], failed: [] });
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('reports a missing per-ID entry in the batch envelope as a failure', async () => {
		globalThis.fetch = reply({ success: true, data: { results: { 'domain|a.example|tcp|443': null } } });
		const result = await api.addAutolistCandidates(['domain|a.example|tcp|443', 'ip|198.51.100.7']);
		expect(result.failed).toEqual([{ id: 'ip|198.51.100.7', error: 'missing candidate result' }]);
	});

	it('turns a per-ID writer result into a raw error instead of false success', async () => {
		const id = 'domain|example.com|tcp|443';
		globalThis.fetch = reply({ success: true, data: { results: { [id]: 'apply failed after content update' } } });
		await expect(api.addAutolistCandidate(id)).rejects.toThrow('apply failed after content update');
	});

	it('rejects a config response without a boolean autoAdd', async () => {
		globalThis.fetch = reply({ success: true, data: { tunnelProbe: true } });
		await expect(api.getAutolistConfig()).rejects.toThrow();
	});

	it('keeps the raw backend message from a failed config save', async () => {
		const raw = 'autoAdd requires tunnelProbe';
		globalThis.fetch = reply({ error: true, message: raw }, 400);
		const err = await api.setAutolistConfig({
			autoAdd: true,
			tunnelProbe: false,
		} as never).catch((e: unknown) => e as Error);
		expect(err).toBeInstanceOf(Error);
		expect((err as Error).message).toBe(raw);
	});

	it('clears history through the proxy envelope { path: "/history/clear", body: {} }', async () => {
		let capturedBody = '';
		globalThis.fetch = vi.fn().mockImplementation(async (_input: RequestInfo | URL, init?: RequestInit) => {
			capturedBody = String(init?.body ?? '');
			return new Response(JSON.stringify({ success: true, data: { cleared: true } }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' },
			});
		});

		await expect(api.clearAutolistHistory()).resolves.toBeUndefined();

		// Тело {} обязательно: прокси превращает его в POST сателлита
		// (без тела был бы GET). Затронута только история.
		expect(JSON.parse(capturedBody)).toEqual({ path: '/history/clear', body: {} });
	});

	it('surfaces the raw satellite error from a failed history clear', async () => {
		globalThis.fetch = reply({ error: true, message: 'history file is not writable' }, 400);
		await expect(api.clearAutolistHistory()).rejects.toThrow('history file is not writable');
	});
});
