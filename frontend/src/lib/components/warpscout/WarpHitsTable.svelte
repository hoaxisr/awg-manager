<script lang="ts">
	import { Copy, Download, MoreHorizontal, Plus, Repeat } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button, IconButton } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { WarpHit } from '$lib/types/warpscout';
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
	let rowMenuPos = $state({ top: 0, right: 16, openUp: false });

	function toggleRowMenu(h: WarpHit, e: MouseEvent): void {
		e.stopPropagation();
		if (rowMenuHit?.endpoint === h.endpoint) {
			rowMenuHit = null;
			return;
		}
		const btn = e.currentTarget as HTMLButtonElement;
		const rect = btn.getBoundingClientRect();
		const menuH = onexport ? 120 : 86;
		const spaceBelow = window.innerHeight - rect.bottom - 12;
		const openUp = spaceBelow < menuH && rect.top > menuH + 12;
		rowMenuPos = {
			top: openUp ? Math.max(8, rect.top - menuH - 4) : rect.bottom + 4,
			right: Math.max(8, window.innerWidth - rect.right),
			openUp,
		};
		rowMenuHit = h;
	}

	function closeRowMenu(): void {
		rowMenuHit = null;
	}

	function handleWindowPointerDown(e: PointerEvent): void {
		if (!rowMenuHit) return;
		const t = e.target as HTMLElement | null;
		if (t?.closest('.row-overflow-menu') || t?.closest('.row-more-btn')) return;
		closeRowMenu();
	}

	function handleWindowKeyDown(e: KeyboardEvent): void {
		if (e.key === 'Escape' && rowMenuHit) closeRowMenu();
	}

	async function copyEndpoint(ep: string): Promise<void> {
		const ok = await copyToClipboard(ep);
		if (ok) notifications.success(m.warpscout_copied_ep({ endpoint: ep }));
		else notifications.error(m.warpscout_copy_failed());
	}
</script>

<svelte:window
	onpointerdown={handleWindowPointerDown}
	onkeydown={handleWindowKeyDown}
	onscroll={closeRowMenu}
	onresize={closeRowMenu}
/>

<div class="hits-table-wrap">
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
						<!-- Мобильная карточка (<= 600px): прямой доступ к действиям без горизонтального скролла -->
						<div class="actions-mobile" class:has-export={Boolean(onexport)}>
							{#if onexport}
								<Button
									size="sm"
									variant="ghost"
									title={m.warpscout_export_conf_tip()}
									onclick={() => onexport(h)}
								>
									{#snippet iconBefore()}<Download size={12} />{/snippet}
									{m.warpscout_export_conf_btn()}
								</Button>
							{/if}
							<Button
								size="sm"
								variant="outline-primary"
								title={m.warpscout_create_tunnel_tip()}
								onclick={() => oncreate(h)}
							>
								{#snippet iconBefore()}<Plus size={12} />{/snippet}
								{m.warpscout_tunnel_btn()}
							</Button>
							<Button size="sm" variant="secondary" title={m.warpscout_swap_ep_tip()} onclick={() => onswap(h)}>
								{#snippet iconBefore()}<Repeat size={12} />{/snippet}
								{m.warpscout_swap_btn()}
							</Button>
						</div>

						<!-- Десктоп / компактный вид: основное действие + меню «⋯» -->
						<div class="actions-desktop">
							<Button size="sm" variant="secondary" title={m.warpscout_swap_ep_tip()} onclick={() => onswap(h)}>
								{#snippet iconBefore()}<Repeat size={12} />{/snippet}
								{m.warpscout_swap_btn()}
							</Button>
							<button
								type="button"
								class="row-more-btn"
								class:open={rowMenuHit?.endpoint === h.endpoint}
								aria-label={m.warpscout_row_more_aria()}
								aria-haspopup="menu"
								aria-expanded={rowMenuHit?.endpoint === h.endpoint}
								onclick={(e) => toggleRowMenu(h, e)}
							>
								<MoreHorizontal size={14} />
							</button>
						</div>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

{#if rowMenuHit}
	{@const hit = rowMenuHit}
	<div
		class="row-overflow-menu"
		class:open-up={rowMenuPos.openUp}
		role="menu"
		style:top={`${rowMenuPos.top}px`}
		style:right={`${rowMenuPos.right}px`}
	>
		<button
			type="button"
			class="row-menu-item"
			role="menuitem"
			onclick={() => {
				closeRowMenu();
				oncreate(hit);
			}}
		>
			<Plus size={13} />
			<span>{m.warpscout_create_tunnel_menu()}</span>
		</button>
		{#if onexport}
			<button
				type="button"
				class="row-menu-item"
				role="menuitem"
				onclick={() => {
					closeRowMenu();
					onexport(hit);
				}}
			>
				<Download size={13} />
				<span>{m.warpscout_download_conf_menu()}</span>
			</button>
		{/if}
		<button
			type="button"
			class="row-menu-item"
			role="menuitem"
			onclick={() => {
				closeRowMenu();
				void copyEndpoint(hit.endpoint);
			}}
		>
			<Copy size={13} />
			<span>{m.warpscout_copy_ep_menu()}</span>
		</button>
	</div>
{/if}

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
	.actions-desktop {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.actions-mobile {
		display: none;
	}
	.row-more-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: color 0.12s ease, border-color 0.12s ease, background 0.12s ease;
	}
	.row-more-btn:hover,
	.row-more-btn.open {
		color: var(--color-text-primary);
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.row-overflow-menu {
		position: fixed;
		z-index: var(--z-floating);
		min-width: 11.25rem;
		padding: 0.25rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.14);
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.row-overflow-menu.open-up {
		transform-origin: bottom right;
	}
	.row-menu-item {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		width: 100%;
		padding: 0.42rem 0.55rem;
		border: 0;
		border-radius: calc(var(--radius-sm) - 2px);
		background: transparent;
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.78rem;
		text-align: left;
		cursor: pointer;
		white-space: nowrap;
	}
	.row-menu-item:hover {
		background: var(--color-bg-hover);
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
		.actions-desktop {
			display: none;
		}
		.actions-mobile {
			display: flex;
			flex-wrap: wrap;
			align-items: center;
			justify-content: flex-start;
			width: 100%;
			gap: 0.35rem;
		}
		.actions-mobile :global(button) {
			min-height: 32px;
		}
		.actions-mobile.has-export :global(button:nth-child(1)) {
			order: 3;
		}
		.actions-mobile.has-export :global(button:nth-child(2)) {
			order: 2;
		}
		.actions-mobile.has-export :global(button:nth-child(3)) {
			order: 1;
		}
		.actions-mobile:not(.has-export) :global(button:nth-child(1)) {
			order: 2;
		}
		.actions-mobile:not(.has-export) :global(button:nth-child(2)) {
			order: 1;
		}
	}
</style>
