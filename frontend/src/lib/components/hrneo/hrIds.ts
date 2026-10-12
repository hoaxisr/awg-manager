import type { DnsRoute } from '$lib/types';
import type { TargetEntry } from './HrNeoTargetSidebar.svelte';

/** id HR-группы: `hr:<ревизия>:<цель>:<n>` (см. hydraroute.GroupRef). */
export interface HrRef {
	revision: string;
	target: string;
	index: number;
}

const PREFIX = 'hr:';

export function parseHrId(id: string): HrRef | null {
	if (!id.startsWith(PREFIX)) return null;
	const s = id.slice(PREFIX.length);
	const first = s.indexOf(':');
	const last = s.lastIndexOf(':');
	if (first <= 0 || last <= first + 1) return null;
	const tail = s.slice(last + 1);
	if (!/^\d+$/.test(tail)) return null;
	const index = Number(tail);
	return { revision: s.slice(0, first), target: s.slice(first + 1, last), index };
}

/** Ключ позиции без ревизии: переживает нашу же запись файлов. */
export function hrPosKey(id: string): string {
	const ref = parseHrId(id);
	return ref ? `${ref.target}:${ref.index}` : id;
}

export function withRevision(id: string, revision: string): string {
	const ref = parseHrId(id);
	return ref ? `${PREFIX}${revision}:${ref.target}:${ref.index}` : id;
}

export function hrRevisionOf(routes: DnsRoute[]): string | null {
	for (const r of routes) {
		const ref = parseHrId(r.id);
		if (ref) return ref.revision;
	}
	return null;
}

export function isHrRulesChanged(e: unknown): boolean {
	const err = e as { status?: number; body?: { code?: string } } | null;
	return err?.status === 409 && err.body?.code === 'HR_RULES_CHANGED';
}

/**
 * Та же группа после чужой правки файлов: та же позиция и то же имя и
 * содержимое. Иначе на этой позиции уже другая группа — править её нельзя.
 */
export function findSameHrGroup(old: DnsRoute, fresh: DnsRoute[]): DnsRoute | null {
	const key = hrPosKey(old.id);
	const same = (a: string[] | undefined, b: string[] | undefined) =>
		JSON.stringify(a ?? []) === JSON.stringify(b ?? []);
	return (
		fresh.find(
			(r) => hrPosKey(r.id) === key && r.name === old.name && same(r.manualDomains, old.manualDomains),
		) ?? null
	);
}

/** Цели групп плюс пустые цели из PolicyOrder; порядок — по PolicyOrder. */
export function buildHrTargets(groupTargets: TargetEntry[], policyOrder: string[]): TargetEntry[] {
	const entries = [...groupTargets];
	for (const name of policyOrder) {
		if (!entries.some((t) => t.name === name)) {
			entries.push({ name, kind: 'policy', ruleCount: 0, empty: true });
		}
	}
	if (policyOrder.length === 0) return entries;
	const orderIdx = new Map(policyOrder.map((n, i) => [n, i]));
	const UNKNOWN = Number.MAX_SAFE_INTEGER;
	return entries
		.map((entry, origIdx) => ({ entry, idx: orderIdx.get(entry.name) ?? UNKNOWN, origIdx }))
		.sort((a, b) => a.idx - b.idx || a.origIdx - b.origIdx)
		.map((x) => x.entry);
}
