import { api } from '$lib/api/client';
import type { ProxyInstallStatus } from '$lib/api/proxyInstances';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

/**
 * Статус установки бинарей подсистем прокси — по одному store на подсистему.
 * Его читает карточка «Интеграции»: версия, наличие обновления и число
 * инстансов, по которому удаление бинарей заперто.
 *
 * Подписан на `proxyrt.instances`: создание и удаление инстанса меняет счётчик, а
 * без инвалидации кнопка «Удалить» осталась бы в прежнем состоянии до
 * перезагрузки страницы. Сами бинари меняются только нашими же действиями,
 * поэтому опрос редкий.
 */
export type ProxySubsystem = 'wdtt' | 'freeturn';

function storeFor(subsystem: ProxySubsystem): PollingStore<ProxyInstallStatus> {
	const store = createPollingStore<ProxyInstallStatus>(
		() => api.proxyInstallStatus(subsystem),
		{ staleTime: 60_000, pollInterval: 60_000 },
	);
	registerStore('proxyrt.instances', store);
	return store;
}

export const proxyInstallStatus: Record<ProxySubsystem, PollingStore<ProxyInstallStatus>> = {
	wdtt: storeFor('wdtt'),
	freeturn: storeFor('freeturn'),
};
