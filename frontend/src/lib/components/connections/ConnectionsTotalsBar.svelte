<script lang="ts">
	import { RefreshCw } from 'lucide-svelte';
	import type { ConnectionStats } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { m } from '$lib/i18n';

	interface Props {
		stats: ConnectionStats | null;
		bytesOut: number;
		bytesIn: number;
		fetchedAt: string;
		loading: boolean;
		progress: number;
		onRefresh: () => void;
	}

	let { stats, bytesOut, bytesIn, fetchedAt, loading, progress, onRefresh }: Props = $props();

	const time = $derived(
		fetchedAt
			? new Date(fetchedAt).toLocaleTimeString('ru-RU', {
					hour: '2-digit',
					minute: '2-digit',
					second: '2-digit',
				})
			: '',
	);
</script>

<div class="totals">
	<span
		class="seg"
		title={m.connections_total_title()}
	>{m.connections_total_label()} <strong class="num">{stats?.total ?? 0}</strong> {m.connections_total_noun({ count: stats?.total ?? 0 })}</span>
	<span class="seg num bytes">↑ {formatBytes(bytesOut)} · ↓ {formatBytes(bytesIn)}</span>
	<span class="seg num protos">
		<span class="p-tcp">TCP {stats?.protocols.tcp ?? 0}</span> /
		<span class="p-udp">UDP {stats?.protocols.udp ?? 0}</span> /
		<span class="p-icmp">ICMP {stats?.protocols.icmp ?? 0}</span>
	</span>
	<span class="tail">
		<span class="live-dot" class:live-dot-loading={loading}></span>
		{#if time}<span class="num time">{time}</span>{/if}
		<button
			type="button"
			class="refresh-btn"
			onclick={onRefresh}
			disabled={loading}
			aria-label={m.connections_refresh_aria()}
			title={m.common_refresh()}
			style={`--refresh-progress:${progress * 360}deg;`}
		>
			<RefreshCw size={15} aria-hidden="true" style="position:relative;z-index:1" />
		</button>
	</span>
</div>

<style>
	.totals {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 20px;
		padding: 9px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font-size: 13px;
		margin-bottom: 12px;
	}
	.num {
		font-family: var(--font-mono);
		font-variant-numeric: tabular-nums;
	}
	.bytes,
	.protos {
		white-space: nowrap;
		font-size: 12px;
	}
	.p-tcp { color: var(--color-accent); }
	.p-udp { color: var(--color-warning); }
	.p-icmp { color: var(--color-info); }
	.tail {
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}
	.time {
		font-size: 11px;
		color: var(--color-text-muted);
	}
	.live-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		animation: totals-pulse 2s ease-in-out infinite;
	}
	.live-dot-loading {
		background: var(--color-warning);
		animation-duration: 1s;
	}
	@keyframes totals-pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.35; }
	}
	.refresh-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.refresh-btn::before {
		content: '';
		position: absolute;
		inset: -1px;
		border-radius: inherit;
		padding: 1px;
		background: conic-gradient(var(--color-accent) var(--refresh-progress), transparent 0deg);
		-webkit-mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		-webkit-mask-composite: xor;
		mask-composite: exclude;
		pointer-events: none;
		opacity: 0.95;
	}
	.refresh-btn:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}
	.refresh-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	@media (max-width: 640px) {
		.totals { gap: 10px 14px; }
		.tail { margin-left: 0; width: 100%; justify-content: flex-end; }
	}
</style>
