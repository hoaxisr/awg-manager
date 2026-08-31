import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { api } from './client';
import {
	bareId,
	instanceKey,
	instancePath,
	startedAtFromUptime,
	toCaptchaOverview,
	toFreeTurnClientConfig,
	toFreeTurnClientPatch,
	toFreeTurnConfig,
	toFreeTurnServerConfig,
	toFreeTurnServerPatch,
	toFreeTurnStatus,
	toWdttClientConfig,
	toWdttClientPatch,
	toWdttConfig,
	effectiveStaticWan,
	toWdttServerConfig,
	toWdttServerPatch,
	toWdttStatus,
	type ProxyInstallStatus,
	type ProxyInstanceView,
	type ProxyListData
} from './proxyInstances';
import type { FreeTurnClientConfig, FreeTurnServerConfig, WdttClientConfig, WdttServerConfig } from '$lib/types';

// Фикстуры — формы ответа новой поверхности (задачи 7-12), а не пересказ
// мапперов: ожидания ниже пишутся литералами, иначе мутация уехала бы вместе
// с ожиданием и тест не смог бы упасть.

const wdttClientView: ProxyInstanceView = {
	key: 'wdtt-client:nl',
	id: 'nl',
	kind: 'wdtt-client',
	name: 'Нидерланды',
	enabled: true,
	sub: 'https://sub.example/nl',
	peerWg: 'vps.example:56002',
	peerRaw: 'vps.example:56003',
	config: {
		connMode: 'wg',
		listen: '127.0.0.1:9000',
		peer: 'vps.example:56002',
		passwordSet: true,
		vkHashes: 'h1,h2',
		workers: 9,
		obfs: 'audio',
		fingerprint: 'chrome',
		deviceId: 'dev-1',
		captchaMode: 'auto',
		vkAuthMode: 'vkcalls',
		policies: [{ name: 'Policy0', order: 0 }]
	},
	process: {
		running: true,
		pid: 4242,
		address: '10.66.0.5',
		uptimeS: 120,
		lastError: 'предыдущий сбой',
		mode: 'wg',
		wgConfig: '[Interface]\n',
		clients: 3,
		log: 'хвост журнала',
		binary: '/opt/bin/wdtt-client',
		binaryPresent: true
	}
};

const wdttServerView: ProxyInstanceView = {
	key: 'wdtt-server:default',
	id: 'default',
	kind: 'wdtt-server',
	name: 'Раздача',
	enabled: false,
	linkPeer: 'wan.example:56002',
	linkVkHashes: 'h9',
	statsLog: 'disk',
	config: {
		listen: '0.0.0.0:56002',
		wgPort: 56001,
		passwordSet: true,
		botTokenSet: false,
		natMode: 'full',
		policy: 'Policy0',
		lanSegments: ['Home'],
		ndmsIface: 'OpkgTun18',
		wgIface: 'opkgtun18',
		rawIface: 'opkgtun19',
		rawNdmsIface: 'OpkgTun19',
		relayMode: 'raw',
		exposeToPolicies: true,
		openFirewall: true
	},
	process: { running: false, binary: '/opt/bin/wdtt-server', binaryPresent: false }
};

const ftClientView: ProxyInstanceView = {
	key: 'freeturn-client:nl',
	id: 'nl',
	kind: 'freeturn-client',
	name: 'FT Нидерланды',
	enabled: true,
	config: {
		listen: '127.0.0.1:9100',
		peer: 'turn.example:3478',
		provider: 'vk',
		links: 'l1',
		streams: 10,
		transport: 'tcp',
		mode: 'udp',
		bond: true,
		turnHost: 'turn.example',
		turnPort: 3478,
		obfProfile: 'rtpopus',
		obfKeySet: true,
		streamsPerCred: 5,
		platform: 'mobile',
		dnsMode: 'doh',
		dnsServers: '1.1.1.1',
		clientId: 'cid-1',
		sub: 'https://sub.example/ft',
		debug: true
	},
	process: {
		running: true,
		pid: 777,
		uptimeS: 60,
		clients: 2,
		log: 'ft-журнал',
		binary: '/opt/bin/freeturn-client',
		binaryPresent: true
	}
};

