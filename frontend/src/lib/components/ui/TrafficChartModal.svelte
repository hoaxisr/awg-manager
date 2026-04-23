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

	// Subscribe to SSE traffic updates while the modal is open so the
	// "Сейчас" KPI reflects the latest rate rather than the value at
	// open-time. Only the KPI updates; the 24h chart itself is not
	// re-rendered on every tick (would be wasteful).
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
	const CHART_W = 800;
	const CHART_H = 200;
	const PAD_L = 80;
	const PAD_R = 8;
	const PAD_TOP = 8;
	const PAD_BOTTOM = 20;

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

	function buildLine(rates: number[]): string {
		if (len < 2) return '';
		const innerW = CHART_W - PAD_L - PAD_R;
		const innerH = CHART_H - PAD_TOP - PAD_BOTTOM;
		const step = innerW / (len - 1);
		const pts: string[] = [];
		for (let i = 0; i < len; i++) {
			const x = PAD_L + i * step;
			const norm = (rates[i] / maxRate) * innerH;
			const y = CHART_H - PAD_BOTTOM - norm;
			pts.push(`${x.toFixed(1)},${y.toFixed(1)}`);
		}
		return `M${pts.join(' L')}`;
	}

	function buildArea(line: string): string {
		if (!line) return '';
		const endX = (CHART_W - PAD_R).toFixed(1);
		const startX = PAD_L.toFixed(1);
		const baseY = (CHART_H - PAD_BOTTOM).toFixed(1);
		return `${line} L${endX},${baseY} L${startX},${baseY} Z`;
	}

	let rxLine = $derived(buildLine(rxRates));
	let txLine = $derived(buildLine(txRates));
	let rxArea = $derived(buildArea(rxLine));

	// 3 horizontal grid lines at 25% / 50% / 75%; label each with the rate.
	let gridLines = $derived.by(() => {
		const innerH = CHART_H - PAD_TOP - PAD_BOTTOM;
		return [0.25, 0.5, 0.75].map((frac) => ({
			y: CHART_H - PAD_BOTTOM - innerH * frac,
			label: formatBitRate(maxRate * frac)
		}));
	});

	// Time axis — 5 labels across the window.
	let xLabels = $derived.by(() => {
		if (timestamps.length < 2) return [] as { x: number; label: string }[];
		const t0 = timestamps[0];
		const tN = timestamps[timestamps.length - 1];
		const innerW = CHART_W - PAD_L - PAD_R;
		const out: { x: number; label: string }[] = [];
		for (let i = 0; i <= 4; i++) {
			const frac = i / 4;
			const x = PAD_L + innerW * frac;
			const t = t0 + (tN - t0) * frac;
			const d = new Date(t * 1000);
			const hh = d.getHours().toString().padStart(2, '0');
			const mm = d.getMinutes().toString().padStart(2, '0');
			out.push({ x, label: `${hh}:${mm}` });
		}
		return out;
	});

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

	let hoverX = $derived.by(() => {
		if (hoverIndex === null || len < 2) return 0;
		const innerW = CHART_W - PAD_L - PAD_R;
		const step = innerW / (len - 1);
		return PAD_L + hoverIndex * step;
	});

	let hoverRxY = $derived.by(() => {
		if (hoverIndex === null) return 0;
		const innerH = CHART_H - PAD_TOP - PAD_BOTTOM;
		const norm = (rxRates[hoverIndex] / maxRate) * innerH;
		return CHART_H - PAD_BOTTOM - norm;
	});

	let hoverTxY = $derived.by(() => {
		if (hoverIndex === null) return 0;
		const innerH = CHART_H - PAD_TOP - PAD_BOTTOM;
		const norm = (txRates[hoverIndex] / maxRate) * innerH;
		return CHART_H - PAD_BOTTOM - norm;
	});

	let hoverTime = $derived.by(() => {
		if (hoverIndex === null || timestamps.length === 0) return '';
		const t = timestamps[Math.min(hoverIndex, timestamps.length - 1)];
		const d = new Date(t * 1000);
		const hh = d.getHours().toString().padStart(2, '0');
		const mm = d.getMinutes().toString().padStart(2, '0');
		return `${hh}:${mm}`;
	});

	// Tooltip placement: flip to left of cursor on the right 30% of the
	// chart so the tooltip doesn't clip off the SVG edge.
	let tooltipFlip = $derived(
		hoverIndex !== null && len >= 2 && hoverIndex / (len - 1) > 0.7
	);
	const TOOLTIP_W = 120;
	const TOOLTIP_H = 52;
	let tooltipX = $derived(tooltipFlip ? hoverX - TOOLTIP_W - 8 : hoverX + 8);
	let tooltipY = $derived(Math.min(hoverRxY, hoverTxY) - TOOLTIP_H - 6);
	let tooltipYClamped = $derived(Math.max(PAD_TOP, tooltipY));
</script>

