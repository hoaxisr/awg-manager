import { writable, derived } from 'svelte/store';
import { browser } from '$app/environment';

export type CenterType = 'error' | 'warning';

export interface CenterAction {
  label: string;
  href: string;
}

export interface CenterEntry {
  id: string;
  type: CenterType;
  message: string;
  action?: CenterAction;
  firstTs: number;
  lastTs: number;
  count: number;
  read: boolean;
}

export interface RecordInput {
  type: CenterType;
  message: string;
  action?: CenterAction;
  ts: number;
}

export type DayBucket = 'today' | 'yesterday' | 'earlier';

const STORAGE_KEY = 'awg-manager-notification-center';
const MAX_ENTRIES = 100;
const RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const COALESCE_WINDOW_MS = 5 * 60 * 1000;

function isCenterEntry(v: unknown): v is CenterEntry {
  if (!v || typeof v !== 'object') return false;
  const e = v as Record<string, unknown>;
  return (
    typeof e.id === 'string' &&
    (e.type === 'error' || e.type === 'warning') &&
    typeof e.message === 'string' &&
    typeof e.firstTs === 'number' &&
    typeof e.lastTs === 'number' &&
    typeof e.count === 'number' &&
    typeof e.read === 'boolean'
  );
}

function loadStored(): CenterEntry[] {
  if (!browser) return [];
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter(isCenterEntry) : [];
  } catch {
    return [];
  }
}

function writeStored(entries: CenterEntry[]): void {
  if (!browser) return;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
  } catch {
    /* ignore quota / private mode */
  }
}

/** Keep newest MAX_ENTRIES and drop anything older than RETENTION_MS relative to `now`. */
function prune(entries: CenterEntry[], now: number): CenterEntry[] {
  return entries
    .filter((e) => now - e.lastTs < RETENTION_MS)
    .sort((a, b) => b.lastTs - a.lastTs)
    .slice(0, MAX_ENTRIES);
}

/** Bucket a timestamp by calendar day relative to `now` (local time). */
export function dayBucket(ts: number, now: number): DayBucket {
  const n = new Date(now);
  const startOfToday = new Date(n.getFullYear(), n.getMonth(), n.getDate()).getTime();
  const startOfYesterday = startOfToday - 24 * 60 * 60 * 1000;
  if (ts >= startOfToday) return 'today';
  if (ts >= startOfYesterday) return 'yesterday';
  return 'earlier';
}

export function createNotificationCenterStore() {
  let counter = 0;
  const { subscribe, update, set } = writable<CenterEntry[]>(prune(loadStored(), Date.now()));

  function commit(entries: CenterEntry[]): CenterEntry[] {
    writeStored(entries);
    return entries;
  }

  function record(input: RecordInput) {
    update((entries) => {
      const idx = entries.findIndex(
        (e) =>
          e.type === input.type &&
          e.message === input.message &&
          input.ts - e.lastTs < COALESCE_WINDOW_MS,
      );
      let next: CenterEntry[];
      if (idx >= 0) {
        const existing = entries[idx];
        const merged: CenterEntry = {
          ...existing,
          lastTs: input.ts,
          count: existing.count + 1,
          read: false,
          action: input.action ?? existing.action,
        };
        next = [merged, ...entries.slice(0, idx), ...entries.slice(idx + 1)];
      } else {
        next = [
          {
            id: `nc-${++counter}`,
            type: input.type,
            message: input.message,
            action: input.action,
            firstTs: input.ts,
            lastTs: input.ts,
            count: 1,
            read: false,
          },
          ...entries,
        ];
      }
      return commit(prune(next, input.ts));
    });
  }

  function markRead(id: string) {
    update((entries) => commit(entries.map((e) => (e.id === id ? { ...e, read: true } : e))));
  }

  function markAllRead() {
    update((entries) => commit(entries.map((e) => (e.read ? e : { ...e, read: true }))));
  }

  function remove(id: string) {
    update((entries) => commit(entries.filter((e) => e.id !== id)));
  }

  function clearAll() {
    set(commit([]));
  }

  return { subscribe, record, markRead, markAllRead, remove, clearAll };
}

export const notificationCenter = createNotificationCenterStore();

export const unreadCount = derived(notificationCenter, ($n) =>
  $n.filter((e) => !e.read).length,
);
