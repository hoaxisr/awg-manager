// Логика мастера «Настроить раздачу» (ia.md §3.4): доступность протоколов на
// шаге 1, готовность шагов, подсказка Raw-порта и создание сервера с первым
// абонентом. Компонент отвечает за экраны, модуль — за решения.

import { api } from '$lib/api/client';
import { setListenPort } from '$lib/utils/listenPortUtils';
import { addedPassword } from './serverClients';
import type { ProxyProtocol } from './rows';
import type {
	FreeTurnServerConfig,
	WdttServerConfig,
} from '$lib/types';

/** Тексты берутся по ID микрокопии; своих строк в мастере нет. */
export const SHARE_WIZARD_TEXT = {
	/** WS-11 */
	wdttExistsBadge: 'уже настроен',
	/** WS-12 */
	wdttExistsNote: 'WDTT-сервер на роутере может быть только один',
	/** WS-13 */
	wdttExistsInfo:
		'Раздача WDTT занимает общий интерфейс роутера с фиксированным адресом, второй такой сервер не поднимется. FreeTurn-серверов можно настроить сколько угодно.',
	/** WS-14 */
	wdttUnsupportedBadge: 'недоступен на этом роутере',
	/** WS-15 */
	wdttUnsupportedInfo: 'Сервер WDTT не собран под процессор этого роутера.',
} as const;

/** Дефолтный DTLS-порт WDTT-сервера (`DefaultServerConfig`, internal/wdtt/types.go). */
export const DEFAULT_WDTT_PORT = 56002;

/** Дефолтный listen FreeTurn-сервера (`DefaultServerConfig`, internal/freeturn/types.go). */
export const DEFAULT_FT_PORT = 56000;

/** Верхняя граница серверного диапазона бэкенда: 56000..56099. */
const SHARE_PORT_MAX = DEFAULT_FT_PORT + 100;

/**
 * Порт нового сервера: первый свободный, начиная с дефолта бинаря, в диапазоне
 * бэкенда 56000..56099 (`nextServerListen` — internal/freeturn/validate.go:135
 * и internal/wdtt/validate.go:187). Это подсказка поля: порт назначает бэкенд,
 * он же подвинет занятый (`ensureUniqueServerListenAddr`). WDTT-сервер на
 * роутере один, но резерв портов у него общий с FreeTurn-серверами
 * (`proxylisten.CrossChecker`, internal/wdtt/server.go:38,130) — занятый порт
 * бэкенд подвинет и ему, поэтому подсказывать занятое нельзя обоим.
 */
export function nextSharePort(protocol: ProxyProtocol, used: number[] = []): number {
	const start = protocol === 'wdtt' ? DEFAULT_WDTT_PORT : DEFAULT_FT_PORT;
	const taken = new Set(used.filter((p) => Number.isInteger(p) && p > 0));
	for (let port = start; port < SHARE_PORT_MAX; port++) {
		if (!taken.has(port)) return port;
	}
	return start;
}

/** Чего не хватило шагу 4 для ссылки: '' — ссылка собралась. */
export type ShareLinkGap = '' | 'addr' | 'conf' | 'confFailed';

/**
 * Почему ссылки нет после запуска (WS-44/WS-47).
 *
 * `conf` — FreeTurn без `.conf` пира: ссылку собирать не из чего (пир заведён
 * в Keenetic OS, приватного ключа у нас нет), и вставить его есть куда.
 * `confFailed` — `.conf` не пришёл: запрос за ним отказал. Поля вставки в этой
 * ветке нет, звать «вставьте .conf» некуда — там печатается сам отказ запроса.
 * `addr` — ссылку запрашивали, но она не собралась: адреса сервера нет.
 * Причины разные, и называть их одной строкой значит врать.
 */
