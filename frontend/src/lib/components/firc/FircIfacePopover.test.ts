import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircGroup, FircGroupLive, FircProxyGroup } from '$lib/types/firc';
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
});
