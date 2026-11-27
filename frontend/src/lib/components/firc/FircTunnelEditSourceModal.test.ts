import { afterEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { api } from '$lib/api/client';
import { m } from '$lib/i18n';
import FircTunnelEditSourceModal from './FircTunnelEditSourceModal.svelte';

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

describe('FircTunnelEditSourceModal: интервал обновления подписки', () => {
	it('передаёт и сохраняет interval = 0 при выборе режима «Вручную»', async () => {
		const normalizeSpy = vi.spyOn(api, 'fircNormalizeSource').mockResolvedValue({
			sources: [
				{
					id: 'a1b2c3d4',
					kind: 'subscription',
					name: 'ManualSub',
					url: 'https://example.com/sub',
					interval: 0,
				},
			],
		});
		const onadd = vi.fn().mockResolvedValue(undefined);

		render(FircTunnelEditSourceModal, {
			kind: 'subscription',
			headerProfiles: [],
			onclose: vi.fn(),
			onadd,
		});

		const urlInput = screen.getByLabelText<HTMLInputElement>(m.firc_tunnel_edit_sub_url_label());
		await fireEvent.input(urlInput, { target: { value: 'https://example.com/sub' } });

		const intervalSelect = screen.getByLabelText<HTMLSelectElement>(
			m.firc_tunnel_edit_auto_refresh_label(),
		);
		await fireEvent.change(intervalSelect, { target: { value: '0' } });

		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => expect(onadd).toHaveBeenCalledTimes(1));
		expect(normalizeSpy).toHaveBeenCalledWith(
			expect.objectContaining({
				input: 'https://example.com/sub',
				interval: 0,
			}),
		);
		expect(onadd).toHaveBeenCalledWith([
			expect.objectContaining({
				kind: 'subscription',
				url: 'https://example.com/sub',
				interval: 0,
			}),
		]);
	});

	it('не затирает interval = 0 из ответа сервера значением по умолчанию', async () => {
		vi.spyOn(api, 'fircNormalizeSource').mockResolvedValue({
			sources: [
				{
					id: 'b2c3d4e5',
					kind: 'subscription',
					name: 'ServerManual',
					url: 'https://example.com/manual',
					interval: 0,
				},
			],
		});
		const onadd = vi.fn().mockResolvedValue(undefined);

		render(FircTunnelEditSourceModal, {
			kind: 'subscription',
			headerProfiles: [],
			onclose: vi.fn(),
			onadd,
		});

		const urlInput = screen.getByLabelText<HTMLInputElement>(m.firc_tunnel_edit_sub_url_label());
		await fireEvent.input(urlInput, { target: { value: 'https://example.com/manual' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => expect(onadd).toHaveBeenCalledTimes(1));
		expect(onadd).toHaveBeenCalledWith([
			expect.objectContaining({
				kind: 'subscription',
				interval: 0,
			}),
		]);
	});

	it('для режима ссылки не отправляет интервал в fircNormalizeSource', async () => {
		const normalizeSpy = vi.spyOn(api, 'fircNormalizeSource').mockResolvedValue({
			sources: [
				{
					id: 'c3d4e5f6',
					kind: 'link',
					name: 'Node',
					link: 'vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls',
				},
			],
		});
		const onadd = vi.fn().mockResolvedValue(undefined);

		render(FircTunnelEditSourceModal, {
			kind: 'link',
			headerProfiles: [],
			onclose: vi.fn(),
			onadd,
		});

		const linkInput = screen.getByLabelText<HTMLTextAreaElement>(m.firc_tunnel_edit_links_label());
		await fireEvent.input(linkInput, {
			target: { value: 'vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => expect(onadd).toHaveBeenCalledTimes(1));
		expect(normalizeSpy).toHaveBeenCalledWith(
			expect.objectContaining({
				interval: undefined,
			}),
		);
	});

	it('сохраняет выбранный «Вручную» (0), если сервер опустил interval или вернул ошибку нормализации', async () => {
		vi.spyOn(api, 'fircNormalizeSource').mockResolvedValueOnce({
			sources: [
				{
					id: 'd4e5f6a7',
					kind: 'subscription',
					name: 'OmittedInterval',
					url: 'https://example.com/sub-omit',
				},
			],
		});
		const onadd1 = vi.fn().mockResolvedValue(undefined);

		render(FircTunnelEditSourceModal, {
			kind: 'subscription',
			headerProfiles: [],
			onclose: vi.fn(),
			onadd: onadd1,
		});

		await fireEvent.input(screen.getByLabelText(m.firc_tunnel_edit_sub_url_label()), {
			target: { value: 'https://example.com/sub-omit' },
		});
		await fireEvent.change(screen.getByLabelText(m.firc_tunnel_edit_auto_refresh_label()), {
			target: { value: '0' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => expect(onadd1).toHaveBeenCalledTimes(1));
		expect(onadd1).toHaveBeenCalledWith([expect.objectContaining({ interval: 0 })]);

		cleanup();
		vi.spyOn(api, 'fircNormalizeSource').mockRejectedValueOnce(new Error('network error'));
		const onadd2 = vi.fn().mockResolvedValue(undefined);

		render(FircTunnelEditSourceModal, {
			kind: 'subscription',
			headerProfiles: [],
			onclose: vi.fn(),
			onadd: onadd2,
		});

		await fireEvent.input(screen.getByLabelText(m.firc_tunnel_edit_sub_url_label()), {
			target: { value: 'https://example.com/sub-fallback' },
		});
		await fireEvent.change(screen.getByLabelText(m.firc_tunnel_edit_auto_refresh_label()), {
			target: { value: '0' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => expect(onadd2).toHaveBeenCalledTimes(1));
		expect(onadd2).toHaveBeenCalledWith([expect.objectContaining({ interval: 0 })]);
	});
});
