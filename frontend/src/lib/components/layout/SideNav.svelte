<script lang="ts">
	import { page } from '$app/stores';
	import { NAV_TREE, activeItem } from '$lib/data/navigation';
	import { navGroups } from '$lib/stores/navGroups';
	import SideNavGroup from './SideNavGroup.svelte';
	import SideNavItem from './SideNavItem.svelte';
	import BrandLogoMark from './BrandLogoMark.svelte';

	interface Props {
		currentVersion?: string;
		versionPending?: boolean;
		hasUpdate?: boolean;
		isPreRelease?: boolean;
		onNavigate?: () => void;
	}

	let {
		currentVersion = '',
		versionPending = false,
		hasUpdate = false,
		isPreRelease = false,
		onNavigate,
	}: Props = $props();

	const activeId = $derived(activeItem($page.url)?.item.id ?? null);
</script>

<div class="sidenav">
	<a href="/" class="brand" aria-label="AWG Manager" onclick={onNavigate}>
		<BrandLogoMark />
		<span class="wordmark">AWG⋅Manager</span>
	</a>

	<nav class="tree" aria-label="Главная навигация">
		{#each NAV_TREE as entry (entry.id)}
			{#if entry.kind === 'group'}
				<SideNavGroup
					group={entry}
					open={$navGroups[entry.id]}
					{activeId}
					onToggle={() => navGroups.toggle(entry.id)}
					{onNavigate}
				/>
			{:else}
				<SideNavItem
					item={entry}
					icon={entry.icon}
					active={entry.id === activeId}
					{onNavigate}
				/>
			{/if}
		{/each}
	</nav>

	<div class="footer">
		{#if currentVersion}
			<a
				href="/settings"
				class="version-badge"
				class:version-update={hasUpdate}
				class:version-prerelease={isPreRelease}
			>
				v{currentVersion}{hasUpdate ? ' ↑' : ''}
			</a>
		{:else if versionPending}
			<span class="version-badge version-pending" aria-busy="true">···</span>
		{/if}
	</div>
</div>

<style>
	.sidenav {
		display: flex;
		flex-direction: column;
		height: 100%;
		background: var(--color-bg-secondary);
		border-right: 1px solid var(--color-border);
		overflow: hidden;
	}

	.brand {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.875rem 0.875rem 0.75rem;
		flex: none;
		color: var(--color-text-primary);
		text-decoration: none;
	}

	.wordmark {
		font-family: var(--font-mono);
		font-weight: 700;
		font-size: 13px;
		letter-spacing: -0.02em;
		text-transform: uppercase;
	}

	.tree {
		flex: 1;
		overflow-y: auto;
		padding: 0.25rem 0.5rem 0.5rem;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.footer {
		flex: none;
		border-top: 1px solid var(--color-border);
		padding: 0.625rem 0.875rem;
		min-height: 38px;
		display: flex;
		align-items: center;
	}

	.version-badge {
		font-family: var(--font-mono, monospace);
		font-size: 10px;
		font-weight: 600;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		text-decoration: none;
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.version-prerelease {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.version-update {
		animation: badge-pulse 4s ease-in-out infinite;
	}

	.version-pending {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	@keyframes badge-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.5;
		}
	}
</style>
