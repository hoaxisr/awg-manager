import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
vi.mock('$lib/utils/qrSvg', () => ({ renderCrispQrDataUrl: vi.fn(async () => 'data:image/png;base64,') }));
import ConnectionResult from './ConnectionResult.svelte';
import { m } from '$lib/i18n';

describe('ConnectionResult: Telegram', () => {
	it('только веб-прокси — без прямой ссылки и ручных параметров MTProxy', () => {
		render(ConnectionResult, {
			props: {
				kind: 'tgwebproxy',
				creds: { kind: 'tgwebproxy', web_link: 'tg://webproxy?server=c.example&secret=abc', tg_secret: 'abc' },
				onreveal: vi.fn(),
			},
		});
		expect(screen.queryByText(m.servers_wizard_result_mtproxy_link())).toBeNull();
		expect(screen.queryByText(m.servers_wizard_result_manual_params())).toBeNull();
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
});
