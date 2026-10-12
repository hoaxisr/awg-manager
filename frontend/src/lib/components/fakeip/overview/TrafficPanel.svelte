<!--
  Панель «Трафик · live» по мокапу dash3: бар-график (TrafficSpark) + строка
  `.tline` с агрегатной скоростью ↓/↑ и объёмом за сессию.

  ИСТОЧНИК: singboxTrafficLive — кумулятивные totals Clash (включая закрытые
  соединения, монотонны до рестарта движка) + скорость как дельта между
  SSE-снимками. Прежний расчёт по per-tag карте занижал объём (закрытые
  соединения исчезали из сумм) и считал каждое звено chain'а.
  Память (/memory): отображается в других местах — поле здесь опущено.

  Живой блок: вне live — честный empty-state.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
	import { formatBytes, formatByteRate } from '$lib/utils/format';
	import TrafficSpark from './TrafficSpark.svelte';

	interface Props {
		/** Живой ли движок (движок запущен и clash-runtime доступен). */
		engineLive: boolean;
		/** Причина не-live для текста empty-state. */
		notLiveReason?: 'stopped' | 'clash-down';
	}

	let { engineLive, notLiveReason }: Props = $props();

	const live = $derived($singboxTrafficLive);
	const rate = $derived(live.rate);
	const sessionTotal = $derived(live.totals.downloadBytes + live.totals.uploadBytes);

	const notLiveText = $derived(
		notLiveReason === 'clash-down'
			? m.fakeip_traffic_clash_down()
			: m.fakeip_traffic_engine_stopped(),
	);
</script>

<div class="panel">
	<div class="ph">
		<span class="nm">{m.fakeip_traffic_title()}</span>
		<span class="meta">Clash /traffic</span>
	</div>

	{#if !engineLive}
		<p class="empty">{notLiveText}</p>
	{:else}
		<TrafficSpark {engineLive} />
		<div class="tline">
			<span class="dn">&darr; <b>{rate.hasRate ? formatByteRate(rate.downloadRate) : '—'}</b></span>
			<span class="up">&uarr; <b>{rate.hasRate ? formatByteRate(rate.uploadRate) : '—'}</b></span>
			<span>{m.fakeip_traffic_session()} <b>{formatBytes(sessionTotal)}</b></span>
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
