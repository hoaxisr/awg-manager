import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api/client';
import { singboxStatus } from './singbox';
import { singboxProxies } from './singboxProxies';
import type { SingboxStatus } from '$lib/types';

function makeStatus(overrides: Partial<SingboxStatus> = {}): SingboxStatus {
	return {
		installed: false,
		running: false,
		tunnelCount: 0,
		proxyComponent: false,
		ndmsProxyEnabled: false,
		requiredVersion: '1.11.0',
		updateAvailable: false,
		...overrides,
	};
}

describe('singboxProxies', () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('awaits singboxStatus when cold and skips proxies/list if sing-box is not running', async () => {
		const statusSpy = vi.spyOn(api, 'singboxGetStatus').mockResolvedValue(makeStatus({ installed: false, running: false }));
		const listSpy = vi.spyOn(api, 'singboxRouterListProxies').mockResolvedValue({ groups: [] });

		await singboxProxies.refetch();

		expect(statusSpy).toHaveBeenCalledTimes(1);
		expect(listSpy).not.toHaveBeenCalled();
		expect(singboxProxies.peek().data).toEqual([]);
	});

	it('fetches proxies/list when sing-box is installed and running', async () => {
		singboxStatus.applyMutationResponse(makeStatus({ installed: true, running: true }));
		const listSpy = vi.spyOn(api, 'singboxRouterListProxies').mockResolvedValue({
			groups: [
				{
					tag: 'proxy',
					type: 'selector',
					now: 'vless-1',
					members: [{ tag: 'vless-1', type: 'vless', lastDelay: 42 }],
				},
			],
		});

		await singboxProxies.refetch();

		expect(listSpy).toHaveBeenCalledTimes(1);
		expect(singboxProxies.peek().data?.[0]?.tag).toBe('proxy');
	});
});
