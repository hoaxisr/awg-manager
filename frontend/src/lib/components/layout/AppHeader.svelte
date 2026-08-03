<script lang="ts">
	import { page } from '$app/stores';
	import { IconButton } from '$lib/components/ui';
	import BrandLogoMark from './BrandLogoMark.svelte';
	import NotificationCenter from './NotificationCenter.svelte';
	import { usageLevel } from '$lib/stores/settings';
	import type { ThemeState } from '$lib/stores/theme';
	import { isAppearanceSettingsVisible, isSectionVisible } from '$lib/types/usageLevel';
	import { handleVersionBadgeClick } from '$lib/utils/versionBadgeEasterEgg';
	import { Sun, Moon, Heart, LogOut, X, Menu, Terminal } from 'lucide-svelte';

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

	function closeMobileMenu() {
		mobileMenuOpen = false;
	}

	function toggleMobileMenu() {
		mobileMenuOpen = !mobileMenuOpen;
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
		width: 100%;
		padding: 0 var(--header-gutter-x);
		height: 56px;
		display: flex;
		justify-content: space-between;
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

	.user-tools {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		flex-shrink: 0;
		overflow: visible;
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

	@media (max-width: 1050px) {
		.hamburger {
			display: inline-flex;
		}

		.brand-group {
			min-width: 0;
		}

		.app-header.unauthenticated .wordmark {
			display: none;
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
