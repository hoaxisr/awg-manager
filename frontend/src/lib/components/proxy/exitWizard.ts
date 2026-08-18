// Логика мастера «Вывести трафик» (ia.md §2.3): готовность шагов, перенос
// значений ссылки в конфиг инстанса и заведение созданного интерфейса в
// политику доступа. Компонент отвечает за экраны, модуль — за решения.

import { api } from '$lib/api/client';
import { linkedTunnelListenPort, patchWgConfEndpoint } from '$lib/utils/serverPeerOptions';
import { setPeer, switchConnMode } from '$lib/utils/wdttPeerMode';
import { findLinkedTunnel } from './linkedTunnel';
import type {
	AccessPolicy,
	FreeTurnClientConfig,
	FreeTurnLinkPayload,
	WdttClientConfig,
	WdttImportPayload,
} from '$lib/types';

export type ExitProtocol = 'wdtt' | 'freeturn';
export type ExitMode = 'wg' | 'raw';

/** Что шаг 1 показывает под полем ссылки. */
export type ExitSourceKind = 'none' | 'unknown' | 'subscription' | 'freeturn' | 'wdtt';

/** Поля шага 2 — всё, что человек правит руками (WE-30..WE-36). */
export interface ExitWizardFields {
	name: string;
	peer: string;
	/** Только WDTT: у FreeTurn-клиента пароля нет. */
	password: string;
	listen: string;
	/** WDTT — `-vk`, FreeTurn — `-links` (ссылки VK Calls). Подпись одна: WE-35. */
	vkHashes: string;
	/** Строкой из поля; в конфиг уезжает числом. */
	workers: string;
}

/** WDTT: 24 клиент округляет вниз до 18, 27 — ближайшее кратное (WE-37). */
export const DEFAULT_WORKERS = '27';

/** FreeTurn: кратности нет, дефолт бинаря (`DefaultClientConfig`, internal/freeturn/types.go). */
export const DEFAULT_FT_STREAMS = '10';

function defaultWorkers(protocol: ExitProtocol): string {
	return protocol === 'freeturn' ? DEFAULT_FT_STREAMS : DEFAULT_WORKERS;
}

// ─── Готовность шагов.

/**
 * Шаг 1: ссылка разобрана до peer и пароля (WDTT) либо до peer (FreeTurn).
 * Ручное создание готово выбором протокола — peer и пароль там спрашивают
 * на шаге 2, и держать пользователя на шаге 1 нечем.
 */
export function exitStep1Ready(s: {
	manual: boolean;
	protocol: ExitProtocol;
	peer: string;
	password: string;
}): boolean {
	if (s.manual) return true;
	if (!s.peer.trim()) return false;
	return s.protocol === 'freeturn' || !!s.password.trim();
}

/**
 * Шаг 2: тот же критерий, по которому клиент считается настроенным и уходит
 * из мастера в деталь (`exitConfigSetupComplete`). Остальные слагаемые
 * FreeTurn (`streamsPerCred`, `platform`, `dnsMode`) приезжают дефолтами
 * бэкенда при создании инстанса, руками их в мастере не задают.
 */
export function exitStep2Ready(s: {
	protocol: ExitProtocol;
	peer: string;
	password: string;
	vkHashes: string;
	workers: string;
}): boolean {
	if (!s.peer.trim() || !s.vkHashes.trim()) return false;
	if (!(Number(s.workers) > 0)) return false;
	return s.protocol === 'freeturn' || !!s.password.trim();
}

/** Тот же критерий для сохранённого конфига: настроен ли инстанс «Выхода». */
export function exitConfigSetupComplete(
	wdtt?: WdttClientConfig,
	ft?: FreeTurnClientConfig,
): boolean {
	if (wdtt) {
		return exitStep2Ready({
			protocol: 'wdtt',
			peer: wdtt.peer,
			password: wdtt.password,
			vkHashes: wdtt.vkHashes,
			workers: String(wdtt.workers),
		});
	}
	if (ft) {
		return exitStep2Ready({
			protocol: 'freeturn',
			peer: ft.peer,
			password: '',
			vkHashes: ft.links ?? '',
			workers: String(ft.streams),
		});
	}
	return false;
}

