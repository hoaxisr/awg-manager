import { describe, expect, it, vi, beforeEach } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
vi.mock('$lib/utils/qrSvg', () => ({
	renderCrispQrDataUrl: vi.fn(async (text: string) => `data:image/svg+xml;utf8,${encodeURIComponent(text)}`),
}));
import ConnectionResult from './ConnectionResult.svelte';
import { EXTERNAL_ACCESS_LABELS } from '../external-access/connectionLabels';
import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
import { m } from '$lib/i18n';

describe('ConnectionResult: Telegram', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it('только веб-прокси — без прямой ссылки, без ручных параметров MTProxy и с не-ee QR', async () => {
		const rawSec = '00112233445566778899aabbccddeeff';
		const webLink = `tg://webproxy?server=c.example&secret=${rawSec}`;
		render(ConnectionResult, {
			props: {
				kind: 'tgwebproxy',
				creds: { kind: 'tgwebproxy', web_link: webLink, tg_secret: rawSec, public_host: 'c.example' },
				onreveal: vi.fn(),
			},
		});
		await waitFor(() => {
			expect(renderCrispQrDataUrl).toHaveBeenCalledWith(webLink);
		});
		expect(screen.queryByText(m.servers_wizard_result_mtproxy_link())).toBeNull();
		expect(screen.queryByText(m.servers_wizard_result_manual_params())).toBeNull();
		expect(screen.getByDisplayValue(webLink)).toBeInTheDocument();
		const openLink = screen.getByRole('button', { name: new RegExp(m.servers_wizard_result_open_telegram()) });
		expect(openLink).toHaveAttribute('href', webLink);
	});

	it('прямой MTProxy — в ручных параметрах секрет Fake-TLS, а не голый', () => {
		render(ConnectionResult, {
			props: {
				kind: 'tgwebproxy',
				creds: {
					kind: 'tgwebproxy',
					direct_link: 'tg://proxy?server=h.example&port=8443&secret=eeabc6578',
					tg_secret: 'abc',
					mtproxy_secret: 'eeabc6578',
					direct_host: 'h.example',
					direct_port: 8443,
				},
				onreveal: vi.fn(),
			},
		});
		expect(screen.getByText('eeabc6578')).toBeTruthy();
		expect(screen.queryByText('abc')).toBeNull();
	});

	it('dual-режим переключает QR, ссылку и ручные параметры между MTProto (ee...) и Web Proxy', async () => {
		const rawSec = '00112233445566778899aabbccddeeff';
		const eeSec = `ee${rawSec}79612e7275`;
		const directLink = `tg://proxy?server=203.0.113.10&port=8443&secret=${eeSec}`;
		const webLink = `tg://webproxy?server=tg.example.com&secret=${rawSec}`;

		render(ConnectionResult, {
			props: {
				kind: 'tgwebproxy',
				creds: {
					kind: 'tgwebproxy',
					direct_link: directLink,
					web_link: webLink,
					tg_secret: rawSec,
					mtproxy_secret: eeSec,
					direct_host: '203.0.113.10',
					direct_port: 8443,
					public_host: 'tg.example.com',
				},
				onreveal: vi.fn(),
			},
		});

		await waitFor(() => {
			expect(renderCrispQrDataUrl).toHaveBeenLastCalledWith(directLink);
		});
		expect(screen.getByDisplayValue(directLink)).toBeInTheDocument();
		expect(screen.getByText(eeSec)).toBeInTheDocument();
		expect(screen.getByText('203.0.113.10')).toBeInTheDocument();

		const webTabBtn = screen.getByRole('button', { name: EXTERNAL_ACCESS_LABELS.telegramWeb });
		await fireEvent.click(webTabBtn);

		await waitFor(() => {
			expect(renderCrispQrDataUrl).toHaveBeenLastCalledWith(webLink);
		});
		expect(screen.getByDisplayValue(webLink)).toBeInTheDocument();
		expect(screen.queryByText(m.servers_wizard_result_manual_params())).toBeNull();
	});
});
