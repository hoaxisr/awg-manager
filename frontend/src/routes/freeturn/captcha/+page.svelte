<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	onMount(() => {
		goto('/?tab=freeturn', { replaceState: true });
	});
</script>
