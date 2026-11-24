import { afterEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircGroupCheck, FircProxyGroup } from '$lib/types/firc';
import type { FircGroupsRuntime } from '$lib/utils/fircGroups';
import FircProxyGroupsModal from './FircProxyGroupsModal.svelte';

const rt: FircGroupsRuntime = {
	groups: [],
	liveMap: {},
	tunnelsState: [],
	interfaces: [],
	resolvers: [],
	proxyGroups: [],
	ifaceProbes: {},
	tunnelProbes: {},
};

function renderModal(check?: FircGroupCheck) {
	const pg: FircProxyGroup = { id: 'pg1', name: 'PG', interfaces: ['eth3', 'tunvless0'], check };
	render(FircProxyGroupsModal, {
		props: {
			open: true,
			draft: [pg],
			rt,
			probingIfaces: {},
			probingGroups: {},
			onprobe: vi.fn(),
			onclose: vi.fn(),
			onsave: vi.fn(),
		},
	});
	return pg;
}

describe('FircProxyGroupsModal', () => {
	afterEach(() => cleanup());

	it('shows a proxy group without check as managed by its groups', () => {
		renderModal();
		const select = screen.getByLabelText(m.firc_group_check_label());
		expect(select).toHaveValue('');
		expect(select).toHaveDisplayValue(m.firc_pg_check_mode_groups());
	});

	it('names the keendns mode', () => {
		renderModal({ mode: 'keendns', timeoutMs: 5000 });
		expect(screen.getByLabelText(m.firc_group_check_label())).toHaveDisplayValue(
			m.firc_group_check_mode_keendns(),
		);
	});

	it('keeps thresholds when the mode or URL is edited', async () => {
		const pg = renderModal({ mode: 'url', url: '', intervalSec: 30, failThreshold: 4 });
		await fireEvent.input(screen.getByLabelText(m.firc_group_check_url_label()), {
			target: { value: 'https://probe.example/204' },
		});
		expect(pg.check).toEqual({
			mode: 'url',
			url: 'https://probe.example/204',
			intervalSec: 30,
			failThreshold: 4,
		});
		await fireEvent.change(screen.getByLabelText(m.firc_group_check_label()), { target: { value: 'none' } });
		expect(pg.check).toEqual({ mode: 'none', url: '', intervalSec: 30, failThreshold: 4 });
	});

	it('keeps a disabled check when the chain shrinks', async () => {
		const pg = renderModal({ mode: 'none', intervalSec: 30 });
		await fireEvent.click(screen.getAllByRole('button', { name: m.firc_groups_pg_remove_iface() })[1]);
		expect(pg.interfaces).toEqual(['eth3']);
		expect(pg.check).toEqual({ mode: 'none', intervalSec: 30 });
	});
});
