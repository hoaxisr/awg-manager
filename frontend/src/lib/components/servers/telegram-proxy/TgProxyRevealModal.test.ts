import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';

const apiMock = vi.hoisted(() => ({ revealTgWebProxySecret: vi.fn(), tgWebProxyAction: vi.fn() }));
const qrMock = vi.hoisted(() => ({ renderCrispQrDataUrl: vi.fn() }));
const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }));
vi.mock('$lib/api/client', () => ({ api: apiMock }));
vi.mock('$lib/utils/qrSvg', () => qrMock);
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import TgProxyRevealModal from './TgProxyRevealModal.svelte';
import { EXTERNAL_ACCESS_LABELS } from '../external-access/connectionLabels';
import { m } from '$lib/i18n';

const users = [
	{ id: 'u1', remark: 'Первый', enabled: true, secret: '' },
	{ id: 'u2', remark: 'Второй', enabled: true, secret: '' },
];

function reveal(userId: string) {
	return {
		user_id: userId,
		secret: '00112233445566778899aabbccddeeff',
		mtproxy_url: `tg://proxy?mt=${userId}`,
		tg_url: `tg://webproxy?web=${userId}`,
		web_proxies: [{ id: 'ep', hostname: 'a.example', tg_url: `tg://webproxy?web=${userId}` }],
		users: [],
	};
}

function setup(onsecretchanged = vi.fn()) {
	const r = render(TgProxyRevealModal, {
		props: {
			users,
			config: null,
			expectsMt: true,
			expectsWeb: true,
			deleteBusy: false,
			ondeleteuser: vi.fn(),
			onsecretchanged,
		},
	});
	return r.component as unknown as {
		show: (tab?: 'mtproxy' | 'webproxy', target?: string, user?: string) => Promise<void>;
		afterUserDeleted: (id: string) => Promise<void>;
	};
}

beforeEach(() => {
	vi.clearAllMocks();
	apiMock.revealTgWebProxySecret.mockImplementation(async (id?: string) => reveal(id || 'u1'));
	qrMock.renderCrispQrDataUrl.mockImplementation(async (url: string) => `data:${url}`);
});

describe('TgProxyRevealModal', () => {
	it('медленный QR прежней вкладки не ложится поверх текущей', async () => {
		const modal = setup();
		await modal.show('mtproxy');
		let releaseWeb: (v: string) => void = () => {};
		qrMock.renderCrispQrDataUrl.mockImplementationOnce(
			() => new Promise<string>((res) => (releaseWeb = res)),
		);
		await fireEvent.click(screen.getByRole('button', { name: /веб|web/i }));
		await fireEvent.click(screen.getByRole('button', { name: EXTERNAL_ACCESS_LABELS.telegramMtproto }));
		await waitFor(() =>
			expect(document.querySelector('img.qr-img')?.getAttribute('src')).toBe('data:tg://proxy?mt=u1'),
		);
		releaseWeb('data:stale-web');
		await new Promise((r) => setTimeout(r, 0));
		await tick();
		expect(document.querySelector('img.qr-img')?.getAttribute('src')).toBe('data:tg://proxy?mt=u1');
	});

	it('отказ показа после удаления пользователя закрывает окно с ошибкой', async () => {
		const modal = setup();
		await modal.show('mtproxy', undefined, 'u2');
		apiMock.revealTgWebProxySecret.mockRejectedValueOnce(new Error('boom'));
		await expect(modal.afterUserDeleted('u2')).resolves.toBeUndefined();
		expect(notify.error).toHaveBeenCalledWith('boom');
		await waitFor(() => expect(document.querySelector('img.qr-img')).toBeNull());
	});

	it('секрет сменён, повторный показ отказал: родитель всё равно узнаёт', async () => {
		const onsecretchanged = vi.fn();
		const modal = setup(onsecretchanged);
		await modal.show('mtproxy');
		const status = { running: true };
		apiMock.tgWebProxyAction.mockResolvedValueOnce(status);
		apiMock.revealTgWebProxySecret.mockRejectedValueOnce(new Error('reveal down'));
		await fireEvent.click(screen.getAllByRole('button', { name: m.tg_user_secret_generate() })[0]);
		await waitFor(() => expect(onsecretchanged).toHaveBeenCalledWith(status));
		expect(notify.error).toHaveBeenCalledWith('reveal down');
	});
});
