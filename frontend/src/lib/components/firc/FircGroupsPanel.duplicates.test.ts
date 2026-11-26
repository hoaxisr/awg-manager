import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircGroup } from '$lib/types/firc';
import { clearFircGroupsDraft } from '$lib/stores/fircDraft';
import { fircOwnRuleOverlapId } from '$lib/utils/fircOverlap';

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

const initialGroups: FircGroup[] = [
	{
		id: 'g-upper',
		name: 'Google AI',
		interface: 'nwg0',
		enable: true,
		devices: { allow: [], deny: [] },
		rules: [
			{ id: 'u-1', type: 'namespace', rule: 'deepmind.com', enable: true },
			{ id: 'u-2', type: 'namespace', rule: 'gemini.google.com', enable: true },
		],
	},
	{
		id: 'g-lower',
		name: 'Antigravity',
		interface: 'nwg0',
		enable: true,
		devices: { allow: [], deny: [] },
		rules: [
			{ id: 'l-1', type: 'namespace', rule: 'deepmind.com', enable: true },
			{ id: 'l-2', type: 'namespace', rule: 'unique-domain.dev', enable: true },
		],
	},
];

beforeEach(() => {
	vi.clearAllMocks();
	localStorage.clear();
	clearFircGroupsDraft();
	localStorage.setItem('awgm.firc.groupView', 'list');
	api.fircGetGroups.mockImplementation(async () => structuredClone(initialGroups));
	api.fircGetGroupsState.mockImplementation(async () => ({ groups: {} }));
	api.fircGetInterfaces.mockImplementation(async () => [
		{ id: 'nwg0', name: 'Finland', role: 'tunnel', up: true },
	]);
	api.fircGetTunnelsState.mockImplementation(async () => []);
	api.fircGetHosts.mockImplementation(async () => []);
	api.fircGetPolicies.mockImplementation(async () => []);
	api.fircGetResolvers.mockImplementation(async () => []);
	api.fircGetExt.mockImplementation(async () => ({
		proxyGroups: [],
		keenDnsCloud: { enabled: false, interfaces: [] },
		serverRouting: { enabledServers: [] },
	}));
	api.fircGetGroupListRules.mockImplementation(async () => ({
		total: 0,
		matched: 0,
		offset: 0,
		rules: [],
	}));
});

afterEach(() => {
	cleanup();
	clearFircGroupsDraft();
});

