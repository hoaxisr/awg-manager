<!--
  Бар-спарклайн агрегатной скорости трафика «Обзора» (мокап dash3 `.spark`):
  ряд столбцов flex-end, высота ~80px, заливка app-accent градиентом.

  ИСТОЧНИК (§4): store singboxTraffic — Map<tag, {upload, download}> с
  КУМУЛЯТИВНЫМИ байтами. Скорость = дельта суммы байт между снимками (та же
  техника, что liveTraffic.ts/computeRate). Держим скользящее окно последних
  WINDOW отсчётов; высота столбца — % от максимума окна. Честно: пока отсчётов
  мало, рисуем меньше столбцов (не выдумываем форму графика).

  Презентационный гейт: рендерится только когда движок live.
-->
<script lang="ts">
	import { singboxTraffic } from '$lib/stores/singbox';
	import { aggregateTotals, computeRate, type RateSnapshot } from './liveTraffic';

	interface Props {
		/** Живой ли движок (движок запущен и clash-runtime доступен). */
		engineLive: boolean;
	}

	let { engineLive }: Props = $props();

	// Размер скользящего окна — столько столбцов на полной картинке (как dash3).
	const WINDOW = 32;

	// Кумулятивные суммы по всем тегам.
	const totals = $derived(aggregateTotals($singboxTraffic));

	// Скользящее окно агрегатной скорости (down+up байт/с). Каждый новый снимок
	// SSE добавляет один отсчёт; держим только последние WINDOW.
	let prevSnapshot: RateSnapshot | null = null;
	let samples = $state<number[]>([]);

	$effect(() => {
		const next: RateSnapshot = {
			timestamp: Date.now(),
			downloadBytes: totals.downloadBytes,
			uploadBytes: totals.uploadBytes,
		};
		const rate = computeRate(prevSnapshot, next);
		prevSnapshot = next;
		// Без второго отсчёта (или после сброса счётчиков) скорости нет — не пишем
		// в окно, чтобы не рисовать ложный нулевой столбец.
		if (rate.hasRate) {
			const total = rate.downloadRate + rate.uploadRate;
			samples = [...samples, total].slice(-WINDOW);
		}
	});

	// Нормализация высот к % от максимума окна. Пустой/нулевой максимум → плоско.
	const peak = $derived(samples.reduce((m, v) => Math.max(m, v), 0));
	// Всегда WINDOW слотов (гистограмма как dash3): пустые слева — бледный baseline
	// (отсчёта ещё нет, честно ≠ ноль трафика), реальные отсчёты справа.
	const slots = $derived<(number | null)[]>([
		...Array(Math.max(0, WINDOW - samples.length)).fill(null),
		...samples.map((v) => (peak > 0 ? Math.max(6, Math.round((v / peak) * 100)) : 6)),
	]);
</script>

<div class="spark" aria-hidden="true">
	{#each slots as h, i (i)}
		{#if !engineLive || h === null}
			<b class="empty" style="height: 3%"></b>
		{:else}
			<b style="height: {h}%"></b>
		{/if}
	{/each}
</div>

<style>
	.spark {
		height: 80px;
		display: flex;
		align-items: flex-end;
		gap: 3px;
		margin: 0.25rem 0 0.75rem;
	}

	.spark b {
		flex: 1;
		min-width: 2px;
		background: linear-gradient(
			var(--color-accent),
			color-mix(in srgb, var(--color-accent) 22%, transparent)
		);
		border-radius: 2px 2px 0 0;
		display: block;
	}

	/* Пустой слот окна — бледная базовая линия (отсчёта ещё нет). */
	.spark b.empty {
		background: var(--color-border, color-mix(in srgb, var(--text-muted) 30%, transparent));
		border-radius: 2px;
	}
</style>
