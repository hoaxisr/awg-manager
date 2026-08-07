// Pure edit-helpers for the QoS/DSCP class list (issue #371).
// Kept out of the Svelte component so normalization rules (dedupe dscp,
// cap 8, clamp 0–63, name ≤ 32) are unit-testable — see qosClasses.test.ts.

import type { SingboxQosClass } from '$lib/types';

export const QOS_MAX_CLASSES = 8;
export const QOS_DSCP_MIN = 0;
export const QOS_DSCP_MAX = 63;
export const QOS_NAME_MAX = 32;

/**
 * Preferred DSCP values for new classes, in pick order. Common presets:
 * 46 = EF (голос/игры), 32 = CS4 (видео), 8 = CS1 (фоновый трафик).
 */
export const QOS_DSCP_PRESETS: ReadonlyArray<{ value: number; label: string }> = [
  { value: 46, label: '46 (EF — голос/игры)' },
  { value: 32, label: '32 (CS4 — видео)' },
  { value: 8, label: '8 (CS1 — фоновая закачка)' },
];

/**
 * Классы DSCP неприменимы в fakeip-tun: netfilter-перехвата там нет.
 * Отсутствующий routingMode — легаси-payload, дефолт бэкенда = tproxy.
 *
 * Предикат ОДИН на два места: `QosSettingsCard` гейтит им рендер таблицы,
 * `EngineQosCard` — прайминг каталога outbound'ов. Разъехавшись, они дали бы
 * пустой дропдаун класса ровно там, ради чего прайминг и заводили: каждый
 * существующий класс выглядел бы как «outbound не найден».
 */
export function isQosLockedByMode(routingMode: string | null | undefined): boolean {
  return routingMode === 'fakeip-tun';
}

/** Clamp an arbitrary number to a valid integer DSCP mark (0–63). */
export function clampDscp(value: number): number {
  if (!Number.isFinite(value)) return QOS_DSCP_MIN;
  return Math.min(QOS_DSCP_MAX, Math.max(QOS_DSCP_MIN, Math.trunc(value)));
}

/**
 * Normalize a raw settings payload into a valid class list:
 * undefined/null (legacy/mock payloads without the field) → [];
 * dscp clamped to 0–63; name truncated to 32 chars; duplicate dscp values
 * deduped (first occurrence wins); list capped at 8 entries.
 */
export function normalizeQosClasses(
  list: SingboxQosClass[] | undefined | null,
): SingboxQosClass[] {
  if (!Array.isArray(list)) return [];
  const seen = new Set<number>();
  const out: SingboxQosClass[] = [];
  for (const raw of list) {
    if (!raw || typeof raw !== 'object') continue;
    const dscp = clampDscp(Number(raw.dscp));
    if (seen.has(dscp)) continue;
    seen.add(dscp);
    out.push({
      dscp,
      name: String(raw.name ?? '').slice(0, QOS_NAME_MAX),
      outbound: String(raw.outbound ?? ''),
      enabled: raw.enabled !== false,
    });
    if (out.length >= QOS_MAX_CLASSES) break;
  }
  return out;
}

/** First unused DSCP: presets (46, 32, 8) first, then lowest free 0–63. */
export function nextFreeDscp(list: SingboxQosClass[]): number | null {
  const used = new Set(list.map((c) => c.dscp));
  for (const p of QOS_DSCP_PRESETS) {
    if (!used.has(p.value)) return p.value;
  }
  for (let d = QOS_DSCP_MIN; d <= QOS_DSCP_MAX; d++) {
    if (!used.has(d)) return d;
  }
  return null;
}

/**
 * Append a new class with the first free DSCP. Returns null when the list
 * is at the 8-class cap (or, theoretically, when all 64 marks are taken).
 */
export function addQosClass(
  list: SingboxQosClass[],
  defaultOutbound: string,
): SingboxQosClass[] | null {
  if (list.length >= QOS_MAX_CLASSES) return null;
  const dscp = nextFreeDscp(list);
  if (dscp === null) return null;
  return [
    ...list,
    {
      dscp,
      name: `Класс ${list.length + 1}`,
      outbound: defaultOutbound,
      enabled: true,
    },
  ];
}

/** True when `dscp` is already used by a class other than list[index]. */
export function isDscpTaken(list: SingboxQosClass[], index: number, dscp: number): boolean {
  return list.some((c, i) => i !== index && c.dscp === dscp);
}

/** Immutable single-entry patch; dscp is clamped, name truncated. */
export function updateQosClass(
  list: SingboxQosClass[],
  index: number,
  patch: Partial<SingboxQosClass>,
): SingboxQosClass[] {
  return list.map((c, i) => {
    if (i !== index) return c;
    const next = { ...c, ...patch };
    next.dscp = clampDscp(next.dscp);
    next.name = next.name.slice(0, QOS_NAME_MAX);
    return next;
  });
}

export function removeQosClass(list: SingboxQosClass[], index: number): SingboxQosClass[] {
  return list.filter((_, i) => i !== index);
}

/** Synthetic entry appended when a stored outbound tag has no matching option. */
export interface StaleOutboundOption {
  value: string;
  label: string;
  disabled: true;
}

/**
 * Options for a row's outbound dropdown, handling a stored tag that is
 * absent from the current option list:
 * - tag present (or empty) → options as-is;
 * - options still cold-loading (empty stores) → single synthetic entry with
 *   the raw tag, no warning — «нет опций» ещё не значит «тег пропал»;
 * - tag missing among real options → append a disabled «(недоступен) tag»
 *   entry (so the trigger shows it instead of the placeholder) and flag
 *   `missing` for the row warning.
 */
export function resolveOutboundOptions<O extends { value: string }>(
  options: O[],
  tag: string,
): { options: Array<O | StaleOutboundOption>; missing: boolean } {
  if (!tag || options.some((o) => o.value === tag)) {
    return { options, missing: false };
  }
  if (options.length === 0) {
    return { options: [{ value: tag, label: tag, disabled: true }], missing: false };
  }
  return {
    options: [...options, { value: tag, label: `(недоступен) ${tag}`, disabled: true }],
    missing: true,
  };
}

/**
 * Serialized save queue for the QoS card: at most one save() in flight;
 * commits made meanwhile are coalesced — only the latest snapshot is sent
 * next (each snapshot is the full class list built on top of the previous
 * one, so nothing is lost). Prevents concurrent PUTs racing through
 * mergeAndSaveSettings with stale store state and out-of-order responses.
 *
 * `onDrained` fires once the queue empties (after success OR failure) —
 * the card uses it to drop its optimistic draft and resync to the store,
 * reverting rejected values. save() errors are swallowed here: the drawer's
 * applyPatch already surfaces them (toast).
 */
export function createSaveQueue<T>(
  save: (snapshot: T) => void | Promise<void>,
  onDrained: () => void,
): (snapshot: T) => void {
  let inFlight = false;
  let pending: { value: T } | null = null;

  async function pump(): Promise<void> {
    inFlight = true;
    try {
      while (pending) {
        const next = pending.value;
        pending = null;
        try {
          await save(next);
        } catch {
          // errors are surfaced by the caller's save() itself; keep draining
        }
      }
    } finally {
      inFlight = false;
      onDrained();
    }
  }

  return (snapshot: T) => {
    pending = { value: snapshot };
    if (!inFlight) void pump();
  };
}
