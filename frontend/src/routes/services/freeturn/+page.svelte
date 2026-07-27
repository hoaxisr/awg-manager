<script lang="ts">
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { FreeTurnTab } from '$lib/components/freeturn';
</script>

<svelte:head>
	<title>FreeTurn - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="FreeTurn" />
	<FreeTurnTab />
</PageContainer>
