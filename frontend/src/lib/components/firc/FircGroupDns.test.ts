import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircGroup, FircInterfaceOption } from '$lib/types/firc';
import { emptyExtConfig } from '$lib/utils/fircGroups';

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
import FircJournalPanel from './FircJournalPanel.svelte';
import FircMonitorTables from './FircMonitorTables.svelte';
import QuickAddRouteModal from '$lib/components/diagnostics/QuickAddRouteModal.svelte';
import { formatOutboundLabel } from './fircMonitorFormat';

const DOH = 'https://9.9.9.9/dns-query';
const OLD: FircInterfaceOption[] = [{ id: 'nwg0', role: 'tunnel' }];
const NEW: FircInterfaceOption[] = [{ id: 'dns' }, ...OLD];
const vpn = (over: Partial<FircGroup> = {}): FircGroup => ({
	id: 'g1', name: 'VPN', interface: 'nwg0', enable: true, rules: [], resolve: { tunnel: true, server: '' }, ...over,
});

describe('DNS группы FIRC', () => {
	let groups: FircGroup[];
	let interfaces: FircInterfaceOption[];

	beforeEach(() => {
		localStorage.clear();
		sessionStorage.clear();
		localStorage.setItem('awgm.firc.groupView', 'list');
		interfaces = NEW;
		api.fircGetGroups.mockImplementation(async () => structuredClone(groups));
		api.fircGetGroupsState.mockImplementation(async () => ({ groups: {} }));
		api.fircGetInterfaces.mockImplementation(async () => structuredClone(interfaces));
		api.fircGetExt.mockImplementation(async () => emptyExtConfig());
		api.fircGetListCatalog.mockImplementation(async () => null);
		api.fircSaveGroups.mockReset();
		api.fircSaveGroups.mockImplementation(async (g: FircGroup[]) => ({ groups: g }));
	});
	afterEach(() => cleanup());

	const button = (name: string) => screen.getByRole<HTMLButtonElement>('button', { name });
	const serverInput = (n: number) => screen.getByRole<HTMLInputElement>('textbox', { name: m.firc_resolve_server_aria({ n }) });

	async function openSettings() {
		render(FircGroupsPanel);
		await fireEvent.click(await screen.findByTitle(m.firc_groups_settings_tip()));
	}

	async function pickIface(value: string) {
		await fireEvent.change(document.getElementById('firc-gm-iface') as HTMLSelectElement, { target: { value } });
	}

	async function save(): Promise<FircGroup[]> {
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		return api.fircSaveGroups.mock.calls[0][0];
	}

	it('makes a dns only group from presets with the fallback off', async () => {
		groups = [vpn()];
		await openSettings();
		await pickIface('dns');
		expect(screen.getByText(m.firc_dns_only_hint())).toBeTruthy();
		expect(document.getElementById('firc-gm-dns')).toBeNull();
		expect(screen.getByRole('alert').textContent).toBe(m.firc_dns_only_server_required());
		expect(button(m.common_apply()).disabled).toBe(true);

		await fireEvent.click(button(m.firc_resolve_preset({ name: 'Quad9' })));
		expect(serverInput(1).value).toBe(DOH);
		expect(button(m.firc_resolve_preset({ name: 'Quad9' })).disabled).toBe(true);
		expect(screen.getByText(m.firc_resolve_hint_literal())).toBeTruthy();
		await fireEvent.click(screen.getByLabelText(m.firc_resolve_fallback()));
		expect(screen.getByText(m.firc_resolve_fallback_off_hint())).toBeTruthy();
		await fireEvent.click(button(m.common_apply()));

		const [saved] = await save();
		expect(saved.interface).toBe('dns');
		expect(saved.resolve).toEqual({ server: DOH, fallback: false });
	});

	it('edits the server list with inline errors and a limit of four', async () => {
		groups = [vpn({ resolve: { tunnel: true, server: '1.1.1.1' } })];
		await openSettings();
		for (const value of ['1.1.1.1:53', 'https://dns.example', '2.2.2.2']) {
			await fireEvent.click(button(m.firc_resolve_add()));
			const n = screen.getAllByRole('textbox', { name: /DNS/ }).length;
			await fireEvent.input(serverInput(n), { target: { value } });
		}
		expect(button(m.firc_resolve_add()).disabled).toBe(true);
		expect(screen.getByText(m.firc_resolve_err_duplicate())).toBeTruthy();
		expect(screen.getByText(m.firc_resolve_err_url())).toBeTruthy();
		expect(button(m.common_apply()).disabled).toBe(true);

		await fireEvent.click(screen.getAllByRole('button', { name: m.firc_resolve_remove() })[1]);
		await fireEvent.input(serverInput(2), { target: { value: DOH } });
		await fireEvent.click(button(m.common_apply()));
		expect((await save())[0].resolve).toEqual({ tunnel: true, server: `1.1.1.1, ${DOH}, 2.2.2.2` });
	});

	it('asks to update FIRC when the daemon knows no dns interface', async () => {
		interfaces = OLD;
		groups = [vpn({ resolve: { tunnel: true, server: '1.1.1.1' } })];
		await openSettings();
		expect([...document.querySelectorAll('#firc-gm-iface option')].map((o) => (o as HTMLOptionElement).value)).not.toContain('dns');
		expect(screen.queryByRole('button', { name: m.firc_resolve_preset({ name: 'Quad9' }) })).toBeNull();
		expect(screen.getByText(m.firc_dns_only_need_update())).toBeTruthy();
		await fireEvent.input(serverInput(1), { target: { value: DOH } });
		expect(screen.getByText(m.firc_resolve_err_doh_unsupported())).toBeTruthy();
		expect(button(m.common_apply()).disabled).toBe(true);
	});

	it('keeps an existing DoH group editable while FIRC is not running', async () => {
		interfaces = [];
		groups = [vpn({ interface: 'dns', resolve: { server: DOH, fallback: false } })];
		await openSettings();
		expect((document.getElementById('firc-gm-iface') as HTMLSelectElement).value).toBe('dns');
		expect(button(m.common_apply()).disabled).toBe(false);
	});

	it('offers dns only in the picker and in bulk with the reason when it is closed', async () => {
		groups = [vpn()];
		render(FircGroupsPanel);
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_bulk_select_groups_btn() }));
		await fireEvent.click(screen.getByText(m.firc_bulk_select_all({ count: 1 })));
		const opt = screen.getByRole<HTMLOptionElement>('option', { name: new RegExp(m.firc_dns_only_need_server()) });
		expect(opt.disabled).toBe(true);

		await fireEvent.click(document.querySelector('.iface-picker-btn') as HTMLButtonElement);
		const row = document.querySelector('.iface-special-row.dns-only') as HTMLButtonElement;
		expect(row.disabled).toBe(true);
		expect(row.textContent).toContain(m.firc_dns_only_need_server());
	});

	it('does not save a dns group with a subnet rule', async () => {
		groups = [vpn({ interface: 'dns', resolve: { server: DOH, fallback: true } })];
		render(FircGroupsPanel);
		await fireEvent.click(await screen.findByTitle(m.firc_groups_own_expand_tip()));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_rule() }));
		const type = document.querySelector('.rule-type-sel') as HTMLSelectElement;
		expect([...type.options].filter((o) => o.disabled).map((o) => o.value)).toEqual(['subnet', 'subnet6']);
		expect(screen.getByText(m.firc_dns_only_no_subnets())).toBeTruthy();
		await fireEvent.change(type, { target: { value: 'subnet' } });
		await fireEvent.input(document.querySelector('.rule-pattern-inp') as HTMLInputElement, { target: { value: '10.0.0.0/8' } });
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await new Promise((r) => setTimeout(r, 0));
		expect(api.fircSaveGroups).not.toHaveBeenCalled();
	});

	it('shows no probe or dot for a dns group on the compact card', async () => {
		localStorage.setItem('awgm.firc.groupView', 'grid');
		groups = [vpn({ interface: 'dns', resolve: { server: DOH, fallback: true } })];
		render(FircGroupsPanel);
		expect(await screen.findByText(m.firc_dns_only(), { selector: '.compact-out-name' })).toBeTruthy();
		expect(document.querySelector('.compact-group-output .iface-dot')).toBeNull();
	});

	it('names the dns outbound in the monitor and the journal', async () => {
		expect(formatOutboundLabel('dns')).toBe(m.firc_dns_only());
		const snapshot = { groups: [{ id: 'g1', name: 'DoH', activeOutbound: 'dns', live: true }], servers: [] };
		render(FircMonitorTables, {
			props: {
				view: 'details', snapshot: snapshot as never, expandedClients: {}, hosts: [], policies: [],
				onToggleClients: vi.fn(), onFilterClientEvents: vi.fn(),
			},
		});
		expect(screen.getByText(m.firc_dns_only())).toBeTruthy();
		cleanup();

		groups = [];
		const event = { seq: 1, at: 1, kind: 'dns', client: '192.168.1.2', name: 'a.example', qtype: 'A', rcode: 'NOERROR', decision: 'dns-only', reals: [] };
		api.fircGetEvents.mockImplementation(async (since: number) => ({ events: since ? [] : [event], next: 1 }));
		api.fircGetDnsStats.mockImplementation(async () => ({
			rows: [{ name: 'a.example', queries: 1, last: 1, clients: [], owner: { id: 'g1', name: 'DoH', interface: 'dns', blocked: false } }],
		}));
		api.fircGetCapture.mockImplementation(async () => ({ running: false }));
		render(FircJournalPanel);
		expect((await screen.findByText(/a\.example/)).textContent).toContain(`[${m.firc_dns_only()}]`);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_journal_tab_dns({ n: 1 }) }));
		expect(await screen.findByText(m.firc_dns_only(), { selector: 'td' })).toBeTruthy();
	});

	it('does not offer a dns group as a destination for a subnet', async () => {
		groups = [vpn(), vpn({ id: 'g2', name: 'DoH', interface: 'dns', resolve: { server: DOH } })];
		const options = async (props: { ip?: string; domain?: string }) => {
			render(QuickAddRouteModal, { props: { open: true, onclose: vi.fn(), ...props } });
			await waitFor(() => expect(document.querySelector('#qa-dest-select option[value="firc:g1"]')).toBeTruthy());
			const has = Boolean(document.querySelector('#qa-dest-select option[value="firc:g2"]'));
			cleanup();
			return has;
		};
		expect(await options({ ip: '9.9.9.9' })).toBe(false);
		expect(await options({ domain: 'a.example' })).toBe(true);
	});

	it('keeps the full DoH address in the badge title', async () => {
		groups = [vpn({ interface: 'dns', resolve: { server: DOH, fallback: true } })];
		render(FircGroupsPanel);
		const chip = await screen.findByText('DNS: 9.9.9.9');
		expect(chip.getAttribute('title')).toContain(DOH);
		expect(document.querySelector('.iface-picker-btn')?.getAttribute('title')).toContain(DOH);
	});
});
