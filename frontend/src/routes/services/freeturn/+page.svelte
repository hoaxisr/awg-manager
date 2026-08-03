<script lang="ts">
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { FreeTurnTab } from '$lib/components/freeturn';
</script>

<svelte:head>
	<title>FreeTurn - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="FreeTurn" />
	<FreeTurnTab />
</PageContainer>
