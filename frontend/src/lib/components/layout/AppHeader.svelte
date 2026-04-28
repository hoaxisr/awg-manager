<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { LegacyTabs, LegacyTab, IconButton, SaveStatusIndicator } from '$lib/components/ui';

	interface Props {
		authenticated: boolean;
		authDisabled?: boolean;
		username?: string | null;
		theme?: 'dark' | 'light';
		currentVersion?: string;
		hasUpdate?: boolean;
		isPreRelease?: boolean;
		mobileMenuOpen?: boolean;
		onToggleTheme: () => void;
		onLogout: () => void;
		onOpenDonate: () => void;
	}

	let {
		authenticated,
		authDisabled = false,
		username = null,
		theme = 'dark',
		currentVersion = '',
		hasUpdate = false,
		isPreRelease = false,
		mobileMenuOpen = $bindable(false),
		onToggleTheme,
		onLogout,
		onOpenDonate,
	}: Props = $props();

	const currentRoute = $derived.by(() => {
		const path = $page.url.pathname;
		if (path === '/' || path.startsWith('/tunnels') || path.startsWith('/system-tunnels')) return '/';
		if (path.startsWith('/servers')) return '/servers';
		if (path.startsWith('/routing')) return '/routing';
		if (
			path.startsWith('/monitoring') ||
			path.startsWith('/pingcheck') ||
			path.startsWith('/connections')
		)
			return '/monitoring';
		if (path.startsWith('/diagnostics') || path.startsWith('/logs')) return '/diagnostics';
		if (path.startsWith('/settings')) return '/settings';
		return '';
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
</script>

<header class="app-header">
	<div class="header-inner">
		<div class="brand-group">
			<a href="/" class="brand" aria-label="AWG Manager" onclick={closeMobileMenu}>
				<svg
					class="logo"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
				</svg>
				<span class="wordmark">AWG/MANAGER</span>
			</a>

			{#if currentVersion}
				{#if hasUpdate && authenticated}
					<a
						href="/settings"
						class="version-badge version-clickable"
						class:version-update-stable={!isPreRelease}
						class:version-update-prerelease={isPreRelease}
					>
						v{currentVersion} ↑
					</a>
				{:else}
					<span
						class="version-badge"
						class:version-stable={!isPreRelease}
						class:version-prerelease={isPreRelease}
					>
						v{currentVersion}
					</span>
				{/if}
			{/if}

			{#if authenticated}
				<SaveStatusIndicator />
			{/if}
		</div>

		{#if authenticated}
			<nav class="nav" aria-label="Главная навигация">
				<LegacyTabs value={currentRoute} onChange={navigate} variant="underline">
					<LegacyTab value="/">ТУННЕЛИ</LegacyTab>
					<LegacyTab value="/servers">СЕРВЕРЫ</LegacyTab>
					<LegacyTab value="/routing">МАРШРУТИЗАЦИЯ</LegacyTab>
					<LegacyTab value="/monitoring">МОНИТОРИНГ</LegacyTab>
					<LegacyTab value="/diagnostics">ДИАГНОСТИКА</LegacyTab>
					<LegacyTab value="/settings">НАСТРОЙКИ</LegacyTab>
				</LegacyTabs>
			</nav>
		{:else}
			<div class="nav-spacer"></div>
		{/if}

		<div class="user-tools">
			{#if authenticated && !authDisabled && username}
				<span class="user-chip">{username}</span>
			{/if}

			{#if authenticated}
				<IconButton ariaLabel="Терминал" href="/terminal">
					<svg
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<polyline points="4 17 10 11 4 5" />
						<line x1="12" y1="19" x2="20" y2="19" />
					</svg>
				</IconButton>
			{/if}

			<IconButton ariaLabel="Переключить тему" onclick={onToggleTheme}>
				{#if theme === 'dark'}
					<svg
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<circle cx="12" cy="12" r="5" />
						<line x1="12" y1="1" x2="12" y2="3" />
						<line x1="12" y1="21" x2="12" y2="23" />
						<line x1="4.22" y1="4.22" x2="5.64" y2="5.64" />
						<line x1="18.36" y1="18.36" x2="19.78" y2="19.78" />
						<line x1="1" y1="12" x2="3" y2="12" />
						<line x1="21" y1="12" x2="23" y2="12" />
						<line x1="4.22" y1="19.78" x2="5.64" y2="18.36" />
						<line x1="18.36" y1="5.64" x2="19.78" y2="4.22" />
					</svg>
				{:else}
					<svg
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
					</svg>
				{/if}
			</IconButton>

			{#if authenticated}
				<IconButton variant="warm" ariaLabel="Поддержать проект" onclick={onOpenDonate}>
					<svg
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path
							d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"
						/>
					</svg>
				</IconButton>
			{/if}

			{#if authenticated && !authDisabled}
				<IconButton variant="danger" ariaLabel="Выйти" onclick={onLogout}>
					<svg
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
						<polyline points="16 17 21 12 16 7" />
						<line x1="21" y1="12" x2="9" y2="12" />
					</svg>
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
						<svg
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
							aria-hidden="true"
						>
							<line x1="18" y1="6" x2="6" y2="18" />
							<line x1="6" y1="6" x2="18" y2="18" />
						</svg>
					{:else}
						<svg
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
							aria-hidden="true"
						>
							<line x1="3" y1="6" x2="21" y2="6" />
							<line x1="3" y1="12" x2="21" y2="12" />
							<line x1="3" y1="18" x2="21" y2="18" />
						</svg>
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
			<a
				href="/"
				class="mobile-nav-link"
				class:active={$page.url.pathname === '/' ||
					$page.url.pathname.startsWith('/tunnels') ||
					$page.url.pathname.startsWith('/system-tunnels')}
				onclick={closeMobileMenu}
			>
				Туннели
			</a>
			<a
				href="/servers"
				class="mobile-nav-link"
				class:active={$page.url.pathname.startsWith('/servers')}
				onclick={closeMobileMenu}
			>
				Серверы
			</a>
			<a
				href="/routing"
				class="mobile-nav-link"
				class:active={$page.url.pathname.startsWith('/routing')}
				onclick={closeMobileMenu}
			>
				Маршрутизация
			</a>
			<a
				href="/monitoring"
				class="mobile-nav-link"
				class:active={$page.url.pathname.startsWith('/monitoring') ||
					$page.url.pathname.startsWith('/pingcheck') ||
					$page.url.pathname.startsWith('/connections')}
				onclick={closeMobileMenu}
			>
				Мониторинг
			</a>
			<a
				href="/diagnostics"
				class="mobile-nav-link"
				class:active={$page.url.pathname.startsWith('/diagnostics') ||
					$page.url.pathname.startsWith('/logs')}
				onclick={closeMobileMenu}
			>
				Диагностика
			</a>
			<a
				href="/settings"
				class="mobile-nav-link"
				class:active={$page.url.pathname.startsWith('/settings')}
				onclick={closeMobileMenu}
			>
				Настройки
			</a>
		</nav>
	{/if}
</header>

<style>
	.app-header {
		position: sticky;
		top: 0;
		z-index: 100;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.header-inner {
		max-width: 1120px;
		margin: 0 auto;
		padding: 0 1rem;
		height: 56px;
		display: grid;
		grid-template-columns: auto 1fr auto;
		align-items: center;
		gap: 1.5rem;
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

	.logo {
		width: 22px;
		height: 22px;
		color: var(--color-accent);
		flex-shrink: 0;
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
		overflow-x: auto;
		scrollbar-width: none;
		justify-self: center;
	}
	.nav::-webkit-scrollbar {
		display: none;
	}

	/* Header-specific tweaks for the underline tabs */
	.nav :global(.tabs.variant-underline) {
		border-bottom: none;
		gap: 1.25rem;
	}

	.nav-spacer {
		min-width: 0;
	}

	.user-tools {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		justify-self: end;
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

	.version-badge {
		font-size: 9px;
		font-weight: 600;
		letter-spacing: 0.3px;
		padding: 2px 5px;
		border-radius: 6px;
		line-height: 1;
		text-decoration: none;
		white-space: nowrap;
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

	.hamburger > :global(svg) {
		width: 16px;
		height: 16px;
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

	@media (max-width: 768px) {
		.nav {
			display: none;
		}
	}

	@media (max-width: 640px) {
		.header-inner {
			grid-template-columns: 1fr auto;
		}

		.wordmark {
			display: none;
		}

		.user-chip {
			display: none;
		}

		.hamburger {
			display: inline-flex;
		}

		.mobile-backdrop {
			display: block;
			position: fixed;
			inset: 56px 0 0 0;
			background: rgba(0, 0, 0, 0.4);
			z-index: 99;
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
			z-index: 100;
			box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
		}

		.mobile-nav-link {
			padding: 0.75rem 1.25rem;
			color: var(--color-text-secondary);
			font-size: 0.9375rem;
			text-decoration: none;
			transition:
				background var(--t-fast) ease,
				color var(--t-fast) ease;
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
</style>
