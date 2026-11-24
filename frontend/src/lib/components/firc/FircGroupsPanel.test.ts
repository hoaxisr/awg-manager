import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircExtConfig, FircGroup } from '$lib/types/firc';

const { api } = vi.hoisted(() => {
	const fns: Record<string, ReturnType<typeof vi.fn>> = {};
	return {
		api: new Proxy(fns, {
			get: (t, key: string) => (t[key] ??= vi.fn(async () => [])),
		}),
	};
});
vi.mock('$lib/api/client', () => ({ api }));

import FircGroupsPanel from './FircGroupsPanel.svelte';

const groups: FircGroup[] = [
	{ id: 'g1', name: 'One', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
	{ id: 'g2', name: 'Two', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
];
const ext: FircExtConfig = {
	proxyGroups: [{ id: 'pg', name: 'PG', interfaces: ['eth3', 'tunvless0'], check: { mode: 'url', url: 'https://pg.example/204' } }],
	keenDnsCloud: { enabled: false, interfaces: [] },
	serverRouting: { enabledServers: [] },
	groupLinks: { g1: 'pg' },
};

describe('FircGroupsPanel', () => {
	beforeEach(() => {
		localStorage.clear();
		localStorage.setItem('awgm.firc.groupView', 'list');
		api.fircGetGroups.mockImplementation(async () => structuredClone(groups));
		api.fircGetGroupsState.mockImplementation(async () => ({ groups: {} }));
		api.fircGetExt.mockImplementation(async () => structuredClone(ext));
		api.fircSaveGroups.mockReset();
		api.fircSaveGroups.mockImplementation(async (g: FircGroup[]) => ({ groups: g }));
	});
	afterEach(() => {
		cleanup();
	});

	async function moveDownAndSave() {
		render(FircGroupsPanel);
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		return api.fircSaveGroups.mock.calls[0];
	}

	it('sends group links beside the groups', async () => {
		const [payload, , links] = await moveDownAndSave();
		expect(payload.map((g: FircGroup) => g.id)).toEqual(['g2', 'g1']);
		expect(JSON.stringify(payload)).not.toContain('proxyGroupId');
		expect(links).toEqual({ g1: 'pg' });

		// После сохранения связь остаётся в панели: второе сохранение её не теряет.
		await fireEvent.click((await screen.findAllByTitle(m.firc_groups_move_down()))[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(2));
		expect(api.fircSaveGroups.mock.calls[1][2]).toEqual({ g1: 'pg' });
	});

	it('links every group of a bulk proxy group pick', async () => {
		render(FircGroupsPanel);
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_bulk_select_groups_btn() }));
		await fireEvent.click(screen.getByText(m.firc_bulk_select_all({ count: 2 })));
		await fireEvent.change(screen.getByRole('combobox', { name: m.firc_bulk_groups_change_iface() }), {
			target: { value: 'pg:pg' },
		});
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		expect(links).toEqual({ g1: 'pg', g2: 'pg' });
		expect(payload.map((g: FircGroup) => g.check?.url)).toEqual(['https://pg.example/204', 'https://pg.example/204']);
	});

	it('leaves links to the server when the ext config could not be read', async () => {
		api.fircGetExt.mockImplementation(async () => {
			throw new Error('down');
		});
		const [, , links] = await moveDownAndSave();
		expect(links).toBeUndefined();
	});
});
