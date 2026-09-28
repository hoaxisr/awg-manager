import type { RoutingTunnel } from '$lib/types';

/**
 * Туннель каталога, в который смотрит interface-цель HR Neo. В domain.conf
 * лежит имя ядра, а у системных интерфейсов `iface` каталога — NDMS-id,
 * поэтому они узнаются по `tunnelId`: бэкенд читает их цель обратно как
 * `system:<id>` (F498). id/name с именем ядра не сравниваются — совпадение
 * было бы случайным.
 */
export function hrTargetTunnel(
	tunnels: RoutingTunnel[],
	target: string,
	tunnelId?: string,
): RoutingTunnel | undefined {
	return tunnels.find((tn) => (!!tunnelId && tn.id === tunnelId) || (!!tn.iface && tn.iface === target));
}
