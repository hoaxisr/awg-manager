<script lang="ts">
    import { goto } from '$app/navigation';
    import { onMount } from 'svelte';
    onMount(() => goto('/diagnostics?tab=logs', { replaceState: true }));
</script>