export function shareLinkGap(o: {
	protocol: ProxyProtocol;
	peerConf: string;
	/** Текст отказа запроса `.conf` пира; пусто — запрос не отказывал. */
	confError?: string;
	link?: string;
	linkQwdtt?: string;
}): ShareLinkGap {
	if (o.link?.trim() || o.linkQwdtt?.trim()) return '';
	if (o.protocol !== 'freeturn' || o.peerConf.trim()) return 'addr';
	return o.confError?.trim() ? 'confFailed' : 'conf';
}

/** Ссылку FreeTurn без `.conf` пира не собрать — по любой из двух причин. */
export function shareLinkNeedsConf(gap: ShareLinkGap): boolean {
	return gap === 'conf' || gap === 'confFailed';
}

/** Карточка протокола заблокирована: бейдж, подпись под карточками и (i). */
export interface ShareCardBlock {
	badge: string;
	/** Подпись рядом с карточками; у ветки «не собран» её нет. */
	note: string;
	info: string;
}

/**
 * Почему WDTT-карточка недоступна (WS-11..WS-15).
 *
 * `serverSupported === undefined` — статус ещё не загружен либо бинарь его не
 * отдаёт: карточка ДОСТУПНА (W-03), гадать не из чего. Сборка под процессор
 * разбирается первой: там, где сервера нет вовсе, ни одного инстанса и быть
 * не может.
 */
export function wdttCardBlock(o: {
	serverExists: boolean;
	serverSupported?: boolean;
}): ShareCardBlock | null {
	if (o.serverSupported === false) {
		return {
			badge: SHARE_WIZARD_TEXT.wdttUnsupportedBadge,
			note: '',
			info: SHARE_WIZARD_TEXT.wdttUnsupportedInfo,
		};
	}
	if (o.serverExists) {
		return {
			badge: SHARE_WIZARD_TEXT.wdttExistsBadge,
			note: SHARE_WIZARD_TEXT.wdttExistsNote,
			info: SHARE_WIZARD_TEXT.wdttExistsInfo,
		};
	}
	return null;
}

/**
 * Верхняя граница порта WDTT-сервера: Raw-половина занимает СЛЕДУЮЩИЙ порт
 * (`internal/wdtt/ports.go`), и для 65535 его уже не существует.
 */
const MAX_WDTT_PORT = 65534;

/**
 * WS-19: Raw-половина занимает следующий порт за DTLS (`internal/wdtt/ports.go`).
 * Число считается от введённого порта и пересчитывается при вводе — литералом
 * его зашивать нельзя (оговорка факт-чека к WS-19). Порт, для которого
 * Raw-половины не существует, подсказку не считает: она соврала бы.
 */
export function rawPortHint(port: string): string {
	const dtls = Number(port.trim());
	const raw =
		Number.isInteger(dtls) && dtls > 0 && dtls <= MAX_WDTT_PORT ? dtls + 1 : DEFAULT_WDTT_PORT + 1;
	return `Raw-половина займёт следующий порт — ${raw}`;
}

/** Поля шага 2 — параметры сервера (WS-16..WS-28). */
export interface ShareWizardFields {
	/** WDTT: главный пароль сервера. */
	password: string;
	/** Порт сервера: DTLS у WDTT, listen у FreeTurn. */
	port: string;
	/** WDTT — три порта сервера, FreeTurn — один (SH-57/SH-57a). */
	firewall: boolean;
	/** FreeTurn: `-connect`, адрес WG-сервера роутера. */
	connect: string;
	obfProfile: FreeTurnServerConfig['obfProfile'];
	obfKey: string;
}

function validPort(port: string, protocol: ProxyProtocol): boolean {
	const n = Number(port.trim());
	// У WDTT порт не последний: следом идёт Raw-половина (WS-19).
	return Number.isInteger(n) && n > 0 && n <= (protocol === 'wdtt' ? MAX_WDTT_PORT : 65535);
}

/**
 * Шаг 2: тот же критерий, по которому бэкенд пускает сервер в старт — главный
 * пароль у WDTT (`internal/wdtt/server.go:220`) и backend-адрес у FreeTurn
 * (`internal/freeturn/service.go:732`). Порт назначает бэкенд, но правленый
 * руками он обязан быть портом.
 */
