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
	/** Live detail from the latest SSE event for this milestone (if any). */
	liveMessage?: string;
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
		detail: 'ожидаем inbounds (TPROXY/REDIRECT)',
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
		detail: 'ожидаем inbounds (TPROXY/REDIRECT)',
	},
	{
		milestone: 'ready',
		title: 'Проверка готовности',
		detail: 'предыдущий режим восстановлен',
	},
];

// TProxy bring-up (off|fakeip-tun → tproxy). Order matches Enable: sing-box
// readiness first, then iptables install.
const TPROXY_ENABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят предыдущий режим', detail: 'прежние маршруты/перехват убраны (если были)' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем inbounds (TPROXY/REDIRECT)' },
	{ milestone: 'provision', title: 'iptables TPROXY установлен', detail: 'jumps + AWGM-цепочки' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'TPROXY-перехват активен' },
];
const TPROXY_DISABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят TPROXY-перехват', detail: 'iptables jumps + цепочки убраны' },
	{ milestone: 'provision', title: 'sing-box перестроен', detail: 'без перехвата' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем inbounds (TPROXY/REDIRECT)' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'маршрутизация выключена' },
];

// policy-tun bring-up (off|tproxy|fakeip-tun → policy-tun). Тот же порядок
// милстоунов, что у fakeip (provision:current до Enable), но без DNS-части:
// перехват делает политика доступа NDMS, а не наши правила.
const POLICY_TUN_ENABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят предыдущий режим', detail: 'прежний перехват/маршруты убраны (если были)' },
	{ milestone: 'provision', title: 'Интерфейс OpkgTun создан', detail: 'ip global · разрешён в списке доступа' },
	{ milestone: 'provision', title: 'Дефолт-маршрут NDMS припаркован', detail: 'v4 (+v6) на интерфейс режима' },
	{ milestone: 'provision', title: 'config.json записан', detail: 'tun inbound · правила маршрутизации' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем tun-inbound' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'интерфейс поднят · маршруты на месте' },
];
const POLICY_TUN_DISABLE_STEPS: UIStepDef[] = [
	{ milestone: 'teardown', title: 'Снят policy-tun', detail: 'исходный NAT сегментов восстановлен' },
	{ milestone: 'provision', title: 'Дефолт-маршрут снят, интерфейс удалён', detail: 'OpkgTun убран · sing-box перестроен' },
	{ milestone: 'readiness', title: 'Перезапуск sing-box', detail: 'ожидаем inbounds' },
	{ milestone: 'ready', title: 'Проверка готовности', detail: 'предыдущий режим восстановлен' },
];

/** The predefined definitions for a transition direction (no derived state). */
export function stepDefsFor(from: FakeIPMode, to: FakeIPMode): UIStepDef[] {
	if (to === 'fakeip-tun') return ENABLE_STEPS;       // rich fakeip bring-up (unchanged)
	if (to === 'policy-tun') return POLICY_TUN_ENABLE_STEPS;
	if (to === 'tproxy') return TPROXY_ENABLE_STEPS;    // tproxy bring-up
	// to === 'off': teardown of the source mode.
	if (from === 'tproxy') return TPROXY_DISABLE_STEPS;
	if (from === 'policy-tun') return POLICY_TUN_DISABLE_STEPS;
	return DISABLE_STEPS;
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
	const messages = new Map<Milestone, string>();
	for (const s of received) {
		status.set(s.step, s.status);
		if (s.message) {
			messages.set(s.step, s.message);
		}
	}

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
		const liveMessage = messages.get(def.milestone);

		const isDone = r <= maxDoneRank;
		if (isDone) {
			return { ...def, state: 'done', liveMessage };
		}

		if (failed) {
			if (!errorAssigned) {
				errorAssigned = true;
				return { ...def, state: 'error', liveMessage };
			}
			return { ...def, state: 'pending', liveMessage };
		}

		if (r === currentRank) {
			return { ...def, state: 'current', liveMessage };
		}

		return { ...def, state: 'pending', liveMessage };
	});
}
