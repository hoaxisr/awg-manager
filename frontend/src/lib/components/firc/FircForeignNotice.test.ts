import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { fircForeignWebUrl } from '$lib/types/firc';

const { fircUpdateSettings } = vi.hoisted(() => ({ fircUpdateSettings: vi.fn() }));
vi.mock('$lib/api/client', () => ({ api: { fircUpdateSettings } }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/firc', () => ({ reloadFircAll: vi.fn().mockResolvedValue(undefined) }));
vi.mock('$lib/stores/singboxRouter', () => ({ singboxRouter: { reloadSettings: vi.fn().mockResolvedValue(undefined) } }));
vi.mock('$lib/stores/singbox', async () => {
	const { readable } = await import('svelte/store');
	return { singboxStatus: readable({ data: { installed: true } }) };
});

import FircForeignNotice from './FircForeignNotice.svelte';
import { notifications } from '$lib/stores/notifications';
import { reloadFircAll } from '$lib/stores/firc';
import { m } from '$lib/i18n';

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe('FircForeignNotice', () => {
	it('без конфликта — пояснение и, если порт известен, ссылка; кнопок нет', () => {
		render(FircForeignNotice, { foreign: { package: 'firc', webPort: 666, webHost: '192.168.1.1' } });
		expect(screen.getByText(m.firc_foreign_text())).toBeInTheDocument();
		expect(screen.getByRole('link')).toHaveAttribute('href', 'http://192.168.1.1:666/');
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('порт неизвестен — ссылки нет', () => {
		render(FircForeignNotice, { foreign: { package: 'firc' }, selected: true });
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('движок FIRC выбран — уход на sing-box и перечитывание статуса', async () => {
		fircUpdateSettings.mockResolvedValue({});
		render(FircForeignNotice, { foreign: { package: 'firc' }, selected: true });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_foreign_leave() }));
		await waitFor(() => expect(reloadFircAll).toHaveBeenCalled());
		expect(fircUpdateSettings).toHaveBeenCalledWith({ routingEngine: 'sing-box' });
	});

	it('сорванный уход показывает причину', async () => {
		fircUpdateSettings.mockRejectedValue(new Error('политика Multi-WAN'));
		render(FircForeignNotice, { foreign: { package: 'firc' }, selected: true });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_foreign_leave() }));
		await waitFor(() =>
			expect(notifications.error).toHaveBeenCalledWith(m.firc_foreign_leave_failed({ error: 'политика Multi-WAN' })),
		);
	});
});

describe('fircForeignWebUrl', () => {
	it.each([
		[{ webPort: 666 }, '192.168.1.1', 'http://192.168.1.1:666/'],
		[{ webPort: 666 }, 'fd00::1', 'http://[fd00::1]:666/'],
		[{ webPort: 666 }, '[fd00::1]', 'http://[fd00::1]:666/'],
		[{ webPort: 666, webHost: '10.0.0.1' }, 'router.lan', 'http://10.0.0.1:666/'],
		[{}, '192.168.1.1', null],
	])('%o на %s → %s', (foreign, host, want) => {
		expect(fircForeignWebUrl(foreign, host)).toBe(want);
	});
});
