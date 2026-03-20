<script lang="ts">
	import { untrack } from 'svelte';
	import type { SystemTunnel, ConnectivityResult } from '$lib/types';
	import { api } from '$lib/api/client';
	import { formatRelativeTime } from '$lib/utils/format';
	import { TrafficChart } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic } from '$lib/stores/traffic';

	interface Props {
		tunnel: SystemTunnel;
		onHide?: (id: string) => void;
		onMarkServer?: (id: string) => void;
	}

	let { tunnel, onHide, onMarkServer }: Props = $props();

	let connectivity = $state<ConnectivityResult | null>(null);
	let checking = $state(false);
	let showEndpoint = $state(false);

	async function checkConnectivity() {
		if (tunnel.status !== 'up' || checking) return;
		checking = true;
		try {
			connectivity = await api.checkSystemTunnelConnectivity(tunnel.id);
		} catch {
			connectivity = null;
		} finally {
			checking = false;
		}
	}

	// Auto-check connectivity every 60s when up
	// Only track tunnel.status to avoid re-running on every poll update
	$effect(() => {
		const status = tunnel.status;
		if (status !== 'up') {
			connectivity = null;
			return;
		}
		untrack(() => checkConnectivity());
		const interval = setInterval(checkConnectivity, 60000);
		return () => clearInterval(interval);
	});

	// LED color
	const ledClass = $derived(
		tunnel.status !== 'up' ? 'led-gray' :
		tunnel.peer?.online ? 'led-green' : 'led-yellow'
	);

	// Traffic chart — live only (no server history for system tunnels)
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);

	$effect(() => {
		const id = tunnel.id;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});
</script>

