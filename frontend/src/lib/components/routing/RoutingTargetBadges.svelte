<script lang="ts">
	import { tick } from 'svelte';
	import { Badge } from '$lib/components/ui';
	import { countVisibleBadges, readBadgeRowBudgetWidth } from '$lib/utils/fittingBadgeLayout';

	export type RoutingTargetBadgeVariant = 'muted' | 'tunnel';

	interface Props {
		labels: string[];
		/** Optional native tooltips; defaults to labels. */
		titles?: string[];
		/** Noun for overflow aria-label, e.g. «интерфейсов», «туннелей». */
		overflowNoun?: string;
		/** muted — NDMS cards; tunnel — accent tiles как OutboundTile в RuleCard. */
		variant?: RoutingTargetBadgeVariant;
		/** Явный бюджет ширины (px) — RuleCard передаёт зазор до кнопок. */
		budgetWidth?: number;
	}

	let { labels, titles = [], overflowNoun = 'целей', variant = 'muted', budgetWidth }: Props = $props();

	let containerEl = $state<HTMLDivElement | null>(null);
	let measureEl = $state<HTMLDivElement | null>(null);
	let visibleCount = $state(1);

	let visibleLabels = $derived(labels.slice(0, visibleCount));
	let overflowCount = $derived(Math.max(0, labels.length - visibleCount));
	let hiddenLabels = $derived(labels.slice(visibleCount));
	let hiddenTitles = $derived(titles.slice(visibleCount));
	let overflowMeasure = $derived(`+${Math.max(1, labels.length - 1)}`);

	function readGap(): number {
		if (!measureEl) return 4;
		const gap = parseFloat(getComputedStyle(measureEl).columnGap || getComputedStyle(measureEl).gap);
		return Number.isFinite(gap) && gap > 0 ? gap : 4;
	}

	function recalc() {
		if (labels.length === 0) return;
		if (!measureEl || !containerEl) {
			visibleCount = labels.length;
			return;
		}

		const availableWidth = budgetWidth != null && budgetWidth > 0
			? budgetWidth
			: readBadgeRowBudgetWidth(containerEl);
		if (availableWidth <= 0) {
			visibleCount = labels.length;
			return;
		}

		const children = Array.from(measureEl.children) as HTMLElement[];
		if (children.length < 2) return;

		const arrowW = children[0].getBoundingClientRect().width;
		const chipW = children[children.length - 1].getBoundingClientRect().width;
		const badgeEls = children.slice(1, -1);
		const badgeWidths = badgeEls.map((el) => el.getBoundingClientRect().width);

		visibleCount = countVisibleBadges({
			badgeWidths,
			arrowWidth: arrowW,
			overflowChipWidth: chipW,
			gap: readGap(),
			availableWidth,
		});
	}

	// Коалесценция: один recalc на кадр на любое число триггеров (effect,
	// ResizeObserver). Поздние сдвиги layout доводит сам RO следующим событием.
	let recalcScheduled = false;
	async function scheduleRecalc() {
		if (recalcScheduled) return;
		recalcScheduled = true;
		await tick();
		requestAnimationFrame(() => {
			recalcScheduled = false;
			recalc();
		});
	}

	$effect(() => {
		if (!measureEl || !containerEl) return;
		void labels.length;
		void labels.join('\0');
		void variant;
		void budgetWidth;
		void scheduleRecalc();
	});

	$effect(() => {
		if (!containerEl) return;
		const ro = new ResizeObserver(() => {
			void scheduleRecalc();
		});
		ro.observe(containerEl);
		// Предки до бюджетной границы ресайзятся при изменении окна —
		// отдельный window.resize listener не нужен.
		let el: HTMLElement | null = containerEl.parentElement;
		while (el) {
			ro.observe(el);
			if (el.classList.contains('action') || el.classList.contains('card-route') || el.classList.contains('trail')) break;
			el = el.parentElement;
		}
		return () => {
			ro.disconnect();
		};
	});
</script>

