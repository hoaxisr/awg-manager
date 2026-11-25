<!-- Обвязка теста: карточка с черновиком монтируется и снимается под одной страницей. -->
<script lang="ts">
	import PingTargetsCard from './PingTargetsCard.svelte';
	import { providePageState } from './pageState.svelte';

	interface Props {
		shown: boolean;
		target: string;
		onsave?: (target: string, url: string) => void;
	}

	let { shown, target, onsave = () => {} }: Props = $props();

	providePageState();
</script>

{#if shown}
	<PingTargetsCard {target} url="http://a/204" {onsave} />
{/if}
