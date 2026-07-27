/**
 * Слияние WARN/ERROR-строк для карточки журнала «Обзора».
 *
 * Источников два и они разной природы: one-shot `GET /api/logs?level=warn`
 * при маунте (клиентские SSE-буферы при холодном заходе пусты) и живые
 * SSE-сторы, которые дополняют картину дальше. Строки дедуплицируются по
 * тому же составному ключу, что использует backend при схлопывании
 * повторов, и сортируются по времени последней активности.
 */
import type { LogEntry } from '$lib/types';

/** Сколько строк показывает карточка. */
export const WARN_LOG_LIMIT = 6;

/** Сколько строк тянуть из каждого bucket при маунте. */
export const WARN_LOG_FETCH_LIMIT = 20;

export function isWarnLevel(level: string): boolean {
	return level === 'warn' || level === 'error';
}

/**
 * Составной ключ записи — тот же, что использует backend при схлопывании
 * повторов (level/group/subgroup/action/target/message + timestamp). Служит
 * и ключом дедупа, и `each`-ключом в карточке: `timestamp + message` слабее
 * и даёт `each_key_duplicate` на записях, которые дедуп законно пропустил
 * (например, singbox-FATAL, зеркалированный в app-журнал с другим group).
 */
export function keyOf(e: LogEntry): string {
	return `${e.timestamp}|${e.level}|${e.group}|${e.subgroup}|${e.action}|${e.target}|${e.message}`;
}

/** Время последней активности записи: lastSeen у схлопнутых повторов. */
export function effectiveTime(e: LogEntry): number {
	return new Date(e.lastSeen ?? e.timestamp).getTime();
}

export function mergeWarnEntries(sources: LogEntry[][], limit = WARN_LOG_LIMIT): LogEntry[] {
	const byKey = new Map<string, LogEntry>();
	for (const source of sources) {
		for (const entry of source) {
			if (!isWarnLevel(entry.level)) continue;
			const key = keyOf(entry);
			const existing = byKey.get(key);
			// Одна и та же строка могла приехать и запросом, и по SSE —
			// оставляем ту, у которой свежее последняя активность.
			if (!existing || effectiveTime(entry) > effectiveTime(existing)) {
				byKey.set(key, entry);
			}
		}
	}
	return [...byKey.values()]
		.sort((a, b) => effectiveTime(b) - effectiveTime(a))
		.slice(0, limit);
}