{#if labels.length > 0}
	<div class="fitting-badges" class:is-tunnel={variant === 'tunnel'} bind:this={containerEl}>
		<div class="measure-row" bind:this={measureEl} aria-hidden="true">
			{#if variant === 'tunnel'}
				<svg class="route-arrow route-arrow-svg" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<line x1="5" y1="12" x2="19" y2="12" />
					<polyline points="12 5 19 12 12 19" />
				</svg>
			{:else}
				<span class="route-arrow">&rarr;</span>
			{/if}
			{#each labels as label, index (`${label}:${index}`)}
				{#if variant === 'tunnel'}
					<span class="tunnel-chip">{label}</span>
				{:else}
					<Badge variant="muted" mono size="xs">{label}</Badge>
				{/if}
			{/each}
			<Badge variant="dotted" mono size="xs" compact>{overflowMeasure}</Badge>
		</div>

		<div
			class="visible-row"
			class:sole={labels.length === 1 && overflowCount === 0}
		>
			{#if variant === 'tunnel'}
				<svg class="route-arrow route-arrow-svg" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<line x1="5" y1="12" x2="19" y2="12" />
					<polyline points="12 5 19 12 12 19" />
				</svg>
			{:else}
				<span class="route-arrow">&rarr;</span>
			{/if}
			{#each visibleLabels as label, index (index)}
				{#if variant === 'tunnel'}
					<span class="tunnel-chip" title={titles[index] ?? label}>{label}</span>
				{:else}
					<Badge variant="muted" mono size="xs" title={titles[index] ?? label}>{label}</Badge>
				{/if}
			{/each}
			{#if overflowCount > 0}
				<span
					class="overflow-tip"
					tabindex="0"
					role="button"
					aria-label={`Ещё ${overflowCount} ${overflowNoun}`}
				>
					<Badge variant="dotted" mono size="xs" compact>+{overflowCount}</Badge>
					<div class="overflow-pop" role="tooltip">
						<div class="overflow-pop-title">Ещё {overflowCount}</div>
						<ul>
							{#each hiddenLabels as label, index (`${label}:${index}`)}
								<li title={hiddenTitles[index] ?? label}>{label}</li>
							{/each}
						</ul>
					</div>
				</span>
			{/if}
		</div>
	</div>
{/if}

<style>
	.fitting-badges {
		position: relative;
		flex: 1 1 auto;
		min-width: 0;
		width: 100%;
		max-width: 100%;
		overflow-x: clip;
		overflow-y: visible;
	}

	.measure-row,
	.visible-row {
		display: flex;
		align-items: center;
		flex-wrap: nowrap;
		gap: 4px;
		line-height: 1;
		min-width: 0;
	}

	.measure-row {
		visibility: hidden;
		position: absolute;
		top: 0;
		left: 0;
		pointer-events: none;
		height: 0;
		overflow: hidden;
	}

	.visible-row {
		max-width: 100%;
		overflow: visible;
	}

	.visible-row :global(.badge) {
		flex-shrink: 0;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.visible-row.sole :global(.badge) {
		flex-shrink: 1;
		min-width: 0;
	}

	.visible-row.sole .tunnel-chip {
		flex-shrink: 1;
		min-width: 0;
	}

	.route-arrow-svg {
		flex-shrink: 0;
		color: var(--text-muted);
	}

	.tunnel-chip {
		display: inline-flex;
		align-items: center;
		padding: 5px 10px;
		border-radius: 6px;
		font-size: 12px;
		line-height: 1;
		border: 1px solid var(--accent-line);
		background: var(--accent-soft);
		color: var(--accent);
		font-weight: 600;
		font-family: var(--font-mono);
		white-space: nowrap;
		flex-shrink: 0;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.is-tunnel .visible-row {
		gap: 6px;
	}

	.overflow-tip {
		position: relative;
		display: inline-flex;
		overflow: visible;
		outline: none;
	}

	.overflow-tip:hover,
	.overflow-tip:focus-visible {
		z-index: 20;
	}

	.overflow-pop {
		position: absolute;
		right: 0;
		bottom: calc(100% + 8px);
		min-width: 7.5rem;
		max-width: min(16rem, calc(100vw - 16px));
		opacity: 0;
		visibility: hidden;
		transform: translateY(4px);
		transition:
			opacity 0.15s ease,
			transform 0.15s ease,
			visibility 0.15s ease;
		pointer-events: none;
		padding: 6px 8px;
		font-size: 11px;
		line-height: 1.35;
		color: var(--text-secondary);
		background: color-mix(in srgb, var(--bg-tertiary) 90%, var(--bg-secondary));
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		box-shadow: 0 6px 16px rgba(0, 0, 0, 0.3);
		text-align: left;
		white-space: normal;
	}

	.overflow-tip:hover .overflow-pop,
	.overflow-tip:focus-visible .overflow-pop,
	.overflow-tip:focus-within .overflow-pop {
		opacity: 1;
		visibility: visible;
		transform: translateY(0);
	}

	.overflow-pop-title {
		margin-bottom: 4px;
		font-size: 10px;
		font-weight: 600;
		color: var(--text-primary);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.overflow-pop ul {
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.overflow-pop li {
		font-family: var(--font-mono);
		font-size: 10px;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.overflow-pop li + li {
		margin-top: 3px;
	}
</style>