const ftServerView: ProxyInstanceView = {
	key: 'freeturn-server:default',
	id: 'default',
	kind: 'freeturn-server',
	name: 'FT Раздача',
	enabled: false,
	config: {
		listen: '0.0.0.0:56000',
		connect: '10.0.0.1:51820',
		mode: 'tcp',
		obfProfile: 'none',
		obfKeySet: false,
		clientsFile: '/opt/etc/ft/clients.json',
		debug: false,
		openFirewall: false
	},
	process: { running: false, binary: '/opt/bin/freeturn-server', binaryPresent: false }
};

const list: ProxyListData = {
	seed: { seeded: true, certified: true },
	instances: [wdttClientView, wdttServerView, ftClientView, ftServerView]
};

const install: ProxyInstallStatus = {
	serverSupported: true,
	installAvailable: true,
	installVersion: '1.4.2',
	installedVersion: '1.4.1',
	updateAvailable: true,
	installing: false,
	routerClock: '2026-08-24 15:00:00 MSK'
};

describe('startedAtFromUptime', () => {
	it('момент старта считается от аптайма снимка', () => {
		expect(startedAtFromUptime(120, Date.parse('2026-08-24T12:02:00.000Z'))).toBe(
			'2026-08-24T12:00:00.000Z'
		);
	});

	it('аптайма нет — метки нет: «стартовал сейчас» выдумывать нельзя', () => {
		expect(startedAtFromUptime(undefined, Date.parse('2026-08-24T12:02:00.000Z'))).toBeUndefined();
		expect(startedAtFromUptime(0, Date.parse('2026-08-24T12:02:00.000Z'))).toBeUndefined();
	});
});

describe('toWdttStatus: блок процесса и install-блок', () => {
	const now = Date.parse('2026-08-24T12:02:00.000Z');

	it('процесс инстанса собирается из process-блока целиком', () => {
		expect(toWdttStatus(list, install, now).clients).toEqual([
			{
				id: 'nl',
				name: 'Нидерланды',
				status: {
					running: true,
					pid: 4242,
					startedAt: '2026-08-24T12:00:00.000Z',
					lastError: 'предыдущий сбой',
					log: 'хвост журнала',
					dtlsConnections: 3,
					binary: '/opt/bin/wdtt-client',
					binaryPresent: true,
					wgConfig: '[Interface]\n',
					rawClientIp: '10.66.0.5',
					rawIface: undefined,
					ndmsIface: undefined,
					rawNdmsIface: undefined
				}
			}
		]);
	});

	it('у остановленного процесса метки старта нет, даже если снимок помнит аптайм', () => {
		const stale = {
			...wdttServerView,
			process: { ...wdttServerView.process, running: false, uptimeS: 900 }
		};
		const st = toWdttStatus({ seed: list.seed, instances: [stale] }, install, now).servers[0].status;
		expect(st.startedAt).toBeUndefined();
	});

	it('поля без производителя приходят пустыми, а не выдуманными', () => {
		const st = toWdttStatus(list, install, now).servers[0].status;
		expect(st.orphanedPid).toBeUndefined();
		expect(st.appliedExposeToPolicies).toBeUndefined();
		expect(st.startedAt).toBeUndefined();
	});

	it('NDMS-имена сервера берутся из конфига записи', () => {
		const st = toWdttStatus(list, install, now).servers[0].status;
		expect(st.ndmsIface).toBe('OpkgTun18');
		expect(st.rawNdmsIface).toBe('OpkgTun19');
		expect(st.rawIface).toBe('opkgtun19');
	});

	it('install-блок целиком приезжает из статуса установки', () => {
		const st = toWdttStatus(list, install, now);
		expect({
			serverSupported: st.serverSupported,
			installAvailable: st.installAvailable,
			installVersion: st.installVersion,
			installedVersion: st.installedVersion,
			updateAvailable: st.updateAvailable,
			installing: st.installing,
			routerClock: st.routerClock
		}).toEqual({
			serverSupported: true,
			installAvailable: true,
			installVersion: '1.4.2',
			installedVersion: '1.4.1',
			updateAvailable: true,
			installing: false,
			routerClock: '2026-08-24 15:00:00 MSK'
		});
	});

	it('пустой install-блок не выдаёт «установлено»', () => {
		const st = toWdttStatus(list, {}, now);
		expect(st.installAvailable).toBe(false);
		expect(st.updateAvailable).toBe(false);
		expect(st.installing).toBe(false);
		expect(st.installedVersion).toBeUndefined();
	});

	it('legacy-зеркала client/server — первый инстанс своей роли', () => {
		const st = toWdttStatus(list, install, now);
		expect(st.client.binary).toBe('/opt/bin/wdtt-client');
		expect(st.server.binary).toBe('/opt/bin/wdtt-server');
	});

	it('инстансов роли нет — зеркало пусто, а не «бинарь на месте»', () => {
		const st = toWdttStatus({ seed: list.seed, instances: [] }, install, now);
		expect(st.client).toEqual({ running: false, binary: '', binaryPresent: false });
		expect(st.clients).toEqual([]);
	});
});

