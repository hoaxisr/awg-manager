<script lang="ts">
	import { formatBitRate } from '$lib/utils/format';
	import { fetchTrafficDetail, subscribeTraffic, getTrafficRates } from '$lib/stores/traffic';
	import Modal from './Modal.svelte';

	interface Props {
		open: boolean;
		tunnelId: string;
		tunnelName?: string;
		ifaceName?: string;
		onclose: () => void;
	}

	let { open, tunnelId, tunnelName = '', ifaceName = '', onclose }: Props = $props();

	let loading = $state(true);
	let error = $state<string | null>(null);
	let timestamps = $state<number[]>([]);
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let stats = $state({
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

	async function load(id: string) {
		loading = true;
		error = null;
		try {
			const d = await fetchTrafficDetail(id);
			timestamps = d.timestamps;
			rxRates = d.rxRates;
			txRates = d.txRates;
			stats = d.stats;
			liveCurrentRx = d.stats.currentRx;
			liveCurrentTx = d.stats.currentTx;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить историю';
		} finally {
			loading = false;
		}
	}

	// Re-fetch whenever the modal opens or the tunnel id changes while open.
	$effect(() => {
		if (open && tunnelId) {
			load(tunnelId);
		}
	});

	// Subscribe to SSE traffic updates while the modal is open. Both the
	// legend KPIs and the chart itself advance in real time — the live
	// buffer in the shared store is small, so a full swap is cheap.
	$effect(() => {
		if (!open || !tunnelId) return;
		const unsub = subscribeTraffic(() => {
			const { rx, tx } = getTrafficRates(tunnelId);
			if (rx.length > 0) liveCurrentRx = rx[rx.length - 1];
			if (tx.length > 0) liveCurrentTx = tx[tx.length - 1];
			// Advance the chart's right edge in real time. Only overwrite
			// if the live buffer has grown past what we fetched — never
			// shrink the history window.
			if (rx.length > rxRates.length) {
				rxRates = rx.slice();
				txRates = tx.slice();
			}
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

	// Two timestamps at bottom corners (first + last point).
	function fmtTime(t: number): string {
		const d = new Date(t * 1000);
		const hh = d.getHours().toString().padStart(2, '0');
		const mm = d.getMinutes().toString().padStart(2, '0');
		const ss = d.getSeconds().toString().padStart(2, '0');
		return `${hh}:${mm}:${ss}`;
	}

	let timeStart = $derived(timestamps.length >= 2 ? fmtTime(timestamps[0]) : '');
	let timeEnd = $derived(
		timestamps.length >= 2 ? fmtTime(timestamps[timestamps.length - 1]) : ''
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
		return fmtTime(timestamps[Math.min(hoverIndex, timestamps.length - 1)]);
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
</script>

<Modal {open} title={tunnelName || tunnelId} size="xl" {onclose}>
	<div class="meta-row">
		{#if ifaceName}<span class="pill">{ifaceName}</span>{/if}
		<span class="pill-muted">последние 24 часа</span>
	</div>

	<div class="stats-line">
		<span class="stat">
			<span class="label">Прием:</span>
			<span class="val rx">{formatBitRate(liveCurrentRx)}</span>
		</span>
		<span class="sep">·</span>
		<span class="stat">
			<span class="label">Передача:</span>
			<span class="val tx">{formatBitRate(liveCurrentTx)}</span>
		</span>
		<span class="sep">·</span>
		<span class="stat">
			<span class="label">Пик:</span>
			<span class="val">{formatBitRate(stats.peakRate)}</span>
		</span>
		<span class="sep">·</span>
		<span class="stat">
			<span class="label">Среднее</span>
			<span class="val rx">↓ {formatBitRate(stats.avgRx)}</span>
			<span class="label">/</span>
			<span class="val tx">↑ {formatBitRate(stats.avgTx)}</span>
		</span>
	</div>

	{#if loading}
		<div class="state-msg">Загрузка…</div>
	{:else if error}
		<div class="state-msg state-err">{error}</div>
	{:else if !hasData}
		<div class="state-msg">Недостаточно данных за 24 часа</div>
	{:else}
		<div class="chart-wrap">
			<div class="chart-top">
				<span class="max-rate">{formatBitRate(maxRate)}</span>
			</div>
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<svg
				bind:this={svgEl}
				class="chart-svg"
				viewBox={`0 0 ${CHART_W} ${CHART_H}`}
				preserveAspectRatio="none"
				role="img"
				aria-label="График трафика за 24 часа"
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
			<div class="chart-bottom">
				<span class="time">{timeStart}</span>
				<span class="legend">
					<span class="dot rx"></span>Прием: <span class="val rx">{formatBitRate(liveCurrentRx)}</span>
					<span class="sep">·</span>
					<span class="dot tx"></span>Передача: <span class="val tx">{formatBitRate(liveCurrentTx)}</span>
				</span>
				<span class="time">{timeEnd}</span>
			</div>
		</div>
	{/if}
</Modal>

<style>
	.meta-row {
		display: flex;
		gap: 8px;
		margin-bottom: 12px;
	}
	.pill,
	.pill-muted {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 4px;
	}
	.pill {
		background: rgba(96, 165, 250, 0.12);
		color: var(--accent, #60a5fa);
	}
	.pill-muted {
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.04));
		color: var(--text-muted, #888);
	}

	.stats-line {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
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

	.chart-wrap {
		border-radius: 8px;
	}
	.chart-svg {
		display: block;
		width: 100%;
		height: auto;
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
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 4px 4px 0;
		font-size: 0.6875rem;
		color: var(--text-muted);
	}
	.chart-bottom .time {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.chart-bottom .legend {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		white-space: nowrap;
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

	.state-msg {
		padding: 40px 0;
		text-align: center;
		color: var(--text-muted, #888);
		font-size: 0.8125rem;
	}
	.state-msg.state-err {
		color: var(--error, #f52a65);
	}
</style>
