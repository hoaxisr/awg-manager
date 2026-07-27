<script lang="ts">
	import type { Snippet } from 'svelte';
	import { afterNavigate } from '$app/navigation';
	import type { ThemeState } from '$lib/stores/theme';
	import SideNav from './SideNav.svelte';
	import TopBar from './TopBar.svelte';

	interface Props {
		authDisabled?: boolean;
		username?: string | null;
		theme: ThemeState;
		currentVersion?: string;
		versionPending?: boolean;
		hasUpdate?: boolean;
		isPreRelease?: boolean;
		onToggleThemeMode: () => void;
		onLogout: () => void;
		onOpenDonate: () => void;
		children: Snippet;
	}

	let {
		authDisabled = false,
		username = null,
		theme,
		currentVersion = '',
		versionPending = false,
		hasUpdate = false,
		isPreRelease = false,
		onToggleThemeMode,
		onLogout,
		onOpenDonate,
		children,
	}: Props = $props();

	let mobileNavOpen = $state(false);

	// SvelteKit при навигации сбрасывает скролл ОКНА, а наш скролл живёт в
	// .shell-content — сбрасываем контейнер сами, иначе следующая страница
	// откроется с прокруткой предыдущей.
	let contentEl = $state<HTMLElement | null>(null);
	afterNavigate(() => contentEl?.scrollTo(0, 0));
</script>

<div class="shell">
	<aside id="app-sidenav" class="shell-aside" class:mobile-open={mobileNavOpen}>
		<SideNav
			{currentVersion}
			{versionPending}
			{hasUpdate}
			{isPreRelease}
			onNavigate={() => (mobileNavOpen = false)}
		/>
	</aside>

	{#if mobileNavOpen}
		<button
			type="button"
			class="mobile-backdrop"
			aria-label="Закрыть меню"
			onclick={() => (mobileNavOpen = false)}
		></button>
	{/if}

	<div class="shell-main">
		<TopBar
			{authDisabled}
			{username}
			{theme}
			{mobileNavOpen}
			{onToggleThemeMode}
			{onLogout}
			{onOpenDonate}
			onToggleMobileNav={() => (mobileNavOpen = !mobileNavOpen)}
		/>
		<main class="shell-content" bind:this={contentEl}>
			<div class="shell-content-inner">
				{@render children()}
			</div>
		</main>
	</div>
</div>

<style>
	.shell {
		display: grid;
		grid-template-columns: 250px 1fr;
		height: 100vh;
		height: 100dvh;
		background: var(--color-bg-primary);
	}

	.shell-aside {
		min-height: 0;
	}

	.shell-main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
	}

	.shell-content {
		flex: 1;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
	}

	.shell-content-inner {
		flex: 1;
		width: 100%;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}

	/* v2.8.2: колонка контента 960px, боковые поля 1rem (компактная ширина). */
	:global(html[data-layout-compact='true']) .shell-content-inner {
		max-width: 960px;
		margin-left: auto;
		margin-right: auto;
		padding: 0 1rem;
	}

	.mobile-backdrop {
		display: none;
		border: none;
		padding: 0;
		cursor: pointer;
		appearance: none;
	}

	@media (max-width: 900px) {
		.shell {
			grid-template-columns: 1fr;
		}

		.shell-aside {
			position: fixed;
			inset: 0 auto 0 0;
			width: 250px;
			z-index: var(--z-drawer);
			transform: translateX(-100%);
			transition: transform var(--t-med) ease;
		}

		.shell-aside.mobile-open {
			transform: translateX(0);
		}

		.mobile-backdrop {
			display: block;
			position: fixed;
			inset: 0;
			background: rgba(0, 0, 0, 0.4);
			z-index: var(--z-drawer-backdrop);
		}
	}
</style>
