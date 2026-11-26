import { afterEach, describe, expect, it } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render } from '@testing-library/svelte';
import type { FircExtConfig, FircInterfaceOption } from '$lib/types/firc';
import FircSettingsKeenDnsSection from './FircSettingsKeenDnsSection.svelte';

function makeExtConfig(globalTarget: string, keenDnsIfaces: string[]): FircExtConfig {
	return {
		globalTarget,
		proxyGroups: [],
		groupLinks: {},
		keenDnsCloud: {
			enabled: true,
			proxyGroupId: '',
			interfaces: keenDnsIfaces,
		},
		serverRouting: { enabledServers: [] },
	};
}

const sampleInterfaces: FircInterfaceOption[] = [
	{ id: 't2s0', name: '!Xray_Nuxt_t2s0', ndmsName: 'Proxy0', role: 'tunnel' },
	{ id: 't2s9', name: 'Singbox Owned', ndmsName: 'Proxy9', role: 'tunnel', awgmSingboxProxy: true },
];

describe('FircSettingsKeenDnsSection interface options', () => {
	afterEach(() => cleanup());

	it('includes non-AWGM t2s* interfaces while hiding unselected sing-box proxies', () => {
		const { container } = render(FircSettingsKeenDnsSection, {
			props: {
				extConfig: makeExtConfig('', ['t2s0']),
				keenDnsSyncError: '',
				interfaces: sampleInterfaces,
				onopenproxygroups: () => {},
			},
		});

		const globalSelect = container.querySelector('#firc-global') as HTMLSelectElement;
		const kdSelect = container.querySelector('#firc-keendns-target') as HTMLSelectElement;

		expect(globalSelect.querySelector('option[value="iface:t2s0"]')).toBeTruthy();
		expect(globalSelect.querySelector('option[value="iface:t2s9"]')).toBeNull();

		expect(kdSelect.querySelector('option[value="iface:t2s0"]')).toBeTruthy();
		expect(kdSelect.querySelector('option[value="iface:t2s9"]')).toBeNull();
		expect(kdSelect.value).toBe('iface:t2s0');
	});

	it('preserves currently selected legacy awgmSingboxProxy until user selects another target', async () => {
		const extConfig = makeExtConfig('iface:t2s9', ['t2s9']);
		const { container, rerender } = render(FircSettingsKeenDnsSection, {
			props: {
				extConfig,
				keenDnsSyncError: '',
				interfaces: sampleInterfaces,
				onopenproxygroups: () => {},
			},
		});

		const globalSelect = container.querySelector('#firc-global') as HTMLSelectElement;
		const kdSelect = container.querySelector('#firc-keendns-target') as HTMLSelectElement;

		expect(globalSelect.querySelector('option[value="iface:t2s9"]')).toBeTruthy();
		expect(globalSelect.value).toBe('iface:t2s9');
		expect(kdSelect.querySelector('option[value="iface:t2s9"]')).toBeTruthy();
		expect(kdSelect.value).toBe('iface:t2s9');

		await fireEvent.change(globalSelect, { target: { value: 'iface:t2s0' } });
		await fireEvent.change(kdSelect, { target: { value: 'iface:t2s0' } });
		expect(extConfig.globalTarget).toBe('iface:t2s0');
		expect(extConfig.keenDnsCloud.interfaces).toEqual(['t2s0']);
		await rerender({
			extConfig: structuredClone(extConfig),
			keenDnsSyncError: '',
			interfaces: sampleInterfaces,
			onopenproxygroups: () => {},
		});

		expect(globalSelect.querySelector('option[value="iface:t2s9"]')).toBeNull();
		expect(kdSelect.querySelector('option[value="iface:t2s9"]')).toBeNull();
		expect(globalSelect.value).toBe('iface:t2s0');
		expect(kdSelect.value).toBe('iface:t2s0');
	});
});
