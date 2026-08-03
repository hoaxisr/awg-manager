<script lang="ts">
	// «Мониторинг» — вынесено из вкладки /tools?tab=monitoring (навигация v3).
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { MonitoringTab } from '$lib/components/pingcheck';
</script>

<svelte:head>
	<title>Мониторинг - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Мониторинг" />
	<MonitoringTab />
</PageContainer>
