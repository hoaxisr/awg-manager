<script lang="ts">
	import type { TrafficPeriod } from '$lib/api/client';
	import { formatBitRate, formatBytes } from '$lib/utils/format';
	import { fetchTrafficDetail, subscribeTraffic, getTrafficRates } from '$lib/stores/traffic';
	import Modal from './Modal.svelte';

	interface Props {
		open: boolean;
		tunnelId: string;
		tunnelName?: string;
		ifaceName?: string;
		onclose: () => void;
	}

	type TrafficModalStats = {
		points: number;
		peakRate: number;
		avgRx: number;
		avgTx: number;
		currentRx: number;
		currentTx: number;
		volumeRx?: number;
		volumeTx?: number;
	};

	let { open, tunnelId, tunnelName = '', ifaceName = '', onclose }: Props = $props();

	const PERIOD_OPTIONS: { value: TrafficPeriod; label: string }[] = [
		{ value: '5m', label: '5 мин' },
		{ value: '10m', label: '10 мин' },
		{ value: '30m', label: '30 мин' },
		{ value: '1h', label: '1 ч' },
		{ value: '3h', label: '3 ч' },
		{ value: '6h', label: '6 ч' },
		{ value: '12h', label: '12 ч' },
		{ value: '24h', label: '24 ч' }
	];

	/** Самый широкий период в UI — по нему пробуем оценить доступную историю при открытии. */
	const WIDEST_PERIOD = PERIOD_OPTIONS[PERIOD_OPTIONS.length - 1].value;

	/** Доля выбранного окна, которую должны покрывать точки (туннель мог прожить меньше). */
	const MIN_PERIOD_COVERAGE = 0.5;

	function dataSpanFromTimestamps(ts: number[]): number {
		if (ts.length < 2) return 0;
		return ts[ts.length - 1] - ts[0];
	}

	/** Наибольший пресет, для которого хватает уже загруженных точек. */
	function pickLargestSufficientPeriod(spanSec: number): TrafficPeriod {
		if (spanSec <= 0) return PERIOD_OPTIONS[0].value;
		for (let i = PERIOD_OPTIONS.length - 1; i >= 0; i--) {
			const { value } = PERIOD_OPTIONS[i];
			if (spanSec >= PERIOD_SECONDS[value] * MIN_PERIOD_COVERAGE) {
				return value;
			}
		}
		return PERIOD_OPTIONS[0].value;
	}

	const PERIOD_LABELS: Record<TrafficPeriod, string> = {
		'5m': 'последние 5 минут',
		'10m': 'последние 10 минут',
		'30m': 'последние 30 минут',
		'1h': 'последний час',
		'3h': 'последние 3 часа',
		'6h': 'последние 6 часов',
		'12h': 'последние 12 часов',
		'24h': 'последние сутки'
	};

	/** Длительность выбранного окна (сек) — для оценки объёма по средним скоростям из API. */
	const PERIOD_SECONDS: Record<TrafficPeriod, number> = {
		'5m': 5 * 60,
		'10m': 10 * 60,
		'30m': 30 * 60,
		'1h': 3600,
		'3h': 3 * 3600,
		'6h': 6 * 3600,
		'12h': 12 * 3600,
		'24h': 24 * 3600
	};

	let loading = $state(true);
	let error = $state<string | null>(null);
	let selectedPeriod = $state<TrafficPeriod>('24h');
	let timestamps = $state<number[]>([]);
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let stats = $state<TrafficModalStats>({
		points: 0,
		peakRate: 0,
		avgRx: 0,
		avgTx: 0,
		currentRx: 0,
		currentTx: 0
	});

	// Live "Сейчас" values driven by SSE while the modal is open. Seeded
	// from the one-shot detail fetch; subsequently tracks the latest point
	// from the shared traffic store so the KPI doesn't stay frozen.
	let liveCurrentRx = $state(0);
	let liveCurrentTx = $state(0);
	let lastLoadToken = 0;
	/** После автоподбора периода при открытии — не повторять для того же туннеля в этой сессии. */
	let periodAutoResolvedFor = '';

	function periodLabel(period: TrafficPeriod): string {
		return PERIOD_LABELS[period];
	}

	function applyTrafficDetail(d: Awaited<ReturnType<typeof fetchTrafficDetail>>) {
		timestamps = d.timestamps;
		rxRates = d.rxRates;
		txRates = d.txRates;
		stats = d.stats as TrafficModalStats;
		liveCurrentRx = d.stats.currentRx;
		liveCurrentTx = d.stats.currentTx;
	}

	async function load(id: string, period: TrafficPeriod, resolveBestPeriod = false) {
		const token = ++lastLoadToken;
		loading = true;
		error = null;
		hoverIndex = null;
		try {
			const d = await fetchTrafficDetail(id, period);
			if (token !== lastLoadToken) return;

			if (resolveBestPeriod) {
				const best = pickLargestSufficientPeriod(dataSpanFromTimestamps(d.timestamps));
				if (best !== period) {
					selectedPeriod = best;
					return load(id, best, false);
				}
				selectedPeriod = best;
			}

			applyTrafficDetail(d);
		} catch (e) {
			if (token !== lastLoadToken) return;
			error = e instanceof Error ? e.message : 'Не удалось загрузить историю';
		} finally {
			if (token !== lastLoadToken) return;
			loading = false;
		}
	}

	// Автоподбор периода только при открытии / смене туннеля (не при клике по пресетам).
	$effect(() => {
		if (!open || !tunnelId) {
			periodAutoResolvedFor = '';
			return;
		}
		if (periodAutoResolvedFor === tunnelId) return;
		periodAutoResolvedFor = tunnelId;
		timestamps = [];
		rxRates = [];
		txRates = [];
		void load(tunnelId, WIDEST_PERIOD, true);
	});

	function selectPeriod(period: TrafficPeriod) {
		if (period === selectedPeriod) return;
		selectedPeriod = period;
		if (!open || !tunnelId) return;
		// Не показываем точки прошлого периода, пока не пришёл ответ API.
		timestamps = [];
		rxRates = [];
		txRates = [];
		void load(tunnelId, period, false);
	}

	// SSE обновляет только «Сейчас» в легенде. Серии графика — только из API
	// (fetchTrafficDetail), иначе буфер карточки (~1 ч) затирает окно 5m/10m.
	$effect(() => {
		if (!open || !tunnelId) return;
		const unsub = subscribeTraffic(() => {
			const { rx, tx } = getTrafficRates(tunnelId);
			if (rx.length > 0) liveCurrentRx = rx[rx.length - 1];
			if (tx.length > 0) liveCurrentTx = tx[tx.length - 1];
		});
		return unsub;
	});

	// ---- Chart geometry --------------------------------------------------
	const CHART_W = 840;
	const CHART_H = 220;
	const PAD_L = 0;
	const PAD_R = 0;
	const PAD_TOP = 16;
	const PAD_BOTTOM = 32;

	let len = $derived(Math.min(rxRates.length, txRates.length));
	let hasData = $derived(len >= 2);

	let dataSpanSec = $derived.by(() => {
		if (timestamps.length < 2) return 0;
		return timestamps[timestamps.length - 1] - timestamps[0];
	});

	let periodSeconds = $derived(PERIOD_SECONDS[selectedPeriod]);

	let hasSufficientPeriodData = $derived(
		hasData && dataSpanSec >= periodSeconds * MIN_PERIOD_COVERAGE
	);

	let maxRate = $derived.by(() => {
		if (!hasData) return 1;
		let m = 1;
		for (let i = 0; i < len; i++) {
			if (rxRates[i] > m) m = rxRates[i];
			if (txRates[i] > m) m = txRates[i];
		}
		return m;
	});

	// y-up model — rate=0 at baseline (CHART_H - PAD_BOTTOM), rate=maxRate at top (PAD_TOP).
	function rateToY(rate: number): number {
		const innerH = CHART_H - PAD_TOP - PAD_BOTTOM;
		const norm = (rate / maxRate) * innerH;
		return CHART_H - PAD_BOTTOM - norm;
	}

	function indexToX(i: number): number {
		if (len < 2) return PAD_L;
		const innerW = CHART_W - PAD_L - PAD_R;
		return PAD_L + (i * innerW) / (len - 1);
	}

	/**
	 * Convert a series of points into a smooth cubic-Bezier path using
	 * Catmull-Rom interpolation (tension ~0.5).
	 */
	function smoothPath(points: [number, number][]): string {
		if (points.length < 2) return '';
		if (points.length === 2) {
			const [[x0, y0], [x1, y1]] = points;
			return `M${x0.toFixed(1)},${y0.toFixed(1)} L${x1.toFixed(1)},${y1.toFixed(1)}`;
		}
		const tension = 0.5;
		let d = `M${points[0][0].toFixed(1)},${points[0][1].toFixed(1)}`;
		for (let i = 0; i < points.length - 1; i++) {
			const p0 = points[Math.max(0, i - 1)];
			const p1 = points[i];
			const p2 = points[i + 1];
			const p3 = points[Math.min(points.length - 1, i + 2)];
			const cp1x = p1[0] + ((p2[0] - p0[0]) / 6) * tension;
			const cp1y = p1[1] + ((p2[1] - p0[1]) / 6) * tension;
			const cp2x = p2[0] - ((p3[0] - p1[0]) / 6) * tension;
			const cp2y = p2[1] - ((p3[1] - p1[1]) / 6) * tension;
			d += ` C${cp1x.toFixed(1)},${cp1y.toFixed(1)} ${cp2x.toFixed(1)},${cp2y.toFixed(1)} ${p2[0].toFixed(1)},${p2[1].toFixed(1)}`;
		}
		return d;
	}

	function buildLine(rates: number[]): string {
		if (len < 2) return '';
		const pts: [number, number][] = [];
		for (let i = 0; i < len; i++) {
			pts.push([indexToX(i), rateToY(rates[i])]);
		}
		return smoothPath(pts);
	}

	function buildArea(linePath: string): string {
		if (!linePath) return '';
		const endX = (CHART_W - PAD_R).toFixed(1);
		const startX = PAD_L.toFixed(1);
		const baseY = (CHART_H - PAD_BOTTOM).toFixed(1);
		return `${linePath} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	let rxLine = $derived(buildLine(rxRates));
	let txLine = $derived(buildLine(txRates));
	let rxArea = $derived(buildArea(rxLine));
	let txArea = $derived(buildArea(txLine));

	function fmtTime(t: number, withDate = false): string {
		const d = new Date(t * 1000);
		const dd = d.getDate().toString().padStart(2, '0');
		const mon = (d.getMonth() + 1).toString().padStart(2, '0');
		const hh = d.getHours().toString().padStart(2, '0');
		const mm = d.getMinutes().toString().padStart(2, '0');
		const ss = d.getSeconds().toString().padStart(2, '0');
		return withDate ? `${dd}.${mon} ${hh}:${mm}` : `${hh}:${mm}:${ss}`;
	}

	let showDateInLabels = $derived(selectedPeriod === '12h' || selectedPeriod === '24h');

	let timeStart = $derived(
		timestamps.length >= 2 ? fmtTime(timestamps[0], showDateInLabels) : ''
	);
	let timeEnd = $derived(
		timestamps.length >= 2 ? fmtTime(timestamps[timestamps.length - 1], showDateInLabels) : ''
	);

	// ---- Hover crosshair + tooltip ---------------------------------------
	let svgEl = $state<SVGSVGElement | null>(null);
	let hoverIndex = $state<number | null>(null);

	function handleMouseMove(e: MouseEvent) {
		if (!svgEl || !hasData) return;
		const rect = svgEl.getBoundingClientRect();
		const mouseX = e.clientX - rect.left;
		// Convert client px to viewBox coordinates so PAD_L/innerW match.
		const scale = CHART_W / rect.width;
		const vbX = mouseX * scale;
		const innerW = CHART_W - PAD_L - PAD_R;
		const step = innerW / (len - 1);
		const idx = Math.round((vbX - PAD_L) / step);
		hoverIndex = Math.max(0, Math.min(len - 1, idx));
	}

	function handleMouseLeave() {
		hoverIndex = null;
	}

	let hoverX = $derived(hoverIndex === null ? 0 : indexToX(hoverIndex));
	let hoverRxY = $derived(hoverIndex === null ? 0 : rateToY(rxRates[hoverIndex]));
	let hoverTxY = $derived(hoverIndex === null ? 0 : rateToY(txRates[hoverIndex]));

	let hoverTime = $derived.by(() => {
		if (hoverIndex === null || timestamps.length === 0) return '';
		return fmtTime(timestamps[Math.min(hoverIndex, timestamps.length - 1)], showDateInLabels);
	});

	// Tooltip placement: flip to left of cursor on the right 30% of the
	// chart so the tooltip doesn't clip off the SVG edge.
	let tooltipFlip = $derived(
		hoverIndex !== null && len >= 2 && hoverIndex / (len - 1) > 0.7
	);
	const TOOLTIP_W = 130;
	const TOOLTIP_H = 58;
	let tooltipX = $derived(tooltipFlip ? hoverX - TOOLTIP_W - 8 : hoverX + 8);
	let tooltipY = $derived(Math.min(hoverRxY, hoverTxY) - TOOLTIP_H - 6);
	let tooltipYClamped = $derived(Math.max(PAD_TOP, tooltipY));
	let showChart = $derived(hasSufficientPeriodData);
	let showChartOverlay = $derived(loading && len >= 2);
	let periodRxBytes = $derived.by(() => {
		if (stats.points >= 2 && stats.volumeRx !== undefined && stats.volumeTx !== undefined) {
			return stats.volumeRx;
		}
		return stats.avgRx * periodSeconds;
	});
	let periodTxBytes = $derived.by(() => {
		if (stats.points >= 2 && stats.volumeRx !== undefined && stats.volumeTx !== undefined) {
			return stats.volumeTx;
		}
		return stats.avgTx * periodSeconds;
	});
	let useServerVolume = $derived(
		stats.points >= 2 && stats.volumeRx !== undefined && stats.volumeTx !== undefined
	);

	let chartStageMessage = $derived.by((): { text: string; error: boolean } | null => {
		if (loading && !showChart) return { text: 'Загрузка…', error: false };
		if (error) return { text: error, error: true };
		if (!loading && !hasSufficientPeriodData) {
			return { text: 'Недостаточно данных за выбранный период', error: false };
		}
		return null;
	});
</script>

<Modal {open} title={tunnelName || tunnelId} size="xl" {onclose}>
	<div class="meta-row">
		<div class="meta-pills">
			{#if ifaceName}<span class="pill">{ifaceName}</span>{/if}
			<span class="pill-muted">{periodLabel(selectedPeriod)}</span>
		</div>
		<div class="period-switch" role="group" aria-label="Период графика трафика">
			{#each PERIOD_OPTIONS as option (option.value)}
				<button
					type="button"
					class="period-btn"
					class:active={selectedPeriod === option.value}
					aria-pressed={selectedPeriod === option.value}
					onclick={() => selectPeriod(option.value)}
				>
					{option.label}
				</button>
			{/each}
		</div>
	</div>

	<div class="stats-line">
		<span
			class="stat stat--period-est"
			title={useServerVolume
				? 'Объём за период на сервере: сумма (скорость×Δt) по сырым точкам истории между опросами.'
				: 'Оценка: средняя скорость × длительность окна (мало точек или ответ API без полей volume).'}
		>
			<span class="label">Трафик:</span>
			{#if !loading && stats.points > 0}
				<span class="val rx">↓ {formatBytes(periodRxBytes)}</span>
				<span class="sep">/</span>
				<span class="val tx">↑ {formatBytes(periodTxBytes)}</span>
			{:else}
				<span class="val">—</span>
			{/if}
		</span>
		<span class="sep">·</span>
		<span class="stat">
			<span class="label">Пик:</span>
			<span class="val">{formatBitRate(stats.peakRate)}</span>
		</span>
		<span class="sep">·</span>
		<span class="stat">
			<span class="label">Средняя:</span>
			<span class="val rx">↓ {formatBitRate(stats.avgRx)}</span>
			<span class="label">/</span>
			<span class="val tx">↑ {formatBitRate(stats.avgTx)}</span>
		</span>
	</div>

	<div class="chart-stage" aria-busy={loading || showChartOverlay}>
		<div class="chart-wrap" class:chart-wrap--loading={showChartOverlay}>
			<div class="chart-top">
				<span class="max-rate">{showChart ? formatBitRate(maxRate) : ''}</span>
			</div>
			<div class="chart-body">
				{#if showChart}
					<!-- svelte-ignore a11y_no_static_element_interactions -->
					<svg
						bind:this={svgEl}
						class="chart-svg"
						viewBox={`0 0 ${CHART_W} ${CHART_H}`}
						preserveAspectRatio="none"
						role="img"
						aria-label={`График трафика за период: ${periodLabel(selectedPeriod)}`}
						onmousemove={handleMouseMove}
						onmouseleave={handleMouseLeave}
					>
					<defs>
						<linearGradient
							id="rx-grad-modal"
							x1="0"
							y1={PAD_TOP}
							x2="0"
							y2={CHART_H - PAD_BOTTOM}
							gradientUnits="userSpaceOnUse"
						>
							<stop offset="0%" stop-color="var(--accent, #60a5fa)" stop-opacity="0.55" />
							<stop offset="100%" stop-color="var(--accent, #60a5fa)" stop-opacity="0" />
						</linearGradient>
						<linearGradient
							id="tx-grad-modal"
							x1="0"
							y1={PAD_TOP}
							x2="0"
							y2={CHART_H - PAD_BOTTOM}
							gradientUnits="userSpaceOnUse"
						>
							<stop offset="0%" stop-color="var(--success, #4ade80)" stop-opacity="0.55" />
							<stop offset="100%" stop-color="var(--success, #4ade80)" stop-opacity="0" />
						</linearGradient>
					</defs>

					<!-- RX first (background), TX on top so smaller series stays visible -->
					<path d={rxArea} fill="url(#rx-grad-modal)" />
					<path
						d={rxLine}
						fill="none"
						stroke="var(--accent, #60a5fa)"
						stroke-width="1.6"
						stroke-linejoin="round"
						stroke-linecap="round"
					/>
					<path d={txArea} fill="url(#tx-grad-modal)" />
					<path
						d={txLine}
						fill="none"
						stroke="var(--success, #4ade80)"
						stroke-width="1.4"
						stroke-linejoin="round"
						stroke-linecap="round"
						opacity="0.95"
					/>

					{#if hoverIndex !== null}
						<g aria-hidden="true">
							<!-- Vertical crosshair -->
							<line
								x1={hoverX}
								y1={PAD_TOP}
								x2={hoverX}
								y2={CHART_H - PAD_BOTTOM}
								stroke="var(--text-muted, #888)"
								stroke-width="0.6"
								stroke-dasharray="2,2"
								opacity="0.8"
							/>
							<!-- Point dots -->
							<circle
								cx={hoverX}
								cy={hoverRxY}
								r="3.5"
								fill="var(--accent, #60a5fa)"
								stroke="var(--bg-primary, #1a1b26)"
								stroke-width="1"
							/>
							<circle
								cx={hoverX}
								cy={hoverTxY}
								r="3.5"
								fill="var(--success, #4ade80)"
								stroke="var(--bg-primary, #1a1b26)"
								stroke-width="1"
							/>
							<!-- Tooltip -->
							<g transform={`translate(${tooltipX}, ${tooltipYClamped})`}>
								<rect
									x="0"
									y="0"
									width={TOOLTIP_W}
									height={TOOLTIP_H}
									rx="4"
									fill="var(--bg-secondary, #16161e)"
									stroke="var(--border, #333)"
									stroke-width="0.6"
									opacity="0.96"
								/>
								<text
									x="8"
									y="16"
									fill="var(--text-muted, #888)"
									font-size="10"
								>{hoverTime}</text>
								<text
									x="8"
									y="32"
									fill="var(--accent, #60a5fa)"
									font-size="11"
								>↓ {formatBitRate(rxRates[hoverIndex])}</text>
								<text
									x="8"
									y="48"
									fill="var(--success, #4ade80)"
									font-size="11"
								>↑ {formatBitRate(txRates[hoverIndex])}</text>
							</g>
						</g>
					{/if}
				</svg>
				{:else if chartStageMessage}
					<p class="chart-state" class:chart-state--err={chartStageMessage.error}>
						{chartStageMessage.text}
					</p>
				{/if}
			</div>
			<div class="chart-bottom">
				<span class="time">{showChart ? timeStart : ''}</span>
				<span class="legend">
					<span class="dot rx"></span>Прием: <span class="val rx">{formatBitRate(liveCurrentRx)}</span>
					<span class="sep">·</span>
					<span class="dot tx"></span>Передача: <span class="val tx">{formatBitRate(liveCurrentTx)}</span>
				</span>
				<span class="time">{showChart ? timeEnd : ''}</span>
			</div>
		</div>
		{#if showChartOverlay}
			<div class="chart-overlay">
				<div class="chart-overlay-label">Обновляем график…</div>
			</div>
		{/if}
	</div>
</Modal>

<style>
	.meta-row {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
		margin-bottom: 12px;
	}
	.meta-pills {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.pill,
	.pill-muted {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
	}
	.pill {
		background: rgba(96, 165, 250, 0.12);
		color: var(--accent, #60a5fa);
	}
	.pill-muted {
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.04));
		color: var(--text-muted, #888);
	}

	.period-switch {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: 0.375rem;
	}

	.period-btn {
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		border-radius: 999px;
		padding: 0.3rem 0.625rem;
		font: inherit;
		font-size: 0.75rem;
		line-height: 1.2;
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			border-color var(--t-fast) ease,
			color var(--t-fast) ease;
	}

	.period-btn:hover {
		border-color: var(--color-border-strong);
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.period-btn.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.period-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.stats-line {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		justify-content: space-between;
		gap: 6px 14px;
		margin-bottom: 12px;
		font-size: 0.8125rem;
		line-height: 1.4;
	}

	.stats-line .stat {
		display: inline-flex;
		align-items: baseline;
		gap: 5px;
		white-space: nowrap;
	}

	.stats-line .stat--period-est {
		row-gap: 2px;
		min-width: 150px;
		white-space: normal;
		max-width: 100%;
	}

	.stats-line .label {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.stats-line .val {
		font-family: inherit;
		font-variant-numeric: tabular-nums;
		font-size: 0.8125rem;
		color: var(--text-primary);
		white-space: nowrap;
	}

	.stats-line .val.rx {
		color: var(--accent);
	}

	.stats-line .val.tx {
		color: var(--success, #4ade80);
	}

	.stats-line .sep {
		color: var(--text-muted);
		opacity: 0.4;
	}

	.chart-stage {
		position: relative;
	}

	.chart-wrap {
		border-radius: var(--radius);
		transition: opacity var(--t-fast) ease;
	}

	.chart-wrap--loading {
		opacity: 0.45;
	}

	.chart-body {
		position: relative;
		width: 100%;
		aspect-ratio: 840 / 220;
		min-height: 120px;
	}

	.chart-body .chart-svg {
		display: block;
		width: 100%;
		height: 100%;
	}

	.chart-state {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		margin: 0;
		padding: 0 1rem;
		text-align: center;
		color: var(--text-muted, #888);
		font-size: 0.8125rem;
	}

	.chart-state--err {
		color: var(--error, #f52a65);
	}

	.chart-top {
		display: flex;
		justify-content: flex-end;
		font-size: 0.6875rem;
		color: var(--text-muted);
		font-variant-numeric: tabular-nums;
		padding: 0 4px 2px;
		min-height: 14px;
	}

	.chart-bottom {
		display: grid;
		grid-template-columns: auto 1fr auto;
		align-items: center;
		column-gap: 16px;
		padding: 4px 4px 0;
		min-height: 2.75rem;
		font-size: 0.6875rem;
		color: var(--text-muted);
	}
	.chart-bottom .time {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.chart-bottom .time:last-child {
		justify-self: end;
	}
	.chart-bottom .legend {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		justify-self: center;
		flex-wrap: wrap;
		justify-content: center;
		row-gap: 2px;
	}
	.chart-bottom .legend .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		display: inline-block;
	}
	.chart-bottom .legend .dot.rx {
		background: var(--accent);
	}
	.chart-bottom .legend .dot.tx {
		background: var(--success, #4ade80);
	}
	.chart-bottom .legend .val {
		font-variant-numeric: tabular-nums;
	}
	.chart-bottom .legend .val.rx {
		color: var(--accent);
	}
	.chart-bottom .legend .val.tx {
		color: var(--success, #4ade80);
	}
	.chart-bottom .legend .sep {
		color: var(--text-muted);
		opacity: 0.4;
		margin: 0 4px;
	}

	.chart-overlay {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		pointer-events: none;
	}

	.chart-overlay-label {
		padding: 0.375rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: 999px;
		background: color-mix(in srgb, var(--color-bg-secondary) 88%, transparent);
		color: var(--color-text-secondary);
		font-size: 0.75rem;
		backdrop-filter: blur(6px);
	}

	@media (max-width: 720px) {
		.period-switch {
			justify-content: flex-start;
		}
	}
</style>
