// Сохранение и откат параметров клиента «Выхода».
//
// Снимок конфига берётся через JSON: конфиг инстанса и так JSON-тело запроса,
// а модуль остаётся обычным .ts — рун (`$state.snapshot`) здесь не нужно.

import { api } from '$lib/api/client';
import { peersEqual } from '$lib/utils/wdttPeer';
import type {
	FreeTurnClientInstance,
	FreeTurnConfig,
	WdttClientInstance,
	WdttConfig,
} from '$lib/types';
import type { ProxyInstanceRow } from './rows';

export interface ExitSaveResult {
	/** Адрес сервера сменился — работающий клиент останавливается (W-27). */
	peerChanged: boolean;
	deletedTunnels?: string[];
	tunnelErrors?: string[];
}

export type ExitInstance = WdttClientInstance | FreeTurnClientInstance;

function clone<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}

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
 * W-22: ответ бэкенда ложится в рабочий конфиг, только если пользователь не
 * правил поля во время запроса — иначе его правки были бы затёрты.
 */
export async function saveExitInstance(
	row: ProxyInstanceRow,
	inst: ExitInstance,
	savedPeer: string,
): Promise<ExitSaveResult> {
	if (row.protocol === 'wdtt') {
		const wdtt = inst as WdttClientInstance;
		const sent = clone(wdtt.config);
		const res = await api.updateWdttClientInstance(row.id, sent);
		if (JSON.stringify(wdtt.config) === JSON.stringify(sent)) wdtt.config = res.config;
		return {
			peerChanged: !peersEqual(savedPeer, res.config.peer),
			deletedTunnels: res.deletedTunnels,
			tunnelErrors: res.tunnelErrors,
		};
	}
	const ft = inst as FreeTurnClientInstance;
	const sent = clone(ft.config);
	const cfg = await api.updateFreeTurnClientInstance(row.id, sent);
	if (JSON.stringify(ft.config) === JSON.stringify(sent)) ft.config = cfg;
	return { peerChanged: !peersEqual(savedPeer, cfg.peer) };
}

/** EX-24 «Отменить» — возврат к последнему загруженному конфигу. */
export function revertExitInstance(inst?: ExitInstance, saved?: ExitInstance): void {
	if (!inst || !saved) return;
	(inst as WdttClientInstance).config = clone(saved.config) as WdttClientInstance['config'];
}
