/** Аптайм процесса для шапки карточки и stat-плитки: «14 мин» / «2 ч 14 мин». */
export function formatUptime(startedAt?: string): string {
	if (!startedAt) return '';
	const ms = Date.now() - new Date(startedAt).getTime();
	const mins = Math.floor(ms / 60000);
	if (mins < 60) return `${mins} мин`;
	const hrs = Math.floor(mins / 60);
	return `${hrs} ч ${mins % 60} мин`;
}
