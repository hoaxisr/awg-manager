// Pure, direction-aware copy for the FakeIP mode-switch confirmation
// (FE-spec §7.2 / §7.3 / §12.4). Kept side-effect-free so the wording can be
// unit-tested without mounting the Svelte component.

import { m } from '$lib/i18n';

export type RoutingMode = 'off' | 'tproxy' | 'fakeip-tun' | 'policy-tun';

/** Display label for a routing mode (no emoji per house rules). */
export function humanLabel(mode: RoutingMode): string {
	switch (mode) {
		case 'off':
			return m.fakeip_mode_off();
		case 'tproxy':
			return 'TPROXY';
		case 'fakeip-tun':
			return 'FakeIP';
		case 'policy-tun':
			return m.sb_router_status_mode_policy_tun();
	}
}

/**
 * The «что произойдёт» action list for a from→to transition: tears down the
 * source mode (teardownOf) then lists the destination mode's bring-up steps;
 * to==='off' is teardown-only (FE-spec §7.2 / §7.3).
 */
export function switchConsequences(from: RoutingMode, to: RoutingMode): string[] {
	const teardownOf = (mode: RoutingMode): string[] => {
		if (mode === 'fakeip-tun') {
			return [m.fakeip_consequence_teardown_fakeip()];
		}
		if (mode === 'policy-tun') {
			return [
				m.fakeip_consequence_teardown_policy_tun(),
			];
		}
		if (mode === 'tproxy') {
			return [m.fakeip_consequence_teardown_tproxy()];
		}
		return [];
	};

	if (to === 'policy-tun') {
		return [
			...teardownOf(from),
			m.fakeip_consequence_restart_tun(),
			m.fakeip_consequence_policy_create_iface(),
			m.fakeip_consequence_policy_rules_kept(),
			m.fakeip_consequence_policy_manual_binding(),
		];
	}
	if (to === 'fakeip-tun') {
		return [
			...teardownOf(from),
			m.fakeip_consequence_restart_tun(),
			m.fakeip_consequence_fakeip_iface(),
			m.fakeip_consequence_fakeip_dns_hijack(),
			m.fakeip_consequence_fakeip_routes(),
		];
	}
	if (to === 'tproxy') {
		return [
			...teardownOf(from),
			m.fakeip_consequence_restart(),
			m.fakeip_consequence_tproxy_install(),
		];
	}
	// to === 'off'
	const td = teardownOf(from);
	return td.length ? td : [m.fakeip_consequence_off()];
}
