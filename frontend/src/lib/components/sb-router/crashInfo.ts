/**
 * Pure helpers для блока «падения движка» в StatusDrawer (#456):
 * форматирование времени окончания паузы авто-перезапуска. Чистые функции —
 * тестируются vitest'ом без DOM.
 */

import { formatLocale } from '$lib/i18n';

/**
 * Время (часы и минуты, локальное) из RFC3339-строки restartSuppressedUntil
 * в формате из настройки formatLocale().
 * null для пустой/битой даты — вызывающий скрывает блок подавления.
 */
export function formatSuppressedUntil(iso: string | null | undefined): string | null {
	if (!iso) return null;
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return null;
	return d.toLocaleTimeString(formatLocale(), { hour: '2-digit', minute: '2-digit' });
}
