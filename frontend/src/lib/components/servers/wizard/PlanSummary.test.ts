import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { ChangePlan } from '$lib/types/serverWizard';
import PlanSummary from './PlanSummary.svelte';

const ACTIONS = ['create', 'modify', 'enable', 'disable', 'keep', 'publish'];
const TARGETS = [
	'awgm-xhttpd', 'cdn-dispatcher', 'client', 'direct_host', 'keendns_proxy', 'origin_source',
	'outbound_routing', 'public_domain', 'telemt', 'tgwebproxy', 'tproxy-server', 'upstream_egress',
];

function plan(items: [string, string][]): ChangePlan {
	return {
		summary: 's', restart_required: false, state_fingerprint: '',
		items: items.map(([action, target]) => ({ action, target, description: 'd' })),
	};
}

describe('PlanSummary', () => {
	it('действия и цели сервера показаны подписями, а не ключами', () => {
		const { container } = render(PlanSummary, {
			props: { plan: plan(TARGETS.map((t, i) => [ACTIONS[i % ACTIONS.length], t])) },
		});
		const raw = new Set([...ACTIONS, ...TARGETS]);
		for (const el of container.querySelectorAll('.action-badge, .target-val')) {
			expect(raw.has(el.textContent!.trim()), el.textContent!).toBe(false);
		}
		expect(container.querySelector('.action-badge')!.textContent).toContain(m.servers_wizard_action_create());
	});

	it('незнакомые действие и цель видны как есть', () => {
		const { container } = render(PlanSummary, { props: { plan: plan([['newact', 'newtarget']]) } });
		expect(container.querySelector('.action-badge')!.textContent).toContain('newact');
		expect(container.querySelector('.target-val')!.textContent).toBe('newtarget');
	});
});
