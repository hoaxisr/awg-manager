<script lang="ts">
	import { Activity, RotateCcw } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { WarpCheckSample, WarpProfileStats } from '$lib/types/warpscout';
	import {
		buildSparkBars,
		formatSlaPct,
		formatTime,
		formatUptime,
		humanizeSwapReason,
		latencyTone,
		parseGoTime,
		sampleBarHeight,
		topColos,
	} from './warpscoutUi';

	interface Props {
		stats?: WarpProfileStats;
		/** Интервал проверки профиля — для подсказки, пока замеров нет. */
		checkIntervalSec: number;
		onreset?: () => void;
	}

	let { stats, checkIntervalSec, onreset }: Props = $props();

	const hasStats = $derived(!!stats && (stats.checksTotal > 0 || stats.swapsOk > 0 || stats.swapsFail > 0));
	const samples = $derived(stats?.samples ?? []);
	const maxSampleMs = $derived(Math.max(80, ...samples.map((s) => s.latencyMs ?? 0)));
	const coloDistribution = $derived(topColos(stats?.colos, 4));

	function sampleTooltip(s: WarpCheckSample): string {
		const time = formatTime(s.at);
		if (!s.ok) {
			const reason = humanizeSwapReason(s.reason || m.warpscout_sample_check_fail());
			return s.swapped
				? m.warpscout_sample_tip_fail_swap({ time, reason })
				: m.warpscout_sample_tip_fail({ time, reason });
		}
		const loc = [s.colo, s.loc].filter(Boolean).join(' ');
		const baseLat = s.latencyMs && s.latencyMs > 0 ? m.warpscout_ms({ ms: s.latencyMs }) : 'ok';
		const latency = loc ? `${baseLat} · ${loc}` : baseLat;
		return s.swapped
			? m.warpscout_sample_tip_ok_swap({ time, latency })
			: m.warpscout_sample_tip_ok({ time, latency });
	}
</script>

