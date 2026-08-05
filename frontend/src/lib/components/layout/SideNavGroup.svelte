<script lang="ts">
	import { groupItems, isSeparator, type NavGroup } from '$lib/data/navigation';
	import type { NavBadgeValues } from '$lib/stores/navBadges';
	import SideNavItem from './SideNavItem.svelte';
	import { ChevronDown, ChevronRight } from 'lucide-svelte';

	interface Props {
		group: NavGroup;
		/** Раскрыта = в группе текущий маршрут (см. SideNav). */
		open: boolean;
		activeId: string | null;
		/** Куда ведёт клик по заголовку — последний открытый пункт группы. */
		href: string;
		/** Живые значения бейджей по источникам (`stores/navBadges.ts`). */
		badges?: NavBadgeValues;
		onPick: (itemId: string) => void;
		onNavigate?: () => void;
	}

	let { group, open, activeId, href, badges = {}, onPick, onNavigate }: Props = $props();
	const Icon = $derived(group.icon);
	const inGroup = $derived(groupItems(group).some((i) => i.id === activeId));
</script>

<a
	{href}
	class="group-header"
	class:in-group={inGroup}
	aria-expanded={open}
	onclick={() => onNavigate?.()}
>
	<span class="group-icon"><Icon size={15} aria-hidden="true" /></span>
	<span class="group-label">{group.label}</span>
	{#if open}
		<ChevronDown size={14} aria-hidden="true" />
	{:else}
		<ChevronRight size={14} aria-hidden="true" />
	{/if}
</a>

{#if open}
	<div class="group-items">
		{#each group.items as entry (entry.id)}
			{#if isSeparator(entry)}
				<!-- Тихая подпись, а не пункт: без ссылки, без роли и вне фокусной
				     цепочки — кликать в ней нечего. -->
				<span class="group-separator">{entry.label}</span>
			{:else}
				<SideNavItem
					item={entry}
					active={entry.id === activeId}
					indent
					badge={entry.badge ? (badges[entry.badge] ?? null) : null}
					onNavigate={() => {
						onPick(entry.id);
						onNavigate?.();
					}}
				/>
			{/if}
		{/each}
	</div>
{/if}

<style>
	.group-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		padding: 0.5rem;
		border-radius: var(--radius-sm);
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		text-decoration: none;
		cursor: pointer;
		user-select: none;
		transition: color var(--t-fast) ease;
	}

	.group-header:hover {
		color: var(--color-text-secondary);
		background: var(--color-bg-hover);
	}

	/* Активен потомок → у предка красится ТОЛЬКО иконка. Фон — признак
	   конечного пункта, и он должен быть в дереве ровно один. */
	.group-header.in-group .group-icon {
		color: var(--color-accent);
	}

	.group-icon {
		display: flex;
		flex: 0 0 15px;
		align-items: center;
		justify-content: center;
		color: var(--color-text-muted);
	}

	.group-label {
		flex: 1;
		text-align: left;
	}

	.group-items {
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding-bottom: 0.375rem;
	}

	.group-separator {
		/* Отступ слева тот же, что у пунктов (.nav-item.indent), — подпись стоит
		   в одной колонке с тем, что подписывает. */
		padding: 0.5rem 0.5rem 0.125rem 2rem;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-muted);
		opacity: 0.7;
		user-select: none;
	}
</style>
