// Повтор classifyLine + InputValidator.isValidToken из HRweb (JS вшит в
// /opt/bin/hrweb, сверено с 1.68.0). HRweb при сохранении проверяет
// этим ВСЕ токены domain.conf и ip.list: один отвергнутый токен в любой группе
// запрещает сохранить в HRweb любое правило (#1045). Бэкенд держит то же
// правило в hrwebTokenError (internal/hydraroute/rules.go).
const BAD_CHAR = /[^A-Za-z0-9.\-:/]/;
const EDGE = /^[.\-:/]|[.\-:/]$/;
const ADJACENT = /[.:/]{2}|-\.|\.-|-\/|\/-|:-|-:/;
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;

function valid(t: string, cidr: boolean): boolean {
	return !(
		BAD_CHAR.test(t) ||
		EDGE.test(t) ||
		ADJACENT.test(cidr ? t.replace(/::/g, 'x') : t) ||
		(cidr && !/^geo(ip|site):/.test(t) && t.includes('-'))
	);
}

/** 'chars' — HRweb отвергнет символы; 'mask' — IP без маски; null — токен годен. */
export function hrTokenError(t: string): 'chars' | 'mask' | null {
	const geosite = t.startsWith('geosite:');
	const cidr = t.startsWith('geoip:') || (!geosite && t.includes('/'));
	if (!cidr && !geosite && (IPV4.test(t) || t.includes(':'))) {
		return valid(t, true) ? 'mask' : 'chars';
	}
	return valid(t, cidr) ? null : 'chars';
}
