import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { WarpConfig, WarpFindJunkStatus, WarpProfile, WarpScoutState, WarpSwapEvent } from '$lib/types/warpscout';
import { warpFindJunkPush, warpScanPush } from '$lib/stores/warpscout';
import { defaultFilters } from './warpscoutUi';

// Панель Dropdown читает ResizeObserver при открытии; в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'ResizeObserver', {
		writable: true,
		configurable: true,
		value: class {
			observe() {}
			unobserve() {}
			disconnect() {}
		},
	});
});

const api = vi.hoisted(() => ({
	warpscoutState: vi.fn(),
	warpscoutTargets: vi.fn(),
	warpscoutSaveConfig: vi.fn(),
	warpscoutSetAddress: vi.fn(),
	warpscoutFindJunkStart: vi.fn(),
	warpscoutFindJunkStop: vi.fn(),
	warpscoutFindJunkApply: vi.fn(),
	warpscoutClearHistory: vi.fn(),
	warpscoutApply: vi.fn(),
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
		warpFindJunkPush.set(null);
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
			const b = Array.from(view.container.querySelectorAll<HTMLButtonElement>('button')).find(
				(x) => x.textContent?.includes('172.16.0.3'),
			);
			expect(b).toBeDefined();
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

	it('clamps scan limit input to 1..100 on typing and restores default on blur when empty', async () => {
		const { container } = await mount(acctState());
		const input = container.querySelector<HTMLInputElement>('.limit-input')!;
		expect(input).not.toBeNull();
		expect(input.value).toBe('20');

		await fireEvent.input(input, { target: { value: '200' } });
		expect(input.value).toBe('100');

		await fireEvent.input(input, { target: { value: '1005' } });
		expect(input.value).toBe('100');

		await fireEvent.input(input, { target: { value: '' } });
		expect(input.value).toBe('');

		await fireEvent.blur(input);
		expect(input.value).toBe('20');
	});

	it('shows WARP routes in route picker and copies obfuscation from existing tunnel', async () => {
		const initial = state(
			{},
			{
				routes: [
					{ name: 'nwg1', label: 'Office', kind: 'tunnel', up: true },
					{
						name: 'nwg3',
						label: 'WARP HEL',
						kind: 'warp',
						up: true,
						tunnelMode: 'native',
						tunnelId: 'awg50',
					},
				],
				obfuscationSources: [
					{
						id: 'awg20',
						name: 'NuxtAWG',
						iface: 'opkgtun20',
						mode: 'kernel',
						running: true,
						isWarp: false,
						awg: {
							signatureProfile: 'custom',
							jc: 6,
							jmin: 15,
							jmax: 65,
							i1: '<b 0xdead><r 4>',
						},
					},
				],
			},
		);
		const { container } = await mount(initial);

		// Route picker lists the WARP-in-WARP group option
		await fireEvent.click(chip(container, 1));
		expect(container.textContent).toContain('WARP HEL');
		await fireEvent.keyDown(window, { key: 'Escape' });

		// AWG popover copies parameters from an existing tunnel
		await fireEvent.click(chip(container, 0));
		const copyOption = () => screen.getByRole('option', { name: /NuxtAWG/ });
		await fireEvent.click(screen.getByLabelText(/Copy from existing tunnel|Скопировать из рабочего туннеля/));
		await fireEvent.click(copyOption());
		await tick();
		expect(chip(container, 0).textContent).toContain('Jc 6');
	});

	it('streams find-junk SSE attempts, guards zero/incomplete results, and applies best without clobbering unsaved profile draft', async () => {
		const initial = state();
		const { container } = await mount(initial);

		// Вносим несохранённую правку профиля автопилота до работы с find-junk.
		const multi = [...container.querySelectorAll<HTMLButtonElement>('.auto-card button')].find(
			(b) => b.getAttribute('aria-pressed') === 'false' || b.getAttribute('aria-checked') === 'false',
		);
		expect(multi).toBeDefined();
		await fireEvent.click(multi!);
		await waitFor(() => expect(container.querySelector('.save-bar')).not.toBeNull());

		// Открываем поповер обфускации AWG (chip 0 при account=null).
		await fireEvent.click(chip(container, 0));
		const dialog = () => container.querySelector('[role="dialog"]')!;
		expect(dialog()).not.toBeNull();

		// 1) При нулевых/неполных результатах кнопка «Применить лучший» не появляется.
		const zeroOrIncomplete: WarpFindJunkStatus = {
			runId: 1,
			running: false,
			attempt: 2,
			maxAttempts: 8,
			thresholdPct: 95,
			attempts: [
				{
					attempt: 1,
					awg: { signatureProfile: 'dns', jc: 4, jmin: 20, jmax: 50 },
					profile: 'dns',
					working: 0,
					scanned: 15,
					total: 15,
					pct: 0,
					durationMs: 2100,
				},
				{
					attempt: 2,
					awg: { signatureProfile: 'stun', jc: 5, jmin: 25, jmax: 75 },
					profile: 'stun',
					working: 10,
					scanned: 10,
					total: 15,
					pct: 67,
					durationMs: 1800,
					incomplete: true,
				},
			],
		};
		warpFindJunkPush.set(zeroOrIncomplete);
		await tick();

		expect(dialog().querySelectorAll('.junk-row').length).toBe(2);
		expect(dialog().textContent).toMatch(/неполн\.|partial/);
		expect(dialog().querySelector('.fj-foot button')).toBeNull();

		// 2) Полноценный лучший результат появляется и применяется без потери правки профилей.
		const bestAwg = {
			signatureProfile: 'stun',
			jc: 6,
			jmin: 30,
			jmax: 90,
			i1: '<b 0xabcd>',
		};
		const validStatus: WarpFindJunkStatus = {
			runId: 2,
			running: false,
			attempt: 1,
			maxAttempts: 8,
			thresholdPct: 95,
			attempts: [
				{
					attempt: 1,
					awg: bestAwg,
					profile: 'stun',
					working: 14,
					scanned: 15,
					total: 15,
					pct: 93,
					durationMs: 2200,
				},
			],
			best: {
				attempt: 1,
				awg: bestAwg,
				profile: 'stun',
				working: 14,
				scanned: 15,
				total: 15,
				pct: 93,
				durationMs: 2200,
			},
		};
		api.warpscoutFindJunkApply.mockResolvedValue(bestAwg);
		api.warpscoutState.mockResolvedValue(state({ awg: bestAwg }, { findJunk: { ...validStatus, applied: true, appliedAttempt: 1 } }));

		warpFindJunkPush.set(validStatus);
		await tick();

		const applyBtn = dialog().querySelector<HTMLButtonElement>('.fj-foot button');
		expect(applyBtn).not.toBeNull();
		await fireEvent.click(applyBtn!);

		await waitFor(() => expect(api.warpscoutFindJunkApply).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(chip(container, 0).textContent).toContain('stun'));
		expect(chip(container, 0).textContent).toContain('Jc 6');
		// Черновик автопилота остался несохранённым.
		expect(container.querySelector('.save-bar')).not.toBeNull();
	});

	it('asks for confirmation before clearing auto-swap history and reloads state', async () => {
		const item: WarpSwapEvent = {
			at: '2026-11-14T10:00:00Z',
			profileId: 'a',
			profileName: 'WARP HEL',
			targetMode: 'kernel',
			targetId: 't-a',
			reason: 'trace timeout',
			oldEndpoint: '162.159.192.1:500',
			newEndpoint: '162.159.192.2:2408',
			colo: 'HEL',
			loc: 'FI',
			pingMs: 24,
			ok: true,
		};
		const { container } = await mount(state({}, { history: [item] }));
		expect(container.querySelectorAll('.swap-item')).toHaveLength(1);

		const clearBtn = container.querySelector<HTMLButtonElement>('.timeline-head button');
		expect(clearBtn).not.toBeNull();
		await fireEvent.click(clearBtn!);
		expect(api.warpscoutClearHistory).not.toHaveBeenCalled();

		api.warpscoutClearHistory.mockResolvedValue({ ok: true });
		api.warpscoutState.mockResolvedValue(state({}, { history: [] }));

		const confirmBtn = await screen.findByRole('button', { name: /^(Очистить журнал|Clear history)$/i });
		await fireEvent.click(confirmBtn);

		await waitFor(() => expect(api.warpscoutClearHistory).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(container.querySelectorAll('.swap-item')).toHaveLength(0));
	});

	it('shows scanner obfuscation summary in swap modal and blocks native swap when protocol is wg', async () => {
		const initial = acctState();
		initial.config.awg = { signatureProfile: 'dtls', jc: 5, jmin: 24, jmax: 105 };
		initial.scan = {
			running: false,
			owner: 'manual',
			total: 1,
			done: 1,
			hits: [
				{
					endpoint: '162.159.192.1:2408',
					handshakeMs: 25,
					pingMs: 25,
					colo: 'HEL',
					loc: 'FI',
					warp: 'on',
					tgOk: true,
				},
			],
		};
		api.warpscoutState.mockResolvedValue(initial);
		api.warpscoutTargets.mockResolvedValue([
			{
				mode: 'native',
				id: 'nat1',
				name: 'WARP Native',
				iface: 'awg0',
				endpoint: '162.159.192.2:2408',
				addressV4: '172.16.0.2',
				isWarp: true,
				running: true,
				protected: false,
			},
		]);

		const view = render(WarpScoutTab);
		await waitFor(() => expect(view.container.querySelector('.actions-cell')).not.toBeNull());

		// Open swap modal in AWG mode: summary shows active Jc/Jmin/Jmax and Swap is enabled.
		const swapRowBtn = view.container.querySelector<HTMLButtonElement>(
			'.actions-cell button:last-of-type',
		);
		expect(swapRowBtn).not.toBeNull();
		await fireEvent.click(swapRowBtn!);

		const dialog = await waitFor(() => {
			const el = document.body.querySelector('[role="dialog"]');
			expect(el).not.toBeNull();
			return el!;
		});
		expect(dialog.textContent).toContain('5');
		expect(dialog.textContent).toContain('24');
		expect(dialog.textContent).toContain('105');

		const modalBtns = [...dialog.querySelectorAll<HTMLButtonElement>('footer button, .modal-footer button')];
		const swapConfirmAwg = modalBtns.at(-1);
		expect(swapConfirmAwg).toBeDefined();
		expect(swapConfirmAwg!.disabled).toBe(false);

		// Close modal, switch scanner protocol to WireGuard, and re-open swap modal.
		const cancelBtn = modalBtns[0];
		expect(cancelBtn).toBeDefined();
		await fireEvent.click(cancelBtn);
		await waitFor(() => expect(document.body.querySelector('[role="dialog"]')).toBeNull());

		const wgBtn = [...view.container.querySelectorAll<HTMLButtonElement>('.proto-seg button')].find(
			(b) => b.textContent?.trim() === 'WireGuard',
		);
		expect(wgBtn).toBeDefined();
		await fireEvent.click(wgBtn!);

		await fireEvent.click(swapRowBtn!);
		const wgDialog = await waitFor(() => {
			const el = document.body.querySelector('[role="dialog"]');
			expect(el).not.toBeNull();
			return el!;
		});
		const wgSwapConfirm = [
			...wgDialog.querySelectorAll<HTMLButtonElement>('footer button, .modal-footer button'),
		].at(-1);
		expect(wgSwapConfirm).toBeDefined();
		expect(wgSwapConfirm!.disabled).toBe(true);
		expect(wgDialog.textContent).toContain('WireGuard');
	});
});
