/**
 * IPv6-записи списка доменов — то же правило, что internal/ipfamily на
 * бэкенде: сеть (2001:db8::/32) или голый адрес (2001:db8::1), включая
 * IPv4-mapped формы (::ffff:10.0.0.0/104). Теги и домены — никогда.
 */
export function isIPv6Entry(entry: string): boolean {
	const s = entry.trim();
	const slash = s.indexOf('/');
	const addr = slash === -1 ? s : s.slice(0, slash);
	if (slash !== -1) {
		const bits = s.slice(slash + 1);
		if (!/^(0|[1-9]\d{0,2})$/.test(bits) || Number(bits) > 128) return false;
	}
	if (!addr.includes(':')) return false;
	try {
		// Парсер URL проверяет IPv6-литерал целиком; geoip:RU и прочее с
		// двоеточием он отвергает.
		new URL(`http://[${addr}]/`);
		return true;
	} catch {
		return false;
	}
}

/**
 * Список со SkipIPv6, в котором все записи — IPv6: на роутер из него не
 * уходит ничего, хотя список включён. Только NDMS — HydraRoute флаг не читает.
 */
export function skipIPv6LeavesNothing(route: {
	skipIPv6?: boolean;
	backend?: string;
	domains?: string[];
	subnets?: string[];
}): boolean {
	if (!route.skipIPv6 || route.backend === 'hydraroute') return false;
	const entries = [...(route.domains ?? []), ...(route.subnets ?? [])];
	return entries.length > 0 && entries.every(isIPv6Entry);
}
