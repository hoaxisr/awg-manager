import { formatLocale } from '$lib/i18n';

export function formatAutolistTime(iso?: string): string {
	if (!iso) return '—';
	try {
		return new Date(iso).toLocaleString(formatLocale());
	} catch {
		return iso;
	}
}
