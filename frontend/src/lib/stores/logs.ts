import { writable } from 'svelte/store';
import type { LogEntry } from '$lib/types';
import type { LogEntryEvent } from '$lib/api/events';

const MAX_ENTRIES = 500;

function keyOf(e: LogEntry): string {
  return `${e.timestamp}|${e.target}|${e.message}`;
}

function createLogStore() {
  const { subscribe, update, set } = writable<LogEntry[]>([]);
  const logsEnabled = writable(true);
  const logsTotal = writable(0);
  const loaded = writable(false);
  const lastSeenTs = writable<number>(0);

  return {
    subscribe,
    enabled: { subscribe: logsEnabled.subscribe },
    total: { subscribe: logsTotal.subscribe },
    loaded: { subscribe: loaded.subscribe },
    lastSeenTs: { subscribe: lastSeenTs.subscribe },
    append(entry: LogEntryEvent) {
      const logEntry: LogEntry = {
        ...entry,
        subgroup: entry.subgroup ?? '',
      };
      const ts = new Date(entry.timestamp).getTime();
      lastSeenTs.update((cur) => (ts > cur ? ts : cur));
      update((entries) => {
        const updated = [logEntry, ...entries];
        if (updated.length > MAX_ENTRIES) updated.length = MAX_ENTRIES;
        return updated;
      });
    },
    appendMany(arr: LogEntry[]) {
      // Merge from catch-up REST fetch with dedup
      update((entries) => {
        const seen = new Set(entries.map(keyOf));
        const newOnes: LogEntry[] = [];
        for (const e of arr) {
          if (!seen.has(keyOf(e))) newOnes.push(e);
        }
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
    clear() {
      set([]);
      logsTotal.set(0);
      lastSeenTs.set(0);
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
  };
}

export const logEntries = createLogStore();
