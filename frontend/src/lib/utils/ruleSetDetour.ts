import type { SingboxRouterRuleSet } from '$lib/types';

/**
 * Outbound, через который скачивается remote rule-set ('' = default
 * HTTP-клиент). Читает новую форму `http_client.detour` (sing-box ≥1.14)
 * и legacy `download_detour` (старые ответы/фикстуры). Строковая форма
 * `http_client` — именованный клиент из top-level `http_clients`, это не
 * ссылка на outbound, для UI считается «автоматически».
 */
export function ruleSetDownloadDetour(
	rs: Pick<SingboxRouterRuleSet, 'http_client' | 'download_detour'>,
): string {
	if (typeof rs.http_client === 'string') return '';
	return rs.http_client?.detour ?? rs.download_detour ?? '';
}
