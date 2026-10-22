import { api } from '$lib/api/client';
import type { ZapretStatus } from '$lib/types';
import { createPollingStore, type PollingStore } from './polling';
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
