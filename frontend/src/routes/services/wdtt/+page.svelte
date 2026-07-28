<script lang="ts">
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { WdttTab } from '$lib/components/wdtt';
</script>

<svelte:head>
	<title>WDTT - AWGM</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="WDTT" />
	<WdttTab />
</PageContainer>
