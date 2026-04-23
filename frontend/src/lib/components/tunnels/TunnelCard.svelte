<script lang="ts">
	import { untrack } from 'svelte';
	import type { TunnelListItem } from '$lib/types';
	import { TrafficChart } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import TunnelCardHeader from './TunnelCardHeader.svelte';
	import TunnelCardDetails from './TunnelCardDetails.svelte';
	import TunnelCardActions from './TunnelCardActions.svelte';

	interface Props {
		tunnel: TunnelListItem;
		toggleLoading?: boolean;
		deleteLoading?: boolean;
		onToggleOnOff?: () => void;
		ondelete?: () => void;
		ondetail?: (id: string) => void;
	}

	let {
		tunnel,
		toggleLoading = false,
		deleteLoading = false,
		onToggleOnOff,
		ondelete,
		ondetail
	}: Props = $props();

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);

	const CHART_KEY_PREFIX = 'chart_expanded_';
	// svelte-ignore state_referenced_locally — intentional: initial value from localStorage
	let chartExpanded = $state(localStorage.getItem(CHART_KEY_PREFIX + tunnel.id) !== 'false');

	function toggleChart() {
		chartExpanded = !chartExpanded;
		localStorage.setItem(CHART_KEY_PREFIX + tunnel.id, String(chartExpanded));
	}

	let tunnelId = $derived(tunnel.id);

	// Subscribe to traffic data updates (rate changes from feedTraffic/loadHistory)
	$effect(() => {
		const id = tunnelId;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	// Load server history (last hour) once per tunnel on mount.
	// Subsequent updates flow via SSE through feedTraffic.
	let initialLoadDone = false;
	$effect(() => {
		const id = tunnelId;
		if (initialLoadDone) return;
		initialLoadDone = true;
		untrack(() => loadHistory(id));
	});

	let hasData = $derived(rxRates.length >= 2);
</script>

<div class="card flex flex-col gap-4 transition-[border-color] duration-200" class:running={tunnel.status === 'running'} class:transitional={tunnel.status === 'starting' || tunnel.status === 'broken' || tunnel.status === 'needs_start' || tunnel.status === 'needs_stop'} class:state-disabled={tunnel.status === 'disabled'} class:stopped={tunnel.status === 'stopped' || tunnel.status === 'not_created'}>
	<TunnelCardHeader {tunnel} {toggleLoading} onToggleOnOff={() => onToggleOnOff?.()} />
	<TunnelCardDetails {tunnel} />
	<TunnelCardActions
		{tunnel}
		{deleteLoading}
		ondelete={() => ondelete?.()}
	/>

	{#if tunnel.status === 'running'}
		<div class="chart-section">
			<button type="button" class="chart-header" onclick={toggleChart}>
				<span class="chart-label">Трафик</span>
				<span class="chart-chevron" class:expanded={chartExpanded}>▾</span>
			</button>
			<div class="chart-body" class:expanded={chartExpanded && hasData}>
				{#if hasData}
					<TrafficChart
						{rxRates}
						{txRates}
						rxTotal={tunnel.rxBytes ?? 0}
						txTotal={tunnel.txBytes ?? 0}
						height={100}
						onclick={() => ondetail?.(tunnelId)}
					/>
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.running {
		border-color: var(--success);
	}

	.transitional {
		border-color: var(--warning, #f59e0b);
	}

	.state-disabled {
		border-color: var(--text-muted, #6b7280);
	}

	.stopped {
		border-color: var(--error, #ef4444);
	}

	.chart-section {
		margin: 0 -1rem -1rem;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-secondary, rgba(0,0,0,0.15));
		overflow: hidden;
	}

	.chart-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		width: 100%;
		padding: 6px 12px;
		border: none;
		background: none;
		cursor: pointer;
		user-select: none;
		transition: background 0.15s;
	}

	.chart-header:hover {
		background: rgba(255,255,255,0.03);
	}

	.chart-label {
		font-size: 0.6875rem;
		font-weight: 500;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.chart-chevron {
		font-size: 0.875rem;
		color: var(--text-muted);
		transition: transform 0.2s ease;
		transform: rotate(-90deg);
	}

	.chart-chevron.expanded {
		transform: rotate(0deg);
	}

	.chart-body {
		max-height: 0;
		overflow: hidden;
		transition: max-height 0.2s ease;
		padding: 0 12px;
	}

	.chart-body.expanded {
		max-height: 300px;
		padding: 0 12px 4px;
	}
</style>
