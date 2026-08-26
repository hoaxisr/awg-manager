import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { FreeTurnServerConfig, WdttPanelUserEntry, WdttServerConfig } from '$lib/types';

const apiMock = vi.hoisted(() => ({
	createWdttServer: vi.fn(),
	updateWdttServerInstance: vi.fn(),
	addWdttServerPanelUser: vi.fn(),
	getWdttServerPanelUsers: vi.fn(),
	startWdttServerInstance: vi.fn(),
	generateWdttServerLink: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

import {
	commitShareWizard,
	nextSharePort,
	peerWithPort,
	rawPortHint,
	shareConfigSetupComplete,
	shareLinkGap,
	shareLinkNeedsConf,
	shareStep2Ready,
	shareStep3Ready,
	wdttCardBlock,
} from './shareWizard';

describe('wdttCardBlock: доступность WDTT-карточки шага 1', () => {
	it('статус не загружен — карточка доступна (W-03)', () => {
		expect(wdttCardBlock({ serverExists: false })).toBeNull();
	});

	it('сервер уже есть — бейдж WS-11, подпись WS-12 и (i) WS-13', () => {
		const block = wdttCardBlock({ serverExists: true, serverSupported: true });
		expect(block?.badge).toBe('уже настроен');
		expect(block?.note).toBe('WDTT-сервер на роутере может быть только один');
		expect(block?.info).toContain('второй такой сервер не поднимется');
	});

	it('сервер не собран под процессор — WS-14 и WS-15, без подписи WS-12', () => {
		const block = wdttCardBlock({ serverExists: false, serverSupported: false });
		expect(block?.badge).toBe('недоступен на этом роутере');
		expect(block?.note).toBe('');
		expect(block?.info).toBe('Сервер WDTT не собран под процессор этого роутера.');
	});

	it('не собран важнее «уже есть»: инстанса без сборки быть не может', () => {
		const block = wdttCardBlock({ serverExists: true, serverSupported: false });
		expect(block?.badge).toBe('недоступен на этом роутере');
	});
});

describe('rawPortHint: WS-19 считается от введённого порта', () => {
	it('дефолтный порт даёт 56003', () => {
		expect(rawPortHint('56002')).toBe('Raw-половина займёт следующий порт — 56003');
	});

	it('порт изменён — подсказка пересчитана, а не зашита литералом', () => {
		expect(rawPortHint('40000')).toBe('Raw-половина займёт следующий порт — 40001');
	});

	it('недопечатанный порт — подсказка держится дефолта', () => {
		expect(rawPortHint('')).toBe('Raw-половина займёт следующий порт — 56003');
		expect(rawPortHint('65535')).toBe('Raw-половина займёт следующий порт — 56003');
	});
});

describe('shareStep2Ready: критерий старта бэкенда', () => {
	const base = { password: '', port: '56002', connect: '' };

	it('WDTT: без главного пароля дальше не пускает', () => {
		expect(shareStep2Ready({ ...base, protocol: 'wdtt' })).toBe(false);
		expect(shareStep2Ready({ ...base, protocol: 'wdtt', password: 'main' })).toBe(true);
	});

	it('FreeTurn: нужен backend-адрес, пароля у сервера нет', () => {
		expect(shareStep2Ready({ ...base, protocol: 'freeturn' })).toBe(false);
		expect(
			shareStep2Ready({ ...base, protocol: 'freeturn', connect: '127.0.0.1:51820' }),
		).toBe(true);
	});

	it('WDTT: 65535 отвергается — Raw-половине занять нечего', () => {
		const wdtt = { protocol: 'wdtt' as const, password: 'main', connect: '' };
		expect(shareStep2Ready({ ...wdtt, port: '65535' })).toBe(false);
		expect(shareStep2Ready({ ...wdtt, port: '65534' })).toBe(true);
	});

	it('FreeTurn: порт один, 65535 допустим', () => {
		const ft = { protocol: 'freeturn' as const, password: '', connect: '127.0.0.1:1' };
		expect(shareStep2Ready({ ...ft, port: '65535' })).toBe(true);
	});

	it('порт вне диапазона отвергается у обоих', () => {
		expect(shareStep2Ready({ protocol: 'wdtt', password: 'main', port: '0', connect: '' })).toBe(
			false,
		);
		expect(
			shareStep2Ready({
				protocol: 'freeturn',
				password: '',
				port: '70000',
				connect: '127.0.0.1:1',
			}),
		).toBe(false);
	});
});

describe('shareStep3Ready: VK-хеш первого абонента', () => {
	it('WDTT: пустое поле дальше не пускает, пробелы за значение не считаются', () => {
		expect(shareStep3Ready({ protocol: 'wdtt', vkHash: '' })).toBe(false);
		expect(shareStep3Ready({ protocol: 'wdtt', vkHash: '   ' })).toBe(false);
		expect(shareStep3Ready({ protocol: 'wdtt', vkHash: 'ab12' })).toBe(true);
	});

	it('FreeTurn: поля хеша на шаге нет — шаг готов и с пустым', () => {
		expect(shareStep3Ready({ protocol: 'freeturn', vkHash: '' })).toBe(true);
	});
});

describe('shareConfigSetupComplete: тот же критерий для сохранённого конфига', () => {
	it('WDTT-сервер настроен главным паролем', () => {
		const cfg = { listen: '0.0.0.0:56002', wgPort: 56001, password: '' } as WdttServerConfig;
		expect(shareConfigSetupComplete(cfg)).toBe(false);
		expect(shareConfigSetupComplete({ ...cfg, password: 'main' })).toBe(true);
	});

	it('FreeTurn-сервер настроен backend-адресом', () => {
		const cfg = {
			enabled: false,
			listen: '0.0.0.0:56000',
			connect: '',
			mode: 'udp',
			obfProfile: 'none',
			debug: false,
		} as FreeTurnServerConfig;
		expect(shareConfigSetupComplete(undefined, cfg)).toBe(false);
		expect(shareConfigSetupComplete(undefined, { ...cfg, connect: '127.0.0.1:51820' })).toBe(true);
	});
});

describe('nextSharePort: порт нового сервера', () => {
	it('FreeTurn берёт первый свободный из 56000..56099', () => {
		expect(nextSharePort('freeturn', [])).toBe(56000);
		expect(nextSharePort('freeturn', [56000, 56001])).toBe(56002);
	});

	it('WDTT стартует с дефолта бинаря', () => {
		expect(nextSharePort('wdtt', [])).toBe(56002);
	});

	it('WDTT обходит занятое: резерв у него общий с FreeTurn-серверами', () => {
		// Дефолтный порт занял FreeTurn-сервер — бэкенд подвинет и WDTT-сервер
		// (`ensureUniqueServerListenAddr`), значит подсказка двигается сама.
		expect(nextSharePort('wdtt', [56002])).toBe(56003);
		expect(nextSharePort('wdtt', [56002, 56003])).toBe(56004);
	});
});

describe('shareLinkGap: почему ссылки нет на шаге 4', () => {
	it('WS-47: FreeTurn без .conf пира — причина в конфиге, а не в адресе', () => {
		expect(shareLinkGap({ protocol: 'freeturn', peerConf: '' })).toBe('conf');
		// Адрес сервера заполнен, а ссылки всё равно нет: причина прежняя.
		expect(shareLinkGap({ protocol: 'freeturn', peerConf: '   ' })).toBe('conf');
	});

	it('WS-44: .conf есть, ссылка не собралась — не хватает адреса', () => {
		expect(shareLinkGap({ protocol: 'freeturn', peerConf: '[Interface]' })).toBe('addr');
		expect(shareLinkGap({ protocol: 'wdtt', peerConf: '' })).toBe('addr');
	});

	it('отказ запроса .conf — своя ветка: поля вставки там нет', () => {
		// Пир не Keenetic, `.conf` не пришёл по отказу запроса: `ConfPasteBox` не
		// показан, и звать «вставьте .conf» (WS-47) было бы в никуда.
		expect(
			shareLinkGap({ protocol: 'freeturn', peerConf: '', confError: 'сервер недоступен' }),
		).toBe('confFailed');
		// Отказа не было — ветка прежняя: вставлять .conf есть куда.
		expect(shareLinkGap({ protocol: 'freeturn', peerConf: '', confError: '   ' })).toBe('conf');
		// .conf на руках — отказ прошлой попытки причиной уже не считается.
		expect(
			shareLinkGap({
				protocol: 'freeturn',
				peerConf: '[Interface]',
				confError: 'сервер недоступен',
			}),
		).toBe('addr');
	});

	it('WS-48: порт клиента неизвестен — три ветки без .conf разведены', () => {
		// Поле вставки есть — зовём вставить (WS-47); порта нет — вставка ничего
		// не решает (WS-48); запрос отказал — печатается сам отказ бэкенда.
		const base = { protocol: 'freeturn' as const, peerConf: '' };
		expect(shareLinkGap(base)).toBe('conf');
		expect(shareLinkGap({ ...base, portUnknown: true })).toBe('port');
		expect(shareLinkGap({ ...base, confError: 'сервер недоступен' })).toBe('confFailed');
		// Отказ запроса конкретнее ненайденного порта: у него есть свой текст.
		expect(
			shareLinkGap({ ...base, portUnknown: true, confError: 'сервер недоступен' }),
		).toBe('confFailed');
		// .conf на руках — значит порт в Endpoint уже подставлен: причина в адресе.
		expect(
			shareLinkGap({ protocol: 'freeturn', peerConf: '[Interface]', portUnknown: true }),
		).toBe('addr');
	});

	it('все ветки без .conf запрещают собирать ссылку', () => {
		expect(shareLinkNeedsConf('conf')).toBe(true);
		expect(shareLinkNeedsConf('confFailed')).toBe(true);
		expect(shareLinkNeedsConf('port')).toBe(true);
		expect(shareLinkNeedsConf('addr')).toBe(false);
		expect(shareLinkNeedsConf('')).toBe(false);
	});

	it('ссылка собралась — подписи нет', () => {
		expect(shareLinkGap({ protocol: 'freeturn', peerConf: '', link: 'ft://x' })).toBe('');
		expect(shareLinkGap({ protocol: 'wdtt', peerConf: '', linkQwdtt: 'qwdtt://x' })).toBe('');
		// Отказ запроса .conf собранной ссылке не мешает: подписи нет и с ним.
		expect(
			shareLinkGap({
				protocol: 'freeturn',
				peerConf: '[Interface]',
				confError: 'сервер недоступен',
				link: 'ft://x',
			}),
		).toBe('');
	});
});

describe('peerWithPort: адрес ссылки', () => {
	it('голому адресу дописывается порт сервера', () => {
		expect(peerWithPort('203.0.113.10', 56002)).toBe('203.0.113.10:56002');
	});

	it('свой порт не перебивается, пустой адрес остаётся пустым', () => {
		expect(peerWithPort('example.org:1234', 56002)).toBe('example.org:1234');
		expect(peerWithPort('  ', 56002)).toBe('');
	});
});

/**
 * Двойник бэкенда мастера. Сохранение конфига абонентов НЕ заводит
 * (Дополнение №5): «Абонент 1» рождается только на цикле старта. Поэтому
 * порядок шагов сторожится самим списком вызовов, а побочный эффект
 * `AddServerClient` — «дописать главный пароль ПОСЛЕ абонента» — двойник
 * повторяет: на нём держится, что ссылка уходит заказанному абоненту.
 */
function fakeWdttBackend() {
	const state = {
		password: '',
		clients: [] as { password: string; comment: string; auto: boolean }[],
		calls: [] as string[],
	};
	const entries = (): WdttPanelUserEntry[] =>
		state.clients.map((c) => ({
			password: c.password,
			comment: c.comment,
			isDeactivated: false,
			isExpired: false,
			isMainPassword: c.password === state.password,
			isAuto: c.auto,
		}));

	apiMock.createWdttServer.mockImplementation(async () => {
		state.calls.push('create');
		return { id: 's1', name: 'Раздача', config: { listen: '0.0.0.0:56002', wgPort: 56001, password: '' } };
	});
	apiMock.updateWdttServerInstance.mockImplementation(async (_id: string, cfg: WdttServerConfig) => {
		state.calls.push('put');
		state.password = (cfg.password ?? '').trim();
		return { config: { ...cfg, clients: state.clients } };
	});
	apiMock.getWdttServerPanelUsers.mockImplementation(async () => {
		state.calls.push('users');
		return { available: true, users: entries() };
	});
	apiMock.addWdttServerPanelUser.mockImplementation(
		async (_id: string, opts: { password?: string; comment?: string; mainPassword?: string }) => {
			state.calls.push('add');
			const main = state.password || (opts.mainPassword ?? '').trim();
			if (!main) throw new Error('сначала задайте пароль сервера');
			state.clients.push({
				password: opts.password || 'gen-1',
				comment: opts.comment ?? '',
				auto: false,
			});
			if (!state.password) state.password = main;
			return { available: true, users: entries(), reload: 'delivered' as const };
		},
	);
	apiMock.startWdttServerInstance.mockImplementation(async () => {
		state.calls.push('start');
	});
	apiMock.generateWdttServerLink.mockImplementation(async () => ({
		link: 'wdtt://x',
		linkQwdtt: 'qwdtt://x',
	}));
	return state;
}

describe('commitShareWizard: WDTT — ссылку получает заведённый абонент', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	const fields = {
		password: 'mainpass0000',
		port: '56002',
		firewall: true,
		connect: '',
		obfProfile: 'none' as const,
		obfKey: '',
	};
	const client = { name: 'Ноутбук Пети', password: '', vkHash: '', clientId: '', allow: true };

	it('абонент заводится ДО сохранения пароля, ссылка уходит ему', async () => {
		const state = fakeWdttBackend();
		const res = await commitShareWizard({
			protocol: 'wdtt',
			fields,
			client,
			withLink: true,
			peer: '203.0.113.10',
		});

		expect(state.calls).toEqual(['create', 'users', 'add', 'put', 'start']);
		expect(state.clients.map((c) => c.comment)).toEqual(['Ноутбук Пети']);
		expect(apiMock.addWdttServerPanelUser.mock.calls[0][1]).toMatchObject({
			mainPassword: 'mainpass0000',
			comment: 'Ноутбук Пети',
		});
		// WS-29: ссылка собрана на пароль абонента шага 3.
		expect(apiMock.generateWdttServerLink.mock.calls[0][1]).toMatchObject({ password: 'gen-1' });
		expect(res.link).toBe('wdtt://x');
	});

	it('заданный руками пароль абонента уходит в ссылку как есть', async () => {
		fakeWdttBackend();
		await commitShareWizard({
			protocol: 'wdtt',
			fields,
			client: { ...client, password: 'petya12345678' },
			withLink: true,
			peer: '',
		});
		expect(apiMock.generateWdttServerLink.mock.calls[0][1]).toMatchObject({
			password: 'petya12345678',
		});
	});

	it('повтор после отказа абонента не заводит: ссылка идёт на пароль прошлой попытки', async () => {
		const state = fakeWdttBackend();
		await commitShareWizard({
			protocol: 'wdtt',
			fields,
			client,
			withLink: true,
			peer: '',
			existing: { id: 's1', config: { listen: '0.0.0.0:56002', wgPort: 56001, password: '' } },
			addedClientPassword: 'gen-1',
		});
		expect(state.calls).toEqual(['put', 'start']);
		expect(apiMock.addWdttServerPanelUser).not.toHaveBeenCalled();
		expect(apiMock.generateWdttServerLink.mock.calls[0][1]).toMatchObject({ password: 'gen-1' });
	});
});