describe('toWdttConfig: секреты и поля записи', () => {
	it('пароль наружу не отдаётся — приходит признак', () => {
		const cfg = toWdttConfig(list).clients[0].config;
		expect(cfg.password).toBe('');
		expect(cfg.passwordSet).toBe(true);
	});

	it('конфиг клиента собирается клиентским маппером, сервера — серверным', () => {
		const cfg = toWdttConfig(list);
		expect(cfg.clients[0].config).toEqual(toWdttClientConfig(wdttClientView));
		expect(cfg.servers[0].config).toEqual(toWdttServerConfig(wdttServerView));
		expect(cfg.clients[0].config.vkHashes).toBe('h1,h2');
		expect(cfg.servers[0].config.wgPort).toBe(56001);
	});

	it('sub и слоты адресов лежат на записи, а не в конфиге роли', () => {
		const cfg = toWdttConfig(list).clients[0].config;
		expect(cfg.sub).toBe('https://sub.example/nl');
		expect(cfg.peerWg).toBe('vps.example:56002');
		expect(cfg.peerRaw).toBe('vps.example:56003');
		expect(cfg.enabled).toBe(true);
	});

	it('память ссылки и режим журнала сервера — тоже поля записи', () => {
		const cfg = toWdttConfig(list).servers[0].config;
		expect(cfg.linkPeer).toBe('wan.example:56002');
		expect(cfg.linkVkHashes).toBe('h9');
		expect(cfg.statsLog).toBe('disk');
		expect(cfg.botTokenSet).toBe(false);
	});
});

describe('toFreeTurnStatus и toFreeTurnConfig: вторая подсистема', () => {
	const now = Date.parse('2026-08-24T12:02:00.000Z');

	it('клиенты и серверы не меняются местами', () => {
		const st = toFreeTurnStatus(list, {}, now);
		expect(st.clients.map((c) => c.id)).toEqual(['nl']);
		expect(st.clients.map((c) => c.name)).toEqual(['FT Нидерланды']);
		expect(st.servers.map((c) => c.id)).toEqual(['default']);
		expect(st.servers.map((c) => c.name)).toEqual(['FT Раздача']);
		expect(st.client.binary).toBe('/opt/bin/freeturn-client');
		expect(st.server.binary).toBe('/opt/bin/freeturn-server');
	});

	it('в конфиге тоже: клиент — клиентом, сервер — сервером', () => {
		const cfg = toFreeTurnConfig(list);
		expect(cfg.clients.map((c) => c.id)).toEqual(['nl']);
		expect(cfg.clients.map((c) => c.name)).toEqual(['FT Нидерланды']);
		expect(cfg.servers.map((s) => s.id)).toEqual(['default']);
		expect(cfg.servers.map((s) => s.name)).toEqual(['FT Раздача']);
		// Сверяем КОНФИГ целиком, а не общий для обеих ролей listen: маппер
		// не той роли отдал бы тот же адрес и остался бы незамеченным.
		expect(cfg.clients[0].config).toEqual(toFreeTurnClientConfig(ftClientView));
		expect(cfg.clients[0].config.streams).toBe(10);
		expect(cfg.clients[0].config.provider).toBe('vk');
		expect(cfg.servers[0].config).toEqual(toFreeTurnServerConfig(ftServerView));
		expect(cfg.servers[0].config.connect).toBe('10.0.0.1:51820');
	});

	it('чужая подсистема в выдачу не попадает', () => {
		const st = toFreeTurnStatus(list, {}, now);
		expect(st.clients.concat(st.servers).map((i) => i.name)).not.toContain('Нидерланды');
		const wd = toWdttStatus(list, {}, now);
		expect(wd.clients.concat(wd.servers).map((i) => i.name)).not.toContain('FT Раздача');
	});

	it('конфиг клиента FreeTurn собирается целиком', () => {
		expect(toFreeTurnClientConfig(ftClientView)).toEqual({
			enabled: true,
			listen: '127.0.0.1:9100',
			peer: 'turn.example:3478',
			provider: 'vk',
			links: 'l1',
			streams: 10,
			transport: 'tcp',
			mode: 'udp',
			bond: true,
			turnHost: 'turn.example',
			turnPort: 3478,
			obfProfile: 'rtpopus',
			obfKey: '',
			obfKeySet: true,
			streamsPerCred: 5,
			platform: 'mobile',
			dnsMode: 'doh',
			dnsServers: '1.1.1.1',
			clientId: 'cid-1',
			sub: 'https://sub.example/ft',
			debug: true
		});
	});

	it('конфиг сервера FreeTurn собирается целиком', () => {
		expect(toFreeTurnServerConfig(ftServerView)).toEqual({
			enabled: false,
			listen: '0.0.0.0:56000',
			connect: '10.0.0.1:51820',
			mode: 'tcp',
			obfProfile: 'none',
			obfKey: '',
			obfKeySet: false,
			clientsFile: '/opt/etc/ft/clients.json',
			debug: false,
			openFirewall: false
		});
	});

	it('процесс FreeTurn несёт журнал, соединения и время старта', () => {
		const st = toFreeTurnStatus(list, {}, now).clients[0].status;
		expect(st).toEqual({
			running: true,
			pid: 777,
			startedAt: '2026-08-24T12:01:00.000Z',
			lastError: undefined,
			log: 'ft-журнал',
			dtlsConnections: 2,
			binary: '/opt/bin/freeturn-client',
			binaryPresent: true
		});
	});
});

