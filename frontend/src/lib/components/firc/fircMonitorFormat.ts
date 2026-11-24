import { m, formatLocale, type UiText } from '$lib/i18n';
import type { FircDomainCheckResult } from '$lib/types/firc';

// Форматтеры монитора и журнала FIRC: общие для панели и её дочерних
// компонентов.

export function formatBytes(bytes: number): string {
	const units = [m.firc_monitor_unit_b(), m.firc_monitor_unit_kb(), m.firc_monitor_unit_mb(), m.firc_monitor_unit_gb()];
	if (!bytes || bytes <= 0) return `0 ${units[0]}`;
	let val = bytes;
	let idx = 0;
	while (val >= 1024 && idx < units.length - 1) {
		val /= 1024;
		idx++;
	}
	return `${val >= 10 || idx === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[idx]}`;
}

export function formatTime(ts?: number): string {
	if (!ts) return '—';
	const ms = ts > 1e12 ? ts : ts * 1000;
	return new Date(ms).toLocaleTimeString(formatLocale(), { hour12: false });
}

export function formatOutboundLabel(id?: string, label?: string): string {
	if (label && id && label !== id) return `${label} (${id})`;
	return label || id || m.firc_monitor_outbound_unknown();
}

/** Результат проверки домена: ошибка — текст бэкенда или отложенное сообщение i18n. */
export type FircDomainCheckView = Omit<FircDomainCheckResult, 'error'> & { error?: UiText };
