import { describe, it, expect, vi, afterEach } from 'vitest';
import { readBackendInstanceId, requestDaemonRestart } from './daemonRestart';

afterEach(() => vi.unstubAllGlobals());

describe('daemonRestart', () => {
	it('обрыв соединения — демон ушёл в перезапуск', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch'); }));
		await expect(requestDaemonRestart()).resolves.toBe('network-drop');
	});

	it('401 — истёкшая сессия, а не успех', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })));
		await expect(requestDaemonRestart()).rejects.toThrow();
	});

	it('instanceId из /api/health; пустой — null', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => Response.json({ data: { instanceId: 'abc' } })));
		await expect(readBackendInstanceId()).resolves.toBe('abc');
		vi.stubGlobal('fetch', vi.fn(async () => Response.json({ data: { instanceId: '' } })));
		await expect(readBackendInstanceId()).resolves.toBeNull();
	});
});
