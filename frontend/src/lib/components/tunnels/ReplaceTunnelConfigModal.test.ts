import { describe, it, expect, vi, beforeEach } from 'vitest';
import pako from 'pako';
import { render, fireEvent, screen, waitFor } from '@testing-library/svelte';
import ReplaceTunnelConfigModal from './ReplaceTunnelConfigModal.svelte';
import type { AmneziaPremiumCatalog, AmneziaPremiumKeyState } from '$lib/types';

const replaceConfig = vi.fn();
const amneziaPremiumKeyState = vi.fn();
const amneziaPremiumCatalog = vi.fn();
const amneziaPremiumConfig = vi.fn();

vi.mock('$lib/api/client', () => ({
	api: {
		replaceConfig: (...a: unknown[]) => replaceConfig(...a),
		amneziaPremiumKeyState: (...a: unknown[]) => amneziaPremiumKeyState(...a),
		amneziaPremiumCatalog: (...a: unknown[]) => amneziaPremiumCatalog(...a),
		amneziaPremiumConfig: (...a: unknown[]) => amneziaPremiumConfig(...a),
		amneziaPremiumSaveKey: vi.fn(),
		amneziaPremiumForgetKey: vi.fn()
	}
}));

// Фикстуры различимы между собой и не совпадают с умолчаниями компонента:
// страна «zq» помечает туннель, «fx» — та, на которую его переводят.
const KEY_PRESENT: AmneziaPremiumKeyState = { stored: true, usable: true, saveError: '' };

const CATALOG: AmneziaPremiumCatalog = {
	planName: 'Fixture Plan Omega',
	subscriptionEndDate: '2031-07-19T00:00:00Z',
	countries: [
		{ code: 'fx', name: 'Fixtureland [P2P]', protocols: ['awg'] },
		{ code: 'zq', name: 'Zedquay', protocols: ['awg'] }
	],
	issuedConfigs: []
};

const PROPS = {
	open: true,
	tunnelId: 'awg5',
	tunnelName: 'fx-tunnel-old',
	tunnelState: 'stopped',
	backendLabel: 'Kernel',
	ndmsName: 'OpkgTun5',
	tunnelCountry: 'zq',
	onclose: () => {},
	onreplaced: () => {}
};

/**
 * Ключ Premium в том виде, в каком его распознаёт classifyVpnLink: zlib-сжатый
 * JSON в base64url. Секрета здесь нет — только признак типа ключа.
 */
function premiumVpnKey(): string {
	const raw = pako.deflate(JSON.stringify({ service_type: 'amnezia-premium' }));
	return 'vpn://' + btoa(String.fromCharCode(...raw)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

async function settle() {
	for (let i = 0; i < 4; i++) await Promise.resolve();
	await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
	vi.clearAllMocks();
	amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
	amneziaPremiumCatalog.mockResolvedValue(CATALOG);
	replaceConfig.mockResolvedValue({ id: 'awg5', warnings: [] });
});

describe('ReplaceTunnelConfigModal', () => {
	it('мастер подписки заменяет конфиг и переносит страну на туннель', async () => {
		amneziaPremiumConfig.mockResolvedValue({
			countryCode: 'fx',
			config: '[Interface]\n# fixture-fx'
		});
		const onreplaced = vi.fn();
		render(ReplaceTunnelConfigModal, { props: { ...PROPS, onreplaced } });

		await fireEvent.click(screen.getByText('Взять конфиг из Amnezia Premium'));
		await settle();

		// Страна туннеля выбрана заранее — мастер нацелен на этот туннель.
		expect(screen.getByText('Amnezia Premium → fx-tunnel-old')).toBeTruthy();
		expect(screen.getByRole('option', { name: /Zedquay/ }).getAttribute('aria-selected')).toBe(
			'true'
		);

		await fireEvent.click(screen.getByText('Fixtureland [P2P]'));
		await fireEvent.click(screen.getByRole('button', { name: 'Заменить конфиг' }));
		await waitFor(() => expect(replaceConfig).toHaveBeenCalledTimes(1));

		// Имя не правили — уезжает undefined; страна обязана уехать новая,
		// иначе туннель остался бы помечен «zq», к которой конфиг не имеет
		// отношения.
		expect(replaceConfig).toHaveBeenCalledWith(
			'awg5',
			'[Interface]\n# fixture-fx',
			undefined,
			'fx'
		);
		await waitFor(() => expect(onreplaced).toHaveBeenCalledTimes(1));
	});

	it('вкладка ссылки подписана постоянно — даже когда вставлен ключ Premium', async () => {
		render(ReplaceTunnelConfigModal, { props: { ...PROPS } });

		await fireEvent.click(screen.getByRole('button', { name: /Вставить ссылку/ }));
		const field = screen.getByPlaceholderText(/Вставьте vpn:\/\//);
		await fireEvent.input(field, { target: { value: premiumVpnKey() } });
		await settle();

		// Ключ распознан как премиальный — это видно по баннеру…
		expect(screen.getByText(/Это ключ Amnezia Premium/)).toBeTruthy();
		// …но подпись вкладки от него больше не зависит.
		expect(screen.getByRole('button', { name: /Вставить ссылку/ })).toBeTruthy();
		expect(screen.queryByRole('button', { name: 'Amnezia Premium' })).toBeNull();
	});
});
