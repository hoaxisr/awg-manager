<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';

	onMount(() => {
		goto('/?tab=freeturn&captcha=1', { replaceState: true });
	});
</script>
