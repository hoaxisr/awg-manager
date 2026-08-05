<script lang="ts">
	// «Журнал движка» — корзина singbox (stdout движка + process/runtime-события).
	// Действия приложения живут в верхнеуровневом «Журнале» (корзина app):
	// у LogsTerminal одна корзина на инстанс, переключателя нет. Уточнение
	// «движка» в подписи обязательно — в свёрнутой группе голый «Журнал»
	// неотличим от /logs.
	//
	// storagePrefix: до разбора терминал журнала был в двух местах с разными
	// пространствами ключей — 'awgm.sb-router' (SingboxRouterRedesignPage) и
	// 'awgm.fakeip' (FakeIPTab). Страница одна, значит и префикс один; взят
	// 'awgm.sb-router': это поверхность по умолчанию (FakeIP включается только
	// при routingMode === 'fakeip-tun'), поэтому у большинства сохранённые
	// фильтры лежат именно там и переживут разбор.
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { LogsTerminal } from '$lib/components/diagnostics';
</script>

<svelte:head>
	<title>Журнал движка - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Журнал движка" />
	<LogsTerminal lockBucket="singbox" storagePrefix="awgm.sb-router" />
</PageContainer>
