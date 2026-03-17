<script lang="ts">
	import type { TunnelListItem } from '$lib/types';
	import { TrafficChart } from '$lib/components/ui';
	import { getTrafficRates, getTrafficPeriod, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import TunnelCardHeader from './TunnelCardHeader.svelte';
	import TunnelCardDetails from './TunnelCardDetails.svelte';
	import TunnelCardActions from './TunnelCardActions.svelte';

	interface Props {
		tunnel: TunnelListItem;
		toggleLoading?: boolean;
		deleteLoading?: boolean;
		onToggleOnOff?: () => void;
		ondelete?: () => void;
	}

	let {
		tunnel,
		toggleLoading = false,
		deleteLoading = false,
		onToggleOnOff,
		ondelete
	}: Props = $props();

	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	// svelte-ignore state_referenced_locally — intentional: initial value from tunnel.id, then user-controlled
	let period = $state(getTrafficPeriod(tunnel.id));

	// Subscribe to traffic data updates (rate changes from feedTraffic/loadHistory)
	$effect(() => {
		const id = tunnel.id;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	// Load server history on mount (once per tunnel)
	$effect(() => {
		loadHistory(tunnel.id, period);
	});

	function handlePeriodChange(newPeriod: string) {
		period = newPeriod;
		loadHistory(tunnel.id, newPeriod);
	}
</script>

<div class="card flex flex-col gap-4 transition-[border-color] duration-200" class:running={tunnel.status === 'running'} class:transitional={tunnel.status === 'starting' || tunnel.status === 'broken' || tunnel.status === 'needs_start' || tunnel.status === 'needs_stop'} class:state-disabled={tunnel.status === 'disabled'} class:stopped={tunnel.status === 'stopped' || tunnel.status === 'not_created'}>
	<TunnelCardHeader {tunnel} {toggleLoading} onToggleOnOff={() => onToggleOnOff?.()} />
	<TunnelCardDetails {tunnel} />
	<TunnelCardActions
		{tunnel}
		{deleteLoading}
		ondelete={() => ondelete?.()}
	/>

	{#if tunnel.status === 'running' && rxRates.length >= 2}
		<div class="chart-wrap">
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={tunnel.rxBytes ?? 0}
				txTotal={tunnel.txBytes ?? 0}
				height={100}
				{period}
				onPeriodChange={handlePeriodChange}
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

	.chart-wrap {
		margin: 0 -1rem -1rem;
		padding: 8px 12px 4px;
		overflow: hidden;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-secondary, rgba(0,0,0,0.15));
	}
</style>
