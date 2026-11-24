import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { EgressOption } from '$lib/types/serverWizard';
import { m } from '$lib/i18n';
import ExtWizardStepEgress from '../ExtWizardStepEgress.svelte';
import { EXTERNAL_ACCESS_LABELS } from '../connectionLabels';
import { createInitialWizardDraft } from '../externalAccessModel';

const REASON = 'Правила FIRC неактивны (выбран движок sing-box)';

function mount(available: boolean) {
	const router: EgressOption = {
		id: 'firc:router', name: 'По правилам роутера', iface: 'router', kind: 'router_outbound', owner: 'firc',
		available, unavailable_reason: available ? undefined : REASON, supports_tcp: true, supports_udp: true, generation: 1,
	};
	const draft = createInitialWizardDraft({});
	draft.selectedKind = 'xray';
	draft.upstreamDevice = 'firc:router';
	const onselectxraycategory = vi.fn();
	render(ExtWizardStepEgress, {
		props: {
			draft, wantXray: true, wantTg: false, egressValidation: { valid: available },
			currentXrayEgressOption: router, currentTgEgressOption: undefined, xrayRouterOption: router,
			xrayEgressCategory: 'router', tgEgressCategory: 'direct', xrayTunnelOptions: [], tgTunnelOptions: [],
			onselectxraycategory, onselecttgcategory: vi.fn(), onselectxrayegress: vi.fn(), onselecttgegress: vi.fn(),
		},
	});
	const title = screen.getByText(EXTERNAL_ACCESS_LABELS.egressFirc);
	return { card: title.closest('button') as HTMLButtonElement, onselectxraycategory };
}

describe('ExtWizardStepEgress: «По правилам роутера»', () => {
	it('недоступный выход не выбирается и называет причину', async () => {
		const { card, onselectxraycategory } = mount(false);
		expect(card.disabled).toBe(true);
		card.click(); // как браузер: на disabled клика нет
		expect(onselectxraycategory).not.toHaveBeenCalled();
		expect(card.textContent).toContain(REASON);
		expect(screen.getAllByText(REASON).length).toBe(2); // карточка и баннер
		expect(screen.queryByText(m.servers_ext_wiz_s4_current_unavailable({ name: 'По правилам роутера' }))).toBeNull();
	});

	it('доступный выход выбирается, причины нет', async () => {
		const { card, onselectxraycategory } = mount(true);
		expect(card.disabled).toBe(false);
		card.click(); // как браузер: на disabled клика нет
		expect(onselectxraycategory).toHaveBeenCalledWith('router');
		expect(screen.queryByText(REASON)).toBeNull();
	});
});
