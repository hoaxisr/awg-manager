<script lang="ts">
    // Страница «Роутер · IP-адреса» — бывшая вкладка /routing?tab=ip (навигация v3).
    import { onMount, onDestroy } from 'svelte';
    import { page } from '$app/stores';
    import { routing, subscribeRouting, routingIpTabReady } from '$lib/stores/routing';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { IpRoutesTab, RoutingRefreshButton } from '$lib/components/routing';
    import { createEditParam } from '$lib/utils/editParam.svelte';

    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    // Сюда приводит поиск по правилам (кнопка живёт на NDMS/HR Neo).
    const edit = createEditParam(() => $page.url);
</script>

<svelte:head>
    <title>IP-адреса - AWGM</title>
</svelte:head>

<PageContainer width="full">
    <PageHeader title="IP-адреса">
        {#snippet actions()}
            <RoutingRefreshButton />
        {/snippet}
    </PageHeader>

    <IpRoutesTab
        ipRoutes={$routing.staticRoutes}
        routingTunnels={$routing.tunnels}
        editRuleId={edit.id}
        editRuleCounter={edit.counter}
        bodyLoading={!$routingIpTabReady}
    />
</PageContainer>
