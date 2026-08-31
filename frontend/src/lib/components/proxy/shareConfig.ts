// Конфиг сервера «Раздачи»: приведение ответа бэкенда к плотному виду, порты
// инстанса и сохранение. Тот же контракт, что у `exitConfig.ts` детали
// «Выход»: конфиг страницы — состояние сервера, правки живут в копии детали.

import { api } from '$lib/api/client';
import { listenPortNumber, setListenPort } from '$lib/utils/listenPortUtils';
import type { NatMode } from '$lib/utils/network';
import type {
	FreeTurnConfig,
	FreeTurnServerConfig,
	FreeTurnServerInstance,
	WdttConfig,
	WdttServerConfig,
	WdttServerInstance,
} from '$lib/types';
import type { ProxyInstanceRow } from './rows';

export type ShareConfig = WdttServerConfig | FreeTurnServerConfig;
export type ShareInstance = WdttServerInstance | FreeTurnServerInstance;

// ─── Плотный конфиг.
//
// Поля с `omitempty` (`internal/wdtt/types.go`, `internal/freeturn/types.go`)
// бэкенд не сериализует, и пустая строка приезжает как отсутствующий ключ.
// `bind:value` на `undefined` бросает `props_invalid_value` (Input объявляет
// значение как `$bindable('')`), поэтому optional-строки заполняются один раз —
// сразу после ответа бэкенда. Тот же класс блокера, что C-1 задачи 3.
//
// Union-поля (`natMode`, `statsLog`, `relayMode`) в списки не входят: '' для
// них не «пусто», а поломка семантики — у контролов они читаются с дефолтом.

const WDTT_SERVER_OPTIONAL_STRINGS: readonly (keyof WdttServerConfig)[] = [
	'configDir',
];

const FT_SERVER_OPTIONAL_STRINGS: readonly (keyof FreeTurnServerConfig)[] = [
	'obfKey',
];

function fillStrings<T extends object>(cfg: T, keys: readonly (keyof T)[]): T {
	for (const key of keys) {
		if (cfg[key] === undefined) (cfg as Record<string, unknown>)[key as string] = '';
	}
	return cfg;
}

export function normalizeWdttServerConfig(cfg: WdttServerConfig): WdttServerConfig {
	return fillStrings(cfg, WDTT_SERVER_OPTIONAL_STRINGS);
}

export function normalizeFreeTurnServerConfig(cfg: FreeTurnServerConfig): FreeTurnServerConfig {
	return fillStrings(cfg, FT_SERVER_OPTIONAL_STRINGS);
}

/** Конфиги серверов страницы — на месте, сразу после загрузки. */
export function normalizeShareConfigs(wdtt: WdttConfig, ft: FreeTurnConfig): void {
	for (const inst of wdtt.servers) normalizeWdttServerConfig(inst.config);
	for (const inst of ft.servers) normalizeFreeTurnServerConfig(inst.config);
}

// ─── Режим NAT (SH-48): подписи одни на секцию и на схему.

export const natModeOptions: { value: NatMode; label: string }[] = [
	{ value: 'full', label: 'Полный' },
	{ value: 'internet-only', label: 'Интернет' },
	{ value: 'none', label: 'Без NAT' },
];

export function natModeLabel(mode?: string): string {
	return natModeOptions.find((o) => o.value === (mode || 'full'))?.label ?? '';
}

// ─── Порты инстанса.

export interface SharePort {
	listen: string;
	proto?: 'udp' | 'tcp';
	/** Подпись порта в мете строки состояния (RB-07). */
	label: string;
	port: number;
}

const DEFAULT_DTLS = 56002;

/**
 * Порты WDTT-сервера. Raw по умолчанию — DTLS+1, Direct показывается, только
 * если отличается от DTLS (иначе пиры идут на DTLS-порт).
 */
export function wdttServerPorts(cfg: WdttServerConfig): SharePort[] {
	const dtls = listenPortNumber(cfg.listen ?? '', DEFAULT_DTLS);
	const rawPort = cfg.rawListen?.trim()
		? listenPortNumber(cfg.rawListen, dtls + 1)
		: dtls + 1;
	const host = (cfg.listen ?? '').split(':')[0] || '0.0.0.0';
	const rawHost = (cfg.rawListen?.trim() ?? '').split(':')[0] || host;
	const ports: SharePort[] = [
		{ listen: setListenPort(cfg.listen || `${host}:${dtls}`, dtls, host), label: 'DTLS', port: dtls },
		{ listen: setListenPort(`${rawHost}:${rawPort}`, rawPort, rawHost), label: 'Raw', port: rawPort },
	];
	const direct = cfg.directListen?.trim();
	if (direct && listenPortNumber(direct, 0) !== dtls) {
		const port = listenPortNumber(direct, dtls);
		ports.push({ listen: direct, label: 'Direct', port });
	}
	return ports;
}

/**
 * Внутренний WG-порт сервера (`-wg-port`). В мете строки состояния его нет —
 * снаружи на него не приходят, — но освобождать его иногда нужно, поэтому в
 * списке «Освобождение портов» он отдельной строкой.
 */
export function wdttServerWgPort(cfg: WdttServerConfig): SharePort {
	const port = cfg.wgPort || 56001;
	return { listen: `0.0.0.0:${port}`, label: 'WG', port };
}

/**
 * Список секции «Освобождение портов» раздачи WDTT: порты сервера плюс
 * внутренний WG-порт, без дублей по `listen`. Совпадения реальны: raw по
 * умолчанию — DTLS+1, и при DTLS :56000 он равен дефолтному WG-порту 56001.
 * Совпавший порт показывается одной строкой.
 */
export function wdttServerKillPorts(cfg: WdttServerConfig): SharePort[] {
	const out: SharePort[] = [];
	for (const p of [...wdttServerPorts(cfg), wdttServerWgPort(cfg)]) {
		if (!out.some((x) => x.listen === p.listen)) out.push(p);
	}
	return out;
}

export function freeTurnServerPorts(cfg: FreeTurnServerConfig): SharePort[] {
	const port = listenPortNumber(cfg.listen ?? '', 56000);
	return [
		{
			listen: cfg.listen || `0.0.0.0:${port}`,
			proto: cfg.mode === 'tcp' ? 'tcp' : 'udp',
			label: '',
			port,
		},
	];
}

// ─── Сохранение.

/** Инстанс выбранной строки в конфиге своего протокола. */
export function shareInstance(
	row: ProxyInstanceRow | null,
	wdtt: WdttConfig | null,
	ft: FreeTurnConfig | null,
): ShareInstance | undefined {
	if (!row) return undefined;
	return row.protocol === 'wdtt'
		? wdtt?.servers.find((s) => s.id === row.id)
		: ft?.servers.find((s) => s.id === row.id);
}

/**
 * Сохранение конфига сервера. Ответ бэкенда ложится в конфиг страницы —
 * состояние сервера после записи; редактируемая копия детали живёт отдельно.
 */
export async function saveShareInstance(
	row: ProxyInstanceRow,
	inst: ShareInstance,
	config: ShareConfig,
): Promise<ShareConfig> {
	if (row.protocol === 'wdtt') {
		const res = await api.updateWdttServerInstance(row.id, config as WdttServerConfig);
		const saved = normalizeWdttServerConfig(res.config);
		(inst as WdttServerInstance).config = saved;
		return saved;
	}
	const saved = normalizeFreeTurnServerConfig(
		await api.updateFreeTurnServerInstance(row.id, config as FreeTurnServerConfig),
	);
	(inst as FreeTurnServerInstance).config = saved;
	return saved;
}
