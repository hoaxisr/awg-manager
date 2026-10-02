import { m } from '$lib/i18n';

/** Аптайм процесса для шапки карточки и stat-плитки: «14 мин» / «2 ч 14 мин». */
export function formatUptime(startedAt?: string): string {
	if (!startedAt) return '';
	const ms = Date.now() - new Date(startedAt).getTime();
	const mins = Math.floor(ms / 60000);
	if (mins < 60) return m.proxy_uptime_minutes({ minutes: mins });
	const hrs = Math.floor(mins / 60);
	return m.proxy_uptime_hours_minutes({ hours: hrs, minutes: mins % 60 });
}
