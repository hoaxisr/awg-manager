<script lang="ts">
	import type { TunnelListItem } from '$lib/types';
	import { formatRelativeTime, formatDuration, secondsSince } from '$lib/utils/format';

	interface Props {
		tunnel: TunnelListItem;
	}

	let { tunnel }: Props = $props();

	let showEndpoint = $state(false);

	// Parse server info - split host and port
	let serverHost = $derived.by(() => {
		const endpoint = tunnel.endpoint ?? '';
		const match = endpoint.match(/^(?:\[([^\]]+)\]|([^:]+)):(\d+)$/);
		if (match) return match[1] || match[2] || endpoint;
		return endpoint;
	});

	let serverPort = $derived.by(() => {
		const endpoint = tunnel.endpoint ?? '';
		const match = endpoint.match(/:(\d+)$/);
		return match ? match[1] : '';
	});

	// Parse IP addresses - separate IPv4 and IPv6
	let addresses = $derived.by(() => {
		const addr = tunnel.address ?? '';
		const parts = addr.split(',').map(s => s.trim());
		const ipv4 = parts.find(p => !p.includes(':')) || '';
		const ipv6 = parts.find(p => p.includes(':')) || '';
		return { ipv4, ipv6 };
	});

	// Format IPv6 for display - abbreviate if too long
	let ipv6Display = $derived.by(() => {
		const full = addresses.ipv6;
		if (!full || full.length <= 20) return full;
		return full.slice(0, 12) + '...' + full.slice(-8);
	});
</script>

<div class="details">
	<div class="flex gap-4 items-start">
		<div class="flex flex-col gap-0.5 min-w-0 flex-1">
			<span class="detail-label">Сервер</span>
			<span class="flex items-center gap-1 min-w-0">
				<span class="detail-value truncate cursor-help" title={showEndpoint ? serverHost : ''}>{showEndpoint ? (serverHost || '—') : '•••••••••'}</span>
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
		<div class="flex flex-col gap-0.5 min-w-0 shrink-0">
			<span class="detail-label">Порт</span>
			<span class="detail-value">{serverPort || '—'}</span>
		</div>
	</div>

	{#if tunnel.backend !== 'kernel' && (tunnel.resolvedIspInterface || tunnel.ispInterface)}
		{@const iface = tunnel.resolvedIspInterface || tunnel.ispInterface}
		{@const label = tunnel.resolvedIspInterfaceLabel || tunnel.ispInterfaceLabel || ''}
		<div class="flex gap-4 items-start">
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">Подключение</span>
				<span class="detail-value">
					{#if label}
						{label}
						<span class="detail-secondary font-mono">({iface})</span>
					{:else}
						<span class="font-mono">{iface}</span>
					{/if}
				</span>
			</div>
		</div>
	{/if}

	<div class="flex gap-4 items-start">
		<div class="flex flex-col gap-0.5 min-w-0">
			<span class="detail-label">IPv4</span>
			<span class="detail-value">{addresses.ipv4 || '—'}</span>
		</div>
		{#if addresses.ipv6}
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="detail-label">IPv6</span>
				<span class="detail-value cursor-help" title={addresses.ipv6}>{ipv6Display}</span>
			</div>
		{/if}
	</div>

	{#if tunnel.status === 'running'}
		<hr class="divider" />
		<div class="flex items-start stats-row">
			<div class="flex flex-col gap-0.5 min-w-0 flex-1">
				<span class="detail-label">Uptime</span>
				<span class="detail-value text-[11px] whitespace-nowrap">
					{tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
				</span>
			</div>
			<div class="flex flex-col gap-0.5 min-w-0 flex-1 items-end">
				<span class="detail-label">Handshake</span>
				<span class="detail-value text-[11px] whitespace-nowrap" title={tunnel.lastHandshake || ''}>
					{tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
				</span>
			</div>
		</div>
	{/if}
</div>

<style>
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

	.detail-secondary {
		color: var(--text-muted);
	}

	.divider {
		border: none;
		border-top: 1px dashed var(--border);
		margin: 4px 0;
	}

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

	.stats-row {
		white-space: nowrap;
	}
</style>
