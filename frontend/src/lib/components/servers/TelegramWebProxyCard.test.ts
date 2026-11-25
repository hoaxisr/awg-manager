import { describe, it, expect, vi, beforeEach } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { render, fireEvent, waitFor, screen } from '@testing-library/svelte';
import TelegramWebProxyCard from './TelegramWebProxyCard.svelte';
import { api } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';
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
		tgWebProxyAction: vi.fn(),
		revealTgWebProxySecret: vi.fn(),
		updateTgWebProxyConfig: vi.fn(),
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
		await findByText(new RegExp(m.servers_tg_partial_disabled_running()));
		await findByText(m.servers_tg_config_disabled());
		await findByText(m.servers_tg_process_running());
		await findByText(m.servers_tg_reach_unchecked());
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

		const settingsBtn = await screen.findByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) });
		await fireEvent.click(settingsBtn);

		await screen.findByText(m.servers_tg_settings_title());
		const diagTab = screen.getByRole('button', { name: m.servers_tg_settings_tab_diagnostics() });
		await fireEvent.click(diagTab);

		await screen.findByText('Telegram: MTProto (прямое подключение)');
		expect(api.verifyWizard).toHaveBeenCalledWith('tgwebproxy');

		const footerCloseBtn = screen.getByText(m.common_close()).closest('button');
		expect(footerCloseBtn).not.toBeNull();
		await fireEvent.click(footerCloseBtn!);

		await waitFor(() => {
			expect(screen.queryByText(m.servers_tg_settings_title())).toBeNull();
		});

		// Re-open, trigger diagnostics again, and close via header × button
		await fireEvent.click(screen.getByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) }));
		await screen.findByText(m.servers_tg_settings_title());
		await fireEvent.click(screen.getByRole('button', { name: m.servers_tg_settings_tab_diagnostics() }));
		await screen.findByText('Telegram: MTProto (прямое подключение)');

		const xBtn = document.body.querySelector('button.modal-close');
		expect(xBtn).not.toBeNull();
		await fireEvent.click(xBtn!);

		await waitFor(() => {
			expect(screen.queryByText(m.servers_tg_settings_title())).toBeNull();
		});
	});

	it('triggers factory reset with unchecked KeenDNS checkbox by default and supports opting in', async () => {
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(baseConfig({ enabled: true }));
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(baseStatus({ running: true }));
		vi.mocked(api.resetTgWebProxy)
			.mockResolvedValueOnce({
				server: 'tgwebproxy',
				reset: true,
				dispatcher_reset: false,
			})
			.mockResolvedValueOnce({
				server: 'tgwebproxy',
				reset: true,
				dispatcher_reset: false,
				keendns_deleted: ['tg'],
				keendns_skipped: ['shared (используется VLESS)'],
			});

		render(TelegramWebProxyCard, {});

		await fireEvent.click(await screen.findByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) }));
		await screen.findByText(m.servers_tg_settings_title());

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
			expect(screen.queryByText(m.servers_tg_settings_title())).toBeNull();
		});

		// Open again and check KeenDNS deletion checkbox
		await fireEvent.click(await screen.findByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) }));
		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_button() }));
		await screen.findByText(m.server_reset_tg_modal_title());

		const kdCheckbox2 = screen.getByLabelText(new RegExp(m.server_reset_tg_keendns_label()));
		await fireEvent.click(kdCheckbox2);
		expect(kdCheckbox2).toBeChecked();

		await fireEvent.click(screen.getByRole('button', { name: m.server_reset_confirm_button() }));
		await waitFor(() => {
			expect(api.resetTgWebProxy).toHaveBeenCalledWith({ delete_keendns: true });
		});
		await waitFor(() => {
			expect(notifications.info).toHaveBeenCalledWith(
				m.server_reset_keendns_deleted({ domains: 'tg' }),
			);
			expect(notifications.warning).toHaveBeenCalledWith(
				m.server_reset_keendns_skipped({ domains: 'shared (используется VLESS)' }),
			);
		});
	});

	it('allows deleting the last remaining user after confirmation', async () => {
		const singleUser = { id: 'default', remark: 'Основной', enabled: true };
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, users: [singleUser] }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, users: [singleUser] }),
		);
		vi.mocked(api.tgWebProxyAction).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, users: [] }),
		);

		const { findByRole, findByText } = render(TelegramWebProxyCard, {});

		const delBtn = await findByRole('button', { name: /Удалить пользователя|Delete user/ });
		await fireEvent.click(delBtn);

		await findByText(/Удалить пользователя Telegram|Delete Telegram user/);
		await findByText(/последний пользователь|last user/);

		const confirmBtn = await findByRole('button', { name: /^(Удалить|Delete)$/ });
		await fireEvent.click(confirmBtn);

		expect(api.tgWebProxyAction).toHaveBeenCalledWith('delete_user', { user_id: 'default' });
	});

	it('allows editing and regenerating a user secret inside the Connect Telegram modal', async () => {
		const user1 = { id: 'default', remark: 'Основной', enabled: true };
		const oldSec = '00112233445566778899aabbccddeeff';
		const newSec = 'ffeeddccbbaa99887766554433221100';
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, scenario: 'direct_fake_tls', users: [user1] }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, users: [user1] }),
		);
		vi.mocked(api.revealTgWebProxySecret).mockResolvedValue({
			user_id: 'default',
			user_remark: 'Основной',
			secret: oldSec,
			direct_host: 'connect.example.netcraze.club',
			direct_port: 8443,
			tls_domain: 'www.google.com',
			tg_url: '',
			tme_url: '',
			bridge_url: '',
			mtproxy_secret: 'ee' + oldSec + '7777772e676f6f676c652e636f6d',
			mtproxy_url: 'tg://proxy?server=1.1.1.1',
			mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
			users: [
				{
					id: 'default',
					remark: 'Основной',
					enabled: true,
					secret: oldSec,
					tg_url: '',
					tme_url: '',
					bridge_url: '',
					mtproxy_secret: 'ee' + oldSec + '7777772e676f6f676c652e636f6d',
					mtproxy_url: 'tg://proxy?server=1.1.1.1',
					mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
				},
			],
		});
		vi.mocked(api.tgWebProxyAction).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, users: [user1] }),
		);

		const { findByRole } = render(TelegramWebProxyCard, {});

		const qrBtn = await findByRole('button', { name: m.servers_tg_user_qr_link() });
		await fireEvent.click(qrBtn);

		const secretInput = (await findByRole('textbox', {
			name: /Хеш \/ базовый секрет|Hash \/ base secret/,
		})) as HTMLInputElement;
		expect(secretInput.value).toBe(oldSec);

		const regenBtn = await findByRole('button', { name: /Сгенерировать|Generate/ });
		await fireEvent.click(regenBtn);
		expect(api.tgWebProxyAction).toHaveBeenCalledWith('update_user', {
			user_id: 'default',
			regenerate_secret: true,
		});

		await fireEvent.input(secretInput, { target: { value: newSec } });
		const saveBtn = await findByRole('button', { name: /Сохранить хеш|Save hash/ });
		await fireEvent.click(saveBtn);
		expect(api.tgWebProxyAction).toHaveBeenCalledWith('update_user', {
			user_id: 'default',
			secret: newSec,
		});
	});

	it('does not show a stale cached secret after regenerating and switching users', async () => {
		const userA = { id: 'u-a', remark: 'Alice', enabled: true };
		const userB = { id: 'u-b', remark: 'Bob', enabled: true };
		const secA1 = '00112233445566778899aabbccddeeff';
		const secA2 = 'ffeeddccbbaa99887766554433221100';
		const secB = 'abcdefabcdefabcdefabcdefabcdefab';
		const secrets: Record<string, string> = { 'u-a': secA1, 'u-b': secB };
		const reveal = (id: string) => ({
			user_id: id,
			user_remark: id === 'u-a' ? 'Alice' : 'Bob',
			secret: secrets[id],
			direct_host: 'connect.example.netcraze.club',
			direct_port: 8443,
			tls_domain: 'www.google.com',
			tg_url: '',
			tme_url: '',
			bridge_url: '',
			mtproxy_secret: 'ee' + secrets[id],
			mtproxy_url: 'tg://proxy?server=1.1.1.1',
			mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
			users: [
				{
					id,
					remark: id === 'u-a' ? 'Alice' : 'Bob',
					enabled: true,
					secret: secrets[id],
					tg_url: '',
					tme_url: '',
					bridge_url: '',
					mtproxy_secret: 'ee' + secrets[id],
					mtproxy_url: 'tg://proxy?server=1.1.1.1',
					mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
				},
			],
		});
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, scenario: 'direct_fake_tls', users: [userA, userB] }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, users: [userA, userB] }),
		);
		vi.mocked(api.revealTgWebProxySecret).mockImplementation(async (id?: string) => reveal(id || 'u-a'));
		vi.mocked(api.tgWebProxyAction).mockImplementation(async (action: string) => {
			if (action === 'update_user') secrets['u-a'] = secA2;
			return baseStatus({ running: true, direct_online: true, users: [userA, userB] });
		});

		const { findAllByRole, findByRole } = render(TelegramWebProxyCard, {});
		const qrButtons = await findAllByRole('button', { name: m.servers_tg_user_qr_link() });
		await fireEvent.click(qrButtons[0]);

		const secretInput = () =>
			findByRole('textbox', { name: /Хеш \/ базовый секрет|Hash \/ base secret/ }) as Promise<HTMLInputElement>;
		await waitFor(async () => expect((await secretInput()).value).toBe(secA1));

		await fireEvent.click(await findByRole('button', { name: /Сгенерировать|Generate/ }));
		await waitFor(async () => expect((await secretInput()).value).toBe(secA2));

		const userSelect = (await findByRole('combobox', { name: /Пользователь/ })) as HTMLSelectElement;
		await fireEvent.change(userSelect, { target: { value: 'u-b' } });
		await waitFor(async () => expect((await secretInput()).value).toBe(secB));

		await fireEvent.change(await findByRole('combobox', { name: /Пользователь/ }), {
			target: { value: 'u-a' },
		});
		await waitFor(async () => expect((await secretInput()).value).toBe(secA2));
	});

	describe('reveal modal lifecycle (per-user reveal, rate limit, deletions)', () => {
		type U = { id: string; remark: string; enabled: boolean };

		// Backend-like reveal: 404 for unknown users, 429 for a repeat of the
		// same secret without an intervening change (the 5 s window is longer
		// than any of these tests), an empty 200 when no users exist.
		function setupBackend(initial: U[], secrets: Record<string, string>) {
			let users = [...initial];
			const revealed = new Map<string, number>();
			const mtproto = (sec: string) => 'ee' + sec;
			const revealFor = (u: U) => ({
				user_id: u.id,
				user_remark: u.remark,
				secret: secrets[u.id],
				direct_host: 'connect.example.netcraze.club',
				direct_port: 8443,
				tls_domain: 'www.google.com',
				tg_url: '',
				tme_url: '',
				bridge_url: '',
				mtproxy_secret: mtproto(secrets[u.id]),
				mtproxy_url: 'tg://proxy?server=1.1.1.1&secret=' + mtproto(secrets[u.id]),
				mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
				users: [
					{
						id: u.id,
						remark: u.remark,
						enabled: u.enabled,
						secret: secrets[u.id],
						tg_url: '',
						tme_url: '',
						bridge_url: '',
						mtproxy_secret: mtproto(secrets[u.id]),
						mtproxy_url: 'tg://proxy?server=1.1.1.1&secret=' + mtproto(secrets[u.id]),
						mtproxy_tme_url: 'https://t.me/proxy?server=1.1.1.1',
					},
				],
			});
			const status = () => baseStatus({ running: true, direct_online: true, users: [...users] });
			vi.mocked(api.getTgWebProxyConfig).mockImplementation(async () =>
				baseConfig({ enabled: true, scenario: 'direct_fake_tls', users: [...users] }),
			);
			vi.mocked(api.getTgWebProxyStatus).mockImplementation(async () => status());
			vi.mocked(api.revealTgWebProxySecret).mockImplementation(async (id?: string) => {
				if (!id) {
					if (users.length > 0) throw new Error('test: UI must name the user when users exist');
					return { secret: '', direct_host: '', direct_port: 0, tls_domain: '', tg_url: '', tme_url: '', bridge_url: '', mtproxy_secret: '', mtproxy_url: '', mtproxy_tme_url: '', users: [] };
				}
				const u = users.find((x) => x.id === id);
				if (!u) throw new Error('telegram user not found');
				const key = id + ':' + secrets[id];
				const last = revealed.get(key);
				if (last !== undefined && Date.now() - last < 5000) throw new Error('Rate limit exceeded: please wait 5 seconds');
				revealed.set(key, Date.now());
				return revealFor(u);
			});
			vi.mocked(api.tgWebProxyAction).mockImplementation(async (action: string, body?: Record<string, unknown>) => {
				const id = body?.user_id as string | undefined;
				if (action === 'delete_user') users = users.filter((x) => x.id !== id);
				if (action === 'update_user' && id && body?.regenerate_secret) secrets[id] = 'ab'.repeat(16);
				return status();
			});
		}

		async function closeModal() {
			await fireEvent.click((await screen.findAllByRole('button', { name: m.common_close() }))[0]);
			await waitFor(() => expect(screen.queryByRole('combobox', { name: /Пользователь/ })).toBeNull());
		}

		async function deleteFirstUser() {
			await fireEvent.click((await screen.findAllByRole('button', { name: /Удалить пользователя|Delete user/ }))[0]);
			await fireEvent.click(await screen.findByRole('button', { name: /^(Удалить|Delete)$/ }));
		}

		const secretInput = () =>
			screen.findByRole<HTMLInputElement>('textbox', { name: /Хеш \/ базовый секрет|Hash \/ base secret/ });

		it('after closing, deleting the previously selected user and reopening, reveals the remaining user', async () => {
			const A = { id: 'u-a', remark: 'Alice', enabled: true };
			const B = { id: 'u-b', remark: 'Bob', enabled: true };
			setupBackend([A, B], { 'u-a': '11'.repeat(16), 'u-b': '22'.repeat(16) });
			render(TelegramWebProxyCard, {});

			await fireEvent.click((await screen.findAllByRole('button', { name: m.servers_tg_user_qr_link() }))[0]);
			await waitFor(async () => expect((await secretInput()).value).toBe('11'.repeat(16)));
			await closeModal();
			await deleteFirstUser();
			await waitFor(() => expect(screen.getAllByRole('button', { name: m.servers_tg_user_qr_link() })).toHaveLength(1));

			vi.mocked(api.revealTgWebProxySecret).mockClear();
			await fireEvent.click(screen.getByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('22'.repeat(16)));
			expect(api.revealTgWebProxySecret).not.toHaveBeenCalledWith('u-a');
			expect(notifications.error).not.toHaveBeenCalled();
		});

		it('after closing and deleting the last user, shows the empty state without reveal errors', async () => {
			const L = { id: 'default', remark: 'Основной', enabled: true };
			setupBackend([L], { default: '33'.repeat(16) });
			render(TelegramWebProxyCard, {});

			await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('33'.repeat(16)));
			await closeModal();

			vi.mocked(api.revealTgWebProxySecret).mockClear();
			await deleteFirstUser();
			await screen.findByText(m.tg_users_empty());
			expect(screen.queryByRole('button', { name: m.servers_tg_user_qr_link() })).toBeNull();
			expect(api.revealTgWebProxySecret).not.toHaveBeenCalled();
			expect(notifications.error).not.toHaveBeenCalled();
		});

		it('reopening the same user right after closing reuses the reveal instead of hitting the rate limit', async () => {
			const A = { id: 'u-a', remark: 'Alice', enabled: true };
			setupBackend([A], { 'u-a': '44'.repeat(16) });
			render(TelegramWebProxyCard, {});

			await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('44'.repeat(16)));
			await closeModal();

			await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('44'.repeat(16)));
			expect(api.revealTgWebProxySecret).toHaveBeenCalledTimes(1);
			expect(notifications.error).not.toHaveBeenCalled();
		});

		it('re-fetches after the reuse window instead of keeping a revealed secret indefinitely', async () => {
			const A = { id: 'u-a', remark: 'Alice', enabled: true };
			setupBackend([A], { 'u-a': '66'.repeat(16) });
			const realNow = Date.now.bind(Date);
			let shift = 0;
			const nowSpy = vi.spyOn(Date, 'now').mockImplementation(() => realNow() + shift);
			try {
				render(TelegramWebProxyCard, {});
				await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
				await waitFor(async () => expect((await secretInput()).value).toBe('66'.repeat(16)));
				await closeModal();

				shift = 6000;
				await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
				await waitFor(async () => expect((await secretInput()).value).toBe('66'.repeat(16)));
				expect(api.revealTgWebProxySecret).toHaveBeenCalledTimes(2);
				expect(notifications.error).not.toHaveBeenCalled();
			} finally {
				nowSpy.mockRestore();
			}
		});

		it('after regenerating a secret and reopening, shows the new secret, not a cached one', async () => {
			const A = { id: 'u-a', remark: 'Alice', enabled: true };
			setupBackend([A], { 'u-a': '55'.repeat(16) });
			render(TelegramWebProxyCard, {});

			await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('55'.repeat(16)));
			await fireEvent.click(await screen.findByRole('button', { name: /Сгенерировать|Generate/ }));
			await waitFor(async () => expect((await secretInput()).value).toBe('ab'.repeat(16)));
			await closeModal();

			await fireEvent.click(await screen.findByRole('button', { name: m.servers_tg_user_qr_link() }));
			await waitFor(async () => expect((await secretInput()).value).toBe('ab'.repeat(16)));
			expect(notifications.error).not.toHaveBeenCalled();
		});
	});

	it('renders multi-upstream summary and opens the upstreams editor', async () => {
		const upstreams = [
			{ id: 'up-1', type: 'direct' as const, device: 'direct', weight: 10, enabled: true },
			{ id: 'up-2', type: 'socks5' as const, address: '127.0.0.1:1099', weight: 2, enabled: true },
		];
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, upstreams }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, webproxy_online: true, upstreams }),
		);

		const { findByText, findByRole } = render(TelegramWebProxyCard, {});

		await findByText(/\(\+1 (резерв|failover)\)/);

		const changeBtn = await findByRole('button', { name: m.common_edit() });
		await fireEvent.click(changeBtn);

		await findByText(/Выходные каналы \(Upstreams\)|Outbound channels \(Upstreams\)/);
		const socksInput = (await findByRole('textbox', {
			name: /Адрес SOCKS5|SOCKS5 address/,
		})) as HTMLInputElement;
		expect(socksInput.value).toBe('127.0.0.1:1099');
	});

	it('offers 1-click KeenDNS 3rd-level domain fill in manual Settings and warns on cloud KeenDNS direct_host', async () => {
		vi.mocked(api.getWizardCapabilities).mockResolvedValue({
			egress_options: [
				{
					id: 'direct',
					kind: 'direct',
					owner: 'system',
					name: 'WAN',
					iface: 'direct',
					available: true,
					state: 'up',
					current: true,
					supports_tcp: true,
					supports_udp: true,
					generation: 1,
				},
			],
			keendns: {
				state: 'supported',
				enabled: true,
				domain: 'dacha.example.keenetic.pro',
				mode: 'cloud',
				cert_ok: false,
				publications: [],
				wan_ip: '10.20.30.40',
				wan_ip_private: true,
			},
		} as unknown as Awaited<ReturnType<typeof api.getWizardCapabilities>>);
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, scenario: 'direct_fake_tls', direct_host: '' }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true }),
		);

		render(TelegramWebProxyCard, {});

		const settingsBtn = await screen.findByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) });
		await fireEvent.click(settingsBtn);

		const pillBtn = await screen.findByRole('button', {
			name: new RegExp('dacha\\.example\\.keenetic\\.pro'),
		});
		await fireEvent.click(pillBtn);

		const directHostInput = screen.getByLabelText(m.servers_tg_direct_host_label());
		expect(directHostInput).toHaveValue('dacha.example.keenetic.pro');
		await screen.findByText(m.servers_tg_direct_host_cloud_warn({ domain: 'dacha.example.keenetic.pro' }));
	});

	// Мутации: не блокировать новый недопустимый порт; блокировать и сохранённый;
	// не показывать, откроется ли порт.
	it('settings name whether the WAN port opens and refuse only a newly entered bad port', async () => {
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, scenario: 'dual', direct_host: 'tg.example.com', direct_port: 443 }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(baseStatus({ running: true, direct_online: true }));
		vi.mocked(api.updateTgWebProxyConfig).mockImplementation(async (c) => c as TgWebProxyConfig);

		render(TelegramWebProxyCard, {});
		const openSettings = async () =>
			fireEvent.click(await screen.findByRole('button', { name: new RegExp(m.servers_tg_settings_btn()) }));
		await openSettings();
		const port = () => screen.getByLabelText(m.servers_tg_direct_port_label());
		const save = () => fireEvent.click(screen.getByRole('button', { name: m.servers_tg_settings_save() }));

		expect((await screen.findByRole('alert')).textContent).toContain(m.servers_tg_direct_wan_bad_port());
		await save(); // сохранённый 443 сохранению не мешает
		await waitFor(() => expect(api.updateTgWebProxyConfig).toHaveBeenCalledTimes(1));

		await openSettings();
		await fireEvent.input(port(), { target: { value: '22' } });
		await save();
		expect(api.updateTgWebProxyConfig).toHaveBeenCalledTimes(1);
		expect(notifications.error).toHaveBeenCalledWith(m.servers_ext_wiz_s3_direct_port_invalid());

		await fireEvent.input(port(), { target: { value: '9443' } });
		await screen.findByText(m.servers_tg_direct_wan_opens({ port: 9443 }));
		await fireEvent.input(screen.getByLabelText(m.servers_tg_direct_host_label()), { target: { value: '' } });
		await screen.findByText(m.servers_tg_direct_wan_no_host({ port: 9443 }));
		await save();
		await waitFor(() => expect(api.updateTgWebProxyConfig).toHaveBeenCalledTimes(2));
		expect(vi.mocked(api.updateTgWebProxyConfig).mock.calls[1][0]).toMatchObject({ direct_host: '', direct_port: 9443 });
	});

	// Мутация: убрать direct_wan_error из TgProxyMethods.
	it('shows the WAN firewall error and the closed port on the card', async () => {
		vi.mocked(api.getTgWebProxyConfig).mockResolvedValue(
			baseConfig({ enabled: true, scenario: 'direct_fake_tls', direct_host: '' }),
		);
		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(baseStatus({ running: true, direct_online: true }));
		const view = render(TelegramWebProxyCard, {});
		await screen.findByText(m.servers_tg_direct_wan_no_host({ port: 8443 }));
		view.unmount();

		vi.mocked(api.getTgWebProxyStatus).mockResolvedValue(
			baseStatus({ running: true, direct_online: true, direct_wan_error: 'xtables lock' }),
		);
		render(TelegramWebProxyCard, {});
		await screen.findByText(m.servers_tg_direct_wan_error({ error: 'xtables lock' }));
	});
});
