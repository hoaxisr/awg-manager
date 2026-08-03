<script lang="ts">
    // Страница «Сервисы · HR Neo» — бывшая вкладка /routing?tab=hrneo
    // (навигация v3). Вкладка существовала только при установленном
    // HydraRoute Neo; на выделенном маршруте вместо неё заглушка.
    import { onMount, onDestroy } from 'svelte';
    import { page } from '$app/stores';
    import { routing, subscribeRouting, hydrarouteStatusStore } from '$lib/stores/routing';
    import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
    import { RoutingRefreshButton, RoutingSearchButton } from '$lib/components/routing';
    import { HrNeoTab } from '$lib/components/hrneo';
    import { createEditParam } from '$lib/utils/editParam.svelte';

    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    const edit = createEditParam(() => $page.url);

    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hrKnown = $derived(
        $hydrarouteStatusStore.lastFetchedAt > 0 || $hydrarouteStatusStore.status === 'error'
    );
</script>

<svelte:head>
    <title>HR Neo - AWGM</title>
</svelte:head>

<PageContainer>
    <div class="routing-page">
        <PageHeader title="HR Neo">
            {#snippet actions()}
                <RoutingSearchButton
                    dnsRoutes={$routing.dnsRoutes}
                    staticRoutes={$routing.staticRoutes}
                    tunnels={$routing.tunnels}
                />
                <RoutingRefreshButton />
            {/snippet}
        </PageHeader>

        {#if hrKnown && !hydrarouteInstalled}
            <EmptyState
                title="HydraRoute Neo не установлен"
                description="Маршрутизация HR Neo доступна после установки пакета — ссылка на установку есть в «Настройках», раздел «Интеграции»."
            />
        {:else}
            <HrNeoTab
                dnsRoutes={$routing.dnsRoutes}
                tunnels={$routing.tunnels}
                policies={$routing.accessPolicies}
                policyInterfaces={$routing.policyInterfaces}
                editRuleId={edit.id}
                editRuleCounter={edit.counter}
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
