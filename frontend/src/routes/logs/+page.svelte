<script lang="ts">
	// «Журнал» — действия приложения (корзина app). Вынесено из вкладки
	// /tools?tab=logs (навигация v3, подэтап 5A). Логи движка sing-box живут
	// отдельной страницей со своей корзиной: у LogsTerminal одна корзина на
	// инстанс, переключателя нет.
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { LogsTerminal } from '$lib/components/diagnostics';
</script>

<svelte:head>
	<title>Журнал - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Журнал" />
	<LogsTerminal lockBucket="app" />
</PageContainer>
