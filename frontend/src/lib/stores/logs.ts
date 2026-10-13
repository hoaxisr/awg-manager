import { writable } from 'svelte/store';
import type { LogEntry } from '$lib/types';
import type { LogEntryEvent } from '$lib/api/events';

export type LogBucket = 'app' | 'singbox' | 'mihomo';

const MAX_ENTRIES = 5000;
// Кап скана при upsert повтора — зеркало backend coalesceScanLimit:
// повторы почти всегда матчатся в свежих строках, полный проход по 5000
// строк на каждое SSE-событие — лишняя работа на UI-потоке.
const REPEAT_SCAN_LIMIT = 300;

function keyOf(e: LogEntry): string {
  return `${e.timestamp}|${e.level}|${e.group}|${e.subgroup}|${e.action}|${e.target}|${e.message}`;
}

export interface BufferStats {
  size: number;
  capacity: number;
  oldest?: string;
}

function createLogStore(bucket: LogBucket) {
  const { subscribe, update, set } = writable<LogEntry[]>([]);
  const logsEnabled = writable(true);
  const logsTotal = writable(0);
  const loaded = writable(false);
  const lastSeenTs = writable<number>(0);
  const stats = writable<BufferStats>({ size: 0, capacity: MAX_ENTRIES });

  return {
    bucket,
    subscribe,
    enabled: { subscribe: logsEnabled.subscribe },
    total: { subscribe: logsTotal.subscribe },
    loaded: { subscribe: loaded.subscribe },
    lastSeenTs: { subscribe: lastSeenTs.subscribe },
    stats: { subscribe: stats.subscribe },
    /** SSE-driven head append (newest at front). Повтор (repeats > 0)
     * обновляет существующую строку по составному ключу вместо новой. */
    append(entry: LogEntryEvent) {
      const logEntry: LogEntry = {
        ...entry,
        subgroup: entry.subgroup ?? '',
      };
      const ts = new Date(entry.lastSeen ?? entry.timestamp).getTime();
      lastSeenTs.update((cur) => (ts > cur ? ts : cur));
      if ((entry.repeats ?? 0) > 0) {
        const key = keyOf(logEntry);
        let found = false;
        update((entries) => {
          const limit = Math.min(entries.length, REPEAT_SCAN_LIMIT);
          for (let i = 0; i < limit; i++) {
            if (keyOf(entries[i]) !== key) continue;
            found = true;
            const next = entries.slice();
            next[i] = { ...next[i], repeats: entry.repeats, lastSeen: entry.lastSeen };
            return next;
          }
          return entries;
        });
        if (found) return;
        // Первое появление не попало в клиентский буфер (страница открыта
        // позже): вставляем строку по её timestamp (первое появление может
        // быть давним — в голове списка она ломала бы порядок), total не
        // трогаем — на сервере повтор не создал новой записи.
        update((entries) => {
          const ts2 = new Date(logEntry.timestamp).getTime();
          const pos = entries.findIndex((e) => new Date(e.timestamp).getTime() <= ts2);
          const at = pos === -1 ? entries.length : pos;
          const updated = [...entries.slice(0, at), logEntry, ...entries.slice(at)];
          if (updated.length > MAX_ENTRIES) updated.length = MAX_ENTRIES;
          return updated;
        });
        return;
      }
      update((entries) => {
        const updated = [logEntry, ...entries];
        if (updated.length > MAX_ENTRIES) updated.length = MAX_ENTRIES;
        return updated;
      });
      logsTotal.update((n) => n + 1);
    },
    /** Catch-up merge after SSE reconnect — keeps entries newest-first, dedups by composite key.
     * Дубликат с бОльшим repeats освежает счётчик существующей строки. */
    appendMany(arr: LogEntry[]) {
      update((entries) => {
        const byKey = new Map(entries.map((e, i) => [keyOf(e), i]));
        const newOnes: LogEntry[] = [];
        let refreshed: LogEntry[] | null = null;
        for (const e of arr) {
          const idx = byKey.get(keyOf(e));
          if (idx === undefined) {
            newOnes.push(e);
          } else if ((e.repeats ?? 0) > ((refreshed ?? entries)[idx].repeats ?? 0)) {
            refreshed = refreshed ?? entries.slice();
            refreshed[idx] = { ...refreshed[idx], repeats: e.repeats, lastSeen: e.lastSeen };
          }
        }
        if (refreshed) entries = refreshed;
        if (newOnes.length === 0) return entries;
        const newestTs = newOnes.reduce((m, e) => Math.max(m, new Date(e.timestamp).getTime()), 0);
        if (newestTs > 0) lastSeenTs.update((cur) => (newestTs > cur ? newestTs : cur));
        const merged = [...newOnes, ...entries].sort(
          (a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
        );
        if (merged.length > MAX_ENTRIES) merged.length = MAX_ENTRIES;
        return merged;
      });
    },
    /** Pagination tail append — concatenates older entries below the newest, dedups. */
    appendPage(arr: LogEntry[]) {
      update((entries) => {
        const seen = new Set(entries.map(keyOf));
        const newOnes: LogEntry[] = [];
        for (const e of arr) {
          if (!seen.has(keyOf(e))) newOnes.push(e);
        }
        if (newOnes.length === 0) return entries;
        return [...entries, ...newOnes];
      });
    },
    clear() {
      set([]);
      logsTotal.set(0);
      lastSeenTs.set(0);
      stats.set({ size: 0, capacity: MAX_ENTRIES });
    },
    setEntries(entries: LogEntry[]) {
      set(entries);
      const newest = entries.reduce((m, e) => Math.max(m, new Date(e.timestamp).getTime()), 0);
      lastSeenTs.set(newest);
    },
    setTotal(n: number) {
      logsTotal.set(n);
    },
    setEnabled(v: boolean) {
      logsEnabled.set(v);
    },
    setLoaded(v: boolean) {
      loaded.set(v);
    },
    setStats(s: BufferStats) {
      stats.set(s);
    },
  };
}

export type LogStore = ReturnType<typeof createLogStore>;

export const appLogEntries = createLogStore('app');
export const singboxLogEntries = createLogStore('singbox');
export const mihomoLogEntries = createLogStore('mihomo');

export function logStoreFor(bucket: LogBucket): LogStore {
  if (bucket === 'singbox') return singboxLogEntries;
  if (bucket === 'mihomo') return mihomoLogEntries;
  return appLogEntries;
}

/** Backwards-compat alias for callers that haven't migrated yet. */
export const logEntries = appLogEntries;
