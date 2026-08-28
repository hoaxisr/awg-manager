<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	onMount(() => {
		goto('/proxy?tab=exit', { replaceState: true });
	});
</script>
