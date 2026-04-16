<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { Snippet } from 'svelte';
	import { page } from '$app/stores';
	import { theme } from '$lib/stores/theme';
	import { auth, isAuthenticated, isLoading } from '$lib/stores/auth';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { connectSSE } from '$lib/api/events';
	import { geoDownloadProgress } from '$lib/stores/geoDownload';
	import { serverOnline } from '$lib/stores/events';
	import { tunnels } from '$lib/stores/tunnels';
	import { logEntries } from '$lib/stores/logs';
	import { pingCheckStatus } from '$lib/stores/pingcheck';
	import { servers } from '$lib/stores/servers';
	import { routing } from '$lib/stores/routing';
	import { systemInfo } from '$lib/stores/system';
	import { feedTraffic } from '$lib/stores/traffic';
	import { singbox } from '$lib/stores/singbox';
	import type { UpdateInfo } from '$lib/types';
	import LoginForm from '$lib/components/LoginForm.svelte';
	import { Modal } from '$lib/components/ui';
	import '../app.css';

	let { children }: { children: Snippet } = $props();

	let mobileMenuOpen = $state(false);
	let donateModalOpen = $state(false);
	let booting = $state(false);

	function closeMobileMenu() {
		mobileMenuOpen = false;
	}

	let backendOffline = $derived(!$serverOnline);

	let updateInfo = $state<UpdateInfo | null>(null);
	const currentVersion = $derived(updateInfo?.currentVersion ?? '');
	const isPreRelease = $derived(
		currentVersion.includes('-rc') ||
		currentVersion.includes('-beta') ||
		currentVersion.includes('-alpha') ||
		currentVersion.includes('-dev')
	);
	const hasUpdate = $derived(updateInfo?.available ?? false);

	let disconnectSSE: (() => void) | null = null;

	let knownInstanceId = '';

	function startSSE() {
		if (disconnectSSE) return;
		singbox.loadStatus();
		singbox.loadTunnels();
		disconnectSSE = connectSSE({
			// System events
			onSystemReady: (data) => {
				serverOnline.set(true);
				booting = false;
				// Detect backend restart — force full page reload to pick up new JS
				if (knownInstanceId && data.instanceId && knownInstanceId !== data.instanceId) {
					location.reload();
					return;
				}
				knownInstanceId = data.instanceId;
			},
			onSystemBooting: () => {
				serverOnline.set(true);
				booting = true;
			},
			onConnected: () => {
				serverOnline.set(true);
			},
			onDisconnected: () => serverOnline.set(false),

			// Snapshot events
			onSnapshotSystem: (data) => systemInfo.setSnapshot(data),
			onSnapshotTunnels: (data) => {
				tunnels.setSnapshot(data);
				// Feed traffic store for system tunnels (they don't get tunnel:traffic events)
				for (const st of data.system ?? []) {
					if (st.status === 'up' && st.peer) {
						feedTraffic(st.id, st.peer.rxBytes, st.peer.txBytes);
					}
				}
			},
			onSnapshotServers: (data) => servers.setSnapshot(data),
			onSnapshotRouting: (data) => routing.setSnapshot(data),
			onSnapshotPingcheck: (data) => pingCheckStatus.setSnapshot(data),
			onSnapshotLogs: (data) => logEntries.setSnapshot(data),

			// Tunnel incremental
			onTunnelState: (data) => tunnels.updateTunnelState(data.id, data.state),
			onTunnelTraffic: (data) => {
				tunnels.updateTraffic(data);
				feedTraffic(data.id, data.rxBytes, data.txBytes);
			},
			onTunnelConnectivity: (data) => tunnels.updateConnectivity(data.id, data.connected, data.latency),
			onTunnelCreated: () => {},
			onTunnelDeleted: (data) => tunnels.removeFromList(data.id),
			onTunnelUpdated: () => {},
			onTunnelsList: (data) => tunnels.setManagedList(data),

			// Server incremental
			onServerUpdated: (data) => servers.updateAll(data),

			// Routing incremental
			onRoutingDnsUpdated: (data) => routing.setDnsRoutes(data),
			onRoutingStaticUpdated: (data) => routing.setStaticRoutes(data),
			onRoutingPoliciesUpdated: (data) => routing.setPolicies(data),
			onRoutingPolicyDevicesUpdated: (data) => routing.setPolicyDevices(data),
			onRoutingPolicyInterfacesUpdated: (data) => routing.setPolicyInterfaces(data),
			onRoutingClientRoutesUpdated: (data) => routing.setClientRoutes(data),
			onRoutingTunnelsUpdated: (data) => routing.setRoutingTunnels(data),
			onDnsRouteFailover: (data) => {
				if (data.action === 'switched') {
					notifications.warning(`DNS-маршрут "${data.listName}" переключён: ${data.fromTunnel || '—'} → ${data.toTunnel || 'нет резерва'}`);
				} else if (data.action === 'restored') {
					notifications.success(`DNS-маршрут "${data.listName}" восстановлен: → ${data.toTunnel || '—'}`);
				} else if (data.action === 'error') {
					notifications.error(`Ошибка переключения DNS-маршрута "${data.listName}": ${data.error || 'неизвестная ошибка'}`);
				}
			},

			// Logs & pingcheck incremental
			onLogEntry: (data) => logEntries.append(data),
			onPingCheckState: (data) => pingCheckStatus.updateStatus(data),
			onPingCheckLog: (data) => pingCheckStatus.appendLog(data),

			// Sing-box
			onSingboxStatus: singbox.applyStatus,
			onSingboxTunnel: singbox.applyTunnelEvent,
			onSingboxTraffic: singbox.applyTraffic,
			onSingboxDelay: (data) => singbox.applyDelay(data.tag, data.delay),

			// HydraRoute geo download progress
			onHydraRouteGeoProgress: (data) => geoDownloadProgress.ingest(data),
		});
	}

	function stopSSE() {
		if (disconnectSSE) {
			disconnectSSE();
			disconnectSSE = null;
		}
	}

	// SSE starts/stops reactively based on auth state
	$effect(() => {
		if ($isAuthenticated) {
			startSSE();
		} else {
			stopSSE();
		}
	});

	// Fetch update info when authenticated
	$effect(() => {
		if ($isAuthenticated) {
			api.checkUpdate().then(info => updateInfo = info).catch(() => null);
		} else {
			updateInfo = null;
		}
	});

	onMount(async () => {
		theme.init();
		await auth.checkStatus();
	});

	onDestroy(() => {
		stopSSE();
	});
