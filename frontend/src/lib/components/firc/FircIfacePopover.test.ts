import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircGroup, FircGroupLive, FircInterfaceOption, FircProxyGroup } from '$lib/types/firc';
import type { FircGroupsRuntime } from '$lib/utils/fircGroups';
import FircIfacePopover from './FircIfacePopover.svelte';

const pg: FircProxyGroup = { id: 'pg1', name: 'Failover', interfaces: ['eth3', 'tunvless0'] };

function renderPopover(active: string) {
	const group: FircGroup = { id: 'g1', name: 'G', interface: [...pg.interfaces], enable: true, rules: [] };
	const live: FircGroupLive = {
		live: true,
		active,
		interfaces: pg.interfaces.map((name) => ({
			name,
			state: name === active ? ('alive' as const) : ('dead' as const),
			since: 0,
			error: '',
		})),
	};
	const rt: FircGroupsRuntime = {
		groups: [group],
		liveMap: { g1: live },
		tunnelsState: [],
		interfaces: [],
		resolvers: [],
		proxyGroups: [pg],
		ifaceProbes: {},
		tunnelProbes: {},
	};
	render(FircIfacePopover, {
		props: {
			group,
			rt,
			ifaceList: [...pg.interfaces],
			activeIface: active,
			isFallbackGroup: true,
			matchedPg: pg,
			probingIfaces: {},
			probingGroups: {},
			showOthers: false,
			ontoggleOthers: vi.fn(),
			onprobe: vi.fn(),
			onopenProxyGroups: vi.fn(),
			onclose: vi.fn(),
		},
	});
}