<div class="card flex flex-col gap-4 transition-[border-color] duration-200" class:status-up={tunnel.status === 'up'} class:status-down={tunnel.status !== 'up'}>
	<!-- Header: name + badge + LED + connectivity -->
	<div class="flex justify-between items-start gap-3">
		<div class="flex flex-col gap-1 min-w-0">
			<h3 class="tunnel-name" title={tunnel.description || tunnel.id}>{tunnel.description || tunnel.id}</h3>
			<div class="flex items-center gap-2 flex-wrap">
				<span class="iface-name">{tunnel.interfaceName}</span>
				<span class="version-badge badge-system">Системный</span>
			</div>
		</div>
		<div class="flex flex-col items-end gap-1.5 shrink-0">
			<span class="led {ledClass}"></span>
			{#if tunnel.status === 'up'}
				<div class="flex items-center gap-1.5">
					{#if connectivity?.connected}
						<span class="latency-value">{connectivity.latency}ms</span>
					{/if}
					<button
						class="connectivity-btn"
						class:connected={connectivity?.connected}
						class:disconnected={connectivity !== null && !connectivity.connected}
						class:checking
						onclick={checkConnectivity}
						title={connectivity?.connected ? 'Связь OK' : connectivity !== null ? 'Нет связи' : 'Проверка связи...'}
					>
						{#if checking}
							<span class="connectivity-spinner"></span>
						{:else if connectivity?.connected}
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
								<path d="M5 12.55a11 11 0 0 1 14.08 0"/>
								<path d="M1.42 9a16 16 0 0 1 21.16 0"/>
								<path d="M8.53 16.11a6 6 0 0 1 6.95 0"/>
								<circle cx="12" cy="20" r="1" fill="currentColor"/>
							</svg>
						{:else}
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
								<line x1="2" y1="2" x2="22" y2="22"/>
								<path d="M8.5 16.5a5 5 0 0 1 7 0"/>
								<path d="M2 8.82a15 15 0 0 1 4.17-2.65"/>
								<path d="M10.66 5c4.01-.36 8.14.9 11.34 3.76"/>
							</svg>
						{/if}
					</button>
				</div>
			{/if}
		</div>
	</div>

	<!-- Details: endpoint + handshake -->
	<div class="details">
		{#if tunnel.peer?.endpoint}
			<div class="flex gap-4 items-start">
				<div class="flex flex-col gap-0.5 min-w-0 flex-1">
					<span class="detail-label">Endpoint</span>
					<span class="flex items-center gap-1 min-w-0">
						<span class="detail-value truncate" title={showEndpoint ? tunnel.peer.endpoint : ''}>{showEndpoint ? tunnel.peer.endpoint : '•••••••••'}</span>
						<button
							class="eye-btn"
							onclick={() => showEndpoint = !showEndpoint}
							title={showEndpoint ? 'Скрыть' : 'Показать'}
						>
							{#if showEndpoint}
								<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
							{:else}
								<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
							{/if}
						</button>
					</span>
				</div>
			</div>
		{/if}
		{#if tunnel.status === 'up' && tunnel.peer?.lastHandshake}
			<div class="flex items-start">
				<div class="flex flex-col gap-0.5 min-w-0 flex-1 items-end">
					<span class="detail-label">Handshake</span>
					<span class="detail-value text-[11px] whitespace-nowrap">{formatRelativeTime(tunnel.peer.lastHandshake)}</span>
				</div>
			</div>
		{/if}
	</div>

	<!-- Actions -->
	<div class="actions-wrapper">
		<div class="actions-row">
			<a href="/system-tunnels/{tunnel.id}" class="btn btn-ghost">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
					<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
				</svg>
				Изменить
			</a>

			<a href="/system-tunnels/{tunnel.id}/test" class="btn btn-ghost" title="Тестирование туннеля">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
					<polyline points="22,4 12,14.01 9,11.01"/>
				</svg>
				Тест
			</a>
		</div>

		{#if onMarkServer || onHide}
			<div class="actions-row">
				{#if onMarkServer}
					<button class="btn btn-ghost" title="Перенести в серверы" onclick={() => onMarkServer?.(tunnel.id)}>
						<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<rect x="2" y="2" width="20" height="8" rx="2" ry="2"/>
							<rect x="2" y="14" width="20" height="8" rx="2" ry="2"/>
							<line x1="6" y1="6" x2="6.01" y2="6"/>
							<line x1="6" y1="18" x2="6.01" y2="18"/>
						</svg>
						В серверы
					</button>
				{/if}

				{#if onHide}
					<button class="btn btn-ghost btn-hide" title="Скрыть туннель" onclick={() => onHide?.(tunnel.id)}>
						<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"/>
							<path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"/>
							<line x1="1" y1="1" x2="23" y2="23"/>
						</svg>
						Скрыть
					</button>
				{/if}
			</div>
		{/if}
	</div>

	<!-- Traffic chart (same as TunnelCard) -->
	{#if tunnel.status === 'up' && rxRates.length >= 2}
		<div class="chart-wrap">
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={tunnel.peer?.rxBytes ?? 0}
				txTotal={tunnel.peer?.txBytes ?? 0}
				height={100}
			/>
		</div>
	{/if}
</div>

<style>
	/* Match TunnelCard border states */
	.status-up {
		border-color: var(--success);
	}

	.status-down {
		border-color: var(--text-muted, #6b7280);
	}

	/* Tunnel name */
	.tunnel-name {
		font-size: 1rem;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.iface-name {
		font-size: 12px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted);
	}

	/* Badge */
	.version-badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.badge-system {
		background: rgba(148, 163, 184, 0.15);
	}

	/* LED indicator */
	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		transition: background 0.3s ease, box-shadow 0.3s ease;
	}

	.led-green {
		background: var(--success, #10b981);
		box-shadow: 0 0 6px var(--success, #10b981);
	}

	.led-yellow {
		background: var(--warning, #f59e0b);
		box-shadow: 0 0 6px var(--warning, #f59e0b);
	}

	.led-gray {
		background: var(--text-muted, #6b7280);
		box-shadow: none;
	}

	/* Latency */
	.latency-value {
		font-variant-numeric: tabular-nums;
		font-size: 13px;
		font-weight: 500;
		color: var(--success);
	}

	/* Connectivity button */
	.connectivity-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		border: none;
		border-radius: 6px;
		cursor: pointer;
		transition: all 0.2s ease;
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.connectivity-btn:hover {
		background: var(--border);
	}

	.connectivity-btn.connected {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success);
	}

	.connectivity-btn.disconnected {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}

	.connectivity-spinner {
		width: 10px;
		height: 10px;
		border: 2px solid currentColor;
		border-top-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	/* Eye toggle */
	.eye-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 2px;
		border: none;
		background: none;
		color: var(--text-muted);
		cursor: pointer;
		border-radius: 4px;
		flex-shrink: 0;
		transition: color 0.15s;
	}

	.eye-btn:hover {
		color: var(--text-secondary);
	}

	/* Details */
	.details {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.detail-label {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}

	.detail-value {
		font-size: 13px;
		font-family: var(--font-mono, monospace);
		color: var(--text-secondary);
	}

	/* Actions */
	.actions-wrapper {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding-top: 12px;
		border-top: 1px solid var(--border);
	}


	.btn-hide:hover {
		color: var(--error);
	}

	/* Traffic chart wrapper (same as TunnelCard) */
	.chart-wrap {
		margin: 0 -1rem -1rem;
		padding: 8px 12px 4px;
		overflow: hidden;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-secondary, rgba(0,0,0,0.15));
	}

	.actions-row {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		justify-content: center;
	}
</style>
