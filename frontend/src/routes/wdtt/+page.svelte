<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	onMount(() => {
		goto('/?tab=wdtt', { replaceState: true });
	});
</script>
