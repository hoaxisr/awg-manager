// Pure mapping from the backend's COARSE transition milestones to the richer,
// PREDEFINED step list the SwitchProgress modal renders (FE-spec §7.3). The
// backend emits only start/teardown/provision/readiness/ready (+ rollback/error
// on failure), but the approved mockup (page-transition.html) shows a vertical
// list with static titles and sub-details. We keep a fixed ordered UI model and
// DERIVE each row's state (done/current/pending/error) from the milestones that
// actually arrived — no fabricated progress.
//
// Side-effect-free so the mapping is unit-tested without mounting Svelte.

import type { SingboxRouterTransitionStep } from '$lib/types';
import type { FakeIPMode } from '$lib/stores/fakeipTransition';

type Milestone = SingboxRouterTransitionStep['step'];
export type UIStepState = 'done' | 'current' | 'pending' | 'error';

/** One predefined row: static copy + the backend milestone it's driven by. */
export interface UIStepDef {
	/** The coarse milestone whose arrival drives this row. */
	milestone: Milestone;
	title: string;
	detail?: string;
}

export interface UIStep extends UIStepDef {
	state: UIStepState;
}

// Ordered milestone progression for the lifecycle. Used to decide whether a
// row's milestone has been superseded by a later `done` (which implies the
// earlier ones completed too — the backend doesn't re-emit them as done).
const MILESTONE_ORDER: Milestone[] = [
	'start',
	'teardown',
	'provision',
	'readiness',
	'ready',
];

// Enable direction (→ fakeip+tun): the full bring-up list from the mockup.
const ENABLE_STEPS: UIStepDef[] = [
	{
		milestone: 'teardown',
		title: 'Снят TPROXY-перехват',
		detail: 'удалены jumps + AWGM-цепочки',
	},
	{
		milestone: 'provision',
		title: 'Интерфейс OpkgTun создан',
		detail: 'gvisor · non-global · private · MTU 1500',
	},
	{
		milestone: 'provision',
		title: 'NDMS auto-маршруты применены',
		detail: 'маршруты на пул fakeip',
	},
	{
		milestone: 'provision',
		title: 'config.json записан',
		detail: 'tun inbound · fakeip DNS · правила',
	},
	{
		milestone: 'readiness',
		title: 'Перезапуск sing-box',
		detail: 'ожидаем Clash API',
	},
	{
		milestone: 'ready',
		title: 'Проверка готовности',
		detail: 'tun up · fakeip отвечает · маршруты · доставка',
	},
];

// Disable / switch-out (fakeip+tun → tproxy|off): a simpler tear-down list.
const DISABLE_STEPS: UIStepDef[] = [
	{
		milestone: 'teardown',
		title: 'Снят fakeip-режим',
		detail: 'reject-маршрут · дренаж соединений',
	},
	{
		milestone: 'provision',
		title: 'NDMS-маршруты сняты, интерфейс удалён',
		detail: 'OpkgTun убран · sing-box перестроен',
	},
	{
		milestone: 'readiness',
		title: 'Перезапуск sing-box',
		detail: 'ожидаем Clash API',
	},
	{
		milestone: 'ready',
		title: 'Проверка готовности',
		detail: 'предыдущий режим восстановлен',
	},
];

// TProxy bring-up (off|fakeip-tun → tproxy).
const TPROXY_ENABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят предыдущий режим', detail: 'прежние маршруты/перехват убраны (если были)' },
	{ milestone: 'provision', title: 'iptables TPROXY установлен', detail: 'jumps + AWGM-цепочки' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем Clash API' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'TPROXY-перехват активен' },
];

// TProxy switch-out (tproxy → off).
const TPROXY_DISABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят TPROXY-перехват', detail: 'iptables jumps + цепочки убраны' },
	{ milestone: 'provision', title: 'sing-box перестроен', detail: 'без перехвата' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем Clash API' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'маршрутизация выключена' },
];

/** The predefined definitions for a transition direction (no derived state). */
export function stepDefsFor(from: FakeIPMode, to: FakeIPMode): UIStepDef[] {
	if (to === 'fakeip-tun') return ENABLE_STEPS;       // rich fakeip bring-up (unchanged)
	if (to === 'tproxy') return TPROXY_ENABLE_STEPS;    // tproxy bring-up
	// to === 'off': teardown of the source mode.
	return from === 'tproxy' ? TPROXY_DISABLE_STEPS : DISABLE_STEPS;
}

function rank(m: Milestone): number {
	const i = MILESTONE_ORDER.indexOf(m);
	return i === -1 ? -1 : i;
}

/**
 * Derive each predefined row's state from the milestones actually received.
 *
 *  - error:   the row's milestone arrived with status `error`, OR the whole
 *             transition errored/rolled back and this row never completed (so
 *             the user sees which step the failure landed on).
 *  - done:    the row's milestone arrived `done`, OR a strictly later milestone
 *             arrived `done` (later success implies earlier steps finished — the
 *             backend doesn't re-emit the earlier ones).
 *  - current: the row's milestone is the highest-ranked one currently `current`
 *             and nothing later is done yet.
 *  - pending: otherwise.
 */
export function deriveSteps(
	from: FakeIPMode,
	to: FakeIPMode,
	received: SingboxRouterTransitionStep[],
	opts: { failed?: boolean } = {},
): UIStep[] {
	const defs = stepDefsFor(from, to);

	// Latest status per milestone (events may repeat current→done in place; the
	// store already upserts, but be defensive and take the last occurrence).
	const status = new Map<Milestone, SingboxRouterTransitionStep['status']>();
	for (const s of received) status.set(s.step, s.status);

	// Highest milestone rank that has reached `done`.
	let maxDoneRank = -1;
	for (const [m, st] of status) {
		if (st === 'done') maxDoneRank = Math.max(maxDoneRank, rank(m));
	}

	// The single `current` milestone to emphasise: the highest-ranked one whose
	// status is `current` and which isn't already superseded by a later `done`.
	let currentRank = -1;
	for (const [m, st] of status) {
		if (st === 'current' && rank(m) > maxDoneRank) {
			currentRank = Math.max(currentRank, rank(m));
		}
	}

	// A failure is either an explicit per-milestone `error` event or a terminal
	// transition that didn't reach `to` (opts.failed). Because several UI rows can
	// share one milestone, we pin the error to the SINGLE first not-yet-done row
	// (the rest stay pending — the backend stopped there and rolled back) rather
	// than reddening every row of the failing milestone.
	const hasErrorEvent = [...status.values()].some((st) => st === 'error');
	const failed = opts.failed === true || hasErrorEvent;
	let errorAssigned = false;

	return defs.map((def): UIStep => {
		const r = rank(def.milestone);

		const isDone = r <= maxDoneRank;
		if (isDone) {
			return { ...def, state: 'done' };
		}

		if (failed) {
			if (!errorAssigned) {
				errorAssigned = true;
				return { ...def, state: 'error' };
			}
			return { ...def, state: 'pending' };
		}

		if (r === currentRank) {
			return { ...def, state: 'current' };
		}

		return { ...def, state: 'pending' };
	});
}
