<script lang="ts">
	import type { NavGroup } from '$lib/data/navigation';
	import SideNavItem from './SideNavItem.svelte';
	import { ChevronDown, ChevronRight } from 'lucide-svelte';

	interface Props {
		group: NavGroup;
		open: boolean;
		activeId: string | null;
		onToggle: () => void;
		onNavigate?: () => void;
	}

	let { group, open, activeId, onToggle, onNavigate }: Props = $props();
	const Icon = $derived(group.icon);
</script>

<button type="button" class="group-header" aria-expanded={open} onclick={onToggle}>
	<Icon size={15} aria-hidden="true" />
	<span class="group-label">{group.label}</span>
	{#if open}
		<ChevronDown size={14} aria-hidden="true" />
	{:else}
		<ChevronRight size={14} aria-hidden="true" />
	{/if}
</button>

{#if open}
	<div class="group-items">
		{#each group.items as item (item.id)}
			<SideNavItem {item} active={item.id === activeId} indent {onNavigate} />
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
		border: none;
		border-radius: var(--radius-sm);
		background: transparent;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		cursor: pointer;
		user-select: none;
		transition: color var(--t-fast) ease;
	}

	.group-header:hover {
		color: var(--color-text-secondary);
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
</style>