describe('режим подключения клиента и режим NAT сервера', () => {
	it('режим подключения читается из конфига, а не подставляется', () => {
		expect(toWdttConfig(list).clients[0].config.connMode).toBe('wg');
		const rawView = {
			...wdttClientView,
			config: { ...wdttClientView.config, connMode: 'raw' }
		};
		expect(
			toWdttConfig({ seed: list.seed, instances: [rawView] }).clients[0].config.connMode
		).toBe('raw');
	});

	it('режим ретрансляции сервера тоже читается, а не подставляется', () => {
		expect(toWdttConfig(list).servers[0].config.relayMode).toBe('raw');
		const wgView = {
			...wdttServerView,
			config: { ...wdttServerView.config, relayMode: 'wg' }
		};
		expect(
			toWdttConfig({ seed: list.seed, instances: [wgView] }).servers[0].config.relayMode
		).toBe('wg');
	});

	it('режим NAT без значения читается как «полный» — тот же дефолт, что у контрола', () => {
		const bare = { ...wdttServerView, config: { listen: '0.0.0.0:56002' } };
		expect(toWdttConfig({ seed: list.seed, instances: [bare] }).servers[0].config.natMode).toBe(
			'full'
		);
	});

	it('дефолт режима NAT ОДИН на чтение и на запись', () => {
		const bare = { ...wdttServerView, config: { listen: '0.0.0.0:56002' } };
		const read = toWdttConfig({ seed: list.seed, instances: [bare] }).servers[0].config;
		expect(toWdttServerPatch(read).natMode).toBe('full');
		// И у формы, где поля нет вовсе: запись не имеет права молча выключить NAT.
		expect(toWdttServerPatch({ listen: '0.0.0.0:56002', wgPort: 0, password: '' }).natMode).toBe(
			'full'
		);
	});

	it('заданные режимы NAT доезжают как есть', () => {
		expect(toWdttServerPatch({ listen: '', wgPort: 0, password: '', natMode: 'none' }).natMode).toBe(
			'none'
		);
		expect(
			toWdttServerPatch({ listen: '', wgPort: 0, password: '', natMode: 'internet-only' }).natMode
		).toBe('internet-only');
	});
});

