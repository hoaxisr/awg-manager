// Конфиг клиента «Выхода»: приведение ответа бэкенда к плотному виду и
// сохранение.
//
// Снимок конфига берётся через JSON: конфиг инстанса и так JSON-тело запроса,
// а модуль остаётся обычным .ts — рун (`$state.snapshot`) здесь не нужно.

import { api } from '$lib/api/client';
import { peersEqual } from '$lib/utils/wdttPeer';
import type {
	FreeTurnClientConfig,
	FreeTurnClientInstance,
	FreeTurnConfig,
	WdttClientConfig,
	WdttClientInstance,
	WdttConfig,
} from '$lib/types';
import type { ProxyInstanceRow } from './rows';

export type ExitConfig = WdttClientConfig | FreeTurnClientConfig;
export type ExitInstance = WdttClientInstance | FreeTurnClientInstance;

export interface ExitSaveResult {
	/** Конфиг, лежащий на сервере после сохранения. */
	config: ExitConfig;
	/** Адрес сервера сменился — работающий клиент останавливается (W-27). */
	peerChanged: boolean;
	deletedTunnels?: string[];
	tunnelErrors?: string[];
}

export function cloneConfig<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}

// ─── Плотный конфиг.
//
// Поля с `omitempty` (`internal/wdtt/types.go`, `internal/freeturn/types.go`)
// бэкенд не сериализует, и пустая строка приезжает как отсутствующий ключ.
// `bind:value={cfg.sub}` на `undefined` бросает `props_invalid_value`: Input и
// Dropdown объявляют значение как `$bindable('')`, а Svelte 5 запрещает
// биндить undefined к пропу с fallback'ом. Поэтому optional-строки
// заполняются один раз — сразу после ответа бэкенда, а не `?? ''` у каждого
// поля формы.
//
// `connMode` в списки не входит намеренно: это не строка, а режим ('wg'|'raw'),
// и '' для него — не «пусто», а поломка семантики.

const WDTT_OPTIONAL_STRINGS: readonly (keyof WdttClientConfig)[] = [
	'deviceId',
	'vkAuthMode',
	'sub',
	'peerWg',
	'peerRaw',
	'ndmsIface',
	'rawIface',
	'rawClientIp',
];

const FT_OPTIONAL_STRINGS: readonly (keyof FreeTurnClientConfig)[] = [
	'links',
	'obfKey',
	'dnsServers',
	'clientId',
	'sub',
];

function fillStrings<T extends object>(cfg: T, keys: readonly (keyof T)[]): T {
	for (const key of keys) {
		if (cfg[key] === undefined) (cfg as Record<string, unknown>)[key as string] = '';
	}
	return cfg;
}

export function normalizeWdttClientConfig(cfg: WdttClientConfig): WdttClientConfig {
	return fillStrings(cfg, WDTT_OPTIONAL_STRINGS);
}

export function normalizeFreeTurnClientConfig(cfg: FreeTurnClientConfig): FreeTurnClientConfig {
	return fillStrings(cfg, FT_OPTIONAL_STRINGS);
}

/** Конфиги страницы — на месте, сразу после загрузки. */
export function normalizeExitConfigs(wdtt: WdttConfig, ft: FreeTurnConfig): void {
	for (const inst of wdtt.clients) normalizeWdttClientConfig(inst.config);
	for (const inst of ft.clients) normalizeFreeTurnClientConfig(inst.config);
}

// ─── Сохранение.

/** Инстанс выбранной строки в конфиге своего протокола. */
export function exitInstance(
	row: ProxyInstanceRow | null,
	wdtt: WdttConfig | null,
	ft: FreeTurnConfig | null,
): ExitInstance | undefined {
	if (!row) return undefined;
	return row.protocol === 'wdtt'
		? wdtt?.clients.find((c) => c.id === row.id)
		: ft?.clients.find((c) => c.id === row.id);
}

/**
 * Сохранение конфига инстанса. Конфиг страницы всегда равен состоянию сервера:
 * в него ложится ответ бэкенда, а правки пользователя живут в редактируемой
 * копии детали (W-22). Отсюда же честный W-27: `peerChanged` сравнивает адрес
 * до и после записи, а не протухший снимок.
 */
export async function saveExitInstance(
	row: ProxyInstanceRow,
	inst: ExitInstance,
	config: ExitConfig,
): Promise<ExitSaveResult> {
	const before = inst.config.peer;
	if (row.protocol === 'wdtt') {
		const res = await api.updateWdttClientInstance(row.id, config as WdttClientConfig);
		const saved = normalizeWdttClientConfig(res.config);
		(inst as WdttClientInstance).config = saved;
		return {
			config: saved,
			peerChanged: !peersEqual(before, saved.peer),
			deletedTunnels: res.deletedTunnels,
			tunnelErrors: res.tunnelErrors,
		};
	}
	const saved = normalizeFreeTurnClientConfig(
		await api.updateFreeTurnClientInstance(row.id, config as FreeTurnClientConfig),
	);
	(inst as FreeTurnClientInstance).config = saved;
	return { config: saved, peerChanged: !peersEqual(before, saved.peer) };
}
