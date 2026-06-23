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
		onpick: vi.fn(),
		onremove: vi.fn(),
		ontoggle: vi.fn(),
		onexclude: vi.fn(),
		...over,
	};
}

function rowLines(container: HTMLElement): HTMLElement[] {
	return Array.from(container.querySelectorAll('.member-list-line')) as HTMLElement[];
}

describe('SubscriptionMemberList', () => {
	it('outside select-mode: clicking a row calls onpick(tag), not ontoggle', async () => {
		const onpick = vi.fn();
		const ontoggle = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ selectMode: false, onpick, ontoggle }),
		});
		await fireEvent.click(rowLines(container)[1]); // bravo
		expect(onpick).toHaveBeenCalledTimes(1);
		expect(onpick).toHaveBeenCalledWith('tag-bravo');
		expect(ontoggle).not.toHaveBeenCalled();
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

	it('URL sub outside select-mode: per-row «Исключить» calls onexclude(tag)', async () => {
		const onexclude = vi.fn();
		const onremove = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ isUrlSub: true, isInline: false, selectMode: false, onexclude, onremove }),
		});
		const exBtns = Array.from(container.querySelectorAll('.ex-btn')) as HTMLButtonElement[];
		expect(exBtns).toHaveLength(2);
		// no remove buttons for a URL sub
		expect(container.querySelectorAll('.member-remove-btn')).toHaveLength(0);

		await fireEvent.click(exBtns[1]); // bravo
		expect(onexclude).toHaveBeenCalledTimes(1);
		expect(onexclude).toHaveBeenCalledWith('tag-bravo');
		expect(onremove).not.toHaveBeenCalled();
	});

	it('inline sub: shows «Удалить» path (onremove), not «Исключить»', async () => {
		const onexclude = vi.fn();
		const onremove = vi.fn();
		const { container } = render(SubscriptionMemberList, {
			props: baseProps({ isInline: true, isUrlSub: false, selectMode: false, onexclude, onremove }),
		});
		const rmBtns = Array.from(container.querySelectorAll('.member-remove-btn')) as HTMLButtonElement[];
		expect(rmBtns).toHaveLength(2);
		// no exclude buttons for an inline sub
		expect(container.querySelectorAll('.ex-btn')).toHaveLength(0);

		await fireEvent.click(rmBtns[0]); // alpha
		expect(onremove).toHaveBeenCalledTimes(1);
		expect(onremove).toHaveBeenCalledWith(members[0]);
		expect(onexclude).not.toHaveBeenCalled();
	});
});
