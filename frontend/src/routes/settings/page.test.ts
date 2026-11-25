import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';
import type { Writable } from 'svelte/store';

const nav = vi.hoisted(() => ({ page: null as unknown as Writable<{ url: URL }>, goto: vi.fn() }));
vi.mock('$app/stores', async () => {
	const { writable } = await import('svelte/store');
	nav.page = writable({ url: new URL('http://router:2222/settings') });
	return { page: nav.page };
});
vi.mock('$app/navigation', () => ({ goto: nav.goto }));

const api = vi.hoisted(() => ({
	getSettings: vi.fn(),
	getSystemInfo: vi.fn(),
	checkUpdate: vi.fn(),
	serverListenConfirm: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api }));

// Вкладки проверяются своими тестами: здесь — только маршрутизация между ними.
vi.mock('$lib/components/settings', () => new Proxy({}, {
	get: (_, name) => (name === 'then' ? undefined : () => {}),
	has: () => true,
}));
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });

import Page from './+page.svelte';

const TOKEN = '0123456789abcdef0123456789abcdef';

function open(href: string) {
	nav.page.set({ url: new URL(href, 'http://router:2222') });
	return render(Page);
}
const activeTab = (c: Element) => c.querySelector('.overflow-tabs .tab.active:not([tabindex])')?.textContent?.trim();

beforeEach(() => {
	vi.clearAllMocks();
	nav.goto.mockImplementation(async (href: string) => nav.page.set({ url: new URL(href, 'http://router:2222') }));
	api.getSettings.mockReturnValue(new Promise(() => {}));
	api.getSystemInfo.mockReturnValue(new Promise(() => {}));
	api.checkUpdate.mockReturnValue(new Promise(() => {}));
	api.serverListenConfirm.mockResolvedValue(undefined);
});

describe('страница настроек: подтверждение смены адреса панели', () => {
	// Не подтвердим — бэкенд откатит адрес, и открытая страница окажется мёртвой.
	it.each(['', '?tab=general', '?tab=updates', '?tab=components', '?tab=access', '?tab=logs', '?tab=system'])(
		'уходит ровно один раз при /settings%s, не дожидаясь настроек',
		async (search) => {
			open(`/settings${search}#listen-confirm=${TOKEN}`);
			await waitFor(() => expect(api.serverListenConfirm).toHaveBeenCalledWith(TOKEN));
			await waitFor(() => expect(nav.goto).toHaveBeenCalled());
			await new Promise((r) => setTimeout(r, 20));
			expect(api.serverListenConfirm).toHaveBeenCalledTimes(1);
		},
	);

	it('токен убран из адреса: перезагрузка не подтверждает повторно', async () => {
		const { unmount } = open(`/settings#listen-confirm=${TOKEN}`);
		let url!: URL;
		nav.page.subscribe((p) => (url = p.url));
		await waitFor(() => expect(url.hash).toBe(''));
		unmount();
		render(Page);
		await new Promise((r) => setTimeout(r, 20));
		expect(api.serverListenConfirm).toHaveBeenCalledTimes(1);
	});

	it('без токена не зовётся', async () => {
		open('/settings?tab=access');
		await new Promise((r) => setTimeout(r, 20));
		expect(api.serverListenConfirm).not.toHaveBeenCalled();
	});
});

describe('страница настроек: вкладки', () => {
	it.each([
		['/settings', 'Общие'],
		['/settings?tab=logs', 'Журнал и проверки'],
		['/settings?mode', 'Общие'],
		['/settings?feedbackFab', 'Обновления'],
		['/settings?highlight=downloads#downloads', 'Обновления'],
		['/settings#logging', 'Журнал и проверки'],
		['/settings#awgm-update', 'Обновления'],
	])('%s открывает «%s»', async (href, label) => {
		const { container } = open(href);
		await waitFor(() => expect(activeTab(container)).toBe(label));
	});

	it('ссылка, открытая поверх другой вкладки, переключает её', async () => {
		const { container } = open('/settings?tab=system');
		await waitFor(() => expect(activeTab(container)).toBe('Система'));
		nav.page.set({ url: new URL('http://router:2222/settings#awgm-update') });
		await waitFor(() => expect(activeTab(container)).toBe('Обновления'));
	});
});
