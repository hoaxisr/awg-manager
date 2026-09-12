import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen, waitFor } from '@testing-library/svelte';
import AmneziaPremiumWizard from './AmneziaPremiumWizard.svelte';
import type { AmneziaPremiumCatalog, AmneziaPremiumKeyState } from '$lib/types';

const amneziaPremiumKeyState = vi.fn();
const amneziaPremiumCatalog = vi.fn();
const amneziaPremiumSaveKey = vi.fn();
const amneziaPremiumForgetKey = vi.fn();
const amneziaPremiumConfig = vi.fn();
const amneziaPremiumMirror = vi.fn();

vi.mock('$lib/api/client', () => ({
	api: {
		amneziaPremiumKeyState: (...a: unknown[]) => amneziaPremiumKeyState(...a),
		amneziaPremiumCatalog: (...a: unknown[]) => amneziaPremiumCatalog(...a),
		amneziaPremiumSaveKey: (...a: unknown[]) => amneziaPremiumSaveKey(...a),
		amneziaPremiumForgetKey: (...a: unknown[]) => amneziaPremiumForgetKey(...a),
		amneziaPremiumConfig: (...a: unknown[]) => amneziaPremiumConfig(...a),
		amneziaPremiumMirror: (...a: unknown[]) => amneziaPremiumMirror(...a),
		amneziaPremiumSaveMirror: vi.fn()
	}
}));

// Фикстуры намеренно НЕ похожи на умолчания компонента и различимы между
// собой: страна «Fixtureland» не встречается нигде, кроме каталога, поэтому её
// появление на экране однозначно означает «каталог отрисован».
const KEY_PRESENT: AmneziaPremiumKeyState = { stored: true, usable: true, saveError: '' };
const KEY_ABSENT: AmneziaPremiumKeyState = { stored: false, usable: false, saveError: '' };

const CATALOG: AmneziaPremiumCatalog = {
	planName: 'Fixture Plan Omega',
	subscriptionEndDate: '2031-07-19T00:00:00Z',
	activeDeviceCount: 3,
	maxDeviceCount: 9,
	countries: [
		{ code: 'fx', name: 'Fixtureland [P2P]', protocols: ['awg'] },
		{ code: 'zq', name: 'Zedquay', protocols: ['awg'] }
	],
	issuedConfigs: []
};

/** Промис, который тест резолвит вручную: им ловится «поздний ответ». */
function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

/** Даёт микрозадачам и эффектам Svelte доработать. */
async function settle() {
	for (let i = 0; i < 4; i++) await Promise.resolve();
	await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
	vi.clearAllMocks();
	localStorage.clear();
	amneziaPremiumCatalog.mockResolvedValue(CATALOG);
	amneziaPremiumForgetKey.mockResolvedValue(KEY_ABSENT);
	amneziaPremiumMirror.mockResolvedValue({ mirrorUrl: 'https://mirror-alpha.fixture.test/cp' });
});

