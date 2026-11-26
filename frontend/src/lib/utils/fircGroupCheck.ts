import type { FircGroup, FircGroupCheck, FircProxyGroup } from '$lib/types/firc';
import { normalizeFircIfaceList } from './fircFallback';

/** Bounds of the per-proxy-group probe interval offered in the UI (fircd accepts 1..3600). */
export const FIRC_PG_INTERVAL_MIN_SEC = 5;
export const FIRC_PG_INTERVAL_MAX_SEC = 3600;

type CheckMode = FircGroupCheck['mode'];

/** A user-entered interval: empty, zero or negative means "inherit the AWGM Route setting" (undefined). */
export function clampPgIntervalSec(raw: unknown): number | undefined {
	const n =
		typeof raw === 'number'
			? raw
			: typeof raw === 'string' && raw.trim() !== ''
				? Number(raw.trim())
				: Number.NaN;
	if (!Number.isFinite(n)) return undefined;
	const t = Math.trunc(n);
	if (t <= 0) return undefined;
	return Math.min(FIRC_PG_INTERVAL_MAX_SEC, Math.max(FIRC_PG_INTERVAL_MIN_SEC, t));
}

/** A stored interval: kept within fircd bounds when positive, so values set elsewhere are not rewritten. */
export function storedIntervalSec(raw: unknown): number | undefined {
	if (typeof raw !== 'number' || !Number.isFinite(raw)) return undefined;
	const t = Math.trunc(raw);
	if (t <= 0) return undefined;
	return Math.min(FIRC_PG_INTERVAL_MAX_SEC, t);
}

/**
 * Fields of `src` that survive a rebuild of the check in `mode`.
 * The interval always survives; timeout and thresholds are tuned per probe kind,
 * so they survive only while the mode stays the same.
 */
export function carryCheckExtras(
	src: FircGroupCheck | null | undefined,
	mode: CheckMode,
): Partial<FircGroupCheck> {
	if (!src || mode === 'none') return {};
	const extras: Partial<FircGroupCheck> = {};
	const intervalSec = storedIntervalSec(src.intervalSec);
	if (intervalSec !== undefined) extras.intervalSec = intervalSec;
	if ((src.mode || 'none') !== mode) return extras;
	if (typeof src.timeoutMs === 'number' && src.timeoutMs > 0) extras.timeoutMs = src.timeoutMs;
	if (typeof src.failThreshold === 'number' && src.failThreshold > 0) {
		extras.failThreshold = src.failThreshold;
	}
	if (typeof src.recoverThreshold === 'number' && src.recoverThreshold > 0) {
		extras.recoverThreshold = src.recoverThreshold;
	}
	return extras;
}

/** Rebuilds a group check after a mode switch in the proxy-group modal, keeping thresholds/interval and dropping keendns's timeout when leaving keendns. */
export function switchGroupCheckMode(
	prev: FircGroupCheck | null | undefined,
	mode: CheckMode,
): FircGroupCheck {
	const { url: prevUrl, timeoutMs: prevTimeout, ...rest } = prev ?? ({} as FircGroupCheck);
	const keepTimeout = prev?.mode !== 'keendns' && typeof prevTimeout === 'number' && prevTimeout > 0;
	const intervalSec = storedIntervalSec(prev?.intervalSec);
	const next: FircGroupCheck = {
		...rest,
		mode,
		url: mode === 'url' ? (prevUrl ?? '').trim() : '',
		...(keepTimeout ? { timeoutMs: prevTimeout } : {}),
		...(mode === 'keendns' ? { timeoutMs: 5000 } : {}),
	};
	if (intervalSec !== undefined) {
		next.intervalSec = intervalSec;
	} else {
		delete next.intervalSec;
	}
	return next;
}

/** The check with its interval set from user input, or cleared to inherit the AWGM Route setting. */
export function withPgIntervalSec(check: FircGroupCheck | null | undefined, raw: string): FircGroupCheck {
	const next: FircGroupCheck = { ...(check ?? { mode: 'url', url: '' }) };
	const intervalSec = clampPgIntervalSec(raw);
	if (intervalSec !== undefined) {
		next.intervalSec = intervalSec;
	} else {
		delete next.intervalSec;
	}
	return next;
}

/** The routing-group check a proxy group implies; undefined when the proxy group has no check. */
export function buildGroupCheckFromProxyGroup(pg: FircProxyGroup): FircGroupCheck | undefined {
	if ((pg.interfaces?.length ?? 0) < 2) {
		return { mode: 'none', url: '' };
	}
	if (!pg.check) return undefined;
	const mode = pg.check.mode || 'none';
	return {
		mode,
		url: mode === 'url' ? (pg.check.url ?? '').trim() : '',
		...carryCheckExtras(pg.check, mode),
		...(mode === 'keendns' ? { timeoutMs: pg.check.timeoutMs || 5000 } : {}),
	};
}

function checkKey(c: FircGroupCheck | undefined): string {
	if (!c) return '';
	return JSON.stringify([
		c.mode || 'none',
		c.mode === 'url' ? (c.url ?? '').trim() : '',
		c.intervalSec || 0,
		c.timeoutMs || 0,
		c.failThreshold || 0,
		c.recoverThreshold || 0,
	]);
}

/**
 * Rewrites the check of every routing group linked to a proxy group (matching proxyGroupId and chain),
 * as the backend does on save; used to keep a locally restored draft in step with saved proxy groups.
 */
export function syncGroupChecksWithProxyGroups(
	groups: FircGroup[],
	proxyGroups: FircProxyGroup[],
): { groups: FircGroup[]; changed: boolean } {
	let changed = false;
	const next = groups.map((g) => {
		if (!g.proxyGroupId) return g;
		const list = normalizeFircIfaceList(g.interface);
		if (list.length < 2) return g;
		const pg = proxyGroups.find(
			(p) =>
				p.id === g.proxyGroupId &&
				p.interfaces.length === list.length &&
				p.interfaces.every((v, i) => v === list[i]),
		);
		const desired = pg ? buildGroupCheckFromProxyGroup(pg) : undefined;
		if (!desired || checkKey(desired) === checkKey(g.check)) return g;
		changed = true;
		return { ...g, check: desired };
	});
	return { groups: changed ? next : groups, changed };
}
