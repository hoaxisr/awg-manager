import { describe, it, expect, vi, beforeEach } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { render, fireEvent, waitFor, screen } from '@testing-library/svelte';
import TelegramWebProxyCard from './TelegramWebProxyCard.svelte';
import { api } from '$lib/api/client';
import { m } from '$lib/i18n';
import { resetServerVerification } from '$lib/stores/serverVerification';
import type { TgWebProxyConfig, TgWebProxyStatus } from '$lib/types';
import type { VerificationReport } from '$lib/types/serverWizard';

vi.mock('$lib/api/client', () => ({
	api: {
		getTgWebProxyStatus: vi.fn(),
		getTgWebProxyConfig: vi.fn(),
		getWizardCapabilities: vi.fn(),
		verifyWizard: vi.fn(),
		resetTgWebProxy: vi.fn(),
	},
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

function baseConfig(over: Partial<TgWebProxyConfig> = {}): TgWebProxyConfig {
	return {
		enabled: false,
		scenario: 'cdn_http',
		listen_port: 8085,
		admin_port: 8086,
		public_hostname: 'connect.example.netcraze.club',
		direct_host: 'connect.example.netcraze.club',
		direct_port: 8443,
		tls_domain: 'www.google.com',
		carrier_mode: 'https-lanes',
		upstream_device: 'direct',
		secret: '00112233445566778899aabbccddeeff',
		backend: '127.0.0.1:2398',
		endpoints: [],
		users: [],
		...over,
	};
}

function baseStatus(over: Partial<TgWebProxyStatus> = {}): TgWebProxyStatus {
	return {
		installed: true,
		running: false,
		port: 8085,
		direct_online: false,
		webproxy_online: false,
		mtproto_online: false,
		mtproto_secret: '00112233445566778899aabbccddeeff',
		public_host: 'connect.example.netcraze.club',
		carrier_mode: 'https-lanes',
		upstream_device: 'direct',
		links: [],
		users: [],
		...over,
	} as unknown as TgWebProxyStatus;
}

describe('TelegramWebProxyCard', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		resetServerVerification();
		vi.mocked(api.getWizardCapabilities).mockResolvedValue({
			egress_options: [{ id: 'direct', kind: 'direct', owner: 'system', name: 'WAN', iface: 'direct', available: true, state: 'up', current: true, supports_tcp: true, supports_udp: true, generation: 1 }],
		} as unknown as Awaited<ReturnType<typeof api.getWizardCapabilities>>);
	});

	it('never shows running when config is disabled even if status reports running, and separates config/process/reachability states', async () => {
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(baseConfig({ enabled: false }));
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(baseStatus({ running: true, webproxy_online: true }));

		const { findByText, queryByText } = render(TelegramWebProxyCard, {});

		await findByText('Частично работает');
		expect(queryByText('Работает')).toBeNull();
		await findByText(/выключён в настройках, но процесс ещё активен/);
		await findByText('Выключена');
		await findByText('Запущен');
		await findByText('Не проверена');
	});

	it('closes Settings & Diagnostics modal via Закрыть and × even after ConnectionChecks completes', async () => {
		const report: VerificationReport = {
			kind: 'tgwebproxy',
			checked_at: '2026-11-21T15:31:38Z',
			state_fingerprint: 'fp-tg-1',
			scope: 'router',
			scope_note: 'Проверка выполняется с самого роутера',
			overall_status: 'ok',
			checks: [
				{
					id: 'tg-mtproto',
					service: 'tg_mtproto',
					title: 'Telegram: MTProto (прямое подключение)',
					status: 'ok',
					target_address: '127.0.0.1:8443',
					message: 'Порт доступен',
				},
			],
		};
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(baseConfig({ enabled: true }));
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, webproxy_online: true, raw_online: true }),
		);
		vi.mocked(api.verifyWizard).mockResolvedValue(report);

		render(TelegramWebProxyCard, {});

		const settingsBtn = await screen.findByRole('button', { name: /Настройки/ });
		await fireEvent.click(settingsBtn);

		await screen.findByText('Настройки и диагностика Telegram');
		const diagTab = screen.getByRole('button', { name: 'Диагностика и проверка' });
		await fireEvent.click(diagTab);

		await screen.findByText('Telegram: MTProto (прямое подключение)');
		expect(api.verifyWizard).toHaveBeenCalledWith('tgwebproxy');

		const footerCloseBtn = screen.getByText('Закрыть').closest('button');
		expect(footerCloseBtn).not.toBeNull();
		await fireEvent.click(footerCloseBtn!);

		await waitFor(() => {
			expect(screen.queryByText('Настройки и диагностика Telegram')).toBeNull();
		});

		// Re-open, trigger diagnostics again, and close via header × button
		await fireEvent.click(screen.getByRole('button', { name: /Настройки/ }));
		await screen.findByText('Настройки и диагностика Telegram');
		await fireEvent.click(screen.getByRole('button', { name: 'Диагностика и проверка' }));
		await screen.findByText('Telegram: MTProto (прямое подключение)');

		const xBtn = document.body.querySelector('button.modal-close');
		expect(xBtn).not.toBeNull();
		await fireEvent.click(xBtn!);

		await waitFor(() => {
			expect(screen.queryByText('Настройки и диагностика Telegram')).toBeNull();
		});
	});

	it('triggers factory reset with unchecked KeenDNS checkbox by default and supports opting in', async () => {
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(baseConfig({ enabled: true }));
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(baseStatus({ running: true }));
		vi.mocked(api.resetTgWebProxy).mockResolvedValue({
			server: 'tgwebproxy',
			reset: true,
			dispatcher_reset: false,
		});

		render(TelegramWebProxyCard, {});

		await fireEvent.click(await screen.findByRole('button', { name: /Настройки/ }));
		await screen.findByText('Настройки и диагностика Telegram');

		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_button() }));
		await screen.findByText(m.server_reset_tg_modal_title());

		const kdCheckbox = screen.getByLabelText(new RegExp(m.server_reset_tg_keendns_label()));
		expect(kdCheckbox).not.toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_confirm_button() }));
		await waitFor(() => {
			expect(api.resetTgWebProxy).toHaveBeenCalledWith({ delete_keendns: false });
		});
		await waitFor(() => {
			expect(screen.queryByText(m.server_reset_tg_modal_title())).toBeNull();
			expect(screen.queryByText('Настройки и диагностика Telegram')).toBeNull();
		});

		// Open again and check KeenDNS deletion checkbox
		await fireEvent.click(await screen.findByRole('button', { name: /Настройки/ }));
		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_button() }));
		await screen.findByText(m.server_reset_tg_modal_title());

		const kdCheckbox2 = screen.getByLabelText(new RegExp(m.server_reset_tg_keendns_label()));
		await fireEvent.click(kdCheckbox2);
		expect(kdCheckbox2).toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_confirm_button() }));
		await waitFor(() => {
			expect(api.resetTgWebProxy).toHaveBeenCalledWith({ delete_keendns: true });
		});
	});
});
