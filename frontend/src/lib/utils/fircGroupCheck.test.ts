import { describe, expect, it } from 'vitest';
import type { FircGroup, FircProxyGroup } from '$lib/types/firc';
import {
	buildGroupCheckFromProxyGroup,
	carryCheckExtras,
	clampPgIntervalSec,
	switchGroupCheckMode,
	withPgIntervalSec,
} from './fircGroupCheck';

function group(
	id: string,
	iface: string | string[],
	check?: FircGroup['check'],
	proxyGroupId?: string,
): FircGroup {
	return {
		id,
		name: id,
		interface: iface,
		enable: true,
		devices: { allow: [], deny: [] },
		rules: [],
		...(proxyGroupId ? { proxyGroupId } : {}),
		...(check ? { check } : {}),
	};
}

describe('clampPgIntervalSec', () => {
	it('treats empty, zero and negative input as inherit', () => {
		expect(clampPgIntervalSec('')).toBeUndefined();
		expect(clampPgIntervalSec('  ')).toBeUndefined();
		expect(clampPgIntervalSec('0')).toBeUndefined();
		expect(clampPgIntervalSec('-4')).toBeUndefined();
		expect(clampPgIntervalSec('abc')).toBeUndefined();
	});

	it('clamps to the UI bounds and truncates', () => {
		expect(clampPgIntervalSec('3')).toBe(5);
		expect(clampPgIntervalSec('45.9')).toBe(45);
		expect(clampPgIntervalSec(99999)).toBe(3600);
	});
});

describe('carryCheckExtras', () => {
	it('keeps the interval across a mode switch but drops the mode-specific timeout', () => {
		const keendns = { mode: 'keendns' as const, timeoutMs: 5000, intervalSec: 90 };
		expect(carryCheckExtras(keendns, 'url')).toEqual({ intervalSec: 90 });
		expect(switchGroupCheckMode(keendns, 'url')).toEqual({ mode: 'url', url: '', intervalSec: 90 });
	});

	it('keeps timeout and thresholds while the mode stays', () => {
		const url = { mode: 'url' as const, url: '', intervalSec: 30, timeoutMs: 2500, failThreshold: 3, recoverThreshold: 4 };
		expect(carryCheckExtras(url, 'url')).toEqual({
			intervalSec: 30,
			timeoutMs: 2500,
			failThreshold: 3,
			recoverThreshold: 4,
		});
	});

	it('carries nothing into none and does not re-clamp a stored low interval', () => {
		expect(carryCheckExtras({ mode: 'url', intervalSec: 30 }, 'none')).toEqual({});
		expect(carryCheckExtras({ mode: 'url', intervalSec: 2 }, 'url')).toEqual({ intervalSec: 2 });
	});
});

describe('withPgIntervalSec', () => {
	it('sets, clamps and clears the interval without touching other fields', () => {
		const base = { mode: 'url' as const, url: 'http://x/204', timeoutMs: 2000 };
		expect(withPgIntervalSec(base, '120')).toEqual({ ...base, intervalSec: 120 });
		expect(withPgIntervalSec({ ...base, intervalSec: 120 }, '')).toEqual(base);
		expect(withPgIntervalSec(undefined, '1')).toEqual({ mode: 'url', url: '', intervalSec: 5 });
	});
});

describe('buildGroupCheckFromProxyGroup', () => {
	it('defaults the keendns timeout and keeps the interval', () => {
		const pg: FircProxyGroup = { id: 'p', name: 'p', interfaces: ['a', 'b'], check: { mode: 'keendns', intervalSec: 60 } };
		expect(buildGroupCheckFromProxyGroup(pg)).toEqual({ mode: 'keendns', url: '', intervalSec: 60, timeoutMs: 5000 });
	});

	it('is none for a single interface and undefined without a check', () => {
		expect(buildGroupCheckFromProxyGroup({ id: 'p', name: 'p', interfaces: ['a'] })).toEqual({ mode: 'none', url: '' });
		expect(buildGroupCheckFromProxyGroup({ id: 'p', name: 'p', interfaces: ['a', 'b'] })).toBeUndefined();
	});
});
