<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	// Прокси живёт страницей /proxy (ia.md §1.1). Маршрут оставлен как redirect
	// для старых закладок и ссылок в документации.
	onMount(() => {
		goto('/proxy?tab=exit', { replaceState: true });
	});
</script>
