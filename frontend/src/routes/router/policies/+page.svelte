<script lang="ts">
    // Страница «Роутер · Политики доступа» — бывшая вкладка
    // /routing?tab=policy (навигация v3).
    import { onMount, onDestroy } from 'svelte';
    import { routing, subscribeRouting } from '$lib/stores/routing';
    import { systemInfo } from '$lib/stores/system';
    import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
    import { AccessPoliciesTab, RoutingRefreshButton } from '$lib/components/routing';

    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let systemKnown = $derived($systemInfo.lastFetchedAt > 0 || $systemInfo.status === 'error');
</script>

<svelte:head>
    <title>Политики доступа - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
    <PageHeader title="Политики доступа">
        {#snippet actions()}
            <RoutingRefreshButton />
        {/snippet}
    </PageHeader>

    {#if systemKnown && !isOS5}
        <!-- Политики доступа NDMS — только OS5; на OS4 вкладки не было. -->
        <EmptyState
            title="Раздел доступен на Keenetic OS 5"
            description="Политики доступа NDMS требуют прошивки OS 5."
        />
    {:else}
        <AccessPoliciesTab
            accessPolicies={$routing.accessPolicies}
            policyDevices={$routing.policyDevices}
            policyInterfaces={$routing.policyInterfaces}
            missing={$routing.missing.includes('accessPolicies')}
        />
    {/if}
</PageContainer>
