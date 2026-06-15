<!--
  Панель «Трафик · live» по мокапу dash3: бар-график (TrafficSpark) + строка
  `.tline` с агрегатной скоростью ↓/↑ и объёмом за сессию.

  ИСТОЧНИК (§4): store singboxTraffic — кумулятивные байты по тегам. Объём ↓/↑ =
  сумма (formatBytes); скорость ↓/↑ = дельта суммы между снимками (computeRate).
  Память (/memory): реального Clash-store нет — поле опущено (не выдумываем).

  Живой блок: вне live — честный empty-state.
-->
<script lang="ts">
	import { singboxTraffic } from '$lib/stores/singbox';
	import { formatBytes } from '$lib/utils/format';
	import {
		aggregateTotals,
		computeRate,
		type RateSnapshot,
		type TrafficRate,
	} from './liveTraffic';
	import TrafficSpark from './TrafficSpark.svelte';

	interface Props {
		/** Живой ли движок (движок запущен и clash-runtime доступен). */
		engineLive: boolean;
		/** Причина не-live для текста empty-state. */
		notLiveReason?: 'stopped' | 'clash-down';
	}

	let { engineLive, notLiveReason }: Props = $props();

	const totals = $derived(aggregateTotals($singboxTraffic));

	// Скорость: дельта кумулятивных сумм между двумя SSE-снимками (та же техника,
	// что в TrafficSpark — но тут нужно мгновенное значение для `.tline`).
	let prevSnapshot: RateSnapshot | null = null;
	let rate = $state<TrafficRate>({ downloadRate: 0, uploadRate: 0, hasRate: false });

	$effect(() => {
		const next: RateSnapshot = {
			timestamp: Date.now(),
			downloadBytes: totals.downloadBytes,
			uploadBytes: totals.uploadBytes,
		};
		rate = computeRate(prevSnapshot, next);
		prevSnapshot = next;
	});

	// Байт-скорость как в мокапе (MB/s), а не бит/с — formatBytes + «/с».
	function byteRate(v: number): string {
		return `${formatBytes(v)}/с`;
	}

	const sessionTotal = $derived(totals.downloadBytes + totals.uploadBytes);

	const notLiveText = $derived(
		notLiveReason === 'clash-down'
			? 'Clash-runtime недоступен — живой трафик временно недоступен.'
			: 'Движок остановлен — живой трафик недоступен.',
	);
</script>

<div class="panel">
	<div class="ph">
		<span class="nm">Трафик · live</span>
		<span class="meta">Clash /traffic</span>
	</div>

	{#if !engineLive}
		<p class="empty">{notLiveText}</p>
	{:else}
		<TrafficSpark {engineLive} />
		<div class="tline">
			<span class="dn">&darr; <b>{rate.hasRate ? byteRate(rate.downloadRate) : '—'}</b></span>
			<span class="up">&uarr; <b>{rate.hasRate ? byteRate(rate.uploadRate) : '—'}</b></span>
			<span>за сессию <b>{formatBytes(sessionTotal)}</b></span>
		</div>
	{/if}
</div>

<style>
	.panel {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
	}

	.ph {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.625rem;
	}

	.ph .nm {
		color: var(--text-primary);
		font-size: 1.0625rem;
		font-weight: 700;
	}

	.ph .meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
	}

	.tline {
		display: flex;
		gap: 1.75rem;
		flex-wrap: wrap;
		font-size: 0.875rem;
		color: var(--text-secondary);
	}

	.tline .up {
		color: var(--color-success);
	}

	.tline .dn {
		color: var(--color-info);
	}

	.tline b {
		color: var(--text-primary);
		font-family: var(--font-mono);
	}

	.empty {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}
</style>
