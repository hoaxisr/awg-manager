<script lang="ts">
	// Временный редирект-стаб: контейнер главной расщеплён по разделам
	// (навигация v3, фаза 2). В фазе 3 сюда встанет «Обзор» — статусная
	// read-only сводка, и редирект уйдёт.
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	onMount(() => goto('/awg/tunnels', { replaceState: true }));
</script>
