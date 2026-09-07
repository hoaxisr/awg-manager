// Блок «Абоненты» FreeTurn-сервера: форма добавления живёт в модалке
// (Дополнение №4 п.1), поле имени подписано SH-39, галка WS-38 решает, вносить
// ли Client ID в список. Тосты TS-10/TS-11 — по ответу бэкенда.
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Баррель $lib/components/ui тянет theme-store: matchMedia читается на импорте.
vi.hoisted(() => {
	// Панель Dropdown виджета пира читает ResizeObserver при открытии.
	Object.defineProperty(globalThis, 'ResizeObserver', {
		writable: true,
		configurable: true,
		value: class {
			observe() {}
			unobserve() {}
			disconnect() {}
		},
	});
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
	addManagedPeer: vi.fn(),
	getManagedPeerConf: vi.fn(),
	addSystemServerPeer: vi.fn(),
	getSystemServerPeerConf: vi.fn(),
	deleteManagedPeer: vi.fn(),
	deleteSystemServerPeer: vi.fn(),
}));

// Системный WG-сервер: ответ addSystemServerPeer — снимок, новый пир в нём
// узнаётся по отправленному адресу, а не по разнице снимков.
const SYS_SNAP = {
	servers: [
		{
			id: 'wg0',
			interfaceName: 'Wireguard0',
			description: 'Системный',
			status: 'up',
			address: '10.9.0.1',
			listenPort: 51820,
			peers: [{ publicKey: 'OLD', description: 'Старый', allowedIPs: ['10.9.0.2/32'], confAvailable: true }],
		},
	],
	managed: [],
	managedStats: {},
};
vi.mock('$lib/api/client', () => ({ api: apiMock }));

// WG-сервер, куда смотрит `-connect` раздачи (listen 51820), и чужой сервер:
// пиры чужого в модалке показываться не должны.
const SNAP = {
	servers: [],
	managed: [
		{
			interfaceName: 'wgm0',
			description: 'Мой WG',
			address: '10.7.0.1',
			listenPort: 51820,
			peers: [{ publicKey: 'PUBKEY0000', description: 'Ноутбук', tunnelIP: '10.7.0.2/32' }],
		},
		{
			interfaceName: 'wgm1',
			description: 'Чужой WG',
			address: '10.8.0.1',
			listenPort: 51821,
			peers: [{ publicKey: 'PUBKEY1111', description: 'Чужой пир', tunnelIP: '10.8.0.2/32' }],
		},
	],
	managedStats: { wgm0: { status: 'up' }, wgm1: { status: 'up' } },
};
const serversMock = vi.hoisted(() => ({ subscribe: vi.fn(), refetch: vi.fn() }));
vi.mock('$lib/stores/servers', () => ({ servers: serversMock }));

const CONF = ['[Interface]', 'PrivateKey = kkk', '', '[Peer]', 'Endpoint = 203.0.113.7:51820'].join(
	'\n',
);

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
		serversMock.subscribe.mockImplementation((fn: (st: unknown) => void) => {
			fn({ data: SNAP });
			return () => {};
		});
		serversMock.refetch.mockResolvedValue(undefined);
		apiMock.addManagedPeer.mockResolvedValue({ publicKey: 'PUBKEYNEW' });
		apiMock.getManagedPeerConf.mockResolvedValue(CONF);
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

	// #871: ссылка второго абонента получала peer 127.0.0.1 — `server.connect`
	// это локальный WG-сервер, а не адрес для абонента.
	it('#871: по умолчанию создаёт пира под абонента на сервере -connect, peer не шлёт', async () => {
		mount();
		const modal = await openModal();
		await fireEvent.input(screen.getByLabelText('Имя абонента'), {
			target: { value: 'Второй роутер' },
		});
		await submitModal(modal);

		await waitFor(() => expect(apiMock.generateFreeTurnLink).toHaveBeenCalled());
		expect(apiMock.addManagedPeer).toHaveBeenCalledWith('wgm0', {
			description: 'Второй роутер',
			tunnelIP: '10.7.0.3/32',
		});
		const req = apiMock.generateFreeTurnLink.mock.calls[0][0];
		expect(req.peer).toBeUndefined();
		expect(req.wg).toContain('PrivateKey = kkk');
		expect(req.wg).toContain('Endpoint = 127.0.0.1:9000');
	});

	it('#871: выбранный существующий пир — .conf его, пира не создаёт; чужой сервер скрыт', async () => {
		mount();
		const modal = await openModal();
		await fireEvent.click(within(modal).getByLabelText('Пир'));
		const own = await screen.findByText('Ноутбук');
		expect(screen.queryByText('Чужой пир')).toBeNull();
		await fireEvent.click(own);
		await waitFor(() =>
			expect(apiMock.getManagedPeerConf).toHaveBeenCalledWith('wgm0', 'PUBKEY0000', '127.0.0.1'),
		);
		await submitModal(modal);

		await waitFor(() => expect(apiMock.generateFreeTurnLink).toHaveBeenCalled());
		expect(apiMock.addManagedPeer).not.toHaveBeenCalled();
		const req = apiMock.generateFreeTurnLink.mock.calls[0][0];
		expect(req.peer).toBeUndefined();
		expect(req.wg).toContain('Endpoint = 127.0.0.1:9000');
	});

	it('#871: системный сервер — новый пир находится по адресу, даже если параллельно добавили чужого', async () => {
		serversMock.subscribe.mockImplementation((fn: (st: unknown) => void) => {
			fn({ data: SYS_SNAP });
			return () => {};
		});
		apiMock.addSystemServerPeer.mockResolvedValue({
			servers: [
				{
					id: 'wg0',
					peers: [
						{ publicKey: 'OLD', allowedIPs: ['10.9.0.2/32'] },
						{ publicKey: 'FOREIGN', allowedIPs: ['10.9.0.4/32'] },
						{ publicKey: 'MINE', allowedIPs: ['10.9.0.3/32'] },
					],
				},
			],
		});
		apiMock.getSystemServerPeerConf.mockResolvedValue(CONF);
		mount();
		const modal = await openModal();
		await submitModal(modal);

		await waitFor(() => expect(apiMock.generateFreeTurnLink).toHaveBeenCalled());
		expect(apiMock.addSystemServerPeer).toHaveBeenCalledWith('wg0', {
			description: expect.any(String),
			tunnelIP: '10.9.0.3/32',
		});
		expect(apiMock.getSystemServerPeerConf).toHaveBeenCalledWith('wg0', 'MINE', '127.0.0.1');
	});

	it('#871: отказ ссылки после создания пира откатывает пира', async () => {
		apiMock.generateFreeTurnLink.mockRejectedValue(new Error('ссылка не собралась'));
		apiMock.deleteManagedPeer.mockResolvedValue({});
		mount();
		const modal = await openModal();
		await submitModal(modal);

		await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('не собралась'));
		expect(apiMock.addManagedPeer).toHaveBeenCalled();
		expect(apiMock.deleteManagedPeer).toHaveBeenCalledWith('wgm0', 'PUBKEYNEW');
		expect(apiMock.addFreeTurnServerAllowlistClient).not.toHaveBeenCalled();
	});
});
