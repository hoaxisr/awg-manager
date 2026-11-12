import type { ComponentProps } from 'svelte';
import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import SubscriptionMemberList from './SubscriptionMemberList.svelte';
import type { SubscriptionMember } from '$lib/types';

const members: SubscriptionMember[] = [
	{ tag: 'tag-alpha', label: 'Alpha NL', protocol: 'vless', server: 'nl.example.com', port: 443 },
	{ tag: 'tag-bravo', label: 'Bravo DE', protocol: 'trojan', server: 'de.example.com', port: 8443 },
];

type Props = ComponentProps<typeof SubscriptionMemberList>;

function baseProps(over: Partial<Props> = {}): Props {
	return {
		members,
		effectiveActiveMember: null,
		switching: null,
		layout: 'list' as const,
		isInline: false,
		removingTag: null,
		minDelayMs: null,
		isUrlSub: true,
		selectMode: false,
		selected: new Set<string>(),
		excluding: false,
		creatingTunnelTag: null,
		totalCount: members.length,
		availableProtocols: [
			{ protocol: 'trojan', label: 'Trojan', count: 1 },
			{ protocol: 'vless', label: 'VLESS', count: 1 },
		],
		excludedProtocols: new Set<string>(),
		searchQuery: '',
		sortKey: null,
		sortAsc: true,
		onpick: vi.fn(),
		onremove: vi.fn(),
		ontoggle: vi.fn(),
		onexclude: vi.fn(),
		oncreatetunnel: vi.fn(),
		onSearchChange: vi.fn(),
		onToggleProtocol: vi.fn(),
		onSelectAllProtocols: vi.fn(),
		onClearAllProtocols: vi.fn(),
		onResetFilters: vi.fn(),
		onSortToggle: vi.fn(),
		onSortSelect: vi.fn(),
		onSortDirToggle: vi.fn(),
		...over,
	};
}

function rowLines(container: HTMLElement): HTMLElement[] {
	return Array.from(container.querySelectorAll('.member-list-line'));
}

