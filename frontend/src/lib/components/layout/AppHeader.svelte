<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { LegacyTabs, LegacyTab, IconButton } from '$lib/components/ui';
	import BrandLogoMark from './BrandLogoMark.svelte';
	import NotificationCenter from './NotificationCenter.svelte';
	import { usageLevel } from '$lib/stores/settings';
	import type { ThemeState } from '$lib/stores/theme';
	import { isAppearanceSettingsVisible, isSectionVisible, type Section } from '$lib/types/usageLevel';
	import { handleVersionBadgeClick } from '$lib/utils/versionBadgeEasterEgg';
	import { Sun, Moon, Heart, LogOut, X, Menu, Terminal, ChevronRight } from 'lucide-svelte';

	type NavItem = {
		section: Section;
		href: string;
		label: string;
		matches: (path: string) => boolean;
	};

	const NAV_ITEMS: NavItem[] = [
		{
			section: 'tunnels',
			href: '/',
			label: 'ТУННЕЛИ',
			matches: (p) =>
				p === '/' || p.startsWith('/tunnels') || p.startsWith('/system-tunnels'),
		},
		{
			section: 'servers',
			href: '/servers',
			label: 'СЕРВЕРЫ',
			matches: (p) => p.startsWith('/servers'),
		},
		{
			section: 'routing',
			href: '/routing',
			label: 'МАРШРУТИЗАЦИЯ',
			matches: (p) => p.startsWith('/routing'),
		},
		{
			section: 'diagnostics',
			href: '/diagnostics',
			label: 'ИНСТРУМЕНТЫ',
			matches: (p) => p.startsWith('/diagnostics') || p.startsWith('/logs'),
		},
		{
			section: 'settings',
			href: '/settings',
			label: 'НАСТРОЙКИ',
			matches: (p) => p.startsWith('/settings'),
		},
	];

	interface Props {
		authenticated: boolean;
		authDisabled?: boolean;
		username?: string | null;
		theme?: ThemeState;
		currentVersion?: string;
		/** Первый запрос версии ещё идёт — показываем плейсхолдер вместо пустоты. */
		versionPending?: boolean;
		hasUpdate?: boolean;
		isPreRelease?: boolean;
		mobileMenuOpen?: boolean;
		onToggleThemeMode: () => void;
		onLogout: () => void;
		onOpenDonate: () => void;
	}

	let {
		authenticated,
		authDisabled = false,
		username = null,
		theme = {
			preset: 'legacy',
			modePreference: 'dark',
			mode: 'dark',
			legacyMode: 'dark',
			custom: {
				accent: '#8b5cf6',
				background: '#111827',
				text: '#f8fafc',
			},
			label: 'AWGM - Legacy',
			summary: '',
			supportsModeToggle: true,
		},
		currentVersion = '',
		versionPending = false,
		hasUpdate = false,
		isPreRelease = false,
		mobileMenuOpen = $bindable(false),
		onToggleThemeMode,
		onLogout,
		onOpenDonate,
	}: Props = $props();

	const visibleItems = $derived(
		NAV_ITEMS.filter((item) => isSectionVisible($usageLevel, item.section)),
	);

	const currentRoute = $derived.by(() => {
		const path = $page.url.pathname;
		return visibleItems.find((item) => item.matches(path))?.href ?? '';
	});

	function navigate(value: string) {
		if (value && value !== currentRoute) {
			goto(value);
		}
	}

	function closeMobileMenu() {
		mobileMenuOpen = false;
	}

	function toggleMobileMenu() {
		mobileMenuOpen = !mobileMenuOpen;
	}

	function prettyMobileLabel(upperLabel: string): string {
		const map: Record<string, string> = {
			ТУННЕЛИ: 'Туннели',
			СЕРВЕРЫ: 'Серверы',
			МАРШРУТИЗАЦИЯ: 'Маршрутизация',
			ИНСТРУМЕНТЫ: 'Инструменты',
			НАСТРОЙКИ: 'Настройки',
		};
		return map[upperLabel] ?? upperLabel;
	}

	/** Для Neo вторая ветка визуально тёмная, но `mode` остаётся dark ради color-scheme — в шапке показываем legacyMode */
	const themeDisplayMode = $derived(theme.preset === 'neo' ? theme.legacyMode : theme.mode);

	const onSettingsPage = $derived($page.url.pathname.startsWith('/settings'));
	const versionClickableOnSettings = $derived(
		onSettingsPage && ($usageLevel === 'expert' || hasUpdate),
	);

	function onVersionBadgeClick(event: MouseEvent) {
		if (!onSettingsPage) return;
		event.preventDefault();
		handleVersionBadgeClick({
			usageLevel: $usageLevel,
			hasUpdate,
			onSettingsPage: true,
		});
	}

	const themeButtonLabel = $derived.by(() => {
		const currentModeLabel = themeDisplayMode === 'light' ? 'светлая' : 'тёмная';
		const nextModeLabel = themeDisplayMode === 'light' ? 'тёмную' : 'светлую';
		if (theme.modePreference === 'system') {
			return `Переключить ${theme.label} с системной на ${nextModeLabel} тему. Сейчас ${currentModeLabel} (по системе).`;
		}
		return `Переключить ${theme.label} на ${nextModeLabel} тему. Сейчас ${currentModeLabel}.`;
	});