// ─── Значения из ссылки.

/**
 * Локальный порт нового клиента: первый свободный, как считает бэкенд —
 * 9000..9199 у WDTT (`nextClientListen`, internal/wdtt/validate.go:87) и
 * 9000..9099 у FreeTurn (internal/freeturn/validate.go:125). Это подсказка
 * поля — порт назначает бэкенд, и он же отвергнет занятый.
 */
export function nextLocalListen(listens: string[], protocol: ExitProtocol = 'wdtt'): string {
	const used = new Set(
		listens
			.map((l) => Number(l?.trim().split(':').pop()))
			.filter((p) => Number.isInteger(p) && p > 0),
	);
	const limit = protocol === 'freeturn' ? 9100 : 9200;
	for (let port = 9000; port < limit; port++) {
		if (!used.has(port)) return `127.0.0.1:${port}`;
	}
	return protocol === 'freeturn' ? '127.0.0.1:9000' : '127.0.0.1:9100';
}

/**
 * Порт из подписки одинаков для всех стран — у каждого клиента он свой,
 * поэтому берётся подсказка, а не значение документа (оговорка унаследована из
 * старого импорта прокси-вкладки главной).
 */
function listenFromPayload(payloadListen: string | undefined, candidate: string, fromSub: boolean) {
	if (fromSub) return candidate;
	return payloadListen?.trim() || candidate;
}

export function fieldsFromWdttPayload(
	p: WdttImportPayload,
	candidateListen: string,
	fromSub = false,
): ExitWizardFields {
	return {
		name: p.name?.trim() ?? '',
		peer: p.peer ?? '',
		password: p.password ?? '',
		listen: listenFromPayload(p.listen, candidateListen, fromSub),
		vkHashes: (p.vkHashes ?? []).join(','),
		workers: p.workers && p.workers > 0 ? String(p.workers) : DEFAULT_WORKERS,
	};
}

export function fieldsFromFtPayload(
	p: FreeTurnLinkPayload,
	candidateListen: string,
): ExitWizardFields {
	return {
		name: p.name?.trim() ?? '',
		peer: p.peer ?? '',
		password: '',
		listen: listenFromPayload(p.listen, candidateListen, false),
		vkHashes: '',
		workers: p.n && p.n > 0 ? String(p.n) : DEFAULT_FT_STREAMS,
	};
}

/** Поля существующего инстанса: мастер, открытый кнопкой «Мастер», правит его. */
export function fieldsFromWdttConfig(cfg: WdttClientConfig, name: string): ExitWizardFields {
	return {
		name,
		peer: cfg.peer ?? '',
		password: cfg.password ?? '',
		listen: cfg.listen ?? '',
		vkHashes: cfg.vkHashes ?? '',
		workers: cfg.workers > 0 ? String(cfg.workers) : DEFAULT_WORKERS,
	};
}

export function fieldsFromFtConfig(cfg: FreeTurnClientConfig, name: string): ExitWizardFields {
	return {
		name,
		peer: cfg.peer ?? '',
		password: '',
		listen: cfg.listen ?? '',
		vkHashes: cfg.links ?? '',
		workers: cfg.streams > 0 ? String(cfg.streams) : DEFAULT_FT_STREAMS,
	};
}

/** Пустые поля ручного создания (WE-10). */
export function emptyFields(candidateListen: string, protocol: ExitProtocol = 'wdtt'): ExitWizardFields {
	return {
		name: '',
		peer: '',
		password: '',
		listen: candidateListen,
		vkHashes: '',
		workers: defaultWorkers(protocol),
	};
}

// ─── Перенос в конфиг инстанса.

/** Параметры ссылки, которых в полях шага 2 нет, но терять их нельзя. */
export function applyWdttPayload(
	cfg: WdttClientConfig,
	p: WdttImportPayload,
	subUrl?: string,
): void {
	if (subUrl) cfg.sub = subUrl;
	if (p.deviceId) cfg.deviceId = p.deviceId;
	if (p.connMode === 'raw' || p.connMode === 'wg') switchConnMode(cfg, p.connMode);
}

