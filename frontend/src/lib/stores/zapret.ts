import { derived, type Readable } from 'svelte/store';
import { api } from '$lib/api/client';
import type { ZapretStatus } from '$lib/types';
import { isRoutingSubTabVisible } from '$lib/types/usageLevel';
import { createPollingStore, type PollingState, type PollingStore } from './polling';
import { usageLevel } from './settings';
import { registerStore } from './storeRegistry';

async function fetchStatus(): Promise<ZapretStatus> {
	return api.getZapretStatus();
}

/**
 * Статус Zapret обновляется при подписке и по SSE-инвалидациям.
 * Периодический опрос не нужен: управляющие операции и записи
 * пользовательских файлов публикуют routing.zapretStatus.
 */
export const zapretStatus: PollingStore<ZapretStatus> = createPollingStore<ZapretStatus>(
	fetchStatus,
	{ staleTime: 30_000, pollInterval: 0 },
);

registerStore('routing.zapretStatus', zapretStatus);

/**
 * Статус для страниц, общих для всех уровней (главная, настройки,
 * маршрутизация). Zapret виден только expert, поэтому остальные статус не
 * запрашивают вовсе: подписка на zapretStatus открывается лишь у expert.
 * null — уровень ниже expert (или настройки ещё не пришли).
 */
export const zapretStatusIfExpert: Readable<PollingState<ZapretStatus> | null> = derived<
	typeof usageLevel,
	PollingState<ZapretStatus> | null
>(
	usageLevel,
	($level, set) => {
		if (!isRoutingSubTabVisible($level, 'zapret')) {
			set(null);
			return;
		}
		return zapretStatus.subscribe(set);
	},
	null,
);