describe('AmneziaPremiumWizard', () => {
	it('гасит ответ, долетевший после закрытия: при следующем открытии каталога нет', async () => {
		const first = deferred<AmneziaPremiumKeyState>();
		const second = deferred<AmneziaPremiumKeyState>();
		amneziaPremiumKeyState.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

		const { rerender } = render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		// Закрыли, пока состояние ключа ещё летело, и открыли заново.
		await rerender({ open: false, onclose: vi.fn(), onconfig: vi.fn() });
		await rerender({ open: true, onclose: vi.fn(), onconfig: vi.fn() });

		// Второе открытие: ключа на роутере нет — экран ввода ключа.
		second.resolve(KEY_ABSENT);
		await settle();
		expect(screen.getByLabelText('Ключ подписки Amnezia Premium')).toBeTruthy();

		// А теперь долетает ответ ПЕРВОГО открытия: ключ есть, каталог грузится.
		first.resolve(KEY_PRESENT);
		await settle();

		expect(screen.queryByText('Fixtureland [P2P]')).toBeNull();
		expect(screen.getByLabelText('Ключ подписки Amnezia Premium')).toBeTruthy();
	});

	it('ввод ключа не перезапускает инициализацию: запрос состояния ровно один', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_ABSENT);

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		const field = screen.getByLabelText('Ключ подписки Amnezia Premium');
		await fireEvent.input(field, { target: { value: 'vpn://test-key-fixture-alpha' } });
		await fireEvent.input(field, { target: { value: 'vpn://test-key-fixture-alpha-2' } });
		await settle();

		expect(amneziaPremiumKeyState).toHaveBeenCalledTimes(1);
	});

	it('после «забыть ключ» поздний каталог не воскрешает список стран', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
		const late = deferred<AmneziaPremiumCatalog>();
		amneziaPremiumCatalog.mockReturnValue(late.promise);

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		await fireEvent.click(screen.getByRole('button', { name: 'Забыть ключ' }));
		await settle();
		expect(screen.getByLabelText('Ключ подписки Amnezia Premium')).toBeTruthy();

		// Каталог забытой подписки долетает уже после сброса.
		late.resolve(CATALOG);
		await settle();

		expect(screen.queryByText('Fixtureland [P2P]')).toBeNull();
		expect(screen.queryByText('Fixture Plan Omega')).toBeNull();
		expect(screen.getByLabelText('Ключ подписки Amnezia Premium')).toBeTruthy();
	});

	it('экран ошибки даёт три действия, включая ввод другого ключа', async () => {
		amneziaPremiumKeyState.mockRejectedValue(new Error('Зеркало Amnezia недоступно (fixture)'));

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		expect(screen.getByText('Зеркало Amnezia недоступно (fixture)')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Закрыть' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Повторить' })).toBeTruthy();

		const another = screen.getByRole('button', { name: 'Ввести другой ключ' });
		await fireEvent.click(another);
		await settle();
		expect(screen.getByLabelText('Ключ подписки Amnezia Premium')).toBeTruthy();
	});

	it('на экране ошибки зеркала адрес зеркала раскрыт и показывает действующий', async () => {
		amneziaPremiumKeyState.mockRejectedValue(new Error('Зеркало Amnezia недоступно (fixture)'));

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		// Правка адреса — единственный способ починиться, когда зеркало не
		// отвечает: прятать её за раскрывашкой на этом экране нельзя.
		const field = screen.getByLabelText<HTMLInputElement>('Адрес зеркала Amnezia');
		expect(field.value).toBe('https://mirror-alpha.fixture.test/cp');
	});

	it('на вводе ключа адрес зеркала свёрнут и запроса за собой не тянет', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_ABSENT);

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		expect(screen.queryByLabelText('Адрес зеркала Amnezia')).toBeNull();
		expect(amneziaPremiumMirror).not.toHaveBeenCalled();
	});

	it('ключ из localStorage подставляется и стирается после успешного входа без «запомнить»', async () => {
		localStorage.setItem('awgm.tunnels.new.premiumVpnKey', 'vpn://test-key-fixture-legacy');
		amneziaPremiumKeyState.mockResolvedValue(KEY_ABSENT);
		amneziaPremiumSaveKey.mockResolvedValue(KEY_ABSENT);

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		const field = screen.getByLabelText<HTMLTextAreaElement>('Ключ подписки Amnezia Premium');
		expect(field.value).toBe('vpn://test-key-fixture-legacy');

		await fireEvent.click(screen.getByRole('button', { name: 'Продолжить' }));
		await settle();

		// store=false — «запомнить» не отмечали, а запись из localStorage всё
		// равно обязана исчезнуть (F272).
		expect(amneziaPremiumSaveKey).toHaveBeenCalledWith('vpn://test-key-fixture-legacy', {
			store: false
		});
		expect(localStorage.getItem('awgm.tunnels.new.premiumVpnKey')).toBeNull();
	});

	it('повторная выдача спрашивает подтверждение и отдаёт конфигурацию вызывающему', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
		amneziaPremiumCatalog.mockResolvedValue({
			...CATALOG,
			issuedConfigs: [
				{
					countryCode: 'fx',
					lastIssuedAt: '2031-01-05T10:00:00Z',
					portalUpdatedAt: '2030-12-01T10:00:00Z',
					sourceType: 'country_config'
				},
				{
					countryCode: 'fx',
					lastIssuedAt: '2031-01-06T10:00:00Z',
					portalUpdatedAt: '2030-12-01T10:00:00Z',
					sourceType: 'gateway_account'
				}
			]
		} satisfies AmneziaPremiumCatalog);
		amneziaPremiumConfig.mockResolvedValue({ countryCode: 'fx', config: '[Interface]\n# fixture' });

		const onconfig = vi.fn();
		const onclose = vi.fn();
		render(AmneziaPremiumWizard, { props: { open: true, onclose, onconfig } });
		await settle();

		await fireEvent.click(screen.getByText('Fixtureland [P2P]'));
		await fireEvent.click(screen.getByRole('button', { name: 'Создать туннель' }));
		await settle();

		// Счётчик активных устройств — длина массива из хелпера: запись
		// gateway_account одна, country_config в него не входит.
		expect(screen.getByText('Активных устройств по этой стране: 1.')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'Выдать повторно' }));
		await waitFor(() => expect(onconfig).toHaveBeenCalledTimes(1));

		expect(onconfig).toHaveBeenCalledWith({
			countryCode: 'fx',
			config: '[Interface]\n# fixture',
			suggestedName: 'awg-fx',
			backend: 'nativewg'
		});
		expect(onclose).toHaveBeenCalledTimes(1);
	});

	it('недоступный NativeWG не выбирается, и выдача уезжает на kernel (F277)', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
		amneziaPremiumConfig.mockResolvedValue({ countryCode: 'zq', config: '[Interface]\n# fixture-zq' });

		const onconfig = vi.fn();
		render(AmneziaPremiumWizard, {
			props: {
				open: true,
				backendAvailability: { nativewg: false, kernel: true },
				onclose: vi.fn(),
				onconfig
			}
		});
		await settle();

		const nativewg = screen.getByRole('button', { name: 'NativeWG' });
		expect(nativewg.hasAttribute('disabled')).toBe(true);
		expect(nativewg.getAttribute('aria-pressed')).toBe('false');
		expect(screen.getByRole('button', { name: 'Kernel' }).getAttribute('aria-pressed')).toBe(
			'true'
		);

		await fireEvent.click(screen.getByText('Zedquay'));
		await fireEvent.click(screen.getByRole('button', { name: 'Создать туннель' }));
		await waitFor(() => expect(onconfig).toHaveBeenCalledTimes(1));

		// Слот устройств подписки тратится ДО импорта, поэтому недоступный
		// бэкенд не должен доехать даже выбранным по умолчанию.
		expect(onconfig.mock.calls[0][0].backend).toBe('kernel');
	});

	it('без единого доступного бэкенда выдача не предлагается (F277)', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);

		render(AmneziaPremiumWizard, {
			props: {
				open: true,
				backendAvailability: { nativewg: false, kernel: false },
				onclose: vi.fn(),
				onconfig: vi.fn()
			}
		});
		await settle();

		await fireEvent.click(screen.getByText('Zedquay'));
		const create = screen.getByRole('button', { name: 'Создать туннель' });
		expect(create.hasAttribute('disabled') || create.classList.contains('is-disabled')).toBe(true);
	});

	it('истёкшая подписка не даёт выдать конфигурацию', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
		amneziaPremiumCatalog.mockResolvedValue({
			...CATALOG,
			subscriptionEndDate: '2019-03-04T00:00:00Z'
		} satisfies AmneziaPremiumCatalog);

		render(AmneziaPremiumWizard, {
			props: { open: true, onclose: vi.fn(), onconfig: vi.fn() }
		});
		await settle();

		expect(screen.getByText('Подписка истекла — конфигурации не выдаются.')).toBeTruthy();
		const create = screen.getByRole('button', { name: 'Создать туннель' });
		expect(create.hasAttribute('disabled') || create.classList.contains('is-disabled')).toBe(true);
	});

	it('режим замены: страна, которой в списке нет, заранее не выбирается', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);
		amneziaPremiumCatalog.mockResolvedValue({
			...CATALOG,
			// Подписка отдаёт эту страну одним vless — мастер её скрывает.
			countries: [...CATALOG.countries, { code: 'vl', name: 'Vlessland', protocols: ['vless'] }]
		} satisfies AmneziaPremiumCatalog);

		render(AmneziaPremiumWizard, {
			props: {
				open: true,
				replaceTarget: { id: 'awg9', name: 'fx-tunnel-vless', country: 'vl' },
				onclose: vi.fn(),
				onconfig: vi.fn()
			}
		});
		await settle();

		expect(screen.queryByText('Vlessland')).toBeNull();
		// Выбранной строки нет — значит и заменять нечем, кнопка недоступна.
		const replace = screen.getByRole('button', { name: 'Заменить конфиг' });
		expect(replace.hasAttribute('disabled') || replace.classList.contains('is-disabled')).toBe(
			true
		);
	});

	it('режим замены: без полей имени и бэкенда, страна туннеля выбрана заранее', async () => {
		amneziaPremiumKeyState.mockResolvedValue(KEY_PRESENT);

		render(AmneziaPremiumWizard, {
			props: {
				open: true,
				replaceTarget: { id: 'awg7', name: 'fx-tunnel-old', country: 'zq' },
				countryTunnels: [{ name: 'fx-tunnel-old', amneziaCountry: 'zq' }],
				onclose: vi.fn(),
				onconfig: vi.fn()
			}
		});
		await settle();

		expect(screen.getByText('Amnezia Premium → fx-tunnel-old')).toBeTruthy();
		expect(screen.queryByLabelText('Имя туннеля')).toBeNull();
		expect(screen.queryByRole('button', { name: 'NativeWG' })).toBeNull();
		expect(screen.getByRole('button', { name: 'Заменить конфиг' })).toBeTruthy();
		// Метка «туннель …» приходит из premiumCountryLabel по карте стран.
		expect(screen.getByText('туннель fx-tunnel-old')).toBeTruthy();
		expect(screen.getByRole('option', { name: /Zedquay/ }).getAttribute('aria-selected')).toBe(
			'true'
		);
	});
});
