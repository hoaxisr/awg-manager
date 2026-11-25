import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import type { ComponentProps } from 'svelte';

const api = vi.hoisted(() => ({
	updateSettings: vi.fn(),
	singboxToggleNDMSProxy: vi.fn(),
	serverListenState: vi.fn(),
	serverListenChange: vi.fn(),
	getAllInterfaces: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));
vi.mock('$lib/stores/singbox', async () => {
	const { readable } = await import('svelte/store');
	return {
		singboxStatus: {
			...readable({ data: { installed: true, running: true, version: '1.15.0', ndmsProxyEnabled: true }, status: 'fresh', lastFetchedAt: 1 }),
			applyMutationResponse: vi.fn(),
		},
	};
});
vi.mock('$lib/stores/zapret', async () => {
	const { readable } = await import('svelte/store');
	return { zapretStatusIfExpert: readable({ data: { installed: true, running: true, base: '/opt/zapret' } }) };
});
const zapret = vi.hoisted(() => ({ run: vi.fn() }));
vi.mock('$lib/components/zapret', () => ({ runZapretControl: zapret.run }));

import { setSettings } from '$lib/stores/settings';
import type { Settings } from '$lib/types';
import Host from './PageStateTestHost.svelte';
import HttpServerCard from './HttpServerCard.svelte';
import InstallSettings from './InstallSettings.svelte';
import NdmsProxyRow from './NdmsProxyRow.svelte';
import PingTargetsCard from './PingTargetsCard.svelte';
import SingboxSection from './SingboxSection.svelte';
import ZapretCard from './ZapretCard.svelte';
import { SettingsEditor } from './settingsEditor.svelte';

type Cards = ComponentProps<typeof Host>['cards'];
const pending = () => new Promise<never>(() => {});
const settings = {
	usageLevel: 'expert',
	singboxBootstrapDNS: '8.8.8.8',
	singboxClashPort: 9099,
	updates: { autoInstallEnabled: false, statsEnabled: true, snapshotDisabled: false },
} as unknown as Settings;

/** Смонтировать карточки; `leave` — уйти со вкладки и вернуться. */
function tab(cards: Cards) {
	const view = render(Host, { props: { shown: true, cards } });
	return {
		...view,
		leave: async () => {
			await view.rerender({ shown: false, cards });
			await view.rerender({ shown: true, cards });
		},
	};
}

beforeEach(() => {
	vi.clearAllMocks();
	setSettings(settings);
	api.serverListenState.mockResolvedValue({ port: 2222, interfaces: [], boundAddrs: ['192.168.1.1:2222'], pendingConfirm: false });
	api.getAllInterfaces.mockResolvedValue([]);
});
afterEach(() => vi.restoreAllMocks());

// Смонтирована только активная вкладка — её формы снимаются при переключении.
describe('черновики страницы настроек', () => {
	const ping = (target: string): Cards => [{ component: PingTargetsCard, props: { target, url: 'http://a/204', onsave: () => {} } }];
	const icmp = () => screen.getAllByRole<HTMLInputElement>('textbox')[0];

	it('несохранённый ввод переживает уход со вкладки и возврат', async () => {
		const { leave } = tab(ping('8.8.8.8'));
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		await leave();
		expect(icmp().value).toBe('1.1.1.1');
	});

	it('сохранённый черновик забыт: дальше поле следует за настройкой', async () => {
		const { rerender } = tab(ping('8.8.8.8'));
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		await rerender({ shown: true, cards: ping('1.1.1.1') });
		await rerender({ shown: true, cards: ping('9.9.9.9') });
		expect(icmp().value).toBe('9.9.9.9');
	});

	it('черновик живёт столько же, сколько страница', async () => {
		const first = tab(ping('8.8.8.8'));
		await fireEvent.input(icmp(), { target: { value: '1.1.1.1' } });
		first.unmount();
		tab(ping('8.8.8.8'));
		expect(icmp().value).toBe('8.8.8.8');
	});
});

describe('идущая операция переживает уход со вкладки: вторую не запустить', () => {
	const button = (name: string | RegExp) => screen.getByRole<HTMLButtonElement>('button', { name });

	it('zapret', async () => {
		zapret.run.mockReturnValue(pending());
		const { leave } = tab([{ component: ZapretCard }]);
		await fireEvent.click(button('Перезапустить'));
		await leave();
		expect(button(/Перезапустить/).disabled).toBe(true);
	});

	it('NDMS Proxy', async () => {
		api.singboxToggleNDMSProxy.mockReturnValue(pending());
		const { leave } = tab([{ component: NdmsProxyRow }]);
		await fireEvent.click(screen.getByRole('checkbox'));
		await fireEvent.click(button('Выключить'));
		expect(api.singboxToggleNDMSProxy).toHaveBeenCalledTimes(1);
		await leave();
		expect(screen.getByRole<HTMLInputElement>('checkbox').disabled).toBe(true);
	});

	it('автоустановка, снимок и статистика', async () => {
		api.updateSettings.mockReturnValue(pending());
		const { leave } = tab([{ component: InstallSettings, props: { settings, updateInfo: null } }]);
		const boxes = () => screen.getAllByRole<HTMLInputElement>('checkbox');
		expect(boxes()).toHaveLength(3);
		for (const box of boxes()) await fireEvent.click(box);
		expect(api.updateSettings).toHaveBeenCalledTimes(3);
		await leave();
		expect(boxes().map((b) => b.disabled)).toEqual([true, true, true]);
	});

	it('адрес панели', async () => {
		api.serverListenChange.mockReturnValue(pending());
		const { leave } = tab([{ component: HttpServerCard }]);
		await waitFor(() => expect(document.getElementById('hs-port')).toBeTruthy());
		await fireEvent.input(document.getElementById('hs-port')!, { target: { value: '3333' } });
		await fireEvent.click(button('Применить'));
		await fireEvent.click(screen.getAllByRole('button', { name: 'Применить' }).at(-1)!);
		await waitFor(() => expect(api.serverListenChange).toHaveBeenCalledTimes(1));
		await leave();
		expect(button('Применяем…').disabled).toBe(true);
	});

	it('bootstrap-DNS и порт Clash; отказ по порту остаётся под полем', async () => {
		const editor = new SettingsEditor(settings);
		const { leave } = tab([{ component: SingboxSection, props: { editor } }]);
		const save = (i: number) => screen.getAllByRole<HTMLButtonElement>('button', { name: /Сохранить/ })[i];
		const [dns, clash] = screen.getAllByRole<HTMLInputElement>('textbox');

		api.updateSettings.mockReturnValueOnce(pending());
		await fireEvent.input(dns, { target: { value: '1.1.1.1' } });
		await fireEvent.click(save(0));
		api.updateSettings.mockRejectedValueOnce(new Error('порт занят'));
		await fireEvent.input(clash, { target: { value: '9100' } });
		await fireEvent.click(save(1));
		await screen.findByText('порт занят');

		await leave();
		expect(save(0).disabled).toBe(true);
		expect(screen.getByText('порт занят')).toBeTruthy();

		api.updateSettings.mockReturnValueOnce(pending());
		await fireEvent.click(save(1));
		await leave();
		expect(save(1).disabled).toBe(true);
	});
});
