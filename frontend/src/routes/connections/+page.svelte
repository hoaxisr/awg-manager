<script lang="ts">
	// «Соединения» — conntrack роутера целиком (GET /connections). Не путать с
	// соединениями sing-box: те приходят по Clash WS и живут в разделе движка.
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import ConnectionsTab from './ConnectionsTab.svelte';
</script>

<svelte:head>
	<title>Соединения - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Соединения" />
	<ConnectionsTab />
</PageContainer>