</script>

<header class="app-header" class:unauthenticated={!authenticated}>
	<div class="header-inner">
		<div class="brand-group">
			<a href="/" class="brand" aria-label="AWG Manager" onclick={closeMobileMenu}>
				<BrandLogoMark />
				<span class="wordmark">AWG⋅Manager</span>
			</a>

			{#if currentVersion || (versionPending && authenticated)}
				<span class="version-slot">
					{#if currentVersion}
						{#if hasUpdate && authenticated && !onSettingsPage}
							<a
								href="/settings"
								class="version-badge version-clickable"
								class:version-update-stable={!isPreRelease}
								class:version-update-prerelease={isPreRelease}
							>
								v{currentVersion} ↑
							</a>
						{:else if authenticated && versionClickableOnSettings}
							<button
								type="button"
								class="version-badge version-clickable"
								class:version-update-stable={hasUpdate && !isPreRelease}
								class:version-update-prerelease={hasUpdate && isPreRelease}
								class:version-stable={!hasUpdate && !isPreRelease}
								class:version-prerelease={!hasUpdate && isPreRelease}
								aria-label={hasUpdate ? 'Показать блок обновления AWGM' : 'Версия AWGM'}
								onclick={onVersionBadgeClick}
							>
								v{currentVersion}{hasUpdate ? ' ↑' : ''}
							</button>
						{:else}
							<span
								class="version-badge"
								class:version-stable={!isPreRelease}
								class:version-prerelease={isPreRelease}
							>
								v{currentVersion}
							</span>
						{/if}
					{:else}
						<span class="version-badge version-pending" aria-busy="true" title="Проверка версии…">
							<span class="version-pending-dots">···</span>
						</span>
					{/if}
				</span>
			{/if}
		</div>

		{#if authenticated}
			<nav class="nav" aria-label="Главная навигация">
				<LegacyTabs value={currentRoute} onChange={navigate} variant="underline">
					{#each visibleItems as item (item.section)}
						<LegacyTab value={item.href}>{item.label}</LegacyTab>
					{/each}
				</LegacyTabs>
			</nav>
		{:else}
			<div class="nav-spacer"></div>
		{/if}

		<div class="user-tools">
			{#if authenticated && !authDisabled && username}
				<span class="user-chip">{username}</span>
			{/if}

			<NotificationCenter {authenticated} />

			{#if authenticated && isSectionVisible($usageLevel, 'terminal')}
				<IconButton ariaLabel="Терминал" href="/terminal">
					<Terminal size={16} aria-hidden="true" />
				</IconButton>
			{/if}

			{#if isAppearanceSettingsVisible($usageLevel) && theme.preset !== 'custom'}
				<IconButton ariaLabel={themeButtonLabel} onclick={onToggleThemeMode}>
					{#if themeDisplayMode === 'dark'}
						<Sun size={16} aria-hidden="true" />
					{:else}
						<Moon size={16} aria-hidden="true" />
					{/if}
				</IconButton>
			{/if}

			{#if authenticated}
				<IconButton variant="warm" ariaLabel="Поддержать проект" onclick={onOpenDonate}>
					<Heart size={16} aria-hidden="true" />
				</IconButton>
			{/if}

			{#if authenticated && !authDisabled}
				<IconButton variant="danger" ariaLabel="Выйти" onclick={onLogout}>
					<LogOut size={16} aria-hidden="true" />
				</IconButton>
			{/if}

			{#if authenticated}
				<button
					type="button"
					class="hamburger"
					onclick={toggleMobileMenu}
					aria-label="Меню"
					aria-expanded={mobileMenuOpen}
				>
					{#if mobileMenuOpen}
						<X size={16} aria-hidden="true" />
					{:else}
						<Menu size={16} aria-hidden="true" />
					{/if}
				</button>
			{/if}
		</div>
	</div>

	{#if mobileMenuOpen && authenticated}
		<button
			type="button"
			class="mobile-backdrop"
			onclick={closeMobileMenu}
			aria-label="Закрыть меню"
		></button>
		<nav class="mobile-nav" aria-label="Мобильная навигация">
			{#each visibleItems as item (item.section)}
				<a
					href={item.href}
					class="mobile-nav-link"
					class:active={item.matches($page.url.pathname)}
					onclick={closeMobileMenu}>{prettyMobileLabel(item.label)}</a
				>
			{/each}
		</nav>
	{/if}
</header>

<style>
	.app-header {
		position: sticky;
		top: 0;
		z-index: var(--z-sticky-header);
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.header-inner {
		max-width: 1120px;
		margin: 0 auto;
		padding: 0 var(--header-gutter-x);
		height: 56px;
		display: grid;
		grid-template-columns: auto 1fr auto;
		align-items: center;
		gap: 1rem;
	}

	.brand-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.brand {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-text-primary);
		text-decoration: none;
		white-space: nowrap;
	}

	.wordmark {
		font-family: var(--font-mono);
		font-weight: 700;
		font-size: 14px;
		letter-spacing: -0.02em;
		text-transform: uppercase;
	}

	.nav {
		min-width: 0;
		display: flex;
		overflow-x: auto;
		overflow-y: hidden;
		overscroll-behavior-y: none;
		scrollbar-width: none;
	}

	.nav::-webkit-scrollbar {
		display: none;
	}

	/* Header-specific tweaks for the underline tabs */
	.nav :global(.tabs.variant-underline) {
		border-bottom: none;
		gap: 1.25rem;
		flex-shrink: 0;
		margin-left: auto;
		margin-right: auto;
	}

	.nav :global(.tab) {
		white-space: nowrap;
	}

	.nav-spacer {
		min-width: 0;
	}

	.user-tools {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		justify-self: end;
		flex-shrink: 0;
	}

	.user-chip {
		font-size: 12px;
		color: var(--color-text-muted);
		padding: 0.25rem 0.625rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		margin-right: 0.25rem;
		white-space: nowrap;
	}

	.version-slot {
		display: inline-flex;
		justify-content: flex-start;
		align-items: center;
		flex-shrink: 0;
		width: 10ch;
		min-width: 10ch;
		overflow: visible;
	}

	.version-badge {
		font-size: 9px;
		font-weight: 600;
		letter-spacing: 0.3px;
		padding: 2px 5px;
		border-radius: 6px;
		line-height: 1;
		text-decoration: none;
		white-space: nowrap;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		box-sizing: border-box;
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	button.version-badge {
		border: none;
		appearance: none;
	}

	.version-pending {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		letter-spacing: 0.12em;
	}

	.version-pending-dots {
		opacity: 0.55;
	}

	.version-stable {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.version-prerelease {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.version-update-stable {
		background: var(--color-success-tint);
		color: var(--color-success);
		animation: badge-pulse 4s ease-in-out infinite;
	}

	.version-update-prerelease {
		background: var(--color-warning-tint);
		color: var(--color-warning);
		animation: badge-pulse 4s ease-in-out infinite;
	}

	.version-clickable {
		cursor: pointer;
	}

	.version-clickable:hover {
		filter: brightness(1.2);
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

	/* Hamburger — hidden on desktop */
	.hamburger {
		display: none;
		width: 28px;
		height: 28px;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease;
	}

	.hamburger:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.hamburger:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.mobile-backdrop {
		display: none;
		border: none;
		padding: 0;
		cursor: pointer;
		-webkit-appearance: none;
		appearance: none;
	}

	.mobile-nav {
		display: none;
	}

	@media (max-width: 1050px) {
		.nav {
			display: none;
		}

		.nav-spacer {
			display: none;
		}

		.hamburger {
			display: inline-flex;
		}

		.header-inner {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.brand-group {
			min-width: 0;
		}

		.app-header.unauthenticated .wordmark {
			display: none;
		}

		.mobile-backdrop {
			display: block;
			position: fixed;
			inset: 56px 0 0 0;
			background: rgba(0, 0, 0, 0.4);
			z-index: var(--z-drawer-backdrop);
		}

		.mobile-nav {
			display: flex;
			flex-direction: column;
			position: absolute;
			top: 100%;
			left: 0;
			right: 0;
			background: var(--color-bg-secondary);
			border-bottom: 1px solid var(--color-border);
			padding: 0.5rem 0;
			z-index: var(--z-drawer);
			box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
		}

		.mobile-nav-link {
			padding: 0.75rem var(--header-gutter-x);
			border-left: 3px solid transparent;
			color: var(--color-text-secondary);
			font-size: 0.9375rem;
			text-decoration: none;
			transition:
				background var(--t-fast) ease,
				color var(--t-fast) ease,
				border-color var(--t-fast) ease;
		}

		.mobile-nav-link:hover {
			color: var(--color-text-primary);
			background: var(--color-bg-hover);
		}

		.mobile-nav-link.active {
			color: var(--color-accent);
			background: var(--color-accent-tint);
			border-left: 3px solid var(--color-accent);
		}
	}

	@media (max-width: 640px) {
		.wordmark {
			display: none;
		}

		.user-chip {
			display: none;
		}
	}
</style>
