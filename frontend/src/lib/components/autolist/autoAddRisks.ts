import { m } from '$lib/i18n';

/**
 * Полный список рисков автодобавления (BACKLOG §3, шаг 4): один источник для
 * wizard и обычных настроек, чтобы два редактора не расходились. Функция, а
 * не константа: тексты должны следовать за локалью интерфейса.
 */
export function autolistRiskItems(): string[] {
	return [
		m.autolist_risk_vpn_ip(),
		m.autolist_risk_auto_connections(),
		m.autolist_risk_wrong_resources(),
		m.autolist_risk_registered_zone(),
		m.autolist_risk_udp_whole_ip(),
		m.autolist_risk_traffic(),
	];
}