describe('FircIfacePopover', () => {
	afterEach(() => cleanup());

	it('names the active interface as primary or backup', () => {
		renderPopover('eth3');
		expect(
			screen.getByTitle(m.firc_fallback_active_primary_title({ iface: 'Direct (eth3) (eth3)' })),
		).toBeTruthy();
		cleanup();
		renderPopover('tunvless0');
		expect(
			screen.getByTitle(m.firc_fallback_active_backup_title({ iface: 'tunvless0 (tunvless0)' })),
		).toBeTruthy();
		expect(screen.queryByTitle(/^(Активен основной|Primary interface)/)).toBeNull();
	});

	it('keeps native t2s0 visible after switching group output away while hiding unselected AWGM sing-box t2s9', async () => {
		const ifaces: FircInterfaceOption[] = [
			{ id: 'nwg0', name: 'Office WG', role: 'tunnel' },
			{ id: 't2s0', ndmsName: 'Proxy0', role: 'tunnel' },
			{ id: 't2s9', name: 'Singbox Tunnel', ndmsName: 'Proxy9', role: 'tunnel', awgmSingboxProxy: true },
		];
		const group: FircGroup = { id: 'g1', name: 'G', interface: 't2s0', enable: true, rules: [] };
		const rt: FircGroupsRuntime = {
			groups: [group],
			liveMap: {},
			tunnelsState: [],
			interfaces: ifaces,
			resolvers: [],
			proxyGroups: [],
			ifaceProbes: {},
			tunnelProbes: {},
		};
		const onclose = vi.fn();
		render(FircIfacePopover, {
			props: {
				group,
				rt,
				ifaceList: ['t2s0'],
				activeIface: null,
				isFallbackGroup: false,
				matchedPg: undefined,
				probingIfaces: {},
				probingGroups: {},
				showOthers: false,
				ontoggleOthers: vi.fn(),
				onprobe: vi.fn(),
				onopenProxyGroups: vi.fn(),
				onclose,
			},
		});

		expect(screen.getByText('t2s0 — Proxy0')).toBeTruthy();
		expect(screen.queryByText('t2s9 — Singbox Tunnel')).toBeNull();

		await fireEvent.click(screen.getByText('nwg0 — Office WG'));
		expect(onclose).toHaveBeenCalledTimes(1);
		expect(group.interface).toBe('nwg0');

		cleanup();
		render(FircIfacePopover, {
			props: {
				group,
				rt,
				ifaceList: ['nwg0'],
				activeIface: null,
				isFallbackGroup: false,
				matchedPg: undefined,
				probingIfaces: {},
				probingGroups: {},
				showOthers: false,
				ontoggleOthers: vi.fn(),
				onprobe: vi.fn(),
				onopenProxyGroups: vi.fn(),
				onclose: vi.fn(),
			},
		});
		expect(screen.getByText('t2s0 — Proxy0')).toBeTruthy();
		expect(screen.queryByText('t2s9 — Singbox Tunnel')).toBeNull();
	});

	it('keeps Xray t2s* members visible when toggled off in a multi-interface chain and lets user re-add them', async () => {
		const ifaces: FircInterfaceOption[] = [
			{ id: 't2s0', name: '!Xray_Nuxt_t2s0', ndmsName: 'Proxy0', role: 'tunnel' },
			{ id: 't2s2', name: '!Xray_OD_PRG_t2s2', ndmsName: 'Proxy2', role: 'tunnel' },
			{ id: 't2s4', name: '!Xray_OD_Hels_t2s4', ndmsName: 'Proxy4', role: 'tunnel' },
		];
		const group: FircGroup = {
			id: 'g-xray',
			name: 'Xray Chain',
			interface: ['t2s0', 't2s4', 't2s2'],
			enable: true,
			rules: [],
		};
		const rt: FircGroupsRuntime = {
			groups: [group],
			liveMap: {
				'g-xray': {
					live: true,
					active: 't2s0',
					interfaces: [
						{ name: 't2s0', state: 'alive', since: 0, error: '' },
						{ name: 't2s4', state: 'alive', since: 0, error: '' },
						{ name: 't2s2', state: 'alive', since: 0, error: '' },
					],
				},
			},
			tunnelsState: [],
			interfaces: ifaces,
			resolvers: [],
			proxyGroups: [],
			ifaceProbes: {},
			tunnelProbes: {},
		};
		const renderChain = () => {
			cleanup();
			const ifaceList = Array.isArray(group.interface)
				? group.interface
				: group.interface
					? [group.interface]
					: [];
			return render(FircIfacePopover, {
				props: {
					group,
					rt,
					ifaceList,
					activeIface: 't2s0',
					isFallbackGroup: ifaceList.length >= 2,
					matchedPg: undefined,
					probingIfaces: {},
					probingGroups: {},
					showOthers: false,
					ontoggleOthers: vi.fn(),
					onprobe: vi.fn(),
					onopenProxyGroups: vi.fn(),
					onclose: vi.fn(),
				},
			});
		};

		const rankBtnFor = (label: string) => {
			const row = screen.getByText(label).closest('.iface-pop-row');
			const btn = row?.querySelector('button.iface-rank-btn') as HTMLButtonElement | null;
			expect(btn).toBeTruthy();
			return btn!;
		};

		renderChain();
		expect(rankBtnFor('t2s0 — !Xray_Nuxt_t2s0').textContent?.trim()).toBe('1');
		expect(rankBtnFor('t2s2 — !Xray_OD_PRG_t2s2').textContent?.trim()).toBe('3');
		expect(rankBtnFor('t2s4 — !Xray_OD_Hels_t2s4').textContent?.trim()).toBe('2');

		await fireEvent.click(rankBtnFor('t2s2 — !Xray_OD_PRG_t2s2'));
		expect(group.interface).toEqual(['t2s0', 't2s4']);
		renderChain();
		expect(screen.getByText('t2s2 — !Xray_OD_PRG_t2s2')).toBeTruthy();
		expect(rankBtnFor('t2s2 — !Xray_OD_PRG_t2s2').textContent?.trim()).toBe('');

		await fireEvent.click(rankBtnFor('t2s4 — !Xray_OD_Hels_t2s4'));
		expect(group.interface).toBe('t2s0');
		renderChain();
		expect(screen.getByText('t2s4 — !Xray_OD_Hels_t2s4')).toBeTruthy();
		expect(rankBtnFor('t2s4 — !Xray_OD_Hels_t2s4').textContent?.trim()).toBe('');

		await fireEvent.click(rankBtnFor('t2s2 — !Xray_OD_PRG_t2s2'));
		expect(group.interface).toEqual(['t2s0', 't2s2']);
	});
});
