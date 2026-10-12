<script lang="ts">
	import { TrafficSparkline } from '$lib/components/ui';
	import { formatBitRate } from '$lib/utils/format';
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';

	interface Props {
		rxRate?: number;
		txRate?: number;
		rxLabel?: string;
		txLabel?: string;
		rxData: number[];
		txData: number[];
		onclick?: () => void;
		title?: string;
		extra?: Snippet;
	}

	let {
		rxRate,
		txRate,
		rxLabel,
		txLabel,
		rxData,
		txData,
		onclick,
		title: titleProp,
		extra,
	}: Props = $props();

	const title = $derived(titleProp ?? m.tunnels_traffic_open_chart());
	const displayRx = $derived(rxLabel ?? `↓ ${formatBitRate(rxRate ?? 0)}`);
	const displayTx = $derived(txLabel ?? `↑ ${formatBitRate(txRate ?? 0)}`);
</script>

{#if onclick}
	<button type="button" class="tunnel-list-traffic-cell awg-rate-button" {title} {onclick}>
		<span class="traffic-rate rx">{displayRx}</span>
		<TrafficSparkline {rxData} {txData} responsive height={18} />
		<span class="traffic-rate tx">{displayTx}</span>
		{#if extra}
			{@render extra()}
		{/if}
	</button>
{:else}
	<div class="tunnel-list-traffic-cell" {title}>
		<span class="traffic-rate rx">{displayRx}</span>
		<TrafficSparkline {rxData} {txData} responsive height={18} />
		<span class="traffic-rate tx">{displayTx}</span>
		{#if extra}
			{@render extra()}
		{/if}
	</div>
{/if}
