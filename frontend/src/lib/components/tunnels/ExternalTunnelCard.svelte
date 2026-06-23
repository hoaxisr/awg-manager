<script lang="ts">
	import { ShieldCheck } from 'lucide-svelte';
	import type { ExternalTunnel } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { Button } from '$lib/components/ui';
	import TunnelTitleRow from '$lib/components/tunnels/TunnelTitleRow.svelte';

	interface Props {
		tunnel: ExternalTunnel;
		view?: 'cards' | 'compact' | 'list';
		onadopt?: (interfaceName: string) => void;
	}

	let { tunnel, view = 'cards', onadopt }: Props = $props();

	let isListCard = $derived(view === 'list');
	let statusDot = $derived(
		tunnel.lastHandshake
			? { variant: 'success' as const, pulse: false, label: 'Подключён' }
			: { variant: 'muted' as const, pulse: false, label: 'Неактивен' },
	);

	function handleAdopt(): void {
		onadopt?.(tunnel.interfaceName);
	}
</script>

<div
	class="card ext-card flex flex-col gap-4"
	class:view-compact={view === 'compact'}
	class:view-list={isListCard}
>
	{#if isListCard}
		<div class="header header-dense">
			<div class="header-dense-body">
				<TunnelTitleRow
					title={tunnel.interfaceName}
					dotVariant={statusDot.variant}
					dotPulse={statusDot.pulse}
					dotLabel={statusDot.label}
					dense
				/>
				<div class="meta-tags-dense">
					<span class="iface-chip-dense">WG туннель</span>
					<span class="version-badge badge-external">Внешний</span>
				</div>
			</div>
		</div>
		<div class="actions">
			<Button variant="primary" onclick={handleAdopt}>
				{#snippet iconBefore()}
					<ShieldCheck size={16} aria-hidden="true" />
				{/snippet}
				Взять под управление
			</Button>
		</div>
	{:else}
		<div class="header flex justify-between items-start gap-3">
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
					<ShieldCheck size={16} aria-hidden="true" />
				{/snippet}
				Взять под управление
			</Button>
		</div>
	{/if}
</div>

<style>
	.ext-card {
		border: 1px dashed color-mix(in srgb, var(--warning, #f59e0b) 40%, transparent);
	}

	.ext-card.view-compact {
		gap: 8px;
		padding: 10px 12px;
	}

	.ext-card.view-list .actions {
		display: flex;
		width: 100%;
	}

	.ext-card.view-list .actions :global(.btn) {
		width: 100%;
		justify-content: center;
	}

	.header.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}

	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		margin-top: 4px;
		align-items: center;
		gap: 3px;
		min-width: 0;
	}

	.iface-chip-dense {
		display: inline-block;
		min-width: 0;
		font-size: 9px;
		font-weight: 500;
		font-family: var(--font-mono, monospace);
		line-height: 1.3;
		padding: 1px 5px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--text-muted);
	}

	.tunnel-name {
		font-size: 1rem;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ext-card.view-compact .tunnel-name {
		font-size: 0.95rem;
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

	.ext-card.view-compact .details {
		gap: 10px;
		padding-top: 10px;
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

	@media (max-width: 720px) {
		.actions-wrapper :global(.btn) {
			width: 100%;
		}
	}
</style>
