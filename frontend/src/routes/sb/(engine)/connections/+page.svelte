<script lang="ts">
	// «Соединения движка» — живые соединения sing-box через Clash WS.
	// Имя пункта с уточнением «движка» обязательно: в свёрнутой группе голые
	// «Соединения» неотличимы от верхнеуровневого conntrack-раздела /connections.
	// Страница живая с 5D1: ConnectionsSubTab самодостаточен — свой WS, фильтры,
	// сортировка, пагинация, breakdown и bulk-kill внутри.
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { ConnectionsSubTab } from '$lib/components/routing/singboxRouter';
</script>

<svelte:head>
	<title>Соединения движка - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Соединения движка" />
	<ConnectionsSubTab />
</PageContainer>