<Modal {open} title={tunnelName || tunnelId} size="xl" {onclose}>
	<div class="meta-row">
		{#if ifaceName}<span class="pill">{ifaceName}</span>{/if}
		<span class="pill-muted">последние 24 часа</span>
	</div>

	<div class="kpi-grid">
		<div class="kpi">
			<div class="kpi-label">Сейчас ↓</div>
			<div class="kpi-val rx">{formatBitRate(liveCurrentRx)}</div>
		</div>
		<div class="kpi">
			<div class="kpi-label">Пик</div>
			<div class="kpi-val">{formatBitRate(stats.peakRate)}</div>
		</div>
		<div class="kpi">
			<div class="kpi-label">Среднее ↓ / ↑</div>
			<div class="kpi-val">
				<span class="rx">{formatBitRate(stats.avgRx)}</span>
				<span class="sep">/</span>
				<span class="tx">{formatBitRate(stats.avgTx)}</span>
			</div>
		</div>
	</div>

	{#if loading}
		<div class="state-msg">Загрузка…</div>
	{:else if error}
		<div class="state-msg state-err">{error}</div>
	{:else if !hasData}
		<div class="state-msg">Недостаточно данных за 24 часа</div>
	{:else}
		<div class="chart-wrap">
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
					<linearGradient id="rx-modal-grad" x1="0" x2="0" y1="0" y2="1">
						<stop offset="0%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0.5" />
						<stop offset="100%" stop-color="var(--accent, #7aa2f7)" stop-opacity="0" />
					</linearGradient>
				</defs>

				{#each gridLines as gl}
					<line
						x1={PAD_L}
						y1={gl.y}
						x2={CHART_W - PAD_R}
						y2={gl.y}
						stroke="var(--border, #333)"
						stroke-width="0.4"
						stroke-dasharray="2,3"
						opacity="0.4"
					/>
					<text
						x={PAD_L - 4}
						y={gl.y}
						text-anchor="end"
						dominant-baseline="middle"
						fill="var(--text-secondary, #bbb)"
						font-size="9"
						font-family="var(--font-mono, monospace)"
					>{gl.label}</text>
				{/each}

				<path d={rxArea} fill="url(#rx-modal-grad)" />
				<path
					d={rxLine}
					fill="none"
					stroke="var(--accent, #7aa2f7)"
					stroke-width="1.5"
					stroke-linejoin="round"
				/>
				<path
					d={txLine}
					fill="none"
					stroke="var(--warning, #e0af68)"
					stroke-width="1.3"
					stroke-linejoin="round"
				/>

				{#each xLabels as xl}
					<text
						x={xl.x}
						y={CHART_H - 4}
						text-anchor="middle"
						fill="var(--text-secondary, #bbb)"
						font-size="9"
						font-family="var(--font-mono, monospace)"
					>{xl.label}</text>
				{/each}

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
							r="3"
							fill="var(--accent, #7aa2f7)"
							stroke="var(--bg-primary, #1a1b26)"
							stroke-width="1"
						/>
						<circle
							cx={hoverX}
							cy={hoverTxY}
							r="3"
							fill="var(--warning, #e0af68)"
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
								y="14"
								fill="var(--text-muted, #888)"
								font-size="9"
								font-family="var(--font-mono, monospace)"
							>{hoverTime}</text>
							<text
								x="8"
								y="28"
								fill="var(--accent, #7aa2f7)"
								font-size="10"
								font-family="var(--font-mono, monospace)"
							>↓ {formatBitRate(rxRates[hoverIndex])}</text>
							<text
								x="8"
								y="42"
								fill="var(--warning, #e0af68)"
								font-size="10"
								font-family="var(--font-mono, monospace)"
							>↑ {formatBitRate(txRates[hoverIndex])}</text>
						</g>
					</g>
				{/if}
			</svg>
		</div>

		<div class="legend">
			<span class="legend-item rx"><span class="sw"></span>RX</span>
			<span class="legend-item tx"><span class="sw"></span>TX</span>
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
		font-family: var(--font-mono, monospace);
		font-size: 11px;
		padding: 2px 8px;
		border-radius: 4px;
	}
	.pill {
		background: rgba(122, 162, 247, 0.12);
		color: var(--accent, #7aa2f7);
	}
	.pill-muted {
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.04));
		color: var(--text-muted, #888);
	}

	.kpi-grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
		margin-bottom: 16px;
	}
	.kpi {
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.04));
		border-radius: 6px;
		padding: 8px 10px;
	}
	.kpi-label {
		font-size: 9px;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--text-muted, #888);
	}
	.kpi-val {
		font-size: 15px;
		font-weight: 600;
		font-family: var(--font-mono, monospace);
		color: var(--text-primary);
	}
	.kpi-val .rx {
		color: var(--accent, #7aa2f7);
	}
	.kpi-val .tx {
		color: var(--warning, #e0af68);
	}
	.kpi-val .sep {
		color: var(--text-muted, #888);
		margin: 0 3px;
	}

	.chart-wrap {
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.04));
		border-radius: 8px;
		padding: 8px;
	}
	.chart-svg {
		display: block;
		width: 100%;
		height: auto;
	}

	.legend {
		display: flex;
		gap: 14px;
		justify-content: flex-end;
		font-size: 11px;
		font-family: var(--font-mono, monospace);
		margin-top: 10px;
		color: var(--text-muted, #888);
	}
	.legend-item .sw {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 2px;
		margin-right: 4px;
		vertical-align: middle;
	}
	.legend-item.rx .sw {
		background: var(--accent, #7aa2f7);
	}
	.legend-item.tx .sw {
		background: var(--warning, #e0af68);
	}

	.state-msg {
		padding: 40px 0;
		text-align: center;
		color: var(--text-muted, #888);
		font-size: 13px;
	}
	.state-msg.state-err {
		color: var(--error, #f52a65);
	}
</style>
