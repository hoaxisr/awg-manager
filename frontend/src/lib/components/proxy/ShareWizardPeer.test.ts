// Виджет пира мастера «Настроить раздачу»: .conf уезжает в ссылку абоненту, и
// правка порта ПОСЛЕ выбора пира обязана пересобрать в нём Endpoint — конфига
// в мастере не видно, битый адрес заметить негде.
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Баррель $lib/components/ui тянет theme-store: тот читает matchMedia на импорте,
// а панель Dropdown — ResizeObserver при открытии; в jsdom нет ни того, ни другого.
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
	getManagedPeerConf: vi.fn(),
	getSystemServerPeerConf: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

const SNAP = {
	servers: [],
	managed: [
		{
			interfaceName: 'wgm0',
			description: 'Мой WG',
			listenPort: 51820,
			peers: [{ publicKey: 'PUBKEY0000', description: 'Ноутбук' }],
		},
	],
	managedStats: { wgm0: { status: 'up' } },
};

const serversMock = vi.hoisted(() => ({
	subscribe: vi.fn(),
	refetch: vi.fn(),
}));
vi.mock('$lib/stores/servers', () => ({ servers: serversMock }));

import { render, screen, waitFor, fireEvent } from '@testing-library/svelte';
import ShareWizardPeer from './ShareWizardPeer.svelte';

const CONF = ['[Interface]', 'PrivateKey = kkk', '', '[Peer]', 'Endpoint = 203.0.113.7:51820'].join(
	'\n',
);

function endpointOf(conf: string): string {
	return conf.split('\n').find((l) => l.trim().toLowerCase().startsWith('endpoint'))?.trim() ?? '';
}

async function pickPeer() {
	await fireEvent.click(screen.getByLabelText('Пир'));
	await fireEvent.click(await screen.findByText('Ноутбук'));
}

describe('ShareWizardPeer: Endpoint конфига пира', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		serversMock.subscribe.mockImplementation((fn: (st: unknown) => void) => {
			fn({ data: SNAP });
			return () => {};
		});
		serversMock.refetch.mockResolvedValue(undefined);
		apiMock.getManagedPeerConf.mockResolvedValue(CONF);
	});

	it('порт, правленный ПОСЛЕ выбора пира, пересобирает Endpoint в .conf', async () => {
		const confs: string[] = [];
		render(ShareWizardPeer, {
			props: {
				endpointPort: 9005,
				onconnect: () => {},
				onpeerconf: (c: string) => confs.push(c),
			},
		});

		await pickPeer();
		await waitFor(() => expect(endpointOf(confs.at(-1) ?? '')).toBe('Endpoint = 127.0.0.1:9005'));

		await fireEvent.input(screen.getByLabelText('Порт'), { target: { value: '9111' } });
		await waitFor(() => expect(endpointOf(confs.at(-1) ?? '')).toBe('Endpoint = 127.0.0.1:9111'));
	});

	it('порт неизвестен и не введён — конфиг наверх не уходит (Endpoint :0 не бывает)', async () => {
		const confs: string[] = [];
		render(ShareWizardPeer, {
			props: {
				endpointPort: 0,
				onconnect: () => {},
				onpeerconf: (c: string) => confs.push(c),
			},
		});

		await pickPeer();
		await waitFor(() => expect(apiMock.getManagedPeerConf).toHaveBeenCalled());
		expect(confs.at(-1) ?? '').toBe('');

		await fireEvent.input(screen.getByLabelText('Порт'), { target: { value: '9200' } });
		await waitFor(() => expect(endpointOf(confs.at(-1) ?? '')).toBe('Endpoint = 127.0.0.1:9200'));
	});
});
