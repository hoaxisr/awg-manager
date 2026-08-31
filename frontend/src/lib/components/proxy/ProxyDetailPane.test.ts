// Мастер подменял деталь у ненастроенного инстанса: клик по строке уводил в
// мастер, параметры были недоступны, и единственным входом в форму оказывался
// тумблер запуска — инстанс поднимался с пустым конфигом и падал.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { FreeTurnConfig, WdttConfig, WdttServerConfig } from '$lib/types';

vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

const apiMock = vi.hoisted(() => ({
	ensureWdttWgTunnel: vi.fn().mockResolvedValue(undefined),
	ensureWdttRawTunnel: vi.fn().mockResolvedValue(undefined),
	importConfig: vi.fn(),
	getFreeTurnCaptchaStatus: vi.fn(),
	getSettings: vi.fn().mockResolvedValue({}),
	getWdttServerClients: vi.fn().mockResolvedValue({ clients: [] }),
	listAccessPolicies: vi.fn().mockResolvedValue([]),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, waitFor } from '@testing-library/svelte';
import ProxyDetailPane from './ProxyDetailPane.svelte';
import type { ProxyInstanceRow } from './rows';

/** Сервер ровно в том состоянии, что привёз стенд: заведён, пароля нет. */
const UNCONFIGURED: WdttServerConfig = {
	listen: '0.0.0.0:56002',
	wgPort: 56001,
	natMode: 'full',
	relayMode: 'wg',
	openFirewall: true,
};

const shareRow: ProxyInstanceRow = {
	key: 'wdtt:server:default',
	id: 'default',
	protocol: 'wdtt',
	role: 'server',
	name: 'Сервер',
	state: 'stopped',
	autostart: false,
	orphanedPid: false,
	binaryPresent: true,
	mode: 'wg',
};

function mount(wizard: 'new' | 'instance' | null) {
	const wdttConfig = {
		clients: [],
		servers: [{ id: 'default', name: 'Сервер', config: { ...UNCONFIGURED } }],
	} as unknown as WdttConfig;
	const ftConfig = { clients: [], servers: [] } as unknown as FreeTurnConfig;
	return render(ProxyDetailPane, {
		props: {
			activeTab: 'share' as const,
			exitRow: null,
			exitKey: null,
			shareRow,
			shareKey: shareRow.key,
			wdttConfig,
			ftConfig,
			wdttStatus: null,
			ftStatus: null,
			policies: [],
			tunnels: [],
			busyKeys: [],
			exitWizard: null,
			shareWizard: wizard,
			ontoggle: async () => {},
			onstatuses: async () => {},
			onconfigs: async () => {},
			onreload: async () => {},
			onexitdone: () => {},
			onsharedone: () => {},
		},
	});
}

describe('ProxyDetailPane: мастер открывается только явно', () => {
	beforeEach(() => vi.clearAllMocks());

	it('ненастроенный сервер показывает деталь, а не мастер', async () => {
		const { queryByRole, findByRole } = mount(null);
		// Заголовок детали — имя инстанса; мастер назвался бы «Настроить раздачу».
		expect(await findByRole('heading', { name: 'Сервер', level: 2 })).toBeTruthy();
		expect(queryByRole('heading', { name: 'Настроить раздачу' })).toBeNull();
	});

	it('поля пароля владельца в детали нет — оно техническое', async () => {
		// Пароль владельца форку не обязателен (`serverWrapKeys.Count() == 0`
		// роняет старт только при полном отсутствии паролей), а панель им не
		// пользуется: абонентов она пишет в passwords.json сама.
		const { findByRole, queryByLabelText } = mount(null);
		await findByRole('heading', { name: 'Сервер', level: 2 });
		expect(queryByLabelText('Главный пароль')).toBeNull();
	});

	it('явно открытый мастер деталь подменяет', async () => {
		const { findByRole } = mount('instance');
		expect(await findByRole('heading', { name: 'Настроить раздачу' })).toBeTruthy();
	});

	it('мастер существующего сервера не спрашивает протокол заново', async () => {
		// Шаг «Протокол» для заведённого инстанса — вопрос о решённом: сменить
		// протокол существующего сервера нельзя. Активный шаг помечен классом
		// `.active` в списке шагов (WizardSteps).
		const { container, findByRole } = mount('instance');
		await findByRole('heading', { name: 'Настроить раздачу' });
		await waitFor(() => {
			const active = container.querySelector('.step.active');
			expect(active?.textContent).toContain('Параметры сервера');
		});
	});

	it('мастер НОВОГО сервера начинается с протокола', async () => {
		// Обратная страховка: пропуск первого шага касается только существующего
		// инстанса — у нового протокол ещё не выбран.
		const { container, findByRole } = mount('new');
		await findByRole('heading', { name: 'Настроить раздачу' });
		await waitFor(() => {
			const active = container.querySelector('.step.active');
			expect(active?.textContent).toContain('Протокол');
		});
	});
});
