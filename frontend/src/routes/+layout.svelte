<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { get } from 'svelte/store';
	import type { Snippet } from 'svelte';
	import { theme } from '$lib/stores/theme';
	import { auth, isAuthenticated, isLoading } from '$lib/stores/auth';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { connectSSE } from '$lib/api/events';
	import { geoDownloadProgress } from '$lib/stores/geoDownload';
	import { serverOnline } from '$lib/stores/events';
	import { healthMonitor } from '$lib/stores/health';
	import { tunnels } from '$lib/stores/tunnels';
	import { logEntries } from '$lib/stores/logs';
	import { monitoringStore } from '$lib/stores/monitoring';
	import { appendPingLog } from '$lib/stores/pingcheck';
	import { systemInfo } from '$lib/stores/system';
	import { feedTraffic } from '$lib/stores/traffic';
	import { applyTraffic as singboxApplyTraffic, applyDelay as singboxApplyDelay } from '$lib/stores/singbox';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { invalidateResource, invalidateAll } from '$lib/stores/storeRegistry';
	import { setDeviceProxyMissingTarget, clearDeviceProxyMissingTarget } from '$lib/stores/deviceproxy';
	import type { UpdateInfo } from '$lib/types';
	import LoginForm from '$lib/components/LoginForm.svelte';
	import { Modal } from '$lib/components/ui';
	import { AppHeader } from '$lib/components/layout';
	import '../app.css';

	let { children }: { children: Snippet } = $props();

	let mobileMenuOpen = $state(false);
	let donateModalOpen = $state(false);
	let booting = $state(false);

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
	let unsubSysInfo: (() => void) | null = null;

	let knownInstanceId = '';

	function startSSE() {
		if (disconnectSSE) return;
		// singboxStatus / singboxTunnels now poll automatically on subscribe;
		// no eager fetch needed here — components subscribe as they mount.
		disconnectSSE = connectSSE({
			onConnected: () => {
				// SSE may have been down for minutes. Clear connectivity side-channel
				// (it's stream-only, not included in the polling snapshot) and force a
				// fresh fetch of tunnel state to catch any drift during the outage.
				tunnels.clearConnectivity();
				tunnels.invalidate();

				// Log catch-up: fetch entries we missed during the outage so the
				// terminal feed has no gap. Use lastSeenTs - 1s for safe overlap
				// (appendMany dedupes by composite key).
				const lastTs = get(logEntries.lastSeenTs);
				if (lastTs > 0) {
					const sinceUnix = Math.floor((lastTs - 1000) / 1000);
					api.getLogs({ since: sinceUnix, limit: 1000 })
						.then((resp) => {
							if (resp.logs.length > 0) {
								logEntries.appendMany(resp.logs);
							}
						})
						.catch(() => {
							// silent — next SSE event will resume the stream
						});
				}
			},
			onDisconnected: () => {
				// Phase C: serverOnline.set() is gone (derived from healthMonitor);
				// nothing else needs to happen on disconnect — health monitor owns the overlay.
			},

			// System events
			onSystemReady: (data) => {
				// Phase C: serverOnline.set() is gone (derived from healthMonitor);
				// keep the booting/instanceId handling — still used by the UI.
				booting = false;
				// Detect backend restart — force full page reload to pick up new JS
				if (knownInstanceId && data.instanceId && knownInstanceId !== data.instanceId) {
					location.reload();
					return;
				}
				knownInstanceId = data.instanceId;
			},
			onSystemBooting: () => {
				// Phase C: serverOnline.set() is gone; booting flag still drives UI.
				booting = true;
			},

			// Tunnel streams (traffic + connectivity are not REST-pollable)
			onTunnelTraffic: (data) => {
				// data.id is the NDMS interface name (e.g. "Wireguard0") on OS 5.x —
				// updateTraffic resolves it to the awg-manager tunnel ID, which is
				// what the traffic store is keyed on.
				const resolvedId = tunnels.updateTraffic(data);
				if (resolvedId !== null) {
					feedTraffic(resolvedId, data.rxBytes, data.txBytes);
				}
			},
			// tunnel:connectivity event was deprecated together with the
			// per-tunnel polling loop in internal/connectivity. Card latency
			// now derives from the monitoring matrix snapshot via
			// applyMatrixSnapshot below; the manual recheck button still
			// flows through api.checkConnectivity → updateConnectivity.

			// Logs & ping-check streams
			onLogEntry: (data) => logEntries.append(data),
			onMonitoringMatrixUpdate: (data) => {
				monitoringStore.setSnapshot(data);
				tunnels.applyMatrixSnapshot(data);
			},
			onPingCheckLog: appendPingLog,

			// Sing-box streams — also feed the rate-history store so the
			// per-tunnel sparkline on the home card has data.
			onSingboxTraffic: (data) => {
				singboxApplyTraffic(data);
				for (const t of data) {
					feedTraffic(t.tag, t.download, t.upload);
				}
			},
			onSingboxDelay: (data) => singboxApplyDelay(data.tag, data.delay),

			// HydraRoute geo download progress
			onHydraRouteGeoProgress: (data) => geoDownloadProgress.ingest(data),

			// DNS-route failover — user-visible notification, not a state stream
			onDnsRouteFailover: (data) => {
				if (data.action === 'switched') {
					notifications.warning(`DNS-маршрут "${data.listName}" переключён: ${data.fromTunnel || '—'} → ${data.toTunnel || 'нет резерва'}`);
				} else if (data.action === 'restored') {
					notifications.success(`DNS-маршрут "${data.listName}" восстановлен: → ${data.toTunnel || '—'}`);
				} else if (data.action === 'error') {
					notifications.error(`Ошибка переключения DNS-маршрута "${data.listName}": ${data.error || 'неизвестная ошибка'}`);
				}
			},

			// Generic resource invalidation hint (state-sync redesign)
			onResourceInvalidated: (data) => {
				invalidateResource(data.resource);
				// A saved-through deviceproxy config clears the missing-target banner.
				// Backend publishes deviceproxy.config (not the bare "deviceproxy" key)
				// immediately after Reconcile disables the proxy on a missing target.
				if (data.resource === 'deviceproxy.config') {
					clearDeviceProxyMissingTarget();
				}
			},

			// Device-proxy: selected outbound was deleted — show a banner in the tab.
			onDeviceProxyMissingTarget: (data) => {
				setDeviceProxyMissingTarget(data.wasTag);
			},

			// Sing-box Router state streams (rules, rule-sets, outbounds, status)
			onSingboxRouterStatus: singboxRouter.applyStatus,
			onSingboxRouterRules: singboxRouter.applyRules,
			onSingboxRouterRuleSets: singboxRouter.applyRuleSets,
			onSingboxRouterOutbounds: singboxRouter.applyOutbounds,
		});
	}

	function stopSSE() {
		if (disconnectSSE) {
			disconnectSSE();
			disconnectSSE = null;
		}
	}

	// SSE starts/stops reactively based on auth state.
	// systemInfo is a cold-tier polling store used by the header version badge
	// and several pages — subscribe globally for the authenticated session so
	// it's ready on any route without every page re-subscribing.
	$effect(() => {
		if ($isAuthenticated) {
			healthMonitor.start();
			startSSE();
			if (!unsubSysInfo) {
				unsubSysInfo = systemInfo.subscribe(() => {});
			}
		} else {
			healthMonitor.stop();
			stopSSE();
			unsubSysInfo?.();
			unsubSysInfo = null;
		}
	});

	let wasOffline = $state(false);
	$effect(() => {
		if (!$serverOnline) {
			wasOffline = true;
		} else if (wasOffline) {
			// Backend recovered from Tier 3 outage — pull fresh state for
			// every active polling store. Inactive stores auto-refetch on
			// their next subscribe via invalidate()'s mark-stale branch.
			invalidateAll();
			wasOffline = false;
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
		healthMonitor.stop();
		stopSSE();
		unsubSysInfo?.();
		unsubSysInfo = null;
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
	<AppHeader
		authenticated={$isAuthenticated}
		authDisabled={$auth.authDisabled}
		username={$auth.login}
		theme={$theme}
		{currentVersion}
		{hasUpdate}
		{isPreRelease}
		bind:mobileMenuOpen
		onToggleTheme={() => theme.toggle()}
		onLogout={() => auth.logout()}
		onOpenDonate={() => (donateModalOpen = true)}
	/>

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

	.main {
		flex: 1;
		width: 100%;
		display: flex;
		flex-direction: column;
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
