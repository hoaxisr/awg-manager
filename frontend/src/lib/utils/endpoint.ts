import { isIPv4 } from '$lib/utils/cidr';

/** Whether a server connect host is empty (WAN fallback) or a valid IPv4/domain. */
export function isValidEndpointHost(val: string): boolean {
	const trimmed = val.trim();
	if (!trimmed) return true;
	if (isIPv4(trimmed)) return true;
	if (/^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/.test(trimmed)) return true;
	return false;
}

/** Mirrors backend resolveWireguardClientEndpointHost + WAN fallback for UI preview. */
export function resolveClientEndpointHost(
	storedEndpoint: string,
	keenDnsDomain: string,
	wanIP: string,
): string {
	const trimmed = storedEndpoint.trim();
	if (trimmed) return trimmed;
	const keen = keenDnsDomain.trim();
	if (keen) return keen;
	return wanIP.trim();
}

export function emptyEndpointDescription(keenDnsDomain: string): string {
	const base = 'Хост для подключения в .conf (без порта). Пустое поле —';
	if (keenDnsDomain.trim()) {
		return `${base} KeenDNS.`;
	}
	return `${base} внешний IP роутера.`;
}

export function emptyEndpointPlaceholder(
	keenDnsDomain: string,
	wanIP: string,
	loadingWanIP = false,
): string {
	if (loadingWanIP) return 'Определение WAN IP...';
	const keen = keenDnsDomain.trim();
	if (keen) return keen;
	return wanIP.trim() || 'WAN IP';
}
