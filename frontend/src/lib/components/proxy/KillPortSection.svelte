<script lang="ts">
	// EX-46..48 / SH-69..71 — единственное место «Kill port» на странице
	// (решение Q10 ИА): одна строка на каждый порт инстанса.
	import ListenPortKillButton from '../proxy-panel/ListenPortKillButton.svelte';

	interface Props {
		/** Все порты инстанса; строка рисуется на каждый. */
		ports: { listen: string; proto?: 'udp' | 'tcp' }[];
	}

	let { ports }: Props = $props();

	const shown = $derived(ports.filter((p) => p.listen?.trim()));
</script>

{#if shown.length}
	<p class="sub-title">Освобождение порта</p>
	{#each shown as port (port.listen)}
		<ListenPortKillButton variant="section" listen={port.listen} proto={port.proto ?? 'udp'} />
	{/each}
{/if}

<style>
	.sub-title {
		margin: 1.25rem 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
</style>