describe('обратные мапперы: секреты (Н5) и поля без писателя', () => {
	const client: WdttClientConfig = {
		enabled: true,
		listen: '127.0.0.1:9000',
		peer: 'vps.example:56002',
		password: '',
		passwordSet: true,
		vkHashes: 'h1',
		workers: 9,
		obfs: 'audio',
		fingerprint: '',
		captchaMode: 'auto',
		connMode: 'raw',
		peerWg: 'vps.example:56002',
		peerRaw: 'vps.example:56003',
		ndmsIface: 'OpkgTun17',
		rawIface: 'opkgtun17'
	};

	it('пустой пароль в теле НЕ едет: пустое значение означает «не менять»', () => {
		expect('password' in toWdttClientPatch(client)).toBe(false);
	});

	it('пробельный пароль тоже не едет', () => {
		expect('password' in toWdttClientPatch({ ...client, password: '   ' })).toBe(false);
	});

	it('введённый пароль едет как есть', () => {
		expect(toWdttClientPatch({ ...client, password: 'secret1' }).password).toBe('secret1');
	});

	it('членство в политиках и пины в тело не попадают: присланный срез заменил бы старый целиком', () => {
		const body = toWdttClientPatch(client);
		expect('policies' in body).toBe(false);
		expect('ndmsIface' in body).toBe(false);
		expect('rawIface' in body).toBe(false);
		expect('peerWg' in body).toBe(false);
		expect('peerRaw' in body).toBe(false);
	});

	it('режим и адрес едут вместе: слот адреса заполняет хранилище', () => {
		expect(toWdttClientPatch(client)).toEqual({
			connMode: 'raw',
			listen: '127.0.0.1:9000',
			peer: 'vps.example:56002',
			vkHashes: 'h1',
			workers: 9,
			obfs: 'audio',
			fingerprint: '',
			deviceId: '',
			captchaMode: 'auto',
			vkAuthMode: ''
		});
	});

	const server: WdttServerConfig = {
		enabled: false,
		listen: '0.0.0.0:56002',
		wgPort: 56001,
		password: '',
		passwordSet: true,
		botToken: '',
		lanSegments: ['Home'],
		natMode: 'full',
		relayMode: 'raw',
		ndmsIface: 'OpkgTun18',
		wgIface: 'opkgtun18'
	};

	it('пустые секреты сервера не едут, пины половин — тоже', () => {
		const body = toWdttServerPatch(server);
		expect('password' in body).toBe(false);
		expect('botToken' in body).toBe(false);
		expect('ndmsIface' in body).toBe(false);
		expect('wgIface' in body).toBe(false);
		expect(body.lanSegments).toEqual(['Home']);
		expect(body.natMode).toBe('full');
		expect(body.relayMode).toBe('raw');
	});

	it('токен бота едет, когда его ввели', () => {
		expect(toWdttServerPatch({ ...server, botToken: '123:abc' }).botToken).toBe('123:abc');
	});

	it('ключ обфускации FreeTurn подчиняется тому же правилу', () => {
		const ftClient: FreeTurnClientConfig = {
			enabled: true,
			listen: '127.0.0.1:9100',
			peer: 'turn.example:3478',
			provider: 'vk',
			streams: 10,
			transport: 'tcp',
			mode: 'udp',
			bond: false,
			obfProfile: 'rtpopus',
			obfKey: '',
			obfKeySet: true,
			streamsPerCred: 10,
			platform: 'desktop',
			dnsMode: 'auto',
			debug: false
		};
		expect('obfKey' in toFreeTurnClientPatch(ftClient)).toBe(false);
		expect(toFreeTurnClientPatch({ ...ftClient, obfKey: 'k1' }).obfKey).toBe('k1');

		const ftServer: FreeTurnServerConfig = {
			enabled: true,
			listen: '0.0.0.0:56000',
			connect: '',
			mode: 'udp',
			obfProfile: 'none',
			obfKey: '',
			debug: false
		};
		expect('obfKey' in toFreeTurnServerPatch(ftServer)).toBe(false);
	});
});

