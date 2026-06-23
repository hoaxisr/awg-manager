import { describe, it, expect } from 'vitest';
import { deriveSteps, stepDefsFor, type UIStepState } from './switchSteps';
import type { SingboxRouterTransitionStep } from '$lib/types';

type S = SingboxRouterTransitionStep['status'];
const ev = (
	step: SingboxRouterTransitionStep['step'],
	status: S,
): SingboxRouterTransitionStep => ({ step, status });

const states = (
	from: 'off' | 'tproxy' | 'fakeip-tun',
	to: 'off' | 'tproxy' | 'fakeip-tun',
	received: SingboxRouterTransitionStep[],
	opts?: { failed?: boolean },
): UIStepState[] => deriveSteps(from, to, received, opts).map((s) => s.state);

describe('stepDefsFor', () => {
	it('returns the enable list for →fakeip-tun', () => {
		const defs = stepDefsFor('tproxy', 'fakeip-tun');
		expect(defs.length).toBe(6);
		expect(defs[0].title).toMatch(/TPROXY-перехват/);
		expect(defs.at(-1)?.title).toMatch(/готовности/);
	});

	it('fakeip-tun → tproxy renders tproxy bring-up (not fakeip teardown copy)', () => {
		const defs = stepDefsFor('fakeip-tun', 'tproxy');
		expect(defs.length).toBe(4);
		expect(defs[0].title).toMatch(/предыдущий режим/);
	});
});

describe('stepDefsFor — mode-specific copy', () => {
	it('off→tproxy uses tproxy vocabulary, not fakeip', () => {
		const joined = stepDefsFor('off', 'tproxy').map((d) => `${d.title} ${d.detail ?? ''}`).join(' ');
		expect(joined).toContain('TPROXY');
		expect(joined).not.toContain('OpkgTun');
		expect(joined).not.toContain('fakeip');
	});
	it('tproxy→off teardown is tproxy-correct, not fakeip', () => {
		const joined = stepDefsFor('tproxy', 'off').map((d) => `${d.title} ${d.detail ?? ''}`).join(' ');
		expect(joined).toContain('TPROXY');
		expect(joined).not.toContain('OpkgTun');
		expect(joined).not.toContain('fakeip');
	});
	it('fakeip-tun directions keep existing rich copy', () => {
		expect(stepDefsFor('off', 'fakeip-tun').length).toBe(6);
		expect(stepDefsFor('fakeip-tun', 'off').length).toBe(4);
	});
});

describe('deriveSteps — enable direction', () => {
	it('all pending before any event', () => {
		expect(states('tproxy', 'fakeip-tun', [])).toEqual([
			'pending',
			'pending',
			'pending',
			'pending',
			'pending',
			'pending',
		]);
	});

	it('teardown current → first row current, rest pending', () => {
		expect(states('tproxy', 'fakeip-tun', [ev('teardown', 'current')])).toEqual(
			['current', 'pending', 'pending', 'pending', 'pending', 'pending'],
		);
	});

	it('teardown done → first row done; provision rows pending', () => {
		expect(states('tproxy', 'fakeip-tun', [ev('teardown', 'done')])).toEqual([
			'done',
			'pending',
			'pending',
			'pending',
			'pending',
			'pending',
		]);
	});

	it('provision done marks all three provision rows done (later implies earlier)', () => {
		// teardown never re-emitted as done, but provision done supersedes it.
		expect(states('tproxy', 'fakeip-tun', [ev('provision', 'done')])).toEqual([
			'done',
			'done',
			'done',
			'done',
			'pending',
			'pending',
		]);
	});

	it('readiness current with provision done → restart row current', () => {
		const got = states('tproxy', 'fakeip-tun', [
			ev('provision', 'done'),
			ev('readiness', 'current'),
		]);
		expect(got).toEqual([
			'done',
			'done',
			'done',
			'done',
			'current',
			'pending',
		]);
	});

	it('ready done → every row done (success)', () => {
		expect(states('tproxy', 'fakeip-tun', [ev('ready', 'done')])).toEqual([
			'done',
			'done',
			'done',
			'done',
			'done',
			'done',
		]);
	});
});

describe('deriveSteps — failure', () => {
	it('explicit milestone error marks that row error', () => {
		const got = states('tproxy', 'fakeip-tun', [
			ev('teardown', 'done'),
			ev('provision', 'error'),
		]);
		expect(got[0]).toBe('done');
		expect(got[1]).toBe('error');
		expect(got.slice(2)).toEqual(['pending', 'pending', 'pending', 'pending']);
	});

	it('failed transition with no per-step error marks first incomplete row error', () => {
		const got = states(
			'tproxy',
			'fakeip-tun',
			[ev('teardown', 'done'), ev('provision', 'current')],
			{ failed: true },
		);
		// teardown done; the first not-done row (a provision row) carries the error.
		expect(got[0]).toBe('done');
		expect(got[1]).toBe('error');
		expect(got.slice(2)).toEqual(['pending', 'pending', 'pending', 'pending']);
	});
});

describe('deriveSteps — disable direction', () => {
	it('teardown done → first disable row done', () => {
		const got = states('fakeip-tun', 'tproxy', [ev('teardown', 'done')]);
		expect(got).toEqual(['done', 'pending', 'pending', 'pending']);
	});

	it('ready done → all disable rows done', () => {
		const got = states('fakeip-tun', 'off', [ev('ready', 'done')]);
		expect(got).toEqual(['done', 'done', 'done', 'done']);
	});
});
