<script lang="ts">
	import type { DnsProxy } from '$lib/types';
	import { Badge } from '$lib/components/ui';
	interface Props { proxy: DnsProxy; open?: boolean; }
	let { proxy, open: initialOpen = false }: Props = $props();
	// svelte-ignore state_referenced_locally — intentional: initial expanded state from prop, then user-controlled
	let expanded = $state(initialOpen);

	const cachePct = $derived(Math.round((proxy.stat.cacheHitRatio || 0) * 100));
	const rawChip = $derived(
		proxy.name !== 'System' && proxy.displayName !== proxy.name ? proxy.name : ''
	);
</script>

<div class="pol" class:open={expanded}>
	<button type="button" class="pol-head" onclick={() => (expanded = !expanded)}>
		<span class="chev">›</span>
		<span class="pol-name">
			{proxy.displayName}
			{#if rawChip}<Badge variant="muted" size="sm" mono>{rawChip}</Badge>{/if}
		</span>
		<span class="pol-port">:{proxy.tcpPort}</span>
		<span class="pol-metrics">
			<span class="metric"><span class="v">{proxy.stat.totalRequests}</span><span class="k">запросов</span></span>
			<span class="metric"><span class="v faint">{proxy.stat.proxyRequestsSent}</span><span class="k">proxy</span></span>
			<span class="cache">
				<span class="bar"><span style="width:{cachePct}%"></span></span>
				<span class="metric"><span class="v">{cachePct}%</span><span class="k">cache</span></span>
			</span>
		</span>
	</button>
	{#if expanded}
		<div class="pol-body">
			<table>
				<thead>
					<tr><th>Сервер</th><th class="num">Отпр</th><th class="num">Получ</th><th class="num">NX</th><th class="num">Медиана</th><th class="num">Среднее</th><th class="num">Rank</th></tr>
				</thead>
				<tbody>
					{#each proxy.upstreams as u}
						<tr>
							<td class="mono">{u.address}</td>
							<td class="num">{u.rSent}</td>
							<td class="num">{u.aRcvd}</td>
							<td class="num">{u.nxRcvd}</td>
							<td class="num">{u.medResp || '—'}</td>
							<td class="num">{u.avgResp || '—'}</td>
							<td class="num"><Badge variant="accent" size="sm" mono>{u.rank}</Badge></td>
						</tr>
					{/each}
				</tbody>
			</table>

			<div class="pol-mobile-list" aria-label={`Статистика DNS-политики ${proxy.displayName}`}>
				{#each proxy.upstreams as u}
					<section class="pol-mobile-card">
						<div class="pol-mobile-title mono">{u.address}</div>
						<div class="pol-mobile-stats">
							<div class="pol-mobile-stat"><span>Отпр</span><strong>{u.rSent}</strong></div>
							<div class="pol-mobile-stat"><span>Получ</span><strong>{u.aRcvd}</strong></div>
							<div class="pol-mobile-stat"><span>NX</span><strong>{u.nxRcvd}</strong></div>
							<div class="pol-mobile-stat"><span>Медиана</span><strong>{u.medResp || '—'}</strong></div>
							<div class="pol-mobile-stat"><span>Среднее</span><strong>{u.avgResp || '—'}</strong></div>
							<div class="pol-mobile-stat pol-mobile-rank"><span>Rank</span><Badge variant="accent" size="sm" mono>{u.rank}</Badge></div>
						</div>
					</section>
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	.pol { border: 1px solid var(--border-soft, var(--border)); border-radius: 10px; margin-bottom: 8px; overflow: hidden; }
	.pol-head { display: flex; align-items: center; gap: 14px; width: 100%; padding: 11px 14px; background: none; border: none; cursor: pointer; text-align: left; color: inherit; font: inherit; }
	.pol-head:hover { background: var(--color-bg-hover); }
	.chev { color: var(--text-muted); transition: transform .15s; }
	.pol.open .chev { transform: rotate(90deg); }
	.pol-name { font-weight: 600; display: flex; align-items: center; gap: 8px; min-width: 150px; }
	.pol-port { font-family: ui-monospace, monospace; font-size: 12px; color: var(--text-muted); min-width: 60px; }
	.pol-metrics { margin-left: auto; display: flex; align-items: center; gap: 18px; }
	.metric { display: flex; flex-direction: column; align-items: flex-end; }
	.metric .v { font-family: ui-monospace, monospace; font-weight: 600; font-size: 14px; }
	.metric .k { font-size: 10px; color: var(--text-muted); text-transform: uppercase; letter-spacing: .04em; }
	.faint { opacity: .65; }
	.cache { display: flex; align-items: center; gap: 8px; }
	.bar { width: 54px; height: 6px; border-radius: 999px; background: color-mix(in srgb, var(--text-muted) 18%, transparent); overflow: hidden; }
	.bar > span { display: block; height: 100%; background: var(--accent); }
	.pol-body { padding: 4px 14px 12px; border-top: 1px solid var(--border-soft, var(--border)); }
	.pol-body table { width: 100%; border-collapse: collapse; margin-top: 6px; }
	.pol-body th { font-size: 11px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: .04em; padding: 4px 10px 6px 0; text-align: left; }
	.pol-body td { padding: 6px 10px 6px 0; border-top: 1px solid var(--border-soft, var(--border)); }
	.pol-mobile-list { display: none; }
	.num { text-align: right; font-family: ui-monospace, monospace; font-variant-numeric: tabular-nums; }
	th.num { text-align: right; }
	.mono { font-family: ui-monospace, monospace; font-size: 13px; }

	@media (max-width: 768px) {
		.pol {
			border-radius: 12px;
		}

		.pol-head {
			display: grid;
			grid-template-columns: auto minmax(0, 1fr) auto;
			align-items: start;
			gap: 0.5rem;
			padding: 0.875rem 1rem;
		}

		.pol-name {
			min-width: 0;
			flex-wrap: wrap;
			gap: 0.35rem;
			line-height: 1.25;
		}

		.pol-port {
			min-width: 0;
			justify-self: end;
			white-space: nowrap;
		}

		.pol-metrics {
			grid-column: 2 / -1;
			margin-left: 0;
			display: grid;
			grid-template-columns: repeat(3, minmax(0, 1fr));
			gap: 0.5rem;
			width: 100%;
		}

		.metric {
			align-items: flex-start;
			min-width: 0;
		}

		.cache {
			align-items: flex-start;
			flex-direction: column;
			gap: 0.35rem;
			min-width: 0;
		}

		.bar {
			width: 100%;
			max-width: 72px;
		}

		.pol-body {
			padding: 0.75rem;
		}

		.pol-body table {
			display: none;
		}

		.pol-mobile-list {
			display: grid;
			grid-template-columns: 1fr;
			gap: 0.625rem;
		}

		.pol-mobile-card {
			min-width: 0;
			padding: 0.75rem;
			border: 1px solid var(--border-soft, var(--border));
			border-radius: var(--radius-sm);
			background: color-mix(in srgb, var(--color-bg-secondary, var(--bg-secondary)) 72%, transparent);
		}

		.pol-mobile-title {
			margin-bottom: 0.65rem;
			overflow-wrap: anywhere;
			font-weight: 600;
			color: var(--text-primary);
		}

		.pol-mobile-stats {
			display: grid;
			grid-template-columns: repeat(3, minmax(0, 1fr));
			gap: 0.5rem;
		}

		.pol-mobile-stat {
			display: flex;
			flex-direction: column;
			gap: 0.2rem;
			min-width: 0;
		}

		.pol-mobile-stat span {
			font-size: 10px;
			font-weight: 700;
			letter-spacing: 0.06em;
			text-transform: uppercase;
			color: var(--text-muted);
		}

		.pol-mobile-stat strong {
			min-width: 0;
			overflow-wrap: anywhere;
			font-family: ui-monospace, monospace;
			font-size: 13px;
			font-weight: 600;
			font-variant-numeric: tabular-nums;
			color: var(--text-primary);
		}

		.pol-mobile-rank {
			align-items: flex-start;
		}
	}
</style>