describe('ключ инстанса и капча', () => {
	it('адрес инстанса — роль:id: id уникален только внутри роли', () => {
		expect(instanceKey('wdtt-server', 'default')).toBe('wdtt-server:default');
		expect(instancePath('wdtt-server', 'default', '/users')).toBe(
			'/proxyrt/instances/wdtt-server%3Adefault/users'
		);
	});

	it('bareId снимает роль с ключа, а строку без роли не трогает', () => {
		expect(bareId('freeturn-client:default')).toBe('default');
		expect(bareId('default')).toBe('default');
	});

	it('обзор капчи возвращает голый id: страница адресует инстансы им', () => {
		expect(
			toCaptchaOverview({
				portOpen: true,
				ownerClientId: 'freeturn-client:nl',
				ownerName: 'НЛ',
				clients: [
					{
						clientId: 'freeturn-client:nl',
						clientName: 'НЛ',
						waiting: true,
						active: true,
						queued: false,
						canOpen: true,
						url: '/api/proxyrt/instances/freeturn-client:nl/captcha/'
					}
				]
			})
		).toEqual({
			portOpen: true,
			ownerClientId: 'nl',
			ownerName: 'НЛ',
			clients: [
				{
					clientId: 'nl',
					clientName: 'НЛ',
					waiting: true,
					active: true,
					queued: false,
					canOpen: true,
					url: '/api/proxyrt/instances/freeturn-client:nl/captcha/'
				}
			]
		});
	});
});

// ─── Транспорт: адреса и тела запросов.

interface Call {
	url: string;
	method: string;
	body?: unknown;
}

function stubFetch(route: (url: string, method: string) => unknown): Call[] {
	const calls: Call[] = [];
	globalThis.fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
		const url = String(input);
		const method = (init?.method ?? 'GET').toUpperCase();
		calls.push({
			url,
			method,
			body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
		});
		return new Response(JSON.stringify({ success: true, data: route(url, method) }), {
			status: 200,
			headers: { 'Content-Type': 'application/json' }
		});
	});
	return calls;
}

