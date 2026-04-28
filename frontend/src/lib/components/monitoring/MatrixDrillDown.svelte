<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { LoadingSpinner } from '$lib/components/layout';
	import { getCachedHistory, setCachedHistory } from '$lib/stores/monitoring';
	import type { MonitoringTarget, MonitoringTunnel, MonitoringSample } from '$lib/types';
	import Sparkline from './Sparkline.svelte';

	interface Props {
		target: MonitoringTarget;
		tunnel: MonitoringTunnel;
		onClose: () => void;
	}

	let { target, tunnel }: Props = $props();

	let samples = $state<MonitoringSample[]>([]);
	let loading = $state(true);

	onMount(async () => {
		const cached = getCachedHistory(target.id, tunnel.id);
		if (cached) {
			samples = cached;
			loading = false;
		}
		try {
			const fresh = await api.getMonitoringHistory({
				target: target.id,
				tunnelId: tunnel.id,
				limit: 60,
			});
			samples = fresh;
			setCachedHistory(target.id, tunnel.id, fresh);
		} catch {
			// Keep cached samples if any; otherwise stay empty.
		} finally {
			loading = false;
		}
	});

	const stats = $derived.by(() => {
		const ok = samples.filter((s) => s.ok && s.latencyMs !== null);
		if (ok.length === 0) {
			return {
				avg: null as number | null,
				min: null as number | null,
				max: null as number | null,
				lossPct: 100,
			};
		}
		const lats = ok.map((s) => s.latencyMs as number);
		const sum = lats.reduce((a, b) => a + b, 0);
		const lossPct = Math.round(((samples.length - ok.length) / samples.length) * 100);
		return {
			avg: Math.round(sum / lats.length),
			min: Math.min(...lats),
			max: Math.max(...lats),
			lossPct,
		};
	});

	const recent = $derived([...samples].reverse().slice(0, 10));
</script>

<div class="drill">
	{#if loading && samples.length === 0}
		<div class="loading"><LoadingSpinner /></div>
	{:else}
		<Sparkline points={samples} width={420} height={100} />

		<div class="stats">
			<div class="stat">
				<span class="stat-value">{stats.avg !== null ? `${stats.avg}ms` : '—'}</span>
				<span class="stat-label">Avg</span>
			</div>
			<div class="stat">
				<span class="stat-value">{stats.min !== null ? `${stats.min}ms` : '—'}</span>
				<span class="stat-label">Min</span>
			</div>
			<div class="stat">
				<span class="stat-value">{stats.max !== null ? `${stats.max}ms` : '—'}</span>
				<span class="stat-label">Max</span>
			</div>
			<div class="stat">
				<span class="stat-value">{stats.lossPct}%</span>
				<span class="stat-label">Loss</span>
			</div>
		</div>

		<h4 class="recent-title">Последние замеры</h4>
		<table class="recent">
			<thead>
				<tr>
					<th>Время</th>
					<th>Latency</th>
					<th>Статус</th>
				</tr>
			</thead>
			<tbody>
				{#each recent as s, i (s.ts + i)}
					<tr>
						<td class="ts">{new Date(s.ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</td>
						<td class="lat">{s.ok && s.latencyMs !== null ? `${s.latencyMs}ms` : '—'}</td>
						<td class="status" class:ok={s.ok} class:fail={!s.ok}>{s.ok ? 'OK' : 'FAIL'}</td>
					</tr>
				{/each}
			</tbody>
		</table>

		<small class="footer">Окно: 1 час · до 60 точек · обновляется каждые 60 секунд</small>
	{/if}
</div>

<style>
	.drill {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.loading {
		display: flex;
		justify-content: center;
		padding: 2rem 0;
	}

	.stats {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 0.5rem;
	}

	.stat {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.5rem 0.625rem;
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.stat-value {
		font-family: var(--font-mono);
		font-size: 16px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}

	.stat-label {
		font-size: 10px;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.recent-title {
		margin: 0.25rem 0 0;
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.recent {
		width: 100%;
		border-collapse: collapse;
		font-family: var(--font-mono);
		font-size: 11px;
	}

	.recent th,
	.recent td {
		padding: 0.25rem 0.5rem;
		border-bottom: 1px solid rgba(255, 255, 255, 0.04);
		text-align: left;
	}

	.recent th {
		font-family: var(--font-sans);
		font-size: 10px;
		color: var(--color-text-muted);
		font-weight: 600;
		text-transform: uppercase;
	}

	.ts { color: var(--color-text-muted); }
	.lat { font-variant-numeric: tabular-nums; }
	.status.ok { color: var(--color-success); }
	.status.fail { color: var(--color-error); }

	.footer {
		font-size: 10px;
		color: var(--color-text-muted);
	}
</style>
