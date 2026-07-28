<script lang="ts">
    // Страница «Роутер · NDMS» — бывшая вкладка /routing?tab=dns (навигация v3).
    import { onMount, onDestroy } from 'svelte';
    import { page } from '$app/stores';
    import { routing, subscribeRouting, routingDnsNdmsTabReady } from '$lib/stores/routing';
    import { systemInfo } from '$lib/stores/system';
    import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
    import { DnsRoutesTab, RoutingRefreshButton, RoutingSearchButton } from '$lib/components/routing';
    import { createEditParam } from '$lib/utils/editParam.svelte';

    // Секционные сторы маршрутизации поллятся, пока страница открыта.
    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    const edit = createEditParam(() => $page.url);

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let systemKnown = $derived($systemInfo.lastFetchedAt > 0 || $systemInfo.status === 'error');
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);
</script>

<svelte:head>
    <title>NDMS - AWGM</title>
</svelte:head>

<PageContainer width="full">
    <div class="routing-page">
        <PageHeader title="NDMS">
            {#snippet actions()}
                <RoutingSearchButton
                    dnsRoutes={$routing.dnsRoutes}
                    staticRoutes={$routing.staticRoutes}
                    tunnels={$routing.tunnels}
                />
                <RoutingRefreshButton />
            {/snippet}
        </PageHeader>

        {#if systemKnown && !isOS5}
            <!-- NDMS dns-proxy с object-group fqdn есть только в OS5; на OS4
                 вкладки не было вовсе — на выделенном маршруте вместо неё
                 заглушка (уводить пользователя некуда). -->
            <EmptyState
                title="Раздел доступен на Keenetic OS 5"
                description="NDMS-маршрутизация по доменам требует прошивки OS 5. На OS 4 используйте HR Neo или маршруты по IP-адресам."
            />
        {:else}
            <DnsRoutesTab
                dnsRoutes={$routing.dnsRoutes}
                routingTunnels={$routing.tunnels}
                editRuleId={edit.id}
                editRuleCounter={edit.counter}
                {isOS5}
                {hasDnsEngine}
                bodyLoading={!$routingDnsNdmsTabReady}
            />
        {/if}
    </div>
</PageContainer>

<style>
	@media (max-width: 640px) {
		.routing-page :global(.page-header .actions) {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
			gap: 0.5rem;
			width: 100%;
		}

		.routing-page :global(.page-header .actions .btn) {
			width: 100%;
			min-height: 28px;
			justify-content: center;
		}
	}
</style>