describe('адреса новой поверхности', () => {
	const originalFetch = globalThis.fetch;

	beforeEach(() => {
		vi.restoreAllMocks();
	});

	afterEach(() => {
		globalThis.fetch = originalFetch;
	});

	it('статус собирается из списка инстансов и статуса установки', async () => {
		const calls = stubFetch((url) => (url.includes('/install/status') ? install : list));
		const st = await api.getWdttStatus();
		expect(calls.map((c) => `${c.method} ${c.url}`).sort()).toEqual([
			'GET /api/proxyrt/install/status?subsystem=wdtt',
			'GET /api/proxyrt/instances'
		]);
		expect(st.clients.map((c) => c.id)).toEqual(['nl']);
	});

	it('несостоявшийся посев — отказ с причиной, а не пустой список', async () => {
		stubFetch(() => ({ seed: { seeded: false, certified: false, error: 'RCI недоступен' }, instances: [] }));
		await expect(api.getWdttConfig()).rejects.toThrow('RCI недоступен');
	});

	it('режим NAT правится тем же PATCH инстанса, что и прочий конфиг', async () => {
		const calls = stubFetch(() => wdttServerView);
		await api.setWdttServerNATMode('default', 'internet-only');
		expect(calls).toEqual([
			{
				url: '/api/proxyrt/instances/wdtt-server%3Adefault',
				method: 'PATCH',
				body: { config: { natMode: 'internet-only' } }
			}
		]);
	});

	it('политика и сегменты LAN — тот же PATCH, своих ручек у них нет', async () => {
		const calls = stubFetch(() => wdttServerView);
		await api.setWdttServerPolicy('default', 'Policy1');
		await api.setWdttServerLANSegments('default', ['Home', 'Guest']);
		expect(calls.map((c) => c.body)).toEqual([
			{ config: { policy: 'Policy1' } },
			{ config: { lanSegments: ['Home', 'Guest'] } }
		]);
		expect(new Set(calls.map((c) => c.url))).toEqual(
			new Set(['/api/proxyrt/instances/wdtt-server%3Adefault'])
		);
	});

	it('подписка клиента едет отдельным полем тела, а не внутри конфига', async () => {
		const calls = stubFetch(() => wdttClientView);
		await api.updateWdttClientInstance('nl', {
			listen: '127.0.0.1:9000',
			peer: 'vps.example:56002',
			password: '',
			vkHashes: 'h1',
			workers: 9,
			obfs: '',
			fingerprint: '',
			captchaMode: 'auto',
			sub: 'https://sub.example/nl'
		});
		const body = calls[0].body as { sub?: string; config: Record<string, unknown> };
		expect(body.sub).toBe('https://sub.example/nl');
		expect('sub' in body.config).toBe(false);
	});

	it('снятая подписка едет пустой строкой: «не менять» — это отсутствие поля', async () => {
		const calls = stubFetch(() => wdttClientView);
		await api.updateWdttClientInstance('nl', {
			listen: '127.0.0.1:9000',
			peer: 'vps.example:56002',
			password: '',
			vkHashes: 'h1',
			workers: 9,
			obfs: '',
			fingerprint: '',
			captchaMode: 'auto',
			sub: ''
		});
		expect((calls[0].body as { sub?: string }).sub).toBe('');
	});

	it('режим журнала статистики едет отдельным полем тела', async () => {
		const calls = stubFetch(() => wdttServerView);
		await api.updateWdttServerInstance('default', {
			enabled: true,
			listen: '0.0.0.0:56002',
			wgPort: 56001,
			password: '',
			statsLog: 'disk'
		});
		const body = calls[0].body as { statsLog?: string; config: Record<string, unknown> };
		expect(body.statsLog).toBe('disk');
		expect('statsLog' in body.config).toBe(false);
	});

	it('правки NAT, политики и LAN режима журнала не касаются', async () => {
		const calls = stubFetch(() => wdttServerView);
		await api.setWdttServerNATMode('default', 'full');
		expect('statsLog' in (calls[0].body as object)).toBe(false);
	});

	it('сохранение формы без ввода пароля не шлёт password', async () => {
		const calls = stubFetch(() => wdttServerView);
		await api.updateWdttServerInstance('default', {
			enabled: true,
			listen: '0.0.0.0:56002',
			wgPort: 56001,
			password: '',
			passwordSet: true
		});
		const body = calls[0].body as { enabled: boolean; config: Record<string, unknown> };
		expect(body.enabled).toBe(true);
		expect('password' in body.config).toBe(false);
		expect('botToken' in body.config).toBe(false);
	});

	it('состав абонентов адресуется ключом инстанса и доносит reload', async () => {
		const calls = stubFetch(() => ({
			available: true,
			users: [
				{
					password: 'p1',
					comment: 'Ноут',
					isDeactivated: false,
					isExpired: false,
					isMainPassword: false,
					isAuto: false
				}
			],
			reload: 'delivered'
		}));
		const st = await api.addWdttServerPanelUser('default', { comment: 'Ноут' });
		expect(calls[0]).toEqual({
			url: '/api/proxyrt/instances/wdtt-server%3Adefault/users',
			method: 'POST',
			body: { comment: 'Ноут' }
		});
		expect(st.reload).toBe('delivered');
		expect(st.users[0].password).toBe('p1');
	});

	it('переименование абонента шлёт пароль в пути, а имя — в теле', async () => {
		const calls = stubFetch(() => ({ available: true, users: [] }));
		await api.renameWdttServerPanelUser('default', 'p 1/2', 'Ноут');
		expect(calls[0]).toEqual({
			url: '/api/proxyrt/instances/wdtt-server%3Adefault/users/p%201%2F2',
			method: 'PATCH',
			body: { name: 'Ноут' }
		});
	});

	it('удаление клиента сперва снимает связи, потом сносит инстанс', async () => {
		const calls = stubFetch((url) =>
			url.endsWith('/linked-tunnels/clear')
				? { deletedTunnels: ['t1'], tunnelErrors: [], message: 'linked AWG tunnels cleared' }
				: { ok: true }
		);
		const res = await api.deleteWdttClient('nl');
		expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
			'POST /api/proxyrt/instances/wdtt-client%3Anl/linked-tunnels/clear',
			'DELETE /api/proxyrt/instances/wdtt-client%3Anl'
		]);
		expect(res.deletedTunnels).toEqual(['t1']);
	});

	it('старт и стоп — намерение записи, а не своя ручка', async () => {
		const calls = stubFetch(() => wdttClientView);
		await api.startWdttClientInstance('nl');
		await api.stopWdttClientInstance('nl');
		expect(calls).toEqual([
			{ url: '/api/proxyrt/instances/wdtt-client%3Anl', method: 'PATCH', body: { enabled: true } },
			{ url: '/api/proxyrt/instances/wdtt-client%3Anl', method: 'PATCH', body: { enabled: false } }
		]);
	});

	it('подготовка WG-туннеля и разбор ссылки живут под новым неймспейсом', async () => {
		const calls = stubFetch((url) =>
			url.includes('ensure-wg-tunnel') ? { created: false } : { profile: { peer: 'x' } }
		);
		await api.ensureWdttWgTunnel('nl');
		await api.decodeWdttLink('wdtt://x');
		expect(calls.map((c) => c.url)).toEqual([
			'/api/proxyrt/instances/wdtt-client%3Anl/ensure-wg-tunnel',
			'/api/proxyrt/wdtt/link/decode'
		]);
	});

	it('установка бинарей — одна ручка с подсистемой в теле', async () => {
		const calls = stubFetch(() => ({ message: 'installed' }));
		await api.installWdttClient();
		await api.installFreeTurn();
		expect(calls).toEqual([
			{ url: '/api/proxyrt/install', method: 'POST', body: { subsystem: 'wdtt' } },
			{ url: '/api/proxyrt/install', method: 'POST', body: { subsystem: 'freeturn' } }
		]);
	});

	it('allowlist FreeTurn адресуется ключом серверного инстанса', async () => {
		const calls = stubFetch(() => ({ enabled: true, clients: [] }));
		await api.getFreeTurnServerAllowlist('default');
		await api.addFreeTurnServerAllowlistClient('default', 'abcdef0123456789', 'Ноут');
		await api.removeFreeTurnServerAllowlistClient('default', 'abcdef0123456789');
		expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
			'GET /api/proxyrt/instances/freeturn-server%3Adefault/allowlist',
			'POST /api/proxyrt/instances/freeturn-server%3Adefault/allowlist',
			'DELETE /api/proxyrt/instances/freeturn-server%3Adefault/allowlist/abcdef0123456789'
		]);
	});

	it('ссылка FreeTurn без serverId уходит инстансу default', async () => {
		const calls = stubFetch(() => ({ link: 'freeturn://x', peer: 'p' }));
		await api.generateFreeTurnLink({ name: 'Ноут' });
		expect(calls[0].url).toBe('/api/proxyrt/instances/freeturn-server%3Adefault/link');
	});
});

