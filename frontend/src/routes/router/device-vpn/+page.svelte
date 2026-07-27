<script lang="ts">
    // Страница «Роутер · VPN для устройств» — бывшая вкладка
    // /routing?tab=clientvpn (навигация v3).
    import { onMount, onDestroy } from 'svelte';
    import { routing, subscribeRouting, routingClientVpnTabReady } from '$lib/stores/routing';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { ClientRoutesTab, RoutingRefreshButton } from '$lib/components/routing';

    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });
</script>

<svelte:head>
    <title>VPN для устройств - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
    <PageHeader title="VPN для устройств">
        {#snippet actions()}
            <RoutingRefreshButton />
        {/snippet}
    </PageHeader>

    <ClientRoutesTab
        clientRoutes={$routing.clientRoutes}
        policyDevices={$routing.policyDevices}
        routingTunnels={$routing.tunnels}
        bodyLoading={!$routingClientVpnTabReady}
    />
</PageContainer>
