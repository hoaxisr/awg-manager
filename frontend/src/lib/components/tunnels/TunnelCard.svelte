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

	{#if tunnel.status === 'running' && hasData}
		<div class="chart-section">
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={tunnel.rxBytes ?? 0}
				txTotal={tunnel.txBytes ?? 0}
				height={68}
				onclick={() => ondetail?.(tunnelId)}
			/>
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
		padding: 6px 12px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-secondary, rgba(0,0,0,0.15));
	}
</style>
