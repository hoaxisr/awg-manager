import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import type { WdttPanelUserEntry, WdttServerConfig } from '$lib/types';

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
	generateWdttServerLink: vi.fn(),
	getWANIP: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import LinkPanel from './LinkPanel.svelte';

const server: WdttServerConfig = {
	listen: '0.0.0.0:56002',
	wgPort: 56001,
	relayMode: 'wg',
	linkPeer: '203.0.113.10:56002',
	linkVkHashes: 'hash1',
};

const user: WdttPanelUserEntry = {
	password: 'secret-pw',
	comment: 'Телефон',
	isAuto: false,
};

describe('LinkPanel: включение ключа диагностики в qwdtt-ссылку', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		apiMock.generateWdttServerLink.mockResolvedValue({
			link: 'wdtt://x',
			linkQwdtt: 'qwdtt://x',
			peer: '203.0.113.10:56002',
		});
	});

	it('по умолчанию тумблер выключен и includeDiagKey не передаётся, при включении уходит includeDiagKey: true', async () => {
		const { getByRole } = render(LinkPanel, {
			props: {
				serverId: 'default',
				serverName: 'Сервер',
				server,
				user,
				onclose: () => {},
			},
		});

		await waitFor(() => expect(apiMock.generateWdttServerLink).toHaveBeenCalledTimes(1));
		expect(apiMock.generateWdttServerLink).toHaveBeenNthCalledWith(
			1,
			'default',
			expect.objectContaining({
				password: 'secret-pw',
				mode: 'wg',
				includeDiagKey: undefined,
			}),
		);

		const toggle = getByRole('checkbox', { name: 'Включать ключ диагностики' }) as HTMLInputElement;
		expect(toggle.checked).toBe(false);

		await fireEvent.click(toggle);

		await waitFor(() => expect(apiMock.generateWdttServerLink).toHaveBeenCalledTimes(2));
		expect(apiMock.generateWdttServerLink).toHaveBeenNthCalledWith(
			2,
			'default',
			expect.objectContaining({
				password: 'secret-pw',
				mode: 'wg',
				includeDiagKey: true,
			}),
		);
	});

	it('откатывает тумблер обратно в false, если бэкенд отказал из-за отсутствия diag.key', async () => {
		apiMock.generateWdttServerLink
			.mockResolvedValueOnce({
				link: 'wdtt://x',
				linkQwdtt: 'qwdtt://x',
				peer: '203.0.113.10:56002',
			})
			.mockRejectedValueOnce(new Error('ключ диагностики сервера ещё не создан'));

		const { getByRole } = render(LinkPanel, {
			props: {
				serverId: 'default',
				serverName: 'Сервер',
				server,
				user,
				onclose: () => {},
			},
		});

		await waitFor(() => expect(apiMock.generateWdttServerLink).toHaveBeenCalledTimes(1));
		const toggle = getByRole('checkbox', { name: 'Включать ключ диагностики' }) as HTMLInputElement;
		expect(toggle.checked).toBe(false);

		await fireEvent.click(toggle);

		await waitFor(() =>
			expect(notify.error).toHaveBeenCalledWith('ключ диагностики сервера ещё не создан'),
		);
		await waitFor(() => expect(toggle.checked).toBe(false));
	});
});
