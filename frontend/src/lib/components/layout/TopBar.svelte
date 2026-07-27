<script lang="ts">
	import { page } from '$app/stores';
	import { breadcrumbFor } from '$lib/data/navigation';
	import NotificationCenter from './NotificationCenter.svelte';
	import { IconButton } from '$lib/components/ui';
	import type { ThemeState } from '$lib/stores/theme';
	import { Sun, Moon, Heart, LogOut, Terminal, Menu, ChevronRight } from 'lucide-svelte';

	interface Props {
		authDisabled?: boolean;
		username?: string | null;
		theme: ThemeState;
		mobileNavOpen?: boolean;
		onToggleThemeMode: () => void;
		onLogout: () => void;
		onOpenDonate: () => void;
		onToggleMobileNav: () => void;
	}

	let {
		authDisabled = false,
		username = null,
		theme,
		mobileNavOpen = false,
		onToggleThemeMode,
		onLogout,
		onOpenDonate,
		onToggleMobileNav,
	}: Props = $props();

	const crumb = $derived(breadcrumbFor($page.url));
	/** Для Neo вторая ветка визуально тёмная, но mode остаётся dark ради color-scheme. */
	const themeDisplayMode = $derived(theme.preset === 'neo' ? theme.legacyMode : theme.mode);
</script>

<header class="topbar">
	<button
		type="button"
		class="burger"
		onclick={onToggleMobileNav}
		aria-label="Меню"
		aria-expanded={mobileNavOpen}
		aria-controls="app-sidenav"
	>
		<Menu size={16} aria-hidden="true" />
	</button>

	{#if crumb}
		<span class="crumbs">
			{#if crumb.group}
				<span class="crumb-group">{crumb.group}</span>
				<ChevronRight size={13} aria-hidden="true" class="crumb-sep" />
			{/if}
			<span class="crumb-label">{crumb.label}</span>
		</span>
	{/if}

	<div class="spacer"></div>

	{#if username && !authDisabled}
		<span class="user-chip">{username}</span>
	{/if}

	<NotificationCenter authenticated={true} />

	<IconButton ariaLabel="Терминал" href="/terminal">
		<Terminal size={16} aria-hidden="true" />
	</IconButton>

	{#if theme.preset !== 'custom'}
		<IconButton ariaLabel="Переключить тему" onclick={onToggleThemeMode}>
			{#if themeDisplayMode === 'dark'}
				<Sun size={16} aria-hidden="true" />
			{:else}
				<Moon size={16} aria-hidden="true" />
			{/if}
		</IconButton>
	{/if}

	<IconButton variant="warm" ariaLabel="Поддержать проект" onclick={onOpenDonate}>
		<Heart size={16} aria-hidden="true" />
	</IconButton>

	{#if !authDisabled}
		<IconButton variant="danger" ariaLabel="Выйти" onclick={onLogout}>
			<LogOut size={16} aria-hidden="true" />
		</IconButton>
	{/if}
</header>

<style>
	.topbar {
		height: 53px;
		flex: none;
		display: flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0 1.125rem;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.crumbs {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.crumb-group {
		font-size: 13px;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.crumbs :global(.crumb-sep) {
		color: var(--color-text-muted);
		flex: none;
	}

	.crumb-label {
		font-size: 13px;
		font-weight: 500;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.spacer {
		flex: 1;
	}

	.user-chip {
		font-size: 12px;
		color: var(--color-text-muted);
		padding: 0.25rem 0.625rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		white-space: nowrap;
	}

	.burger {
		display: none;
		width: 28px;
		height: 28px;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.burger:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	@media (max-width: 900px) {
		.burger {
			display: inline-flex;
		}

		.crumb-group,
		.crumbs :global(.crumb-sep) {
			display: none;
		}

		.user-chip {
			display: none;
		}
	}
</style>
