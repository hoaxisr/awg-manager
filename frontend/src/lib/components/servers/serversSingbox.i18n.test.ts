import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import ServerRail, { type RailItem } from './ServerRail.svelte';
import ManagedServerDriftBanner from './ManagedServerDriftBanner.svelte';
import { routerDnsHint } from './routerDnsHint';
import { locale, m } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({
	api: {
		managedServerDrift: vi.fn(),
		managedServerRestoreDrift: vi.fn(),
	},
}));

import { api } from '$lib/api/client';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

const items: RailItem[] = [
	{ id: 'managed:Wireguard1', name: 'Home', iface: 'Wireguard1', listenPort: 51820, status: 'running', kind: 'managed' },
];

describe('servers i18n', () => {
	it('ServerRail: заголовок, aria-label и кнопка создания переключаются на английский', () => {
		const { container } = render(ServerRail, {
			props: { items, activeId: 'managed:Wireguard1', onSelect: () => {}, onCreate: () => {} },
		});
		expect(container.querySelector('aside')?.getAttribute('aria-label')).toBe('Список серверов');
		expect(screen.getByText('Серверы (1)')).toBeTruthy();
		expect(screen.getAllByText('Новый сервер').length).toBeGreaterThan(0);

		locale.set('en');
		flushSync();

		expect(container.querySelector('aside')?.getAttribute('aria-label')).toBe('Server list');
		expect(screen.getByText('Servers (1)')).toBeTruthy();
		expect(screen.getAllByText('New server').length).toBeGreaterThan(0);
	});

	it('ManagedServerDriftBanner: множественные формы счётчика серверов', async () => {
		const drift = [1, 2, 5].map((n) => ({ interfaceName: `Wireguard${n}` }));
		vi.mocked(api.managedServerDrift).mockResolvedValue({ drift: drift.slice(0, 1) } as never);
		const first = render(ManagedServerDriftBanner);
		await waitFor(() => expect(first.container.querySelector('strong')).toBeTruthy());
		expect(first.container.querySelector('strong')?.textContent).toBe(
			'Обнаружен 1 сервер в конфигурации, отсутствующий в NDMS.',
		);
		first.unmount();

		vi.mocked(api.managedServerDrift).mockResolvedValue({ drift } as never);
		const second = render(ManagedServerDriftBanner);
		await waitFor(() => expect(second.container.querySelector('strong')).toBeTruthy());
		expect(second.container.querySelector('strong')?.textContent).toBe(
			'Обнаружено 3 сервера в конфигурации, отсутствующих в NDMS.',
		);

		locale.set('en');
		flushSync();
		expect(second.container.querySelector('strong')?.textContent).toBe(
			'Detected 3 servers in the configuration that are missing in NDMS.',
		);
		expect(screen.getByText('Restore')).toBeTruthy();
	});

	it('routerDnsHint: функция возвращает текст в текущей локали', () => {
		expect(routerDnsHint()).toMatch(/^Запросы к DNS роутера отвечает/);
		locale.set('en');
		expect(routerDnsHint()).toMatch(/^The router’s own resolver answers/);
	});

	it('servers_import_summary: два плюральных селектора', () => {
		expect(m.servers_import_summary({ servers: 1, peers: 2 })).toBe('Файл содержит 1 сервер, 2 пира.');
		expect(m.servers_import_summary({ servers: 5, peers: 21 })).toBe('Файл содержит 5 серверов, 21 пир.');
		locale.set('en');
		expect(m.servers_import_summary({ servers: 1, peers: 2 })).toBe('The file contains 1 server, 2 peers.');
	});
});
