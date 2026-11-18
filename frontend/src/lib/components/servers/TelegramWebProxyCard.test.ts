import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';
import TelegramWebProxyCard from './TelegramWebProxyCard.svelte';
import { api } from '$lib/api/client';
import type { TgWebProxyConfig, TgWebProxyStatus } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: {
		getTgWebProxyStatus: vi.fn(),
		getTgWebProxyConfig: vi.fn(),
		getWizardCapabilities: vi.fn(),
		verifyWizard: vi.fn(),
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
});