<!-- Мини-дашборд статистики автопилота: KPI, лента проверок, дата-центры -->
<div class="stats-panel">
	{#if !hasStats || !stats}
		<div class="stats-waiting">
			<Activity size={13} />
			<span>{m.warpscout_stats_waiting({ sec: checkIntervalSec })}</span>
		</div>
	{:else}
		{@const slaPct = formatSlaPct(stats.checksOk, stats.checksTotal)}
		{@const slaRatio = stats.checksTotal > 0 ? stats.checksOk / stats.checksTotal : 1}
		{@const avgTone = latencyTone(stats.avgLatencyMs)}
		<div class="kpi-grid">
			<div
				class="kpi-tile"
				title={m.warpscout_kpi_success_tip({
					ok: stats.checksOk,
					total: stats.checksTotal,
					fail: stats.checksFail,
				})}
			>
				<span class="kpi-label">{m.warpscout_kpi_success()}</span>
				<span
					class="kpi-val mono"
					class:ok={slaRatio >= 0.97}
					class:warn={slaRatio < 0.97 && slaRatio >= 0.85}
					class:err={slaRatio < 0.85}
				>
					{slaPct}
				</span>
				<span class="kpi-sub">{m.warpscout_kpi_ok_ratio({ ok: stats.checksOk, total: stats.checksTotal })}</span>
			</div>

			<div class="kpi-tile" title={m.warpscout_kpi_ping_tip()}>
				<span class="kpi-label">{m.warpscout_kpi_avg_ping()}</span>
				<span class="kpi-val mono {avgTone}">
					{stats.avgLatencyMs && stats.avgLatencyMs > 0 ? m.warpscout_ms({ ms: stats.avgLatencyMs }) : '—'}
				</span>
				<span class="kpi-sub mono">
					{#if stats.minLatencyMs && stats.maxLatencyMs}
						{m.warpscout_ms_range({ min: stats.minLatencyMs, max: stats.maxLatencyMs })}
					{:else}
						{m.warpscout_kpi_no_samples()}
					{/if}
				</span>
			</div>

			<div class="kpi-tile" title={m.warpscout_kpi_swaps_tip({ ok: stats.swapsOk, fail: stats.swapsFail })}>
				<span class="kpi-label">{m.warpscout_kpi_swaps()}</span>
				<span class="kpi-val mono">
					{stats.swapsOk}
					{#if stats.swapsFail > 0}
						<span class="kpi-err-inline">{m.warpscout_kpi_err_short({ n: stats.swapsFail })}</span>
					{/if}
				</span>
				<span class="kpi-sub">
					{stats.checksFail > 0 ? m.warpscout_kpi_fails_count({ n: stats.checksFail }) : m.warpscout_kpi_no_fails()}
				</span>
			</div>

			<div
				class="kpi-tile"
				title={parseGoTime(stats.endpointSince)
					? m.warpscout_kpi_node_since_tip({ time: formatTime(stats.endpointSince) })
					: m.warpscout_kpi_node_time_tip()}
			>
				<span class="kpi-label">{m.warpscout_kpi_on_node()}</span>
				<span class="kpi-val mono">{formatUptime(stats.endpointSince || stats.startedAt)}</span>
				<span class="kpi-sub">
					{parseGoTime(stats.startedAt)
						? m.warpscout_kpi_since({ time: formatTime(stats.startedAt) })
						: m.warpscout_kpi_session()}
				</span>
			</div>
		</div>

		<div class="spark-bar-wrap">
			<div class="spark-header">
				<span class="spark-caption">
					{m.warpscout_spark_title()}
					{#if samples.length > 0}
						<span class="muted">({samples.length})</span>
					{:else}
						<span class="muted">{m.warpscout_spark_collecting()}</span>
					{/if}
				</span>

				{#if onreset}
					<Button size="sm" variant="ghost" title={m.warpscout_reset_stats_tip()} onclick={onreset}>
						{#snippet iconBefore()}<RotateCcw size={11} />{/snippet}
						{m.warpscout_reset_btn()}
					</Button>
				{/if}
			</div>

			{#if samples.length > 0}
				<div class="spark-strip" role="img" aria-label={m.warpscout_spark_aria()}>
					{#each buildSparkBars(samples) as s, idx (idx)}
						{#if !s}
							<span class="spark-col empty"><span class="spark-fill"></span></span>
						{:else}
							{@const barTone = !s.ok ? 'fail' : s.swapped ? 'swap' : latencyTone(s.latencyMs)}
							{@const hPct = sampleBarHeight(s, maxSampleMs)}
							<span class="spark-col {barTone}" title={sampleTooltip(s)}>
								<span class="spark-fill" style={`height:${hPct}%`}></span>
							</span>
						{/if}
					{/each}
				</div>
			{/if}

			{#if coloDistribution.length > 0}
				<div class="colo-dist">
					<span class="colo-dist-label">{m.warpscout_colos_label()}</span>
					{#each coloDistribution as item (item.colo)}
						<span
							class="colo-chip mono"
							title={m.warpscout_colo_chip_tip({
								colo: item.colo,
								count: item.count,
								pct: item.pct,
							})}
						>
							<b>{item.colo}</b>
							<span class="muted">{item.pct}%</span>
						</span>
					{/each}
				</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.stats-panel {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem 0.6rem;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-secondary) 55%, transparent);
	}
	.stats-waiting {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.73rem;
		color: var(--color-text-muted);
	}
	.kpi-grid {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.4rem;
	}
	.kpi-tile {
		display: flex;
		flex-direction: column;
		gap: 0.08rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		min-width: 0;
	}
	.kpi-label {
		font-size: 0.67rem;
		font-weight: 500;
		text-transform: uppercase;
		letter-spacing: 0.025em;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.kpi-val {
		font-size: 0.84rem;
		font-weight: 700;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.kpi-val.ok {
		color: var(--latency-color-ok, var(--color-success));
	}
	.kpi-val.slow,
	.kpi-val.warn {
		color: var(--latency-color-slow, var(--color-warning));
	}
	.kpi-val.fail,
	.kpi-val.err {
		color: var(--latency-color-fail, var(--color-error));
	}
	.kpi-err-inline {
		font-size: 0.7rem;
		font-weight: 600;
		color: var(--color-error);
		margin-left: 0.2rem;
	}
	.kpi-sub {
		font-size: 0.68rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.spark-bar-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.32rem;
	}
	.spark-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.7rem;
	}
	.spark-caption {
		color: var(--color-text-secondary);
		font-weight: 500;
	}
	.spark-strip {
		display: grid;
		grid-template-columns: repeat(32, minmax(0, 1fr));
		gap: 2px;
		height: 20px;
		align-items: end;
		padding: 2px 3px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
	}
	.spark-col {
		height: 100%;
		display: flex;
		align-items: flex-end;
		min-width: 0;
	}
	.spark-fill {
		width: 100%;
		border-radius: 1.5px;
		transition: height 0.2s ease;
	}
	.spark-col.empty .spark-fill {
		height: 22%;
		background: color-mix(in srgb, var(--color-border) 55%, transparent);
	}
	.spark-col.ok .spark-fill,
	.spark-col.none .spark-fill {
		background: var(--color-success);
	}
	.spark-col.slow .spark-fill {
		background: var(--color-warning);
	}
	.spark-col.fail .spark-fill {
		background: var(--color-error);
	}
	.spark-col.swap .spark-fill {
		background: var(--color-info);
	}
	.colo-dist {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		flex-wrap: wrap;
		font-size: 0.7rem;
	}
	.colo-dist-label {
		color: var(--color-text-muted);
		margin-right: 0.1rem;
	}
	.colo-chip {
		display: inline-flex;
		align-items: baseline;
		gap: 0.25rem;
		padding: 0.08rem 0.4rem;
		border-radius: 999px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		font-size: 0.7rem;
	}
	.colo-chip b {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.muted {
		color: var(--color-text-muted);
	}

	@media (max-width: 640px) {
		.kpi-grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
