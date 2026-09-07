import { api } from '$lib/api/client';
import type { ProxyInstallStatus, ProxySubsystem } from '$lib/api/proxyInstances';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

export type { ProxySubsystem } from '$lib/api/proxyInstances';

/**
 * Статус установки бинарей подсистем прокси — по одному store на подсистему.
 * Его читает карточка «Интеграции»: версия, наличие обновления и число
 * инстансов, по которому удаление бинарей заперто.
 *
 * `wdtt`/`freeturn` подписаны на `proxyrt.instances`, `obf-phobos`/`obf-clusterm` —
 * на `tunnels`: создание и удаление инстанса/туннеля меняет счётчик, а без
 * инвалидации кнопка «Удалить» осталась бы в прежнем состоянии до перезагрузки
 * страницы. Сами бинари меняются только нашими же действиями, поэтому опрос
 * редкий.
 */
function storeFor(
	subsystem: ProxySubsystem,
	resource: 'proxyrt.instances' | 'tunnels',
): PollingStore<ProxyInstallStatus> {
	const store = createPollingStore<ProxyInstallStatus>(
		() => api.proxyInstallStatus(subsystem),
		{ staleTime: 60_000, pollInterval: 60_000 },
	);
	registerStore(resource, store);
	return store;
}

export const proxyInstallStatus: Record<ProxySubsystem, PollingStore<ProxyInstallStatus>> = {
	wdtt: storeFor('wdtt', 'proxyrt.instances'),
	freeturn: storeFor('freeturn', 'proxyrt.instances'),
	'obf-phobos': storeFor('obf-phobos', 'tunnels'),
	'obf-clusterm': storeFor('obf-clusterm', 'tunnels'),
};
