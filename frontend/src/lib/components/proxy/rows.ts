// Модель строки списка/детали страницы «Прокси» и её сборка из статуса и
// конфига. Список слева един для обоих протоколов: протокол и режим — признаки
// строки, а не уровень навигации (ia.md §1).

import type {
	FreeTurnConfig,
	FreeTurnProcessStatus,
	FreeTurnStatus,
	WdttConfig,
	WdttProcessStatus,
	WdttStatus,
} from '$lib/types';

export type ProxyProtocol = 'wdtt' | 'freeturn';

export type ProxyRole = 'client' | 'server';

export type ProxyRunState = 'running' | 'error' | 'stopped';

export interface ProxyInstanceRow {
	/** Ключ строки: id инстанса уникален только внутри протокола. */
	key: string;
	/** id инстанса для ручек API. */
	id: string;
	protocol: ProxyProtocol;
	role: ProxyRole;
	name: string;
	state: ProxyRunState;
	/** UI-имя флага Enabled бэкенда (оговорка LS-08). */
	autostart: boolean;
	pid?: number;
	startedAt?: string;
	/**
	 * Наш живой процесс с унаследованным pid-файлом (LS-09). Готовое поле
	 * бэкенда — на фронте НЕ вычисляется (спека §2.1).
	 */
	orphanedPid: boolean;
	binaryPresent: boolean;
	/** Режим WDTT: connMode клиента / relayMode сервера. У FreeTurn режима нет. */
	mode?: 'wg' | 'raw';
}

type ProcessStatus = WdttProcessStatus | FreeTurnProcessStatus;
type InstanceStatus = { id: string; name: string; status: ProcessStatus };

/** «Не запускается» (LS-06, RB-02) — остановлен с ошибкой последнего запуска. */
function runState(s: ProcessStatus): ProxyRunState {
	if (s.running) return 'running';
	return s.lastError ? 'error' : 'stopped';
}

function toRow(
	protocol: ProxyProtocol,
	role: ProxyRole,
	inst: InstanceStatus,
	autostart: boolean,
	mode?: 'wg' | 'raw',
): ProxyInstanceRow {
	const s = inst.status;
	return {
		key: `${protocol}:${role}:${inst.id}`,
		id: inst.id,
		protocol,
		role,
		name: inst.name,
		state: runState(s),
		autostart,
		pid: s.pid,
		startedAt: s.startedAt,
		orphanedPid: s.orphanedPid === true,
		binaryPresent: s.binaryPresent,
		mode,
	};
}

export interface ProxySources {
	wdttStatus: WdttStatus | null;
	wdttConfig: WdttConfig | null;
	ftStatus: FreeTurnStatus | null;
	ftConfig: FreeTurnConfig | null;
}

/** Вкладка «Выход»: клиенты обоих протоколов. */
export function exitRows(src: ProxySources): ProxyInstanceRow[] {
	return [
		...(src.wdttStatus?.clients ?? []).map((i) => {
			const c = src.wdttConfig?.clients.find((x) => x.id === i.id)?.config;
			return toRow('wdtt', 'client', i, c?.enabled === true, c?.connMode === 'raw' ? 'raw' : 'wg');
		}),
		...(src.ftStatus?.clients ?? []).map((i) =>
			toRow(
				'freeturn',
				'client',
				i,
				src.ftConfig?.clients.find((x) => x.id === i.id)?.config.enabled === true,
			),
		),
	];
}

/** Вкладка «Раздача»: серверы обоих протоколов. */
export function shareRows(src: ProxySources): ProxyInstanceRow[] {
	return [
		...(src.wdttStatus?.servers ?? []).map((i) => {
			const c = src.wdttConfig?.servers.find((x) => x.id === i.id)?.config;
			return toRow('wdtt', 'server', i, c?.enabled === true, c?.relayMode === 'raw' ? 'raw' : 'wg');
		}),
		...(src.ftStatus?.servers ?? []).map((i) =>
			toRow(
				'freeturn',
				'server',
				i,
				src.ftConfig?.servers.find((x) => x.id === i.id)?.config.enabled === true,
			),
		),
	];
}
