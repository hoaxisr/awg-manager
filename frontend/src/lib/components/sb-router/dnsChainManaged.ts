import type { SingboxRouterDNSRule } from '$lib/types';

/**
 * Префикс тегов managed-правил DNS-пресета — зеркало backend-конвенции
 * (router.dnsChainTagPrefix). Wire-поля «это правило пресета» нет: признак —
 * префикс evaluate-тега либо ссылки match_response.
 */
const DNS_CHAIN_TAG_PREFIX = 'awgm-dns-';

/**
 * Правило принадлежит оверлею DNS-пресета: бэкенд его перезаписывает на каждой
 * записи конфига и отклоняет ручные edit/delete/move (DNS_RULE_MANAGED), поэтому
 * UI прячет у таких строк действия.
 */
export function isManagedDnsChainRule(r: SingboxRouterDNSRule): boolean {
	if (r.tag?.startsWith(DNS_CHAIN_TAG_PREFIX)) return true;
	return typeof r.match_response === 'string' && r.match_response.startsWith(DNS_CHAIN_TAG_PREFIX);
}
