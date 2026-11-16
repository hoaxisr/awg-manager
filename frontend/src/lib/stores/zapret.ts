import { derived, writable, type Readable } from 'svelte/store';
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

const TUNNELS_CARD_HIDDEN_KEY = 'awg.zapret.tunnels-card-hidden';

function readTunnelsCardHidden(): boolean {
	try {
		return localStorage.getItem(TUNNELS_CARD_HIDDEN_KEY) === '1';
	} catch {
		return false;
	}
}

/**
 * Скрыта ли карточка Zapret на странице туннелей. Настройка браузера
 * (localStorage), на роутер не уходит: на другом устройстве карточка видна.
 */
export const zapretTunnelsCardHidden = writable<boolean>(readTunnelsCardHidden());

export function setZapretTunnelsCardHidden(hidden: boolean): void {
	try {
		if (hidden) localStorage.setItem(TUNNELS_CARD_HIDDEN_KEY, '1');
		else localStorage.removeItem(TUNNELS_CARD_HIDDEN_KEY);
	} catch {
		// Хранилище недоступно (приватный режим): настройка живёт до перезагрузки.
	}
	zapretTunnelsCardHidden.set(hidden);
}