describe('FircGroupsPanel duplicate detection and import deduplication', () => {
	it('marks only the shadowed lower group and rule row, portals popover to body, and removes shadowed duplicates', async () => {
		const { container } = render(FircGroupsPanel);
		await screen.findByText('Antigravity');

		const upperCard = container.querySelector('[data-firc-group-id="g-upper"]')!;
		const lowerCard = container.querySelector('[data-firc-group-id="g-lower"]')!;

		expect(upperCard.querySelector('.dup-menu-trigger')).toBeNull();
		const dupTrigger = lowerCard.querySelector('.dup-menu-trigger') as HTMLButtonElement;
		expect(dupTrigger).not.toBeNull();
		expect(dupTrigger.getAttribute('data-overlap')).toBe('shadowed');
		expect(dupTrigger.textContent).toContain('1');

		// Expand lower group to inspect rule-level warning icon
		const expandBtn = lowerCard.querySelector(
			`button[title="${m.firc_groups_expand_rules()}"]`,
		) as HTMLButtonElement;
		await fireEvent.click(expandBtn);

		const lowerRule1Key = fircOwnRuleOverlapId('g-lower', 'l-1');
		const lowerRule2Key = fircOwnRuleOverlapId('g-lower', 'l-2');
		const shadowedRow = container.querySelector(`[data-firc-rule-key="${lowerRule1Key}"]`)!;
		const cleanRow = container.querySelector(`[data-firc-rule-key="${lowerRule2Key}"]`)!;
		const rowWarn = shadowedRow.querySelector('.rule-dup-indicator') as HTMLButtonElement;
		expect(rowWarn).not.toBeNull();
		expect(rowWarn.getAttribute('data-overlap')).toBe('shadowed');
		expect(rowWarn.getAttribute('title')).toContain('Google AI');
		expect(cleanRow.querySelector('.rule-dup-indicator')).toBeNull();

		// Clicking row warning jumps to covering upper rule and expands upper group
		await fireEvent.click(rowWarn);
		await waitFor(() => {
			expect(
				container.querySelector(`[data-firc-rule-key="${fircOwnRuleOverlapId('g-upper', 'u-1')}"]`),
			).not.toBeNull();
		});

		// Open duplicate menu (portaled to document.body outside overflow:hidden .main) and close with Escape
		await fireEvent.click(dupTrigger);
		const popover = document.body.querySelector('.dup-popover');
		expect(popover).not.toBeNull();
		expect(popover!.parentElement).toBe(document.body);

		await fireEvent.keyDown(window, { key: 'Escape' });
		await waitFor(() => {
			expect(document.body.querySelector('.dup-popover')).toBeNull();
		});

		// Re-open and remove shadowed duplicates via popover button
		await fireEvent.click(dupTrigger);
		const removeBtn = document.body.querySelector('.dup-popover .dup-clean-btn') as HTMLButtonElement;
		expect(removeBtn).not.toBeNull();
		expect(removeBtn.textContent).toContain(m.firc_dup_remove_shadowed_btn({ count: 1 }));
		await fireEvent.click(removeBtn);

		await waitFor(() => {
			expect(container.querySelector(`[data-firc-rule-key="${lowerRule1Key}"]`)).toBeNull();
			expect(container.querySelector(`[data-firc-rule-key="${lowerRule2Key}"]`)).not.toBeNull();
			expect(lowerCard.querySelector('.dup-menu-trigger')).toBeNull();
			expect(document.body.querySelector('.dup-popover')).toBeNull();
		});
	});

	it('detects and skips duplicate rules during bulk import by default, and allows force-import', async () => {
		const { container } = render(FircGroupsPanel);
		await screen.findByText('Antigravity');

		const lowerCard = container.querySelector('[data-firc-group-id="g-lower"]')!;
		const importBtn = lowerCard.querySelector(
			`button[title="${m.firc_groups_import_rules_tip()}"]`,
		) as HTMLButtonElement;
		await fireEvent.click(importBtn);

		const textarea = await screen.findByPlaceholderText(/openai\.com/);
		await fireEvent.input(textarea, {
			target: {
				value: ['deepmind.com', 'gemini.google.com', 'fresh.example.org', 'fresh.example.org'].join(
					'\n',
				),
			},
		});

		expect(await screen.findByText(m.firc_import_chip_same_or_batch({ count: 2 }))).toBeTruthy();
		expect(
			screen.getByText(new RegExp(m.firc_import_chip_upper_shadowed({ count: 2 }))).textContent,
		).toContain('Google AI');

		const submitBtn = screen.getByRole('button', {
			name: m.firc_import_rules_btn_count({ count: 1 }),
		});
		await fireEvent.click(submitBtn);

		await waitFor(() => {
			const inputs = Array.from(
				lowerCard.querySelectorAll<HTMLInputElement>('input.rule-pattern-inp'),
			).map((el) => el.value);
			expect(inputs).toEqual(['fresh.example.org', 'deepmind.com', 'unique-domain.dev']);
		});

		// Re-open import and force-import an upper-group duplicate by unchecking the shadowed skip box
		await fireEvent.click(importBtn);
		const textarea2 = await screen.findByPlaceholderText(/openai\.com/);
		await fireEvent.input(textarea2, { target: { value: 'gemini.google.com' } });

		const skipShadowedCheckbox = screen.getByLabelText<HTMLInputElement>(
			m.firc_import_skip_upper_label({ groups: 'Google AI' }),
		);
		expect(skipShadowedCheckbox.checked).toBe(true);
		await fireEvent.click(skipShadowedCheckbox);

		const submitForced = screen.getByRole('button', {
			name: m.firc_import_rules_btn_count({ count: 1 }),
		});
		await fireEvent.click(submitForced);

		await waitFor(() => {
			const dupTrigger = lowerCard.querySelector('.dup-menu-trigger') as HTMLButtonElement;
			expect(dupTrigger).not.toBeNull();
			expect(dupTrigger.textContent).toContain('2');
		});
	});

	it('shows duplicate warning badge in compact grid view and jumps to rule on click', async () => {
		localStorage.setItem('awgm.firc.groupView', 'grid');
		const { container } = render(FircGroupsPanel);
		await screen.findByText('Antigravity');

		const upperCard = container.querySelector('[data-firc-group-id="g-upper"]')!;
		const lowerCard = container.querySelector('[data-firc-group-id="g-lower"]')!;

		expect(upperCard.querySelector('.compact-dup-badge')).toBeNull();
		const compactBadge = lowerCard.querySelector('.compact-dup-badge') as HTMLButtonElement;
		expect(compactBadge).not.toBeNull();
		expect(compactBadge.getAttribute('data-overlap')).toBe('shadowed');
		expect(compactBadge.getAttribute('aria-label')).toContain('1');
		expect(compactBadge.textContent).toContain('1');

		await fireEvent.click(compactBadge);

		await waitFor(() => {
			expect(
				container.querySelector(`[data-firc-rule-key="${fircOwnRuleOverlapId('g-lower', 'l-1')}"]`),
			).not.toBeNull();
		});
	});

	it('clears stale remote list overlap warnings when the remote list is removed from an upper group', async () => {
		const groupsWithList: FircGroup[] = [
			{
				id: 'g-list',
				name: 'Remote List AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: 'https://example.com/ai.srs', interval: 86400, rulesTotal: 1 },
			},
			{
				id: 'g-manual',
				name: 'Manual AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'm-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(groupsWithList));
		api.fircGetGroupListRules.mockImplementation(async () => ({
			total: 1,
			matched: 1,
			offset: 0,
			rules: [{ id: 'lr-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
		}));

		const { container } = render(FircGroupsPanel);
		await screen.findByText('Remote List AI');

		const listCard = container.querySelector('[data-firc-group-id="g-list"]')!;
		const manualCard = container.querySelector('[data-firc-group-id="g-manual"]')!;

		// Expand upper list group to load its remote list page
		const expandListBtn = listCard.querySelector(
			`button[title="${m.firc_groups_expand_rules()}"]`,
		) as HTMLButtonElement;
		await fireEvent.click(expandListBtn);

		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});

		// Remove the remote list from the upper group -> stale overlap on lower group must disappear
		const clearListBtn = listCard.querySelector(
			`button[title="${m.firc_groups_list_clear_tip()}"]`,
		) as HTMLButtonElement;
		await fireEvent.click(clearListBtn);

		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();
		});
	});

	it('invalidates listPages on inline list URL edit and reverts toggled list rules on resetAllChanges', async () => {
		const groupsWithList: FircGroup[] = [
			{
				id: 'g-list',
				name: 'Remote List AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: 'https://example.com/ai.srs', interval: 86400, rulesTotal: 1 },
			},
			{
				id: 'g-manual',
				name: 'Manual AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'm-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(groupsWithList));
		api.fircGetGroupListRules.mockImplementation(async () => ({
			total: 1,
			matched: 1,
			offset: 0,
			rules: [{ id: 'lr-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
		}));

		const { container } = render(FircGroupsPanel);
		await screen.findByText('Remote List AI');

		const listCard = container.querySelector('[data-firc-group-id="g-list"]')!;
		const manualCard = container.querySelector('[data-firc-group-id="g-manual"]')!;

		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_expand_rules()}"]`) as HTMLButtonElement,
		);
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});

		// Disable the remote list rule -> lower overlap disappears
		const listRuleToggle = listCard.querySelector(
			`[aria-label="${m.firc_groups_toggle_list_rule_aria()}"]`,
		) as HTMLElement;
		expect(listRuleToggle).not.toBeNull();
		await fireEvent.click(listRuleToggle);
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();
		});

		// Discard changes -> listPages must reload from backend and restore overlap
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_reset() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_discard_confirm() }));
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});

		// Edit list URL inline -> stale list rules and overlap must clear immediately
		const urlInput = listCard.querySelector('input.list-url-inline') as HTMLInputElement;
		await fireEvent.input(urlInput, { target: { value: 'https://example.com/other.srs' } });
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();
			expect(listCard.querySelector('.list-rule-row')).toBeNull();
		});

		// Collapsing and re-expanding before saving must NOT re-fetch stale persisted rules under the new draft URL
		const callsBeforeReexpand = api.fircGetGroupListRules.mock.calls.length;
		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_collapse_rules()}"]`) as HTMLButtonElement,
		);
		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_expand_rules()}"]`) as HTMLButtonElement,
		);
		expect(api.fircGetGroupListRules).toHaveBeenCalledTimes(callsBeforeReexpand);
		expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();

		// Reverting the inline URL back to the persisted URL while expanded reloads the list and restores overlap
		await fireEvent.input(urlInput, { target: { value: 'https://example.com/ai.srs' } });
		await waitFor(() => {
			expect(listCard.querySelector('.list-rule-row')).not.toBeNull();
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});
	});

	it('explains upper-group shadowing in Modal 3 even when rule is already in current group', async () => {
		const { container } = render(FircGroupsPanel);
		await screen.findByText('Antigravity');

		const lowerCard = container.querySelector('[data-firc-group-id="g-lower"]')!;
		const importBtn = lowerCard.querySelector(
			`button[title="${m.firc_groups_import_rules_tip()}"]`,
		) as HTMLButtonElement;
		await fireEvent.click(importBtn);

		const textarea = await screen.findByPlaceholderText(/openai\.com/);
		// deepmind.com is in BOTH Google AI (upper) and Antigravity (target)
		await fireEvent.input(textarea, { target: { value: 'deepmind.com' } });

		// Uncheck same-group skip while keeping upper-shadowed skip checked
		const skipSameCheckbox = screen.getByLabelText<HTMLInputElement>(
			m.firc_import_skip_same_label(),
		);
		await fireEvent.click(skipSameCheckbox);

		// Chip must explain why import is still 0 (blocked by upper group Google AI)
		const upperChip = await screen.findByText(
			new RegExp(m.firc_import_chip_upper_shadowed({ count: 1 })),
		);
		expect(upperChip.textContent).toContain('Google AI');

		// Unchecking upper-shadowed skip too must keep the upper-shadowed chip visible and stable
		const skipShadowedCheckbox = screen.getByLabelText<HTMLInputElement>(
			m.firc_import_skip_upper_label({ groups: 'Google AI' }),
		);
		await fireEvent.click(skipShadowedCheckbox);
		expect(
			screen.getByText(new RegExp(m.firc_import_chip_upper_shadowed({ count: 1 }))).textContent,
		).toContain('Google AI');

		// Expanding conflicts list shows both same-group and upper-group reason
		await fireEvent.click(
			screen.getByRole('button', { name: m.firc_import_show_conflicts({ count: 1 }) }),
		);
		expect(
			await screen.findByText(
				`${m.firc_import_status_same_group()} · ${m.firc_import_status_shadowed_by({ group: 'Google AI' })}`,
			),
		).toBeTruthy();
	});

	it('re-fetches an expanded list page cleanly instead of sticking in loading state when saved mid-load', async () => {
		const groupsWithList: FircGroup[] = [
			{
				id: 'g-list',
				name: 'Remote List AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: 'https://example.com/ai.srs', interval: 86400, rulesTotal: 1 },
			},
			{
				id: 'g-manual',
				name: 'Manual AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'm-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(groupsWithList));
		api.fircSaveGroups.mockImplementation(async (payload: FircGroup[]) => ({
			groups: JSON.parse(JSON.stringify(payload)) as FircGroup[],
		}));

		let resolveFirstLoad!: (v: unknown) => void;
		const firstLoadPromise = new Promise((res) => {
			resolveFirstLoad = res;
		});
		api.fircGetGroupListRules
			.mockReturnValueOnce(firstLoadPromise)
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				offset: 0,
				rules: [{ id: 'lr-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
			});

		const { container } = render(FircGroupsPanel);
		await screen.findByText('Remote List AI');

		const listCard = container.querySelector('[data-firc-group-id="g-list"]')!;
		const manualCard = container.querySelector('[data-firc-group-id="g-manual"]')!;

		// Start expanding list group (first request stays pending)
		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_expand_rules()}"]`) as HTMLButtonElement,
		);
		expect(await screen.findByText(m.firc_groups_list_loading())).toBeTruthy();

		// Make an unrelated dirty edit on manual group and click Save while list fetch is still in flight
		const manualToggle = manualCard.querySelector(
			`[aria-label="${m.firc_groups_toggle_group_aria({ name: 'Manual AI' })}"]`,
		) as HTMLElement;
		await fireEvent.click(manualToggle);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_save_changes() }));

		// Second list load triggered after save resolves and populates rules + overlap warning
		await waitFor(() => {
			expect(listCard.querySelector('.list-rule-row')).not.toBeNull();
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});

		// Late resolution of interrupted first request must not corrupt the loaded state
		resolveFirstLoad({ total: 0, matched: 0, offset: 0, rules: [] });
		await Promise.resolve();
		expect(listCard.querySelector('.list-rule-row')).not.toBeNull();
		expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
	});

	it('loads remote list rules when jumping to a collapsed upper group via duplicate list URL warning and retries failed initial list loads', async () => {
		const sameUrl = 'https://example.com/shared.srs';
		const groupsWithDupList: FircGroup[] = [
			{
				id: 'g-top-list',
				name: 'Top Remote',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: sameUrl, interval: 86400, rulesTotal: 1 },
			},
			{
				id: 'g-bot-list',
				name: 'Bottom Remote',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: sameUrl, interval: 86400, rulesTotal: 1 },
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(groupsWithDupList));
		api.fircGetGroupListRules
			.mockRejectedValueOnce(new Error('transient network error'))
			.mockResolvedValue({
				total: 1,
				matched: 1,
				offset: 0,
				rules: [{ id: 'lr-shared', type: 'namespace', rule: 'shared.ai', enable: true }],
			});

		const { container } = render(FircGroupsPanel);
		await screen.findByText('Bottom Remote');

		const topCard = container.querySelector('[data-firc-group-id="g-top-list"]')!;
		const botCard = container.querySelector('[data-firc-group-id="g-bot-list"]')!;

		// Jump from bottom group's duplicate list URL button -> expands top group and attempts list load (first attempt fails)
		const urlDupBtn = botCard.querySelector('.list-dup-warn') as HTMLButtonElement;
		expect(urlDupBtn).not.toBeNull();
		await fireEvent.click(urlDupBtn);

		await waitFor(() => {
			expect(api.fircGetGroupListRules).toHaveBeenCalledTimes(1);
			expect(topCard.querySelector('.rules-empty')).not.toBeNull();
		});

		// Collapsing and re-expanding top group must retry the failed initial load instead of sticking on cached empty page
		await fireEvent.click(
			topCard.querySelector(`button[title="${m.firc_groups_collapse_rules()}"]`) as HTMLButtonElement,
		);
		await fireEvent.click(
			topCard.querySelector(`button[title="${m.firc_groups_expand_rules()}"]`) as HTMLButtonElement,
		);

		await waitFor(() => {
			expect(api.fircGetGroupListRules).toHaveBeenCalledTimes(2);
			expect(topCard.querySelector('.list-rule-row')).not.toBeNull();
		});
	});

	it('excludes auto-detected CIDR subnets when bulk-importing into a DNS-only group', async () => {
		const dnsGroups: FircGroup[] = [
			{
				id: 'g-dns',
				name: 'DoH Group',
				interface: 'dns',
				enable: true,
				devices: { allow: [], deny: [] },
				resolve: { server: 'https://9.9.9.9/dns-query', fallback: true },
				rules: [],
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(dnsGroups));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'dns', role: 'other' },
			{ id: 'nwg0', name: 'Finland', role: 'tunnel', up: true },
		]);

		const { container } = render(FircGroupsPanel);
		await screen.findByText('DoH Group');

		const dnsCard = container.querySelector('[data-firc-group-id="g-dns"]')!;
		await fireEvent.click(
			dnsCard.querySelector(`button[title="${m.firc_groups_import_rules_tip()}"]`) as HTMLButtonElement,
		);

		const textarea = await screen.findByPlaceholderText(/openai\.com/);
		await fireEvent.input(textarea, {
			target: { value: ['allowed.example', '149.154.160.0/20'].join('\n') },
		});

		expect(await screen.findByText(m.firc_import_chip_unsupported_subnet({ count: 1 }))).toBeTruthy();
		const submitBtn = screen.getByRole('button', {
			name: m.firc_import_rules_btn_count({ count: 1 }),
		});
		await fireEvent.click(submitBtn);

		await waitFor(() => {
			const inputs = Array.from(
				dnsCard.querySelectorAll<HTMLInputElement>('input.rule-pattern-inp'),
			).map((el) => el.value);
			expect(inputs).toEqual(['allowed.example']);
		});
	});

	it('preserves unsaved remote-list rule edits when syncing a collapsed group', async () => {
		const groupsWithList: FircGroup[] = [
			{
				id: 'g-list',
				name: 'Remote List AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [],
				list: { url: 'https://example.com/ai.srs', interval: 86400, rulesTotal: 1 },
			},
			{
				id: 'g-manual',
				name: 'Manual AI',
				interface: 'nwg0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'm-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
			},
		];
		api.fircGetGroups.mockImplementation(async () => structuredClone(groupsWithList));
		api.fircGetGroupListRules.mockImplementation(async () => ({
			total: 1,
			matched: 1,
			offset: 0,
			rules: [{ id: 'lr-1', type: 'namespace', rule: 'remote-only.ai', enable: true }],
		}));
		api.fircSyncGroupList.mockResolvedValue(undefined);
		api.fircUpdateGroupListRules.mockResolvedValue(undefined);
		api.fircSaveGroups.mockImplementation(async (payload: FircGroup[]) => ({
			groups: JSON.parse(JSON.stringify(payload)) as FircGroup[],
		}));

		const { container } = render(FircGroupsPanel);
		await screen.findByText('Remote List AI');

		const listCard = container.querySelector('[data-firc-group-id="g-list"]')!;
		const manualCard = container.querySelector('[data-firc-group-id="g-manual"]')!;

		// Expand, load remote list, and disable the list rule
		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_expand_rules()}"]`) as HTMLButtonElement,
		);
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).not.toBeNull();
		});
		await fireEvent.click(
			listCard.querySelector(`[aria-label="${m.firc_groups_toggle_list_rule_aria()}"]`) as HTMLElement,
		);
		await waitFor(() => {
			expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();
			expect(screen.getByText(m.firc_groups_unsaved_title())).toBeTruthy();
		});

		// Collapse the group, then click Sync on the group row
		await fireEvent.click(
			listCard.querySelector(`button[title="${m.firc_groups_collapse_rules()}"]`) as HTMLButtonElement,
		);
		const callsBeforeSync = api.fircGetGroupListRules.mock.calls.length;
		const syncBtn = listCard.querySelector(
			`button[title="${m.firc_groups_sync_list_tip()}"]`,
		) as HTMLButtonElement;
		expect(syncBtn).not.toBeNull();

		vi.useFakeTimers();
		try {
			await fireEvent.click(syncBtn);
			await vi.advanceTimersByTimeAsync(1900);
		} finally {
			vi.useRealTimers();
		}

		// Sync refreshed list rules while preserving the unsaved toggle and dirty state
		expect(api.fircGetGroupListRules).toHaveBeenCalledTimes(callsBeforeSync + 1);
		expect(screen.getByText(m.firc_groups_unsaved_title())).toBeTruthy();
		expect(manualCard.querySelector('.dup-menu-trigger')).toBeNull();

		// Saving must still send the pending remote-list edit to the backend
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_save_changes() }));
		await waitFor(() => {
			expect(api.fircUpdateGroupListRules).toHaveBeenCalledWith(
				'g-list',
				[{ id: 'lr-1', enable: false }],
				false,
			);
		});
	});
});
