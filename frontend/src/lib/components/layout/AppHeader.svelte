<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { LegacyTabs, LegacyTab, IconButton, SaveStatusIndicator } from '$lib/components/ui';
	import { usageLevel } from '$lib/stores/settings';
	import { isSectionVisible, type Section } from '$lib/types/usageLevel';

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
			section: 'monitoring',
			href: '/monitoring',
			label: 'МОНИТОРИНГ',
			matches: (p) =>
				p.startsWith('/monitoring') ||
				p.startsWith('/pingcheck') ||
				p.startsWith('/connections'),
		},
		{
			section: 'diagnostics',
			href: '/diagnostics',
			label: 'ДИАГНОСТИКА',
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
			МОНИТОРИНГ: 'Мониторинг',
			ДИАГНОСТИКА: 'Диагностика',
			НАСТРОЙКИ: 'Настройки',
		};
		return map[upperLabel] ?? upperLabel;
	}
</script>

<header class="app-header" class:unauthenticated={!authenticated}>
	<div class="header-inner">
		<div class="brand-group">
			<a href="/" class="brand" aria-label="AWG Manager" onclick={closeMobileMenu}>
				<!-- <img src="/favicon.svg" alt="" class="logo"/> -->
				<svg class="logo" viewBox="0 0 550 550" aria-hidden="true">
					<g transform="matrix(1.2090726,0,0,1.2090726,-57.773414,-56.207997)">
						<path
							fill="currentColor"
							d="m 314.13069,342.61231 c -1.55,0.3125 -1.55078,1.93868 -1.30078,5.13867 0.3,5.39999 0.002,5.09922 8.10156,8.19922 2,0.7 4.49844,2.89922 5.89844,5.19922 1.6,2.59999 3.20078,3.90117 4.30078,3.70117 1.4,-0.3 1.69922,-1.60002 1.69922,-8.5 h 0.10156 v -8.20117 l -8.20117,-2.79883 c -5.94998,-2.05 -9.04961,-3.05078 -10.59961,-2.73828 z"
						/>
						<path
							fill="currentColor"
							d="m 247.03108,277.04981 c -5.29998,0 -9.70117,0.40078 -9.70117,0.80078 0,1.8 8.00117,13.19882 10.70117,15.29883 2.6,1.99999 4.00002,2.30077 15.5,2.30077 6.99998,0 12.69922,-0.20078 12.69922,-0.30077 v 0 c 0.10096,-0.71602 -4.49883,-4.39923 -9.79883,-9.19923 l -9.80078,-8.90039 z"
						/>
						<path
							fill="currentColor"
							stroke="currentColor"
							stroke-width="4.223"
							stroke-linecap="square"
							d="m 275.32991,63.250975 53.40039,19.39844 c 76.89984,27.899945 111.19961,40.500395 115.59961,42.400395 l 3.80078,1.59961 v 13.10156 c 0,19.29996 -1.79961,60.69926 -3.59961,81.69922 -5.89998,69.19986 -21.20123,122.19968 -45.70117,158.59961 -16.19997,23.79995 -39.39889,47.90121 -65.79883,67.70117 -19.89996,14.79997 -54.7,37.2 -58,37 -0.7,0 -7.80119,-4.0004 -15.70117,-8.90039 v -0.0996 c -22.19996,-13.89998 -35.29846,-22.90042 -48.89844,-33.9004 -14.49996,-11.79998 -34.40119,-31.00002 -43.70117,-42.5 l -6.40039,-8.09961 1.20117,-6.20117 c 0.7,-3.4 2.00039,-9.29962 2.90039,-13.09961 l 1.5,-6.90039 9.69922,-6.29883 9.69922,-6.40039 5.30078,2.90039 c 20.69996,11.39997 93,47.69922 95,47.69922 1.8,-0.1 27.39963,-7.30079 34.59961,-9.80078 0.9,-0.3 3.40078,1.00156 5.80078,3.10156 2.3,2 4.59961,3.69961 5.09961,3.59961 0.4,0 6.80002,-8.39963 14,-18.59961 l 13.19922,-18.60156 -0.59961,-15.79883 -0.59961,-15.80078 7.80078,-8.79883 c 25.99996,-29.59994 41.29845,-63.30047 47.39844,-104.40039 1.9,-13.39998 3.90078,-43.09961 2.80078,-43.09961 -0.3,0 -2.40039,3.10001 -4.40039,7 -10.09998,18.29996 -21.29924,36.39924 -27.69922,44.69922 -7.99998,10.29998 -21.30041,23.19962 -29.90039,29.09961 -8.29998,5.59998 -24.69922,13.70117 -24.69922,12.20117 0,-0.7 0.7,-4.60001 1.5,-8.5 1.4,-6.99998 2.29844,-42.5 0.89844,-42.5 -0.3,0 -6.19924,4.30001 -13.19922,9.5 -14.59997,10.89998 -28.80078,18.69883 -32.30078,17.79883 -3.7,-0.9 -15.19844,-10.69883 -16.39844,-13.79883 -0.6,-1.5 -3.60078,-11.50002 -6.80078,-22 l -5.59961,-19.20117 5.09961,-5.40039 c 2.8,-3 5.99922,-7.19844 7.19922,-9.39844 2.8,-5.29999 5.30078,-18.10158 5.30078,-26.10156 0,-8.49999 -2.09922,-24.69883 -3.19922,-24.29883 -1,0.4 -13.20041,9.79963 -26.40039,20.59961 -5.29998,4.29999 -10.00078,7.90039 -10.30078,7.90039 -0.3,0 -1.20039,-2.90079 -1.90039,-6.30078 -1.4,-7.09999 -7.79883,-21.10041 -11.79883,-25.90039 l -2.5,-3.09961 -4.90039,2.59961 c -6.19998,3.39999 -20.3,16.50157 -23.5,22.10156 -1.4,2.5 -3.19922,4.5 -3.69922,4.5 -0.6,0 -11.20002,-4.60119 -23.5,-10.20117 l -22.7595,-5.53077 -17.94167,4.53077 c -10.09998,5.09998 -18.60039,9.20117 -18.90039,9.20117 -0.3,0 -0.5,-4.0004 -0.5,-8.90039 v -9 l 6.40039,-2.40039 c 3.5,-1.3 42.40008,-15.50004 86.5,-31.500005 z"
						/>
					</g>
				</svg>
				<span class="wordmark">AWG⋅Manager</span>
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

			{#if authenticated && isSectionVisible($usageLevel, 'terminal')}
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
		width: 40px;
		height: 40px;
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
		.app-header.unauthenticated .user-tools {
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
