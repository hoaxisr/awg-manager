<script lang="ts">
	// Верхняя полоса «Обзора»: соединение, аптайм, память, версия.
	// Источники (дата-контракт 2026-07-27): wanStatus (GET /api/wan/status),
	// systemInfo.routerDetails (poll 30s), updateInfo из +layout.
	import { StatusDot } from '$lib/components/ui';
	import { systemInfo, updateInfo } from '$lib/stores/system';
	import { wanStatus } from '$lib/stores/wanStatus';
	import { MEMORY_ALERT_PERCENT } from './overviewRollup';

	const DASH = '—';

	const details = $derived($systemInfo.data?.routerDetails);
	const wan = $derived($wanStatus.data);

	/** Активный WAN — поднятый интерфейс с максимальным приоритетом. */
	const activeWan = $derived.by(() => {
		const entries = Object.entries(wan?.interfaces ?? {}).filter(([, s]) => s.up);
		if (entries.length === 0) return null;
		return entries.sort(([, a], [, b]) => (b.priority ?? 0) - (a.priority ?? 0))[0];
	});

	const wanSub = $derived(
		[details?.modelDisplay || details?.model || '', activeWan?.[1].label || activeWan?.[0] || '']
			.filter(Boolean)
			.join(' · '),
	);

	const memPercent = $derived(details?.memoryUsedPercent);
	const memVariant = $derived(
		memPercent === undefined
			? 'plain'
			: memPercent >= MEMORY_ALERT_PERCENT
				? 'error'
				: memPercent >= 80
					? 'warning'
					: 'plain',
	);

	const version = $derived($systemInfo.data?.version || $updateInfo?.currentVersion || '');
	const load = $derived(details?.loadAverage?.trim().split(/[\s,]+/)[0] ?? '');
</script>

<div class="tiles">
	<div class="tile">
		<span class="tile-label">Соединение</span>
		{#if !wan}
			<span class="tile-value muted">{DASH}</span>
			<span class="tile-sub">нет данных от роутера</span>
		{:else}
			<span class="tile-value with-dot">
				<StatusDot variant={wan.anyWANUp ? 'success' : 'error'} />
				{wan.anyWANUp ? 'В сети' : 'Нет сети'}
			</span>
			<span class="tile-sub">{wanSub || 'интерфейс неизвестен'}</span>
		{/if}
	</div>

	<div class="tile">
		<span class="tile-label">Аптайм</span>
		{#if details?.uptimeHuman}
			<span class="tile-value mono">{details.uptimeHuman}</span>
			<span class="tile-sub">{load ? `load ${load}` : ''}</span>
		{:else}
			<span class="tile-value mono muted">{DASH}</span>
			<span class="tile-sub">нет данных от роутера</span>
		{/if}
	</div>

	<div class="tile">
		<span class="tile-label">Память</span>
		{#if memPercent !== undefined}
			<span class="tile-value mono" class:warn={memVariant === 'warning'} class:err={memVariant === 'error'}>
				{Math.round(memPercent)}%
			</span>
			<span class="tile-sub">{details?.memoryUsedMB ?? DASH} / {details?.memoryTotalMB ?? DASH} MB</span>
		{:else}
			<span class="tile-value mono muted">{DASH}</span>
			<span class="tile-sub">нет данных от роутера</span>
		{/if}
	</div>

	<div class="tile">
		<span class="tile-label">Версия</span>
		{#if version}
			<span class="tile-value mono">v{version}</span>
			{#if $updateInfo?.available}
				<span class="tile-sub warn">обновление доступно</span>
			{:else}
				<span class="tile-sub">{$systemInfo.data?.keeneticOS ? `KeeneticOS ${$systemInfo.data.keeneticOS}` : ''}</span>
			{/if}
		{:else}
			<span class="tile-value mono muted">{DASH}</span>
			<span class="tile-sub">нет данных</span>
		{/if}
	</div>
</div>

<style>
	.tiles {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.75rem;
	}

	.tile {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.8125rem 0.9375rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		min-width: 0;
	}

	.tile-label {
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.tile-value {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text-primary);
		line-height: 1.2;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.tile-value.with-dot {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.9375rem;
	}

	.tile-sub {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		min-height: 0.9rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.muted {
		color: var(--color-text-muted);
	}

	.warn {
		color: var(--color-warning);
	}

	.err {
		color: var(--color-error);
	}

	@media (max-width: 900px) {
		.tiles {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
