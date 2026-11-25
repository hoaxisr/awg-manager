<!-- Обвязка теста: карточки монтируются и снимаются под одной страницей, как вкладки. -->
<script lang="ts">
	import type { Component } from 'svelte';
	import { providePageState } from './pageState.svelte';

	interface Props {
		shown: boolean;
		// Карточка любая: свои пропсы у каждой.
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		cards: Array<{ component: Component<any>; props?: Record<string, unknown> }>;
	}

	let { shown, cards }: Props = $props();

	providePageState();
</script>

{#if shown}
	{#each cards as { component: Card, props } (Card)}
		<Card {...props} />
	{/each}
{/if}
