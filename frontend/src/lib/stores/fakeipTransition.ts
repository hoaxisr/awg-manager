// Ephemeral store for the FakeIP mode-switch live progress screen (1E.6).
//
// Consumes the `singbox-router:transition` SSE stream (wired in +layout.svelte)
// and reduces the sequence of milestone events into an ordered, deduped step
// list the SwitchProgress modal renders DATA-DRIVEN. The backend emits a COARSE
// set of milestones (start/teardown/provision/readiness/ready, plus rollback +
// error on failure) — the same `step.step` may arrive first as `current` then
// later as `done`, so we upsert by step name and update in place.
//
// Mirrors the singboxRouter store's `apply*` idiom: a plain writable plus a
// small set of exposed methods. Lifecycle is owned by the page: `begin()` opens
// the modal optimistically, `applyTransition()` folds in SSE events, `reset()`
// tears it down on close.

import { writable } from 'svelte/store';
import type {
	SingboxRouterTransitionData,
	SingboxRouterTransitionStep,
} from '$lib/types';

export type FakeIPMode = 'off' | 'tproxy' | 'fakeip-tun';

export interface FakeIPTransitionState {
	transitionId: string;
	from: FakeIPMode;
	to: FakeIPMode;
	/** Ordered, accumulated/deduped by step name (current→done updates in place). */
	steps: SingboxRouterTransitionStep[];
	done: boolean;
	finalState?: FakeIPMode;
	error?: string;
}

function upsertStep(
	steps: SingboxRouterTransitionStep[],
	next: SingboxRouterTransitionStep,
): SingboxRouterTransitionStep[] {
	const idx = steps.findIndex((s) => s.step === next.step);
	if (idx === -1) {
		// New milestone — append, preserving arrival order.
		return [...steps, next];
	}
	// Same milestone seen again (e.g. current→done) — replace in place so the
	// list position is stable and only the status/message update.
	const out = steps.slice();
	out[idx] = next;
	return out;
}

function createFakeipTransitionStore() {
	const store = writable<FakeIPTransitionState | null>(null);

	/**
	 * Reducer over one transition's SSE events. A `data.transitionId` that
	 * differs from the current state's id (or a null state) starts a fresh
	 * transition: this is also how `begin()`'s placeholder id ('') is replaced
	 * by the first real event. Otherwise the event's step is upserted and the
	 * terminal fields (done/finalState/error) propagate.
	 */
	function applyTransition(data: SingboxRouterTransitionData): void {
		store.update((prev) => {
			const fresh = prev === null || prev.transitionId !== data.transitionId;
			const base: FakeIPTransitionState = fresh
				? {
						transitionId: data.transitionId,
						from: data.from,
						to: data.to,
						steps: [],
						done: false,
					}
				: prev;

			return {
				...base,
				transitionId: data.transitionId,
				from: data.from,
				to: data.to,
				steps: upsertStep(base.steps, data.step),
				done: data.done ?? base.done,
				finalState: data.finalState ?? base.finalState,
				error: data.error ?? base.error,
			};
		});
	}

	/**
	 * Pre-seed an empty transition so the modal can open immediately, before the
	 * first SSE event arrives. The placeholder id ('') guarantees the first real
	 * event triggers the fresh-start path in `applyTransition`.
	 */
	function begin(from: FakeIPMode, to: FakeIPMode): void {
		store.set({ transitionId: '', from, to, steps: [], done: false });
	}

	/**
	 * Force a terminal error onto the current transition without an SSE event —
	 * used when the POST itself throws (network) so the modal never spins forever.
	 * Appends a synthetic `error` step and marks the transition done with the
	 * `from` mode as the effective final state (no switch happened).
	 */
	function fail(message: string): void {
		store.update((prev) => {
			if (prev === null) return prev;
			return {
				...prev,
				steps: upsertStep(prev.steps, {
					step: 'error',
					status: 'error',
					message,
				}),
				done: true,
				finalState: prev.finalState ?? prev.from,
				error: prev.error ?? message,
			};
		});
	}

	function reset(): void {
		store.set(null);
	}

	return { subscribe: store.subscribe, applyTransition, begin, fail, reset };
}

export const fakeipTransition = createFakeipTransitionStore();
