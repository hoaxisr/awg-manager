import { formatLocale } from '$lib/i18n';
import { m } from '$lib/i18n';

/** Время проверки/наблюдения в локали интерфейса; пусто — прочерк. */
export function formatAutolistTime(iso?: string): string {
	if (!iso) return '—';
	try {
		const date = new Date(iso);
		if (Number.isNaN(date.getTime())) return iso;
		return date.toLocaleString(formatLocale());
	} catch {
		return iso;
	}
}

/** Человекочитаемая длительность проверки: мс или секунды с десятыми. */
export function formatAutolistDuration(ms?: number): string {
	if (ms === undefined || ms === null) return '—';
	if (ms < 1000) return m.autolist_duration_ms({ value: ms });
	return m.autolist_duration_s({ value: (ms / 1000).toFixed(1) });
}

/** Байты ответа проверки: КиБ с одной десятичной. */
export function formatAutolistBytes(bytes?: number): string {
	if (bytes === undefined || bytes === null) return '—';
	if (bytes < 1024) return m.autolist_bytes_b({ value: bytes });
	return m.autolist_bytes_kib({ value: (bytes / 1024).toFixed(1) });
}