</script>

{#if backendOffline}
	<div class="offline-screen">
		<svg class="offline-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
			<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
			<line x1="12" y1="9" x2="12" y2="13"/>
			<line x1="12" y1="17" x2="12.01" y2="17"/>
		</svg>
		<h2 class="offline-title">Сервер недоступен</h2>
		<p class="offline-status">Не удалось подключиться к AWG Manager</p>
		<div class="offline-spinner"></div>
		<p class="offline-hint">Переподключение...</p>
	</div>
{:else if booting}
	<div class="loading-screen">
		<div class="loading-spinner"></div>
		<p style="color: var(--text-muted); font-size: 0.875rem; margin-top: 1rem;">Роутер загружается...</p>
	</div>
{:else if $isLoading}
	<div class="loading-screen">
		<div class="loading-spinner"></div>
	</div>
{:else}
	<header class="header">
		<div class="header-content">
			<div class="logo-group">
				<a href="/" class="logo" onclick={closeMobileMenu}>
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
					</svg>
					<span class="logo-text">AWG Manager</span>
				</a>
				{#if currentVersion}
					{#if hasUpdate && $isAuthenticated}
						<a href="/settings" class="version-badge version-clickable" class:version-update-stable={!isPreRelease} class:version-update-prerelease={isPreRelease}>
							v{currentVersion} ↑
						</a>
					{:else}
						<span class="version-badge" class:version-stable={!isPreRelease} class:version-prerelease={isPreRelease}>
							v{currentVersion}
						</span>
					{/if}
				{/if}
			</div>

			{#if $isAuthenticated}
				<nav class="nav">
					<a href="/" class="nav-link" class:active={$page.url.pathname === '/' || $page.url.pathname.startsWith('/tunnels')}>Туннели</a>
					<a href="/servers" class="nav-link" class:active={$page.url.pathname.startsWith('/servers')}>Серверы</a>
					<a href="/routing" class="nav-link" class:active={$page.url.pathname.startsWith('/routing')}>Маршрутизация</a>
					<a href="/pingcheck" class="nav-link" class:active={$page.url.pathname.startsWith('/pingcheck')}>Мониторинг</a>
					<a href="/diagnostics" class="nav-link" class:active={$page.url.pathname.startsWith('/diagnostics') || $page.url.pathname.startsWith('/connections') || $page.url.pathname.startsWith('/logs')}>Диагностика</a>
					<a href="/settings" class="nav-link" class:active={$page.url.pathname.startsWith('/settings')}>Настройки</a>
				</nav>
			{:else}
				<div></div>
			{/if}

			<div class="header-actions">
				{#if $isAuthenticated && !$auth.authDisabled}
					<span class="user-info">{$auth.login}</span>
				{/if}

				{#if $isAuthenticated}
				<a href="/terminal" class="btn btn-icon btn-header-icon" title="Терминал">
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
						<polyline points="4 17 10 11 4 5" />
						<line x1="12" y1="19" x2="20" y2="19" />
					</svg>
				</a>
			{/if}

			<button class="btn btn-icon btn-header-icon" onclick={() => theme.toggle()} title="Переключить тему">
					{#if $theme === 'dark'}
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
							<circle cx="12" cy="12" r="5"/>
							<line x1="12" y1="1" x2="12" y2="3"/>
							<line x1="12" y1="21" x2="12" y2="23"/>
							<line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/>
							<line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/>
							<line x1="1" y1="12" x2="3" y2="12"/>
							<line x1="21" y1="12" x2="23" y2="12"/>
							<line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/>
							<line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/>
						</svg>
					{:else}
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
							<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>
						</svg>
					{/if}
				</button>

				{#if $isAuthenticated}
					<button class="btn btn-icon btn-donate" onclick={() => donateModalOpen = true} title="Поддержать проект">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
							<path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"/>
						</svg>
					</button>
				{/if}

				{#if $isAuthenticated && !$auth.authDisabled}
					<button class="btn btn-icon btn-logout" onclick={() => auth.logout()} title="Выйти" aria-label="Выйти">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
							<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>
							<polyline points="16 17 21 12 16 7"/>
							<line x1="21" y1="12" x2="9" y2="12"/>
						</svg>
					</button>
				{/if}

				{#if $isAuthenticated}
					<button
						class="btn btn-icon btn-hamburger"
						onclick={() => mobileMenuOpen = !mobileMenuOpen}
						title="Меню"
						aria-label="Меню"
						aria-expanded={mobileMenuOpen}
					>
						{#if mobileMenuOpen}
							<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
								<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
							</svg>
						{:else}
							<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
								<line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/>
							</svg>
						{/if}
					</button>
				{/if}
			</div>
		</div>

		{#if mobileMenuOpen && $isAuthenticated}
			<button type="button" class="mobile-backdrop" onclick={closeMobileMenu} aria-label="Закрыть меню"></button>
			<nav class="mobile-nav">
				<a href="/" class="mobile-nav-link" class:active={$page.url.pathname === '/'} onclick={closeMobileMenu}>Туннели</a>
				<a href="/servers" class="mobile-nav-link" class:active={$page.url.pathname.startsWith('/servers')} onclick={closeMobileMenu}>Серверы</a>
				<a href="/routing" class="mobile-nav-link" class:active={$page.url.pathname.startsWith('/routing')} onclick={closeMobileMenu}>Маршрутизация</a>
				<a href="/pingcheck" class="mobile-nav-link" class:active={$page.url.pathname.startsWith('/pingcheck')} onclick={closeMobileMenu}>Мониторинг</a>
				<a href="/diagnostics" class="mobile-nav-link" class:active={$page.url.pathname.startsWith('/diagnostics') || $page.url.pathname.startsWith('/connections') || $page.url.pathname.startsWith('/logs')} onclick={closeMobileMenu}>Диагностика</a>
				<a href="/settings" class="mobile-nav-link" class:active={$page.url.pathname.startsWith('/settings')} onclick={closeMobileMenu}>Настройки</a>
			</nav>
		{/if}
	</header>

	{#if !$isAuthenticated}
		<LoginForm />
	{:else}
		<main class="main">
			{@render children()}
		</main>

		<div class="toast-container">
			{#if $notifications.length > 1}
				<button class="toast-dismiss-all" onclick={() => notifications.clearAll()}>
					Закрыть все ({$notifications.length})
				</button>
			{/if}
			{#each $notifications as notification (notification.id)}
				<button class="toast toast-{notification.type}" onclick={() => notifications.remove(notification.id)}>
					{notification.message}
				</button>
			{/each}
		</div>

	{/if}

	<Modal bind:open={donateModalOpen} title="Поддержать проект" size="sm" onclose={() => donateModalOpen = false}>
		<div class="donate-wallets">
			<div class="donate-wallet">
				<span class="donate-wallet-label">USDT / ETH</span>
				<code class="donate-wallet-addr">0x7eae43b82157f2e4ea233eddf5d9ce19a1064f04</code>
			</div>
			<div class="donate-wallet">
				<span class="donate-wallet-label">USDT ERC20</span>
				<code class="donate-wallet-addr">0x35eC46d51f06DAf2DDbfA2a1b9B28a360643fEa8</code>
			</div>
			<div class="donate-wallet">
				<span class="donate-wallet-label">USDT / TRC20</span>
				<code class="donate-wallet-addr">TEpJh2p9j3fp6MigyqGvq1gC5D3CsxBeJw</code>
			</div>
			<div class="donate-wallet">
				<span class="donate-wallet-label">Boosty</span>
				<a class="donate-wallet-link" href="https://boosty.to/awgm_hoaxisr/donate" target="_blank" rel="noopener">boosty.to/awgm_hoaxisr/donate</a>
			</div>
			<div class="donate-wallet">
				<span class="donate-wallet-label">ЮMoney</span>
				<a class="donate-wallet-link" href="https://yoomoney.ru/fundraise/1GF36UHR07L.260312" target="_blank" rel="noopener">yoomoney.ru/fundraise</a>
			</div>
		</div>
	</Modal>
{/if}

<style>
	.loading-screen {
		min-height: 100vh;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		background: var(--bg-primary);
	}

	.loading-spinner {
		width: 40px;
		height: 40px;
		border: 3px solid var(--border);
		border-top-color: var(--accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.header {
		background: var(--bg-secondary);
		border-bottom: 1px solid var(--border);
		height: 56px;
		position: sticky;
		top: 0;
		z-index: 100;
	}

	.header-content {
		max-width: 1120px;
		margin: 0 auto;
		padding: 0 1rem;
		height: 100%;
		display: grid;
		grid-template-columns: auto 1fr auto;
		align-items: center;
	}

	.logo-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.logo {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--text-primary);
		white-space: nowrap;
	}

	.logo svg {
		width: 24px;
		height: 24px;
		color: var(--accent);
	}

	.nav {
		display: flex;
		gap: 0.25rem;
		justify-content: center;
	}

	.nav-link {
		color: var(--text-secondary);
		padding: 0.375rem 0.625rem;
		border-radius: var(--radius-sm);
		font-size: 0.875rem;
		transition: all 0.15s ease;
	}

	.nav-link:hover {
		color: var(--text-primary);
		background: var(--bg-hover);
	}

	.nav-link.active {
		color: var(--accent);
		background: rgba(122, 162, 247, 0.1);
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		justify-self: end;
	}

	.user-info {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.main {
		flex: 1;
		width: 100%;
		max-width: 960px;
		margin: 0 auto;
		padding: 0 1rem;
	}

	.offline-screen {
		min-height: 100vh;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		background: var(--bg-primary);
		gap: 0.75rem;
	}

	.offline-icon {
		width: 48px;
		height: 48px;
		color: var(--warning, #f59e0b);
	}

	.offline-title {
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0;
	}

	.offline-status {
		color: var(--text-secondary);
		font-size: 0.875rem;
		margin: 0;
	}

	.offline-spinner {
		width: 32px;
		height: 32px;
		border: 3px solid var(--border);
		border-top-color: var(--warning, #f59e0b);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	.offline-hint {
		color: var(--text-tertiary);
		font-size: 0.8125rem;
		margin: 0;
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
		background: rgba(34, 197, 94, 0.15);
		color: var(--success, #22c55e);
	}

	.version-prerelease {
		background: rgba(245, 158, 11, 0.2);
		color: var(--warning, #f59e0b);
	}

	.version-update-stable {
		background: rgba(34, 197, 94, 0.15);
		color: var(--success, #22c55e);
		animation: badge-pulse 4s ease-in-out infinite;
	}

	.version-update-prerelease {
		background: rgba(245, 158, 11, 0.2);
		color: var(--warning, #f59e0b);
		animation: badge-pulse 4s ease-in-out infinite;
	}

	.version-clickable {
		cursor: pointer;
	}

	.version-clickable:hover {
		filter: brightness(1.2);
	}

	@keyframes badge-pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.5; }
	}

	/* Header icon buttons */
	.btn-header-icon,
	.btn-logout {
		width: 32px;
		height: 32px;
		border-radius: 6px;
		border: none;
		background: transparent;
		color: var(--text-muted);
		transition: all 0.15s ease;
	}

	.btn-header-icon:hover {
		background: var(--bg-hover);
		color: var(--accent);
	}

	.btn-donate {
		width: 32px;
		height: 32px;
		border-radius: 6px;
		border: none;
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.btn-donate:hover {
		background: rgba(226, 85, 85, 0.1);
		color: #e25555;
	}

	.btn-logout:hover {
		background: rgba(239, 68, 68, 0.1);
		color: var(--error);
	}

	/* Hamburger — hidden on desktop */
	.btn-hamburger {
		display: none;
		width: 32px;
		height: 32px;
		border-radius: 6px;
		border: none;
		background: transparent;
		color: var(--text-muted);
		transition: all 0.15s ease;
	}

	.btn-hamburger:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
	}

	/* Mobile nav dropdown */
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

	@media (max-width: 640px) {
		.header-content {
			grid-template-columns: 1fr auto;
		}

		.nav {
			display: none;
		}

		.logo-text {
			display: none;
		}

		.user-info {
			display: none;
		}

		.btn-hamburger {
			display: flex;
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
			background: var(--bg-secondary);
			border-bottom: 1px solid var(--border);
			padding: 0.5rem 0;
			z-index: 100;
			box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
		}

		.mobile-nav-link {
			padding: 0.75rem 1.25rem;
			color: var(--text-secondary);
			font-size: 0.9375rem;
			transition: all 0.15s;
		}

		.mobile-nav-link:hover {
			color: var(--text-primary);
			background: var(--bg-hover);
		}

		.mobile-nav-link.active {
			color: var(--accent);
			background: rgba(122, 162, 247, 0.1);
			border-left: 3px solid var(--accent);
		}
	}

	.donate-wallets {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.donate-wallet {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.donate-wallet-label {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.donate-wallet-addr {
		font-size: 0.8125rem;
		color: var(--text-primary);
		background: var(--bg-tertiary);
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius-sm);
		word-break: break-all;
		user-select: all;
	}

	.donate-wallet-link {
		font-size: 0.8125rem;
		color: var(--accent);
		background: var(--bg-tertiary);
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius-sm);
		text-decoration: none;
	}

	.donate-wallet-link:hover {
		text-decoration: underline;
	}
</style>
