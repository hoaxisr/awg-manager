import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
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
	serverListenConfirm: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api }));

// Вкладки проверяются своими тестами: здесь — только маршрутизация между ними.
const mounted = vi.hoisted(() => [] as string[]);
const expandLevel = vi.hoisted(() => [] as unknown[]);
vi.mock('$lib/components/settings', () => new Proxy({}, {
	get: (_, name) =>
		name === 'then'
			? undefined
			: (_anchor: unknown, props: Record<string, unknown>) => {
					mounted.push(String(name));
					if (name === 'SettingsGeneralTab') expandLevel.push(props.expandLevel);
				},
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
	mounted.length = 0;
	expandLevel.length = 0;
	nav.goto.mockImplementation(async (href: string) => nav.page.set({ url: new URL(href, 'http://router:2222') }));
	api.getSettings.mockReturnValue(new Promise(() => {}));
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

	it('стор страницы переиздан, пока адрес ещё не очищен, — второго вызова нет', async () => {
		nav.goto.mockReturnValue(new Promise(() => {}));
		const href = `/settings#listen-confirm=${TOKEN}`;
		open(href);
		await waitFor(() => expect(api.serverListenConfirm).toHaveBeenCalledTimes(1));
		nav.page.set({ url: new URL(href, 'http://router:2222') });
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

	it.each([
		['/settings?tab=system', 'Система', '/settings#awgm-update', 'Обновления'],
		['/settings?tab=logs', 'Журнал и проверки', '/settings?mode', 'Общие'],
	])('ссылка поверх другой вкладки переключает её: %s (%s) → %s (%s)', async (from, before, link, after) => {
		const { container } = open(from);
		await waitFor(() => expect(activeTab(container)).toBe(before));
		nav.page.set({ url: new URL(link, 'http://router:2222') });
		await waitFor(() => expect(activeTab(container)).toBe(after));
		// Не отскочила назад, когда Tabs и страница дописали адрес.
		await new Promise((r) => setTimeout(r, 20));
		expect(activeTab(container)).toBe(after);
	});

	it('ручная смена вкладки забывает, куда вела ссылка', async () => {
		api.getSettings.mockResolvedValue({});
		const { container } = open('/settings?mode');
		await waitFor(() => expect(expandLevel).toEqual([true]));
		const tabButton = (name: string) => screen.getAllByRole('button', { name }).find((b) => !b.hasAttribute('tabindex'))!;
		await fireEvent.click(tabButton('Доступ'));
		await waitFor(() => expect(activeTab(container)).toBe('Доступ'));
		await fireEvent.click(tabButton('Общие'));
		await waitFor(() => expect(expandLevel).toHaveLength(2));
		expect(expandLevel[1]).toBe(false);
	});

	// Опросы и подписки вкладки живут, пока она смонтирована.
	it('смонтирована только активная вкладка, подвал и патруль — всегда', async () => {
		api.getSettings.mockResolvedValue({});
		open('/settings?tab=logs');
		await waitFor(() => expect(mounted).toContain('SettingsLogsTab'));
		expect(mounted.filter((n) => n.endsWith('Tab'))).toEqual(['SettingsLogsTab']);
		expect(mounted).toEqual(expect.arrayContaining(['SettingsFooter', 'PukhososPatrol']));
	});

	it('блок, к которому ведёт ссылка, прокручен и подсвечен после загрузки настроек', async () => {
		const el = document.body.appendChild(document.createElement('div'));
		el.id = 'logging';
		el.scrollIntoView = vi.fn();
		let loaded!: (settings: unknown) => void;
		api.getSettings.mockReturnValue(new Promise((resolve) => (loaded = resolve)));
		open('/settings#logging');
		await new Promise((r) => setTimeout(r, 50));
		expect(el.scrollIntoView).not.toHaveBeenCalled();
		loaded({});
		await waitFor(() => expect(el.scrollIntoView).toHaveBeenCalled());
		expect(el.classList.contains('highlighted')).toBe(true);
		el.remove();
	});
});
