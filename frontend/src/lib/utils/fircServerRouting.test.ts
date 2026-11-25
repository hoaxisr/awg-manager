import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { FircExtConfig } from '$lib/types/firc';

const { fircGetExt, fircSaveExt, fircGetStatus } = vi.hoisted(() => ({
	fircGetStatus: vi.fn<() => Promise<{ foreign?: object }>>(),
	fircGetExt: vi.fn<() => Promise<FircExtConfig>>(),
	fircSaveExt: vi.fn<(cfg: FircExtConfig) => Promise<FircExtConfig>>(),
}));

vi.mock('$lib/api/client', () => ({
	api: {
		fircGetExt,
		fircSaveExt,
		fircGetStatus,
	},
}));

import { setFircServerRoutingEnabled } from './fircServerRouting';

describe('setFircServerRoutingEnabled', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		fircGetStatus.mockResolvedValue({});
	});

	it('при стороннем firc ничего не сохраняет: настройка sing-box уже записана, отказ был бы ложной ошибкой', async () => {
		fircGetStatus.mockResolvedValue({ foreign: { package: 'firc' } });
		expect(await setFircServerRoutingEnabled('managed:Wireguard5', true)).toBeNull();
		expect(fircSaveExt).not.toHaveBeenCalled();
	});

	it('enabling managed:Wireguard5 preserves globalTarget, proxyGroups, keenDnsCloud, and appends managed:Wireguard5', async () => {
		const initial: FircExtConfig = {
			globalTarget: 'tunvless0',
			proxyGroups: [{ id: 'pg1', name: 'Main', interfaces: ['tunvless0', 'eth3'] }],
			keenDnsCloud: { enabled: true, proxyGroupId: 'pg1', interfaces: ['tunvless0', 'eth3'] },
			serverRouting: {
				enabledServers: ['wdtt_server'],
				wdttServer: true,
			},
		};
		fircGetExt.mockResolvedValue(initial);
		fircSaveExt.mockImplementation(async (cfg) => cfg);

		const saved = await setFircServerRoutingEnabled('managed:Wireguard5', true);

		expect(fircSaveExt).toHaveBeenCalledTimes(1);
		const payload = fircSaveExt.mock.calls[0][0];
		expect(payload.globalTarget).toBe('tunvless0');
		expect(payload.proxyGroups).toEqual(initial.proxyGroups);
		expect(payload.keenDnsCloud).toEqual(initial.keenDnsCloud);
		expect(payload.serverRouting.enabledServers).toEqual(['wdtt_server', 'managed:Wireguard5']);
		expect(saved).toEqual(payload);
	});

	it('disabling managed:Wireguard5 with [awg_server] removes both managed:Wireguard5 and legacy awg_server', async () => {
		const initial: FircExtConfig = {
			globalTarget: '',
			proxyGroups: [],
			keenDnsCloud: { enabled: false, proxyGroupId: '', interfaces: [] },
			serverRouting: {
				enabledServers: ['managed:Wireguard5', 'awg_server', 'wdtt_server'],
				awgServer: true,
				wdttServer: true,
			},
		};
		fircGetExt.mockResolvedValue(initial);
		fircSaveExt.mockImplementation(async (cfg) => cfg);

		await setFircServerRoutingEnabled('managed:Wireguard5', false, ['awg_server']);

		expect(fircSaveExt).toHaveBeenCalledTimes(1);
		const payload = fircSaveExt.mock.calls[0][0];
		expect(payload.serverRouting.enabledServers).toEqual(['wdtt_server']);
		expect(payload.serverRouting.awgServer).toBe(false);
		expect(payload.serverRouting.wdttServer).toBe(true);
	});

	it('disabling wg:Wireguard2 with [keenetic_wg] removes both while keeping other enabled servers intact', async () => {
		const initial: FircExtConfig = {
			globalTarget: 'tunvless0',
			proxyGroups: [],
			keenDnsCloud: { enabled: false, proxyGroupId: '', interfaces: [] },
			serverRouting: {
				enabledServers: ['managed:Wireguard5', 'wg:Wireguard2', 'keenetic_wg', 'keenetic_sstp'],
			},
		};
		fircGetExt.mockResolvedValue(initial);
		fircSaveExt.mockImplementation(async (cfg) => cfg);

		await setFircServerRoutingEnabled('wg:Wireguard2', false, ['keenetic_wg']);

		expect(fircSaveExt).toHaveBeenCalledTimes(1);
		const payload = fircSaveExt.mock.calls[0][0];
		expect(payload.serverRouting.enabledServers).toEqual(['managed:Wireguard5', 'keenetic_sstp']);
	});
});