export function shareStep2Ready(s: {
	protocol: ProxyProtocol;
	password: string;
	port: string;
	connect: string;
}): boolean {
	if (!validPort(s.port, s.protocol)) return false;
	return s.protocol === 'wdtt' ? !!s.password.trim() : !!s.connect.trim();
}

/** Тот же критерий для сохранённого конфига: настроен ли сервер раздачи. */
export function shareConfigSetupComplete(
	wdtt?: WdttServerConfig,
	ft?: FreeTurnServerConfig,
): boolean {
	if (wdtt) return !!wdtt.password?.trim();
	if (ft) return !!ft.connect?.trim();
	return false;
}

/** Поля шага 3 — первый абонент (WS-29..WS-38). */
export interface ShareWizardClient {
	/** WDTT: имя абонента (WS-31), FreeTurn: комментарий (WS-37). */
	name: string;
	/** WDTT: пароль абонента, пусто — сгенерирует бэкенд (WS-33). */
	password: string;
	/** WDTT: VK-хеш абонента (WS-34). */
	vkHash: string;
	/** FreeTurn: Client ID (WS-36). */
	clientId: string;
	/** FreeTurn: внести Client ID в список разрешённых (WS-38). */
	allow: boolean;
}

/** Случайный hex — главный пароль (WS-20), ключ обфускации (WS-20), Client ID. */
export function randomHex(bytes: number): string {
	const buf = new Uint8Array(bytes);
	crypto.getRandomValues(buf);
	return Array.from(buf, (b) => b.toString(16).padStart(2, '0')).join('');
}

/** Peer ссылки: голому адресу дописывается порт сервера, как в панели ссылок. */
export function peerWithPort(peer: string, port: number): string {
	const raw = peer.trim();
	if (!raw || raw.includes(':') || !port) return raw;
	return `${raw}:${port}`;
}

export interface ShareCommitInput {
	protocol: ProxyProtocol;
	fields: ShareWizardFields;
	client: ShareWizardClient;
	/** Собрать ссылку после запуска (WS-43) либо только запустить (WS-42). */
	withLink: boolean;
	/** Адрес сервера для абонента (WS-39); пусто — подставит бэкенд. */
	peer: string;
	/** .conf выбранного пира: он вкладывается в ссылку FreeTurn. */
	peerConf?: string;
	/**
	 * Сервер, который мастер настраивает вместо создания нового: инстанс,
	 * открытый кнопкой «Мастер», либо созданный прошлой попыткой. Отказ любого
	 * следующего этапа оставляет сервер на бэкенде — второго не появляется.
	 */
	existing?: { id: string; config: WdttServerConfig | FreeTurnServerConfig };
	oncreated?: (created: {
		id: string;
		config: WdttServerConfig | FreeTurnServerConfig;
	}) => void;
	/**
	 * Пароль абонента, заведённого прошлой попыткой: повтор его не дублирует,
	 * а выдаёт ссылку на него.
	 */
	addedClientPassword?: string;
	onclientadded?: (password: string) => void;
}

export interface ShareCommitResult {
	id: string;
	protocol: ProxyProtocol;
	/** `wdtt://` либо `freeturn://`; пусто — ссылку собрать не удалось (WS-44). */
	link: string;
	/** `qwdtt://` — только у WDTT. */
	linkQwdtt: string;
}

/**
 * Создание сервера раздачи с первым абонентом и его запуск (ia.md §3.4).
 *
 * Порядок этапов — требование бэкенда: абонент отвергается, пока не сохранён
 * главный пароль (`AddServerClient`, server_clients.go:290), а запись
 * allowlist до старта не требует перезапуска сервера. Каждый этап бросает
 * свою ошибку: что не прошло, владелец видит как есть.
 */
