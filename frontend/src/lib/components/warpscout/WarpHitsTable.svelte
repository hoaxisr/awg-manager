<script lang="ts">
	import { Copy } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { IconButton } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { WarpHit } from '$lib/types/warpscout';
	import WarpHitActions from './WarpHitActions.svelte';
	import WarpHitRowMenu from './WarpHitRowMenu.svelte';
	import { countryFlag, latencyTone } from './warpscoutUi';

	interface Props {
		/** Уже отсортированные результаты: первая строка получает метку TOP. */
		hits: WarpHit[];
		showTgCol?: boolean;
		oncreate: (h: WarpHit) => void;
		onswap: (h: WarpHit) => void;
		onexport?: (h: WarpHit) => void;
	}

	let { hits, showTgCol = true, oncreate, onswap, onexport }: Props = $props();

	let rowMenuHit = $state<WarpHit | null>(null);
	let rowMenuIndex = $state(-1);
	let rowMenuPos = $state({ top: 0, right: 16, openUp: false });
	let rowMenuTriggerEl = $state<HTMLButtonElement | null>(null);

	function toggleRowMenu(h: WarpHit, e: MouseEvent): void {
		e.stopPropagation();
		if (rowMenuHit?.endpoint === h.endpoint) {
			rowMenuHit = null;
			rowMenuIndex = -1;
			return;
		}
		const btn = e.currentTarget as HTMLButtonElement;
		const rect = btn.getBoundingClientRect();
		const menuH = onexport ? 86 : 48;
		const spaceBelow = window.innerHeight - rect.bottom - 12;
		const openUp = spaceBelow < menuH && rect.top > menuH + 12;
		rowMenuPos = {
			top: openUp ? Math.max(8, rect.top - menuH - 4) : rect.bottom + 4,
			right: Math.max(8, window.innerWidth - rect.right),
			openUp,
		};
		rowMenuTriggerEl = btn;
		rowMenuIndex = hits.findIndex((item) => item.endpoint === h.endpoint);
		rowMenuHit = h;
	}

	function closeRowMenu(): void {
		rowMenuHit = null;
		rowMenuIndex = -1;
	}

	$effect(() => {
		if (!rowMenuHit) return;
		const idx = hits.findIndex((item) => item.endpoint === rowMenuHit?.endpoint);
		if (idx === -1 || idx !== rowMenuIndex) {
			rowMenuHit = null;
			rowMenuIndex = -1;
		}
	});

	async function copyEndpoint(ep: string): Promise<void> {
		const ok = await copyToClipboard(ep);
		if (ok) notifications.success(m.warpscout_copied_ep({ endpoint: ep }));
		else notifications.error(m.warpscout_copy_failed());
	}
</script>

<div class="hits-table-wrap" onscroll={closeRowMenu}>
	<table class="hits">
		<thead>
			<tr>
				<th>Endpoint</th>
				<th>{m.warpscout_col_location()}</th>
				<th class="r">{m.warpscout_col_latency()}</th>
				{#if showTgCol}<th class="c hide-xs">TG</th>{/if}
				<th></th>
			</tr>
		</thead>
		<tbody>
			{#each hits as h, i (h.endpoint)}
				{@const tone = latencyTone(h.pingMs)}
				<tr class:top-hit={i === 0}>
					<td class="mono cell-ep">
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
					<td class="cell-geo">
						<span class="geo">
							{#if h.loc}<span class="flag">{countryFlag(h.loc)}</span>{/if}
							<span class="mono colo-cell">{h.colo || h.loc || '—'}</span>
							{#if h.colo && h.loc}<span class="mono muted">· {h.loc}</span>{/if}
						</span>
					</td>
					<td
						class="r mono cell-lat"
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
					{#if showTgCol}
						<td class="c hide-xs cell-tg">
							{#if h.tgOk}<span class="ok">✓</span>{:else}<span class="muted">—</span>{/if}
						</td>
					{/if}
					<td class="actions-cell">
						<WarpHitActions
							hit={h}
							menuOpen={rowMenuHit?.endpoint === h.endpoint}
							{oncreate}
							{onswap}
							{onexport}
							ontogglemenu={toggleRowMenu}
						/>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<WarpHitRowMenu
	hit={rowMenuHit}
	pos={rowMenuPos}
	{onexport}
	oncopy={(ep) => void copyEndpoint(ep)}
	onclose={closeRowMenu}
	onrestorefocus={() => rowMenuTriggerEl?.focus()}
/>

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

	@media (max-width: 600px) {
		.hits-table-wrap {
			max-height: none;
			overflow: visible;
		}
		.hits thead {
			display: none;
		}
		.hits,
		.hits tbody {
			display: block;
			width: 100%;
		}
		.hits tbody tr {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			grid-template-areas:
				'ep lat'
				'geo tg'
				'actions actions';
			align-items: center;
			gap: 0.35rem 0.5rem;
			padding: 0.65rem 0.7rem;
			border-bottom: 1px solid var(--color-border);
		}
		.hits tbody tr:last-child {
			border-bottom: 0;
		}
		.hits td {
			border-bottom: 0;
			padding: 0;
			min-width: 0;
			white-space: normal;
		}
		.hits td.cell-ep {
			grid-area: ep;
		}
		.hits td.cell-geo {
			grid-area: geo;
		}
		.hits td.cell-lat {
			grid-area: lat;
			justify-self: end;
			white-space: nowrap;
		}
		.hits td.cell-tg {
			grid-area: tg;
			display: block;
			justify-self: end;
			font-size: 0.72rem;
		}
		.hits td.actions-cell {
			grid-area: actions;
			width: 100%;
			padding-top: 0.25rem;
		}
		.ep {
			flex-wrap: wrap;
		}
	}
</style>
