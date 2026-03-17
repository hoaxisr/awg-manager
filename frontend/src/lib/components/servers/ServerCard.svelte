<script lang="ts">
	import type { WireguardServer, WireguardServerConfig, ASCParams } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { formatBytes } from '$lib/utils/format';
	import { TrafficChart } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic } from '$lib/stores/traffic';
	import { PeerTable, ConfGeneratorModal } from '$lib/components/servers';

	interface Props {
		server: WireguardServer;
		isBuiltIn: boolean;
		wanIP: string;
		onUnmark?: (id: string) => void;
	}

	let { server, isBuiltIn, wanIP, onUnmark }: Props = $props();

	let confModalOpen = $state(false);
	let confPeerKey = $state('');
	let serverConfig = $state<WireguardServerConfig | null>(null);
	let ascParams = $state<ASCParams | null>(null);
	let loadingConfig = $state(false);

	// Computed stats
	let onlineCount = $derived((server.peers ?? []).filter(p => p.online && p.enabled).length);
	let totalPeers = $derived((server.peers ?? []).length);
	let totalRx = $derived((server.peers ?? []).reduce((sum, p) => sum + p.rxBytes, 0));
	let totalTx = $derived((server.peers ?? []).reduce((sum, p) => sum + p.txBytes, 0));

	// Traffic chart
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);

	$effect(() => {
		const id = server.id;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	async function openConfModal(publicKey: string) {
		confPeerKey = publicKey;
		loadingConfig = true;
		try {
			const [config, asc] = await Promise.all([
				api.getServerConfig(server.id),
				api.getASCParams(server.id).catch(() => null)
			]);
			serverConfig = config;
			ascParams = asc;
			confModalOpen = true;
		} catch (e) {
			notifications.error('Не удалось загрузить конфигурацию');
		} finally {
			loadingConfig = false;
		}
	}

	let confPeer = $derived(
		serverConfig?.peers.find(p => p.publicKey === confPeerKey) ?? null
	);
</script>

<div class="card server-card" class:status-up={server.status === 'up'} class:status-down={server.status !== 'up'}>
	<!-- Header -->
	<div class="server-header">
		<div class="server-info">
			<div class="flex items-center gap-2">
				<h3 class="server-name">{server.description || server.id}</h3>
				{#if isBuiltIn}
					<span class="badge badge-builtin">Встроенный</span>
				{/if}
			</div>
			<div class="server-meta">
				<span class="meta-item mono">{server.interfaceName}</span>
				<span class="meta-item mono">{server.address}/{server.mask === '255.255.255.0' ? '24' : server.mask}</span>
				<span class="meta-item mono">:{server.listenPort}</span>
			</div>
		</div>
		<div class="server-status">
			<span class="led" class:led-up={server.status === 'up'} class:led-down={server.status !== 'up'}></span>
			<span class="peer-count">{onlineCount}/{totalPeers}</span>
		</div>
	</div>

	<!-- Stats -->
	<div class="server-stats">
		<div class="stat">
			<span class="stat-label">RX</span>
			<span class="stat-value">{formatBytes(totalRx)}</span>
		</div>
		<div class="stat">
			<span class="stat-label">TX</span>
			<span class="stat-value">{formatBytes(totalTx)}</span>
		</div>
		<div class="stat">
			<span class="stat-label">Пиры</span>
			<span class="stat-value">{onlineCount} онлайн</span>
		</div>
	</div>

	<!-- Peer table -->
	{#if (server.peers ?? []).length > 0}
		<PeerTable
			peers={server.peers}
			onDownloadConf={isBuiltIn ? undefined : openConfModal}
		/>
	{/if}

	<!-- Actions -->
	{#if !isBuiltIn && onUnmark}
		<div class="server-actions">
			<button class="btn btn-ghost btn-sm" onclick={() => onUnmark?.(server.id)}>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<polyline points="15,3 21,3 21,9"/>
					<polyline points="9,21 3,21 3,15"/>
					<line x1="21" y1="3" x2="14" y2="10"/>
					<line x1="3" y1="21" x2="10" y2="14"/>
				</svg>
				Вернуть в туннели
			</button>
		</div>
	{/if}

	<!-- Traffic chart (not for built-in server — read-only) -->
	{#if !isBuiltIn && server.status === 'up' && rxRates.length >= 2}
		<div class="chart-wrap">
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={totalRx}
				txTotal={totalTx}
				height={100}
			/>
		</div>
	{/if}
</div>

{#if confModalOpen && serverConfig && confPeer}
	<ConfGeneratorModal
		bind:open={confModalOpen}
		{serverConfig}
		peer={confPeer}
		{ascParams}
		{wanIP}
		onclose={() => { confModalOpen = false; }}
	/>
{/if}

<style>
	.server-card {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		transition: border-color 0.2s;
	}

	.status-up {
		border-color: var(--success);
	}

	.status-down {
		border-color: var(--text-muted, #6b7280);
	}

	.server-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
	}

	.server-info {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		min-width: 0;
	}

	.server-name {
		font-size: 1.125rem;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.server-meta {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.meta-item {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.mono {
		font-family: var(--font-mono, monospace);
	}

	.badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
	}

	.badge-builtin {
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
	}

	.server-status {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		transition: background 0.3s ease, box-shadow 0.3s ease;
	}

	.led-up {
		background: var(--success, #10b981);
		box-shadow: 0 0 6px var(--success, #10b981);
	}

	.led-down {
		background: var(--text-muted, #6b7280);
	}

	.peer-count {
		font-size: 0.875rem;
		font-weight: 500;
		font-variant-numeric: tabular-nums;
		color: var(--text-secondary);
	}

	.server-stats {
		display: flex;
		gap: 1.5rem;
		padding: 0.5rem 0;
		border-top: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.stat-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}

	.stat-value {
		font-size: 0.8125rem;
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
	}

	.server-actions {
		display: flex;
		gap: 0.5rem;
	}

	.chart-wrap {
		margin: 0 -1rem -1rem;
		padding: 8px 12px 4px;
		overflow: hidden;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-secondary, rgba(0,0,0,0.15));
	}
</style>
