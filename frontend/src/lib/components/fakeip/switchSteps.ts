// Pure mapping from the backend's COARSE transition milestones to the richer,
// PREDEFINED step list the SwitchProgress modal renders (FE-spec §7.3). The
// backend emits only start/teardown/provision/readiness/ready (+ rollback/error
// on failure), but the approved mockup (page-transition.html) shows a vertical
// list with static titles and sub-details. We keep a fixed ordered UI model and
// DERIVE each row's state (done/current/pending/error) from the milestones that
// actually arrived — no fabricated progress.
//
// Side-effect-free so the mapping is unit-tested without mounting Svelte.

import { m } from '$lib/i18n';
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
// Functions (not constants) so the copy follows the active locale at call time.
const enableSteps = (): UIStepDef[] => [
	{
		milestone: 'teardown',
		title: m.fakeip_step_tproxy_removed_title(),
		detail: m.fakeip_step_jumps_chains_removed_detail(),
	},
	{
		milestone: 'provision',
		title: m.fakeip_step_opkgtun_created_title(),
		detail: 'non-global · private · MTU 1500',
	},
	{
		milestone: 'provision',
		title: m.fakeip_step_ndms_routes_applied_title(),
		detail: m.fakeip_step_ndms_routes_applied_detail(),
	},
	{
		milestone: 'provision',
		title: m.fakeip_step_config_written_title(),
		detail: m.fakeip_step_config_written_detail_fakeip(),
	},
	{
		milestone: 'readiness',
		title: m.fakeip_step_restart_title(),
		detail: m.fakeip_step_restart_detail_inbounds(),
	},
	{
		milestone: 'ready',
		title: m.fakeip_step_ready_title(),
		detail: m.fakeip_step_ready_detail_fakeip(),
	},
];

// Disable / switch-out (fakeip+tun → tproxy|off): a simpler tear-down list.
const disableSteps = (): UIStepDef[] => [
	{
		milestone: 'teardown',
		title: m.fakeip_step_fakeip_removed_title(),
		detail: m.fakeip_step_fakeip_removed_detail(),
	},
	{
		milestone: 'provision',
		title: m.fakeip_step_ndms_routes_removed_title(),
		detail: m.fakeip_step_opkgtun_removed_detail(),
	},
	{
		milestone: 'readiness',
		title: m.fakeip_step_restart_title(),
		detail: m.fakeip_step_restart_detail_inbounds(),
	},
	{
		milestone: 'ready',
		title: m.fakeip_step_ready_title(),
		detail: m.fakeip_step_ready_detail_restored(),
	},
];

// TProxy bring-up (off|fakeip-tun → tproxy). Order matches Enable: sing-box
// readiness first, then iptables install.
const tproxyEnableSteps = (): UIStepDef[] => [
	{ milestone: 'teardown', title: m.fakeip_step_prev_mode_removed_title(), detail: m.fakeip_step_prev_mode_removed_detail_tproxy() },
	{ milestone: 'readiness', title: m.fakeip_step_restart_title(), detail: m.fakeip_step_restart_detail_inbounds() },
	{ milestone: 'provision', title: m.fakeip_step_iptables_installed_title(), detail: m.fakeip_step_jumps_chains_detail() },
	{ milestone: 'ready', title: m.fakeip_step_ready_title(), detail: m.fakeip_step_ready_detail_tproxy() },
];
const tproxyDisableSteps = (): UIStepDef[] => [
	{ milestone: 'teardown', title: m.fakeip_step_tproxy_removed_title(), detail: m.fakeip_step_iptables_removed_detail() },
	{ milestone: 'provision', title: m.fakeip_step_singbox_rebuilt_title(), detail: m.fakeip_step_singbox_rebuilt_detail() },
	{ milestone: 'readiness', title: m.fakeip_step_restart_title(), detail: m.fakeip_step_restart_detail_inbounds() },
	{ milestone: 'ready', title: m.fakeip_step_ready_title(), detail: m.fakeip_step_ready_detail_off() },
];

// policy-tun bring-up (off|tproxy|fakeip-tun → policy-tun). Тот же порядок
// милстоунов, что у fakeip (provision:current до Enable), но без DNS-части:
// перехват делает политика доступа NDMS, а не наши правила.
const policyTunEnableSteps = (): UIStepDef[] => [
	{ milestone: 'teardown', title: m.fakeip_step_prev_mode_removed_title(), detail: m.fakeip_step_prev_mode_removed_detail_policy() },
	{ milestone: 'provision', title: m.fakeip_step_opkgtun_created_title(), detail: m.fakeip_step_opkgtun_created_detail_policy() },
	{ milestone: 'provision', title: m.fakeip_step_default_route_parked_title(), detail: m.fakeip_step_default_route_parked_detail() },
	{ milestone: 'provision', title: m.fakeip_step_config_written_title(), detail: m.fakeip_step_config_written_detail_policy() },
	{ milestone: 'readiness', title: m.fakeip_step_restart_title(), detail: m.fakeip_step_restart_detail_tun() },
	{ milestone: 'ready', title: m.fakeip_step_ready_title(), detail: m.fakeip_step_ready_detail_policy() },
];
const policyTunDisableSteps = (): UIStepDef[] => [
	{ milestone: 'teardown', title: m.fakeip_step_policy_tun_removed_title(), detail: m.fakeip_step_policy_tun_removed_detail() },
	{ milestone: 'provision', title: m.fakeip_step_default_route_removed_title(), detail: m.fakeip_step_opkgtun_removed_detail() },
	{ milestone: 'readiness', title: m.fakeip_step_restart_title(), detail: m.fakeip_step_restart_detail_plain() },
	{ milestone: 'ready', title: m.fakeip_step_ready_title(), detail: m.fakeip_step_ready_detail_restored() },
];

/** The predefined definitions for a transition direction (no derived state). */
export function stepDefsFor(from: FakeIPMode, to: FakeIPMode): UIStepDef[] {
	if (to === 'fakeip-tun') return enableSteps();       // rich fakeip bring-up (unchanged)
	if (to === 'policy-tun') return policyTunEnableSteps();
	if (to === 'tproxy') return tproxyEnableSteps();    // tproxy bring-up
	// to === 'off': teardown of the source mode.
	if (from === 'tproxy') return tproxyDisableSteps();
	if (from === 'policy-tun') return policyTunDisableSteps();
	return disableSteps();
}

function rank(milestone: Milestone): number {
	const i = MILESTONE_ORDER.indexOf(milestone);
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
	for (const [ms, st] of status) {
		if (st === 'done') maxDoneRank = Math.max(maxDoneRank, rank(ms));
	}

	// The single `current` milestone to emphasise: the highest-ranked one whose
	// status is `current` and which isn't already superseded by a later `done`.
	let currentRank = -1;
	for (const [ms, st] of status) {
		if (st === 'current' && rank(ms) > maxDoneRank) {
			currentRank = Math.max(currentRank, rank(ms));
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