describe('SubscriptionMemberList', () => {
	it('outside select-mode: clicking row text does not activate, clicking Activate button calls onpick(tag)', async () => {
		const onpick = vi.fn();
		const ontoggle = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ selectMode: false, onpick, ontoggle }),
		});

		await fireEvent.click(rowLines(container)[1]);
		expect(onpick).not.toHaveBeenCalled();
		expect(ontoggle).not.toHaveBeenCalled();

		const activateBtns = Array.from(container.querySelectorAll('.c-state button'));
		expect(activateBtns).toHaveLength(2);
		await fireEvent.click(activateBtns[1]); // bravo
		expect(onpick).toHaveBeenCalledTimes(1);
		expect(onpick).toHaveBeenCalledWith('tag-bravo');
	});

	it('in select-mode: clicking a row calls ontoggle(tag), not onpick', async () => {
		const onpick = vi.fn();
		const ontoggle = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ selectMode: true, onpick, ontoggle }),
		});
		await fireEvent.click(rowLines(container)[0]); // alpha
		expect(ontoggle).toHaveBeenCalledTimes(1);
		expect(ontoggle).toHaveBeenCalledWith('tag-alpha');
		expect(onpick).not.toHaveBeenCalled();
	});

	it('URL sub outside select-mode: renders +Tunnel left of Exclude and triggers callbacks', async () => {
		const onexclude = vi.fn();
		const onremove = vi.fn();
		const oncreatetunnel = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ isUrlSub: true, isInline: false, selectMode: false, onexclude, onremove, oncreatetunnel }),
		});
		const toTunnelBtns = Array.from(container.querySelectorAll('.to-tunnel-btn'));
		const exBtns = Array.from(container.querySelectorAll('.ex-btn'));
		expect(toTunnelBtns).toHaveLength(2);
		expect(exBtns).toHaveLength(2);
		expect(container.querySelectorAll('.member-remove-btn')).toHaveLength(0);

		await fireEvent.click(toTunnelBtns[0]);
		expect(oncreatetunnel).toHaveBeenCalledTimes(1);
		expect(oncreatetunnel).toHaveBeenCalledWith(members[0]);

		await fireEvent.click(exBtns[1]); // bravo
		expect(onexclude).toHaveBeenCalledTimes(1);
		expect(onexclude).toHaveBeenCalledWith('tag-bravo');
		expect(onremove).not.toHaveBeenCalled();
	});

	it('inline sub: shows Delete path (onremove) and +Tunnel, not Exclude', async () => {
		const onexclude = vi.fn();
		const onremove = vi.fn();
		const oncreatetunnel = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ isInline: true, isUrlSub: false, selectMode: false, onexclude, onremove, oncreatetunnel }),
		});
		const rmBtns = Array.from(container.querySelectorAll('.member-remove-btn'));
		expect(rmBtns).toHaveLength(2);
		expect(container.querySelectorAll('.to-tunnel-btn')).toHaveLength(2);
		expect(container.querySelectorAll('.ex-btn')).toHaveLength(0);

		await fireEvent.click(rmBtns[0]); // alpha
		expect(onremove).toHaveBeenCalledTimes(1);
		expect(onremove).toHaveBeenCalledWith(members[0]);
		expect(onexclude).not.toHaveBeenCalled();
	});

	it('card layout: body is selectable text div (no activation on click), card Activate and +Tunnel work', async () => {
		const onpick = vi.fn();
		const oncreatetunnel = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({
				layout: 'compact',
				effectiveActiveMember: 'tag-alpha',
				onpick,
				oncreatetunnel,
			}),
		});
		expect(container.querySelectorAll('div.card')).toHaveLength(2);
		expect(container.querySelectorAll('button.card')).toHaveLength(0);

		const activateBtns = Array.from(container.querySelectorAll('.card-slot-left button'));
		// Only inactive card gets Activate button
		expect(activateBtns).toHaveLength(1);
		await fireEvent.click(activateBtns[0]);
		expect(onpick).toHaveBeenCalledWith('tag-bravo');

		const toTunnelBtns = Array.from(container.querySelectorAll('.to-tunnel-btn'));
		expect(toTunnelBtns).toHaveLength(2);
		await fireEvent.click(toTunnelBtns[0]);
		expect(oncreatetunnel).toHaveBeenCalledWith(members[0]);
	});

	it('clicking table sort headers triggers onSortToggle with matching column key', async () => {
		const onSortToggle = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ layout: 'list', onSortToggle }),
		});
		const headers = Array.from(container.querySelectorAll('.sbx-member-list-row--head .sort-header-btn'));
		expect(headers).toHaveLength(6);

		await fireEvent.click(headers[0]); // Delay
		await fireEvent.click(headers[1]); // Server
		await fireEvent.click(headers[2]); // Protocol
		expect(onSortToggle).toHaveBeenNthCalledWith(1, 'delay');
		expect(onSortToggle).toHaveBeenNthCalledWith(2, 'server');
		expect(onSortToggle).toHaveBeenNthCalledWith(3, 'protocol');
	});

	it('protocol filter popover opens, toggles protocol checkboxes and Select/Clear all', async () => {
		const onToggleProtocol = vi.fn();
		const onSelectAllProtocols = vi.fn();
		const onClearAllProtocols = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({
				onToggleProtocol,
				onSelectAllProtocols,
				onClearAllProtocols,
			}),
		});

		const filterBtn = container.querySelector('.proto-filter > button') as HTMLButtonElement;
		expect(filterBtn).toBeTruthy();
		await fireEvent.click(filterBtn);

		const bulkBtns = Array.from(container.querySelectorAll('.proto-actions button'));
		expect(bulkBtns).toHaveLength(2);
		await fireEvent.click(bulkBtns[0]);
		await fireEvent.click(bulkBtns[1]);
		expect(onSelectAllProtocols).toHaveBeenCalledTimes(1);
		expect(onClearAllProtocols).toHaveBeenCalledTimes(1);

		const checkboxes = Array.from(container.querySelectorAll('.proto-check'));
		expect(checkboxes).toHaveLength(2);
		await fireEvent.click(checkboxes[0]);
		expect(onToggleProtocol).toHaveBeenCalledWith('trojan');
	});

	it('search input updates onSearchChange and Reset filters works on empty results', async () => {
		const onSearchChange = vi.fn();
		const onResetFilters = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({
				members: [],
				totalCount: 2,
				searchQuery: 'missing',
				onSearchChange,
				onResetFilters,
			}),
		});

		const searchInput = container.querySelector('.member-search-box input') as HTMLInputElement;
		expect(searchInput).toBeTruthy();
		await fireEvent.input(searchInput, { target: { value: 'vless' } });
		expect(onSearchChange).toHaveBeenCalledWith('vless');

		const resetBtn = container.querySelector('.filter-empty button') as HTMLButtonElement;
		expect(resetBtn).toBeTruthy();
		await fireEvent.click(resetBtn);
		expect(onResetFilters).toHaveBeenCalledTimes(1);
	});
});
