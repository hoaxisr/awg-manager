import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { fakeipTransition } from './fakeipTransition';
import type { SingboxRouterTransitionData } from '$lib/types';

function ev(
	partial: Partial<SingboxRouterTransitionData> & {
		step: SingboxRouterTransitionData['step'];
	},
): SingboxRouterTransitionData {
	return {
		transitionId: 't1',
		from: 'tproxy',
		to: 'fakeip-tun',
		...partial,
	};
}

describe('fakeipTransition reducer', () => {
	beforeEach(() => fakeipTransition.reset());

	it('starts fresh when transitionId differs (resets steps)', () => {
		fakeipTransition.applyTransition(
			ev({ transitionId: 'old', step: { step: 'start', status: 'current' } }),
		);
		fakeipTransition.applyTransition(
			ev({ transitionId: 'new', step: { step: 'provision', status: 'current' } }),
		);
		const s = get(fakeipTransition)!;
		expect(s.transitionId).toBe('new');
		expect(s.steps).toHaveLength(1);
		expect(s.steps[0].step).toBe('provision');
	});

	it('updates a same-name step current→done in place (no duplicate)', () => {
		fakeipTransition.applyTransition(
			ev({ step: { step: 'provision', status: 'current' } }),
		);
		fakeipTransition.applyTransition(
			ev({ step: { step: 'provision', status: 'done', message: 'ok' } }),
		);
		const s = get(fakeipTransition)!;
		expect(s.steps).toHaveLength(1);
		expect(s.steps[0].status).toBe('done');
		expect(s.steps[0].message).toBe('ok');
	});

	it('appends distinct steps in arrival order', () => {
		fakeipTransition.applyTransition(ev({ step: { step: 'start', status: 'done' } }));
		fakeipTransition.applyTransition(
			ev({ step: { step: 'teardown', status: 'done' } }),
		);
		fakeipTransition.applyTransition(
			ev({ step: { step: 'provision', status: 'current' } }),
		);
		const s = get(fakeipTransition)!;
		expect(s.steps.map((x) => x.step)).toEqual(['start', 'teardown', 'provision']);
	});

	it('propagates error + the step error status', () => {
		fakeipTransition.applyTransition(
			ev({ step: { step: 'provision', status: 'current' } }),
		);
		fakeipTransition.applyTransition(
			ev({
				step: { step: 'provision', status: 'error', message: 'boom' },
				error: 'provision failed',
			}),
		);
		const s = get(fakeipTransition)!;
		expect(s.error).toBe('provision failed');
		const failed = s.steps.find((x) => x.step === 'provision')!;
		expect(failed.status).toBe('error');
		expect(failed.message).toBe('boom');
	});

	it('propagates done + finalState from the terminal event', () => {
		fakeipTransition.applyTransition(ev({ step: { step: 'ready', status: 'current' } }));
		fakeipTransition.applyTransition(
			ev({
				step: { step: 'ready', status: 'done' },
				done: true,
				finalState: 'fakeip-tun',
			}),
		);
		const s = get(fakeipTransition)!;
		expect(s.done).toBe(true);
		expect(s.finalState).toBe('fakeip-tun');
	});

	it('begin() pre-seeds an empty transition replaced by the first real event', () => {
		fakeipTransition.begin('tproxy', 'fakeip-tun');
		let s = get(fakeipTransition)!;
		expect(s.transitionId).toBe('');
		expect(s.steps).toHaveLength(0);
		fakeipTransition.applyTransition(
			ev({ transitionId: 'real', step: { step: 'start', status: 'current' } }),
		);
		s = get(fakeipTransition)!;
		expect(s.transitionId).toBe('real');
		expect(s.steps).toHaveLength(1);
	});

	it('fail() marks a terminal error with from as final state', () => {
		fakeipTransition.begin('tproxy', 'fakeip-tun');
		fakeipTransition.fail('network down');
		const s = get(fakeipTransition)!;
		expect(s.done).toBe(true);
		expect(s.error).toBe('network down');
		expect(s.finalState).toBe('tproxy');
		expect(s.steps.some((x) => x.step === 'error' && x.status === 'error')).toBe(true);
	});
});