export function applyFtPayload(cfg: FreeTurnClientConfig, p: FreeTurnLinkPayload): void {
	if (p.provider) cfg.provider = p.provider;
	if (p.obf) cfg.obfProfile = p.obf as FreeTurnClientConfig['obfProfile'];
	if (p.key) cfg.obfKey = p.key;
	if (p.spc && p.spc > 0) cfg.streamsPerCred = p.spc;
	if (p.cid) cfg.clientId = p.cid;
	if (p.transport) cfg.transport = p.transport as FreeTurnClientConfig['transport'];
	if (p.mode) cfg.mode = p.mode as FreeTurnClientConfig['mode'];
	if (typeof p.bond === 'boolean') cfg.bond = p.bond;
	if (p.dns === 'plain' || p.dns === 'doh' || p.dns === 'auto') cfg.dnsMode = p.dns;
}

export function applyWdttFields(
	cfg: WdttClientConfig,
	f: ExitWizardFields,
	mode: ExitMode,
): WdttClientConfig {
	switchConnMode(cfg, mode);
	setPeer(cfg, f.peer.trim());
	cfg.password = f.password;
	cfg.listen = f.listen.trim();
	cfg.vkHashes = f.vkHashes.trim();
	cfg.workers = Number(f.workers) || cfg.workers;
	return cfg;
}

export function applyFtFields(cfg: FreeTurnClientConfig, f: ExitWizardFields): FreeTurnClientConfig {
	cfg.peer = f.peer.trim();
	cfg.listen = f.listen.trim();
	cfg.links = f.vkHashes.trim();
	cfg.streams = Number(f.workers) || cfg.streams;
	return cfg;
}

/** Имя AWG-туннеля клиента (W-30, F-15): «<профиль> wdtt» / «<инстанс> FT». */
export function proxyTunnelName(protocol: ExitProtocol, name: string): string {
	const suffix = protocol === 'wdtt' ? 'wdtt' : 'FT';
	const base = name.trim() || (protocol === 'wdtt' ? 'WDTT' : 'FreeTurn');
	if (base.toLowerCase().endsWith(` ${suffix.toLowerCase()}`)) return base.slice(0, 60);
	return `${base} ${suffix}`.slice(0, 60);
}

// ─── Создание инстанса и заведение в политику.

export interface ExitCommitInput {
	protocol: ExitProtocol;
	mode: ExitMode;
	fields: ExitWizardFields;
	wdttPayload?: WdttImportPayload | null;
	ftPayload?: FreeTurnLinkPayload | null;
	/** URL подписки, если источником была она. */
	subUrl?: string;
	/** WireGuard-конфиг из ссылки: из него создаётся AWG-туннель (W-28, F-12). */
	wgConf?: string;
	/**
	 * Инстанс, открытый в мастере кнопкой «Мастер»: настройки ложатся в него,
	 * а не в новый. Конфиг — копия серверного, её мастер и правит.
	 */
	existing?: { id: string; config: WdttClientConfig | FreeTurnClientConfig };
	/**
	 * Инстанс только что создан. Отказ любого следующего шага оставляет его на
	 * бэкенде, поэтому мастер запоминает id и при повторе идёт по ветке
	 * `existing` — второго инстанса-сироты не появляется.
	 */
	oncreated?: (created: { id: string; config: WdttClientConfig | FreeTurnClientConfig }) => void;
}

export interface ExitCommitResult {
	id: string;
	protocol: ExitProtocol;
	/** Созданный AWG-туннель, если ссылка принесла конфиг. */
	tunnelId?: string;
}

/**
 * Создание инстанса под настройки мастера и его запуск (WE-48).
 *
 * Инстанс создаётся пустым и настраивается вторым запросом: бэкенд конфиг
 * запроса не мержит с дефолтами и порт назначает сам (`CreateClient`), так
 * что фронт получает честные значения бинаря, а не выдумывает свои.
 */
