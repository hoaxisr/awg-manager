import type { PolicyDevice } from '$lib/types';

/**
 * Имя клиента по его IP или MAC. Кандидат автолиста знает оба адреса, а
 * панель знает имена устройств из hotspot роутера. MAC надёжнее: IP клиента
 * меняется при переподключении, а имя в hotspot привязано к MAC.
 */
export function clientName(devices: PolicyDevice[], ip?: string, mac?: string): string {
	if (!ip && !mac) return '';
	const wanted = (mac ?? '').trim().toLowerCase();
	const byMac = wanted ? devices.find((d) => d.mac?.trim().toLowerCase() === wanted) : undefined;
	if (byMac) return byMac.name || byMac.hostname || byMac.ip;
	const byIp = ip ? devices.find((d) => d.ip === ip) : undefined;
	return byIp?.name || byIp?.hostname || byIp?.ip || '';
}
