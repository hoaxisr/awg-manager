<!-- frontend/src/lib/components/ExternalTunnelCard.svelte -->
<script lang="ts">
	import type { ExternalTunnel } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { Button } from '$lib/components/ui';

	interface Props {
		tunnel: ExternalTunnel;
		onadopt?: (interfaceName: string) => void;
	}

	let { tunnel, onadopt }: Props = $props();

	function handleAdopt(): void {
		onadopt?.(tunnel.interfaceName);
	}
</script>

<div class="card ext-card flex flex-col gap-4">
	<div class="flex justify-between items-start gap-3">
		<div class="flex flex-col gap-1 min-w-0">
			<h3 class="tunnel-name">{tunnel.interfaceName}</h3>
			<div class="flex items-center gap-2 flex-wrap">
				<span class="iface-name">WG туннель</span>
				<span class="version-badge badge-external">Внешний</span>
			</div>
		</div>
		<div class="shrink-0">
			{#if tunnel.lastHandshake}
				<span class="status-badge status-active">
					<span class="led-dot"></span>
					Подключён
				</span>
			{:else}
				<span class="status-badge status-inactive">
					<span class="led-dot"></span>
					Неактивен
				</span>
			{/if}
		</div>
	</div>

	<div class="details">
		{#if tunnel.endpoint}
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">Endpoint</span>
				<span class="detail-value">{tunnel.endpoint}</span>
			</div>
		{/if}
		{#if tunnel.lastHandshake}
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">Handshake</span>
				<span class="detail-value">{tunnel.lastHandshake}</span>
			</div>
		{/if}
		<div class="flex gap-6">
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">RX</span>
				<span class="detail-value">{formatBytes(tunnel.rxBytes)}</span>
			</div>
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">TX</span>
				<span class="detail-value">{formatBytes(tunnel.txBytes)}</span>
			</div>
		</div>
	</div>

	<div class="actions-wrapper">
		<Button variant="primary" onclick={handleAdopt}>
			{#snippet iconBefore()}
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
					<polyline points="9 12 12 15 16 10"/>
				</svg>
			{/snippet}
			Взять под управление
		</Button>
	</div>
</div>

<style>
	.ext-card {
		border: 1px dashed color-mix(in srgb, var(--warning, #f59e0b) 40%, transparent);
	}

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

	.version-badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
	}

	.badge-external {
		background: rgba(245, 158, 11, 0.15);
		color: var(--warning, #f59e0b);
	}

	.status-badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 10px;
		font-size: 12px;
		font-weight: 500;
		border-radius: 10px;
	}

	.status-active {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success, #10b981);
	}

	.status-inactive {
		background: rgba(148, 163, 184, 0.15);
		color: var(--text-muted);
	}

	.led-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
		flex-shrink: 0;
	}

	.details {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding-top: 12px;
		border-top: 1px solid var(--border);
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

	.actions-wrapper {
		padding-top: 12px;
		border-top: 1px solid var(--border);
	}
</style>
