import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { WarpConfig, WarpProfile, WarpScoutState } from '$lib/types/warpscout';
import { warpScanPush } from '$lib/stores/warpscout';
import { defaultFilters } from './warpscoutUi';

const api = vi.hoisted(() => ({
	warpscoutState: vi.fn(),
	warpscoutTargets: vi.fn(),
	warpscoutSaveConfig: vi.fn(),
	warpscoutSetAddress: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api }));

import WarpScoutTab from './WarpScoutTab.svelte';

function profile(id: string): WarpProfile {
	return {
		id,
		name: id,
		enabled: false,
		targetMode: 'kernel',
		targetId: `t-${id}`,
		filters: defaultFilters(0),
		checkIntervalSec: 60,
		failThreshold: 2,
		cooldownSec: 120,
	};
}

function state(config: Partial<WarpConfig> = {}, over: Partial<WarpScoutState> = {}): WarpScoutState {
	return {
		engine: { path: '/opt/sbin/awgm-warpscout', available: true },
		account: null,
		accountUnreadable: false,
		defaultWan: 'ppp0',
		routes: [{ name: 'nwg1', label: 'Office', kind: 'tunnel', up: true }],
		config: {
			profileMode: 'single',
			profiles: [profile('a')],
			awg: { signatureProfile: 'quic_initial', jc: 4, jmin: 40, jmax: 70 },
			...config,
		},
		scan: { running: false, total: 0, done: 0, hits: [] },
		watch: [],
		stats: [],
		history: [],
		...over,
	};
}

async function mount(initial: WarpScoutState) {
	api.warpscoutState.mockResolvedValue(initial);
	api.warpscoutTargets.mockResolvedValue([]);
	const view = render(WarpScoutTab);
	await waitFor(() => expect(view.container.querySelector('.top-bar')).not.toBeNull());
	return view;
}

function chip(container: HTMLElement, index: number): HTMLButtonElement {
	return container.querySelectorAll<HTMLButtonElement>('.status-chip')[index];
}

function acctState(): WarpScoutState {
	return state(
		{},
		{
			account: {
				id: 'dev-1',
				warpPlus: false,
				accountType: 'free',
				addressV4: '172.16.0.2',
				addressV6: '',
				publicKey: 'pub-1',
				peerPublicKey: 'peer-1',
				createdAt: '2026-11-12T10:00:00Z',
			},
			usedIpv4: ['172.16.0.2'],
		},
	);
}

describe('WarpScoutTab', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		warpScanPush.set(null);
	});
	afterEach(cleanup);

	it('saves the scan route without the unsaved profile edits', async () => {
		const initial = state();
		const { container } = await mount(initial);

		// Несохранённая правка профиля: переключение режима.
		const multi = [...container.querySelectorAll<HTMLButtonElement>('.auto-card button')].find(
			(b) => b.getAttribute('aria-pressed') === 'false' || b.getAttribute('aria-checked') === 'false',
		);
		expect(multi).toBeDefined();
		await fireEvent.click(multi!);
		await waitFor(() => expect(container.querySelector('.save-bar')).not.toBeNull());

		api.warpscoutSaveConfig.mockImplementation(async (cfg: WarpConfig) => cfg);
		await fireEvent.click(chip(container, 1));
		const option = [...container.querySelectorAll<HTMLButtonElement>('.route-opt')].at(-1)!;
		await fireEvent.click(option);

		await waitFor(() => expect(api.warpscoutSaveConfig).toHaveBeenCalledTimes(1));
		const sent = api.warpscoutSaveConfig.mock.calls[0][0] as WarpConfig;
		expect(sent.scanInterface).toBe('nwg1');
		expect(sent.profileMode).toBe('single');
		// Правка профилей осталась в черновике и по-прежнему не сохранена.
		await tick();
		expect(container.querySelector('.save-bar')).not.toBeNull();
	});

	it('closes a popover on Escape and returns focus to its chip', async () => {
		const { container } = await mount(state());
		const routeChip = chip(container, 1);
		await fireEvent.click(routeChip);
		expect(container.querySelector('[role="dialog"]')).not.toBeNull();

		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(container.querySelector('[role="dialog"]')).toBeNull();
		expect(document.activeElement).toBe(routeChip);
	});

	it('applies a scan event without another state request', async () => {
		const { container } = await mount(state());
		const calls = api.warpscoutState.mock.calls.length;

		warpScanPush.set({ running: true, owner: 'manual', total: 200, done: 50, hits: [] });
		await tick();

		const bar = container.querySelector('[role="progressbar"]');
		expect(bar?.getAttribute('aria-valuenow')).toBe('25');
		expect(bar?.getAttribute('aria-label')).toBeTruthy();
		expect(api.warpscoutState.mock.calls.length).toBe(calls);
	});

	it('ignores a scan event that was published before the tab opened', async () => {
		warpScanPush.set({ running: true, owner: 'manual', total: 10, done: 5, hits: [] });
		const { container } = await mount(state());
		await tick();
		expect(container.querySelector('[role="progressbar"]')).toBeNull();
	});

	it('warns on IPv4 conflict in account popover, suggests free IP and saves it', async () => {
		const initial = acctState();
		api.warpscoutState.mockResolvedValue(initial);
		api.warpscoutTargets.mockResolvedValue([
			{
				mode: 'kernel',
				id: 't1',
				name: 'WARP HEL',
				iface: 'nwg3',
				endpoint: '162.159.192.1:2408',
				addressV4: '172.16.0.2',
				isWarp: true,
				running: true,
				protected: false,
			},
		]);
		api.warpscoutSetAddress.mockImplementation(async (addressV4: string) => ({
			...initial.account!,
			addressV4,
		}));

		const view = render(WarpScoutTab);
		await waitFor(() => expect(view.container.querySelector('.top-bar')).not.toBeNull());
		await fireEvent.click(chip(view.container, 0));

		const suggestBtn = await waitFor(() => {
			const b = view.container.querySelector<HTMLButtonElement>('.ipv4-suggest-btn');
			expect(b).not.toBeNull();
			return b!;
		});
		expect(suggestBtn.textContent).toContain('172.16.0.3');
		await fireEvent.click(suggestBtn);

		const input = view.container.querySelector<HTMLInputElement>('#warp-acct-ipv4')!;
		expect(input.value).toBe('172.16.0.3');

		const form = input.closest('form')!;
		await fireEvent.submit(form);
		await waitFor(() => expect(api.warpscoutSetAddress).toHaveBeenCalledWith('172.16.0.3'));
	});
});
