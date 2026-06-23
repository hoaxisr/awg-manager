import { describe, it, expect } from 'vitest';
import { deriveFakeIPEngineState, type FakeIPEngineState } from './engineState';

describe('deriveFakeIPEngineState', () => {
	// Full input matrix: routingMode (tproxy | fakeip-tun | undefined)
	// × running (true | false) × clashReachable (true | false).
	// routingMode !== 'fakeip-tun' short-circuits to 'not-fakeip' regardless
	// of running/clash, so those rows all expect 'not-fakeip'.
	const cases: Array<{
		routingMode: 'tproxy' | 'fakeip-tun' | undefined;
		enabled: boolean;
		running: boolean;
		clashReachable: boolean;
		expected: FakeIPEngineState;
	}> = [
		// routingMode: tproxy → always not-fakeip (enabled irrelevant)
		{ routingMode: 'tproxy', enabled: true, running: false, clashReachable: false, expected: 'not-fakeip' },
		{ routingMode: 'tproxy', enabled: true, running: true, clashReachable: true, expected: 'not-fakeip' },
		// routingMode: undefined (legacy → treated as tproxy) → always not-fakeip
		{ routingMode: undefined, enabled: true, running: true, clashReachable: true, expected: 'not-fakeip' },
		// routingMode: fakeip-tun but DISABLED → not-fakeip (empty state), regardless of running/clash
		{ routingMode: 'fakeip-tun', enabled: false, running: false, clashReachable: false, expected: 'not-fakeip' },
		{ routingMode: 'fakeip-tun', enabled: false, running: true, clashReachable: true, expected: 'not-fakeip' },
		// routingMode: fakeip-tun, enabled, not running → stopped (clash irrelevant)
		{ routingMode: 'fakeip-tun', enabled: true, running: false, clashReachable: false, expected: 'stopped' },
		{ routingMode: 'fakeip-tun', enabled: true, running: false, clashReachable: true, expected: 'stopped' },
		// routingMode: fakeip-tun, enabled, running, clash unreachable → clash-down
		{ routingMode: 'fakeip-tun', enabled: true, running: true, clashReachable: false, expected: 'clash-down' },
		// routingMode: fakeip-tun, enabled, running, clash reachable → live
		{ routingMode: 'fakeip-tun', enabled: true, running: true, clashReachable: true, expected: 'live' },
	];

	for (const c of cases) {
		it(`routingMode=${String(c.routingMode)} enabled=${c.enabled} running=${c.running} clashReachable=${c.clashReachable} → ${c.expected}`, () => {
			expect(
				deriveFakeIPEngineState({
					routingMode: c.routingMode,
					enabled: c.enabled,
					running: c.running,
					clashReachable: c.clashReachable,
				}),
			).toBe(c.expected);
		});
	}
});