export async function commitShareWizard(input: ShareCommitInput): Promise<ShareCommitResult> {
	const { fields, client } = input;
	const port = Number(fields.port.trim()) || 0;

	if (input.protocol === 'wdtt') {
		let id = input.existing?.id ?? '';
		let cfg = input.existing?.config as WdttServerConfig | undefined;
		if (!cfg) {
			const inst = await api.createWdttServer();
			id = inst.id;
			cfg = inst.config;
			input.oncreated?.({ id, config: cfg });
		}

		// Абонент заводится ПЕРЕД сохранением конфига и сам приносит главный
		// пароль: `AddServerClient` дописывает его ПОСЛЕ абонента
		// (server_clients.go). Обратный порядок сохранил бы пароль сервера и
		// при отказе добавления — сервер остался бы с паролем и без единого
		// абонента, то есть незапускаемым (гейт SH-91).
		let password = input.addedClientPassword ?? '';
		if (!password) {
			const before = cfg.clients ?? [];
			const st = await api.addWdttServerPanelUser(id, {
				password: client.password.trim() || undefined,
				comment: client.name.trim() || undefined,
				vkHash: client.vkHash.trim() || undefined,
				mainPassword: fields.password.trim(),
			});
			// Заданный руками пароль знаем и без сверки состава; сгенерированный
			// ищем как запись, которой до добавления не было.
			password = client.password.trim() || addedPassword(before, st.users ?? []);
			input.onclientadded?.(password);
		}

		cfg.password = fields.password.trim();
		cfg.listen = setListenPort(cfg.listen || `0.0.0.0:${DEFAULT_WDTT_PORT}`, port);
		cfg.openFirewall = fields.firewall;
		const saved = await api.updateWdttServerInstance(id, cfg);
		cfg = saved.config;
		input.oncreated?.({ id, config: cfg });

		await api.startWdttServerInstance(id);
		if (!input.withLink) return { id, protocol: 'wdtt', link: '', linkQwdtt: '' };

		const res = await api.generateWdttServerLink(id, {
			peer: peerWithPort(input.peer, port) || undefined,
			password: password || undefined,
			vkHashes: client.vkHash.trim() ? [client.vkHash.trim()] : undefined,
		});
		return {
			id,
			protocol: 'wdtt',
			link: res.link ?? '',
			linkQwdtt: res.linkQwdtt ?? '',
		};
	}

	let id = input.existing?.id ?? '';
	let cfg = input.existing?.config as FreeTurnServerConfig | undefined;
	if (!cfg) {
		const inst = await api.createFreeTurnServer();
		id = inst.id;
		cfg = inst.config;
		input.oncreated?.({ id, config: cfg });
	}
	cfg.connect = fields.connect.trim();
	cfg.listen = setListenPort(cfg.listen || `0.0.0.0:${DEFAULT_FT_PORT}`, port);
	cfg.obfProfile = fields.obfProfile;
	cfg.obfKey = fields.obfKey.trim();
	cfg.openFirewall = fields.firewall;
	cfg = await api.updateFreeTurnServerInstance(id, cfg);
	input.oncreated?.({ id, config: cfg });

	const clientId = client.clientId.trim();
	// Запись в список — ДО старта: тогда сервер читает её сразу при запуске и
	// перезапускать его не придётся (`-clients-file` читается на старте).
	if (client.allow && clientId) {
		await api.addFreeTurnServerAllowlistClient(id, clientId, client.name.trim());
	}

	await api.startFreeTurnServer(id);
	if (!input.withLink) return { id, protocol: 'freeturn', link: '', linkQwdtt: '' };

	const res = await api.generateFreeTurnLink({
		serverId: id,
		clientId: clientId || undefined,
		name: client.name.trim() || undefined,
		peer: peerWithPort(input.peer, port) || undefined,
		wg: input.peerConf?.trim() || undefined,
	});
	return { id, protocol: 'freeturn', link: res.link ?? '', linkQwdtt: '' };
}
