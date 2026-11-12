<script lang="ts">
	import { Copy, Plus, Repeat } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button, IconButton } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { WarpHit } from '$lib/types/warpscout';
	import { countryFlag, latencyTone } from './warpscoutUi';

	interface Props {
		/** Уже отсортированные результаты: первая строка получает метку TOP. */
		hits: WarpHit[];
		oncreate: (h: WarpHit) => void;
		onswap: (h: WarpHit) => void;
	}

	let { hits, oncreate, onswap }: Props = $props();

	async function copyEndpoint(ep: string): Promise<void> {
		const ok = await copyToClipboard(ep);
		if (ok) notifications.success(m.warpscout_copied_ep({ endpoint: ep }));
		else notifications.error(m.warpscout_copy_failed());
	}
</script>

<div class="hits-table-wrap">
	<table class="hits">
		<thead>
			<tr>
				<th>Endpoint</th>
				<th>{m.warpscout_col_location()}</th>
				<th class="r">{m.warpscout_col_latency()}</th>
				<th class="c hide-xs">TG</th>
				<th></th>
			</tr>
		</thead>
		<tbody>
			{#each hits as h, i (h.endpoint)}
				{@const tone = latencyTone(h.pingMs)}
				<tr class:top-hit={i === 0}>
					<td class="mono">
						<span class="ep">
							{#if i === 0}<span class="top-badge">TOP</span>{/if}
							<span>{h.endpoint}</span>
							<IconButton
								ariaLabel={m.warpscout_copy_ep_aria()}
								title={m.common_copy()}
								onclick={() => copyEndpoint(h.endpoint)}
							>
								<Copy size={12} />
							</IconButton>
						</span>
					</td>
					<td>
						<span class="geo">
							{#if h.loc}<span class="flag">{countryFlag(h.loc)}</span>{/if}
							<span class="mono colo-cell">{h.colo || h.loc || '—'}</span>
							{#if h.colo && h.loc}<span class="mono muted">· {h.loc}</span>{/if}
						</span>
					</td>
					<td
						class="r mono"
						title={h.handshakeMs > 0 ? m.warpscout_handshake_tip({ ms: h.handshakeMs }) : undefined}
					>
						{#if h.pingMs > 0}
							<span class="lat-pill {tone}">
								<span class="lat-dot"></span>
								{m.warpscout_ms({ ms: h.pingMs })}
							</span>
						{:else}
							—
						{/if}
					</td>
					<td class="c hide-xs">
						{#if h.tgOk}<span class="ok">✓</span>{:else}<span class="muted">—</span>{/if}
					</td>
					<td class="actions-cell">
						<div class="actions">
							<Button
								size="sm"
								variant="outline-primary"
								title={m.warpscout_create_tunnel_tip()}
								onclick={() => oncreate(h)}
							>
								{#snippet iconBefore()}<Plus size={12} />{/snippet}
								{m.warpscout_tunnel_btn()}
							</Button>
							<Button size="sm" variant="ghost" title={m.warpscout_swap_ep_tip()} onclick={() => onswap(h)}>
								{#snippet iconBefore()}<Repeat size={12} />{/snippet}
								{m.warpscout_swap_btn()}
							</Button>
						</div>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<style>
	.hits-table-wrap {
		max-height: 22rem;
		overflow: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}
	.hits {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.78rem;
	}
	.hits th {
		position: sticky;
		top: 0;
		z-index: 1;
		text-align: left;
		font-weight: 500;
		font-size: 0.7rem;
		color: var(--color-text-muted);
		padding: 0.38rem 0.55rem;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
		white-space: nowrap;
	}
	.hits td {
		padding: 0.32rem 0.55rem;
		border-bottom: 1px solid var(--color-border);
		color: var(--color-text-primary);
		white-space: nowrap;
		vertical-align: middle;
	}
	.hits tr:last-child td {
		border-bottom: 0;
	}
	.hits tr.top-hit td {
		background: color-mix(in srgb, var(--color-accent-tint) 45%, transparent);
	}
	.hits tr:hover td {
		background: var(--color-bg-hover);
	}
	.top-badge {
		display: inline-flex;
		align-items: center;
		padding: 0.05rem 0.32rem;
		border-radius: 4px;
		font-family: var(--font-sans);
		font-size: 0.62rem;
		font-weight: 700;
		letter-spacing: 0.03em;
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border);
	}
	.colo-cell {
		font-weight: 600;
	}
	.geo {
		display: inline-flex;
		align-items: center;
		gap: 0.28rem;
	}
	.lat-pill {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.32rem;
		font-weight: 600;
	}
	.lat-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}
	.lat-pill.ok {
		color: var(--latency-color-ok, var(--color-success));
	}
	.lat-pill.ok .lat-dot {
		background: var(--latency-color-ok, var(--color-success));
	}
	.lat-pill.slow {
		color: var(--latency-color-slow, var(--color-warning));
	}
	.lat-pill.slow .lat-dot {
		background: var(--latency-color-slow, var(--color-warning));
	}
	.lat-pill.fail {
		color: var(--latency-color-fail, var(--color-error));
	}
	.lat-pill.fail .lat-dot {
		background: var(--latency-color-fail, var(--color-error));
	}
	.r {
		text-align: right !important;
	}
	.c {
		text-align: center !important;
	}
	.ep {
		display: inline-flex;
		align-items: center;
		gap: 0.32rem;
	}
	.actions-cell {
		width: 1%;
	}
	.actions {
		display: inline-flex;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.ok {
		color: var(--color-success);
		font-weight: 600;
	}
	.muted {
		color: var(--color-text-muted);
	}
	.mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 580px) {
		.hide-xs {
			display: none;
		}
	}
</style>
