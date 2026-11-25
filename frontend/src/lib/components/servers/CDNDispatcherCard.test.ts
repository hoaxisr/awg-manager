import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { CDNDispatcherConfig } from '$lib/types';
import { notifications } from '$lib/stores/notifications';
import CDNDispatcherCard from './CDNDispatcherCard.svelte';

const { getDispatcherConfig, updateDispatcherConfig } = vi.hoisted(() => ({
	getDispatcherConfig: vi.fn<() => Promise<CDNDispatcherConfig>>(),
	updateDispatcherConfig: vi.fn<(cfg: CDNDispatcherConfig) => Promise<CDNDispatcherConfig>>(),
}));

vi.mock('$lib/api/client', () => ({
	api: {
		getDispatcherConfig,
		updateDispatcherConfig,
	},
}));

vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

describe('CDNDispatcherCard', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	afterEach(() => {
		cleanup();
	});

	it('renders stopped muted badge and closed-port hint when dispatcher is not running', async () => {
		getDispatcherConfig.mockResolvedValueOnce({
			enabled: false,
			running: false,
			listen_addr: ':9009',
			xray_target: 'http://127.0.0.1:9008',
			tg_target: 'http://127.0.0.1:8085',
			routes: [],
			default_action: 'reject',
		});
		const onStatusChange = vi.fn<(running: boolean) => void>();

		render(CDNDispatcherCard, { props: { onStatusChange } });

		const badge = await screen.findByTestId('dispatcher-status-badge');
		expect(badge).toHaveClass('badge-muted');
		expect(badge).not.toHaveClass('badge-success');
		expect(badge).toHaveTextContent(m.servers_dispatcher_status_stopped({ port: '9009' }));

		expect(screen.getByTestId('dispatcher-status-note')).toHaveTextContent(
			m.servers_dispatcher_stopped_hint({ port: '9009' }),
		);
		await waitFor(() => expect(onStatusChange).toHaveBeenCalledWith(false));
	});

	it('renders green active badge and listening-port hint when dispatcher is running', async () => {
		getDispatcherConfig.mockResolvedValueOnce({
			enabled: true,
			running: true,
			listen_addr: '127.0.0.1:9443',
			xray_target: 'http://127.0.0.1:9008',
			tg_target: 'http://127.0.0.1:8085',
			routes: [],
			default_action: '404',
		});
		const onStatusChange = vi.fn<(running: boolean) => void>();

		render(CDNDispatcherCard, { props: { onStatusChange } });

		const badge = await screen.findByTestId('dispatcher-status-badge');
		expect(badge).toHaveClass('badge-success');
		expect(badge).toHaveTextContent(m.servers_dispatcher_status_running({ port: '9443' }));

		expect(screen.getByTestId('dispatcher-status-note')).toHaveTextContent(
			m.servers_dispatcher_running_hint({ port: '9443' }),
		);
		await waitFor(() => expect(onStatusChange).toHaveBeenCalledWith(true));
	});

	// Мутация: показывать успех при предупреждении.
	it('shows the warning when routes are saved but Telegram domains are not', async () => {
		const cfg: CDNDispatcherConfig = {
			enabled: true, running: true, listen_addr: ':9009', xray_target: '', tg_target: '', routes: [], default_action: '404',
		};
		getDispatcherConfig.mockResolvedValueOnce(cfg);
		updateDispatcherConfig.mockResolvedValueOnce({ ...cfg, warning: 'домены Telegram не обновлены' });
		render(CDNDispatcherCard, { props: {} });
		await screen.findByTestId('dispatcher-status-badge');
		await fireEvent.click(screen.getByRole('button', { name: new RegExp(m.servers_cdn_save_changes()) }));
		await waitFor(() => expect(notifications.warning).toHaveBeenCalledWith('домены Telegram не обновлены'));
		expect(notifications.success).not.toHaveBeenCalled();
	});
});
