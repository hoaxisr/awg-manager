import { describe, expect, it, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';

const proxyInstallStatusMock = vi.fn(async (subsystem: string) => ({
	binariesPresent: true,
	installedVersion: '1.4.0-3',
	instances: subsystem === 'wdtt' ? 2 : 0,
}));

vi.mock('$lib/api/client', () => ({
	api: {
		proxyInstallStatus: (s: string) => proxyInstallStatusMock(s),
	},
}));

describe('proxyInstallStatus', () => {
	beforeEach(() => {
		proxyInstallStatusMock.mockClear();
	});

	it('удаление инстанса инвалидирует статус подсистемы', async () => {
		const { proxyInstallStatus } = await import('./proxyInstall');
		const { invalidateResource } = await import('./storeRegistry');

		// Подписка запускает первую загрузку: без неё invalidate только пометит
		// store устаревшим, и проверять было бы нечего.
		const stop = proxyInstallStatus.wdtt.subscribe(() => {});
		await vi.waitFor(() => expect(get(proxyInstallStatus.wdtt).data?.instances).toBe(2));
		const afterFirst = proxyInstallStatusMock.mock.calls.length;

		// Ровно то, что делает SSE-обработчик, получив resource:invalidated с
		// ресурсом proxy.instances (бэкенд публикует его при create/delete).
		invalidateResource('proxy.instances');
		await vi.waitFor(() =>
			expect(proxyInstallStatusMock.mock.calls.length).toBeGreaterThan(afterFirst),
		);
		stop();
	});

	it('у каждой подсистемы свой статус', async () => {
		const { proxyInstallStatus } = await import('./proxyInstall');
		const stopW = proxyInstallStatus.wdtt.subscribe(() => {});
		const stopF = proxyInstallStatus.freeturn.subscribe(() => {});
		await vi.waitFor(() => {
			expect(get(proxyInstallStatus.wdtt).data?.instances).toBe(2);
			expect(get(proxyInstallStatus.freeturn).data?.instances).toBe(0);
		});
		stopW();
		stopF();
	});
});