// F61: у wdtt-сервера, мигрированного с post-#750 конфигом, выход NAT лежит в
// СПИСКЕ natStaticWans, а фронт знал только одиночку — селектор был пуст, гейт
// говорил «не выбран WAN», а патч слал natStaticWan:"" и чистил список на
// бэкенде, из-за чего несвязанные сохранения отбивались 422.
describe('natStaticWans (F61)', () => {
	it('парсит список выходов NAT', () => {
		const cfg = toWdttServerConfig({
			...wdttServerView,
			config: {
				...wdttServerView.config,
				natMode: 'internet-only',
				natStaticWans: ['ISP', 'Sfp0']
			}
		});
		expect(cfg.natStaticWans).toEqual(['ISP', 'Sfp0']);
	});

	it('effectiveStaticWan: список старше одиночки, зеркало StaticNATList', () => {
		expect(effectiveStaticWan({ natStaticWans: ['ISP', 'Sfp0'], natStaticWan: 'Old' })).toBe('ISP');
		expect(effectiveStaticWan({ natStaticWans: [], natStaticWan: 'Old' })).toBe('Old');
		expect(effectiveStaticWan({ natStaticWan: 'Old' })).toBe('Old');
		expect(effectiveStaticWan({})).toBe('');
	});

	it('патч со списком шлёт список и НЕ шлёт одиночку', () => {
		const patch = toWdttServerPatch({
			listen: '0.0.0.0:56000',
			wgPort: 0,
			password: '',
			natMode: 'internet-only',
			natStaticWans: ['ISP', 'Sfp0']
		});
		expect(patch.natStaticWans).toEqual(['ISP', 'Sfp0']);
		expect('natStaticWan' in patch).toBe(false);
	});

	// Гвард новой ветки, не пин дефекта: на текущем коде тоже зелёный (M2).
	it('патч без списка шлёт одиночку — выбор пользователя становится правдой', () => {
		const patch = toWdttServerPatch({
			listen: '0.0.0.0:56000',
			wgPort: 0,
			password: '',
			natMode: 'internet-only',
			natStaticWan: 'ISP'
		});
		expect(patch.natStaticWan).toBe('ISP');
		expect('natStaticWans' in patch).toBe(false);
	});
});
