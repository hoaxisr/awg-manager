// Блок «Абоненты» FreeTurn-сервера: форма добавления живёт в модалке
// (Дополнение №4 п.1), поле имени подписано SH-39, галка WS-38 решает, вносить
// ли Client ID в список. Тосты TS-10/TS-11 — по ответу бэкенда.
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Баррель $lib/components/ui тянет theme-store: matchMedia читается на импорте.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

const apiMock = vi.hoisted(() => ({
	getFreeTurnServerAllowlist: vi.fn(),
	generateFreeTurnLink: vi.fn(),
	addFreeTurnServerAllowlistClient: vi.fn(),
	removeFreeTurnServerAllowlistClient: vi.fn(),
	disableFreeTurnServerAllowlist: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, screen, waitFor, within, fireEvent } from '@testing-library/svelte';
import ServerAllowlist from './ServerAllowlist.svelte';
import type { FreeTurnServerConfig } from '$lib/types';

const SERVER = {
	enabled: false,
	listen: '0.0.0.0:56000',
	connect: '127.0.0.1:51820',
	mode: 'udp',
	obfProfile: 'none',
	debug: false,
} as FreeTurnServerConfig;

function mount(enabled = true) {
	apiMock.getFreeTurnServerAllowlist.mockResolvedValue({
		enabled,
		clients: [],
		clientsFile: '/opt/etc/freeturn/clients.json',
	});
	return render(ServerAllowlist, {
		props: {
			serverId: 'ft1',
			serverName: 'Раздача FreeTurn',
			server: SERVER,
			peerConf: '[Interface]\nPrivateKey = k',
			locked: async (fn: () => Promise<void>) => {
				await fn();
			},
		},
	});
}

async function openModal() {
	await fireEvent.click(screen.getByRole('button', { name: 'Добавить' }));
	await screen.findByText('Новый абонент');
	return screen.getByRole('dialog');
}

/** «Добавить» две — в шапке блока и в окне; нажимаем ту, что в окне. */
async function submitModal(modal: HTMLElement) {
	await fireEvent.click(within(modal).getByRole('button', { name: 'Добавить' }));
}

describe('ServerAllowlist: добавление абонента модалкой', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		apiMock.generateFreeTurnLink.mockResolvedValue({ link: 'freeturn://x', clientId: 'cid-1' });
		apiMock.addFreeTurnServerAllowlistClient.mockResolvedValue({
			enabled: true,
			clients: [{ clientId: 'cid-1', comment: 'Ноутбук Пети' }],
			clientsFile: '/opt/etc/freeturn/clients.json',
		});
	});

	it('inline-формы нет: поля живут в окне, имя подписано SH-39', async () => {
		mount();
		await waitFor(() => expect(apiMock.getFreeTurnServerAllowlist).toHaveBeenCalled());
		expect(screen.queryByLabelText('Имя абонента')).toBeNull();
		expect(screen.queryByText('Комментарий')).toBeNull();

		await openModal();
		expect(screen.getByLabelText('Имя абонента')).toBeTruthy();
		expect(screen.getByLabelText('Client ID')).toBeTruthy();
		expect(screen.getByText('Внести в список разрешённых')).toBeTruthy();
	});

	it('галка WS-38 включена — ссылка выдана и Client ID внесён (TS-10)', async () => {
		mount();
		const modal = await openModal();
		await fireEvent.input(screen.getByLabelText('Имя абонента'), {
			target: { value: 'Ноутбук Пети' },
		});
		await submitModal(modal);

		await waitFor(() => expect(apiMock.addFreeTurnServerAllowlistClient).toHaveBeenCalled());
		expect(apiMock.generateFreeTurnLink.mock.calls[0][0]).toMatchObject({
			serverId: 'ft1',
			name: 'Ноутбук Пети',
		});
		expect(apiMock.addFreeTurnServerAllowlistClient.mock.calls[0][1]).toBe('cid-1');
		expect(notify.success).toHaveBeenCalledWith('Client ID внесён в список разрешённых');
		// Успех закрывает окно и показывает ссылку.
		await waitFor(() => expect(screen.queryByText('Новый абонент')).toBeNull());
	});

	it('галка снята — ссылка есть, записи в списке нет', async () => {
		mount();
		const modal = await openModal();
		await fireEvent.click(within(modal).getByRole('checkbox'));
		await submitModal(modal);

		await waitFor(() => expect(apiMock.generateFreeTurnLink).toHaveBeenCalled());
		expect(apiMock.addFreeTurnServerAllowlistClient).not.toHaveBeenCalled();
	});

	it('отказ печатается В окне, окно остаётся открытым', async () => {
		mount();
		apiMock.addFreeTurnServerAllowlistClient.mockRejectedValue(new Error('список не записался'));
		const modal = await openModal();
		await submitModal(modal);

		await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('не записался'));
		expect(screen.queryByText('Новый абонент')).toBeTruthy();
		expect(notify.error).not.toHaveBeenCalled();
	});
});
