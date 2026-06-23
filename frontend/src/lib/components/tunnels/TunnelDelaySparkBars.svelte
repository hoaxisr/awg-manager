<script lang="ts">
	import type { SingboxDelayState } from '$lib/utils/singboxDelay';
	import { latencyTier } from '$lib/utils/latencyTier';

	interface Props {
		history: number[];
		state: SingboxDelayState | 'stopped';
		maxBars?: number;
		layout?: 'list' | 'dense' | 'compact';
		onclick?: () => void;
		title?: string;
		colorPerBar?: boolean;
	}

	let {
		history,
		state,
		maxBars = 14,
		layout,
		onclick,
		title = 'Клик — обновить delay',
		colorPerBar = false,
	}: Props = $props();

	const max = $derived(
		history.length > 0 ? Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100) : 100,
	);
	const bars = $derived(history.slice(-maxBars));
</script>

<div
	class="tunnel-delay-spark {state}"
	class:tunnel-delay-spark--list={layout === 'list'}
	class:tunnel-delay-spark--dense={layout === 'dense'}
	class:tunnel-delay-spark--compact={layout === 'compact'}
	{title}
	role="button"
	tabindex="0"
	onclick={onclick}
	onkeydown={(e) => {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			onclick?.();
		}
	}}
>
	{#if bars.length === 0}
		{#each Array(10) as _, i (i)}
			<div class="bar empty"></div>
		{/each}
	{:else}
		{#each bars as d, i (i)}
			<div
				class="bar"
				class:fail={colorPerBar && (d <= 0 || latencyTier(d) === 'error')}
				class:slow={colorPerBar && d > 0 && latencyTier(d) === 'warning'}
				style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"
			></div>
		{/each}
	{/if}
</div>
