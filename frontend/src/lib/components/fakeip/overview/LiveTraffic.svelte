<!--
  Широкая полоса живого трафика «Обзора» (FE-spec §5.1).

  ИСТОЧНИК (§4): store singboxTraffic — Map<tag, {upload, download}> с
  КУМУЛЯТИВНЫМИ байт-счётчиками по outbound-тегам (кормится SSE singbox:traffic;
  см. stores/singbox.ts). Отсюда честно выводим:
    - агрегатный объём ↓/↑ = сумма download/upload по всем тегам (formatBytes),
    - агрегатную скорость ↓/↑ = дельта этих сумм между снимками (computeRate),
      ровно как stores/traffic.ts делает per-tunnel.
  Per-outbound throughput НЕ показываем (§4 запрещает) — только агрегат.

  RAM (/memory): на живом Clash НЕ подтверждён, реального store нет — опускаем.
  // TODO(verify /memory on live Clash)

  Живой блок: рендерится только когда движок live; иначе честный empty-state.
-->
<script lang="ts">
	import { Card } from '$lib/components/ui';
	import { ArrowDown, ArrowUp } from 'lucide-svelte';
	import { singboxTraffic } from '$lib/stores/singbox';
	import { formatBytes, formatBitRate } from '$lib/utils/format';
	import {
		aggregateTotals,
		computeRate,
		type RateSnapshot,
		type TrafficRate,
	} from './liveTraffic';

	interface Props {
		/** Живой ли движок (движок запущен и clash-runtime доступен). */
		engineLive: boolean;
		/** Причина не-live для текста empty-state. */
		notLiveReason?: 'stopped' | 'clash-down';
	}

	let { engineLive, notLiveReason }: Props = $props();

	// Кумулятивные суммы по всем тегам — прямой честный объём.
	const totals = $derived(aggregateTotals($singboxTraffic));

	// Скорость: дельта кумулятивных сумм между двумя SSE-снимками. Предыдущий
	// снимок держим локально; перезапуск счётчиков (откат сумм) computeRate
	// пропускает, не показывая отрицательную скорость.
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

	const notLiveText = $derived(
		notLiveReason === 'clash-down'
			? 'Clash-runtime недоступен — живой трафик временно недоступен.'
			: 'Движок остановлен — живой трафик недоступен.',
	);
</script>

<Card padding="md">
	{#snippet header()}
		<h3 class="lt-title">Живой трафик</h3>
	{/snippet}

	{#if !engineLive}
		<p class="lt-empty">{notLiveText}</p>
	{:else}
		<div class="lt-grid">
			<div class="lt-item">
				<div class="lt-head">
					<ArrowDown size={16} class="lt-icon lt-down" />
					<span class="lt-label">Загрузка</span>
				</div>
				<span class="lt-rate">{rate.hasRate ? formatBitRate(rate.downloadRate) : '—'}</span>
				<span class="lt-total">всего {formatBytes(totals.downloadBytes)}</span>
			</div>

			<div class="lt-item">
				<div class="lt-head">
					<ArrowUp size={16} class="lt-icon lt-up" />
					<span class="lt-label">Отдача</span>
				</div>
				<span class="lt-rate">{rate.hasRate ? formatBitRate(rate.uploadRate) : '—'}</span>
				<span class="lt-total">всего {formatBytes(totals.uploadBytes)}</span>
			</div>

			<div class="lt-item">
				<span class="lt-label">Активных тегов</span>
				<span class="lt-rate lt-rate-sm">{totals.tagCount}</span>
				<span class="lt-total">outbounds с трафиком</span>
			</div>
		</div>
		{#if !rate.hasRate}
			<p class="lt-note">Скорость появится после второго отсчёта трафика.</p>
		{/if}
	{/if}
</Card>

<style>
	.lt-title {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.lt-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
		gap: 1.25rem;
	}

	.lt-item {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}

	.lt-head {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.lt-head :global(.lt-icon.lt-down) {
		color: var(--color-success, #22c55e);
	}
	.lt-head :global(.lt-icon.lt-up) {
		color: var(--color-info, #3b82f6);
	}

	.lt-label {
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.03em;
		text-transform: uppercase;
		color: var(--text-secondary);
	}

	.lt-rate {
		font: 700 1.5rem/1.1 var(--font-sans);
		letter-spacing: -0.02em;
		color: var(--text-primary);
		white-space: nowrap;
	}

	.lt-rate-sm {
		font-size: 1.25rem;
	}

	.lt-total {
		font-size: 0.75rem;
		color: var(--text-muted);
		font-family: var(--font-mono);
	}

	.lt-empty,
	.lt-note {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}

	.lt-note {
		margin-top: 0.75rem;
		font-size: 0.8125rem;
	}
</style>
