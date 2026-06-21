<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	// FakeIP переехал во вкладку sing-box router. Сохраняем deep-links/закладки.
	onMount(() => {
		goto('/routing?tab=fakeip', { replaceState: true });
	});
</script>