export async function commitExitWizard(input: ExitCommitInput): Promise<ExitCommitResult> {
	const { protocol, mode, fields } = input;
	const tunnelName = proxyTunnelName(protocol, fields.name);

	const name = fields.name.trim();

	if (protocol === 'wdtt') {
		let id = input.existing?.id ?? '';
		let cfg = input.existing?.config as WdttClientConfig | undefined;
		if (!cfg) {
			const inst = await api.createWdttClient(name || undefined);
			id = inst.id;
			cfg = inst.config;
			input.oncreated?.({ id, config: cfg });
		} else if (name) {
			await api.renameWdttClient(id, name);
		}
		if (input.wdttPayload) applyWdttPayload(cfg, input.wdttPayload, input.subUrl);
		applyWdttFields(cfg, fields, mode);
		await api.updateWdttClientInstance(id, cfg);
		const tunnelId = await importWgTunnel(input.wgConf, mode, cfg.listen, tunnelName, {
			wdttClientId: id,
		});
		await api.startWdttClientInstance(id);
		return { id, protocol, tunnelId };
	}

	let id = input.existing?.id ?? '';
	let cfg = input.existing?.config as FreeTurnClientConfig | undefined;
	if (!cfg) {
		const inst = await api.createFreeTurnClient(name || undefined);
		id = inst.id;
		cfg = inst.config;
		input.oncreated?.({ id, config: cfg });
	} else if (name) {
		await api.renameFreeTurnClient(id, name);
	}
	if (input.ftPayload) applyFtPayload(cfg, input.ftPayload);
	if (input.subUrl) cfg.sub = input.subUrl;
	applyFtFields(cfg, fields);
	await api.updateFreeTurnClientInstance(id, cfg);
	const tunnelId = await importWgTunnel(input.wgConf, 'wg', cfg.listen, tunnelName, {
		freeTurnClientId: id,
	});
	await api.startFreeTurnClient(id);
	return { id, protocol, tunnelId };
}

async function importWgTunnel(
	wgConf: string | undefined,
	mode: ExitMode,
	listen: string,
	name: string,
	link: { wdttClientId?: string; freeTurnClientId?: string },
): Promise<string | undefined> {
	const conf = wgConf?.trim();
	if (!conf || mode === 'raw') return undefined;
	const port = linkedTunnelListenPort(listen);
	if (port == null) return undefined;
	const tunnel = await api.importConfig(
		patchWgConfEndpoint(conf, port),
		name,
		undefined,
		link.freeTurnClientId,
		link.wdttClientId,
	);
	return tunnel.id;
}

/**
 * NDMS-имя интерфейса созданного инстанса: у режима Raw его выдаёт клиент при
 * старте (`ndmsIface` статуса), у режима WG им владеет AWG-туннель. Имя
 * появляется не мгновенно, поэтому опрос повторяется несколько раз.
 */
export async function resolveExitInterface(
	o: { protocol: ExitProtocol; id: string; mode: ExitMode; listen: string; tunnelId?: string },
	attempts = 5,
	delayMs = 1500,
): Promise<string> {
	for (let i = 0; i < attempts; i++) {
		if (i > 0) await new Promise((r) => setTimeout(r, delayMs));
		if (o.protocol === 'wdtt' && o.mode === 'raw') {
			const status = await api.getWdttStatus();
			const iface = status.clients?.find((c) => c.id === o.id)?.status.ndmsIface?.trim();
			if (iface) return iface;
			continue;
		}
		const all = await api.getTunnelsAll();
		const tunnels = all.tunnels ?? [];
		const byId = o.tunnelId ? tunnels.find((t) => t.id === o.tunnelId) : null;
		const linked =
			byId ?? findLinkedTunnel(tunnels, o.listen, o.protocol === 'wdtt' ? o.id : undefined);
		const iface = linked?.ndmsName?.trim();
		if (iface) return iface;
	}
	return '';
}

/** Интерфейс дописывается в КОНЕЦ порядка политики (WE-45/46). */
export function policyPermitOrder(policies: AccessPolicy[], name: string): number {
	return policies.find((p) => p.name === name)?.interfaces.length ?? 0;
}
