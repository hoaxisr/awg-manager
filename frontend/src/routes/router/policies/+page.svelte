<script lang="ts">
    // Страница «Роутер · Политики доступа» — бывшая вкладка
    // /routing?tab=policy (навигация v3).
    import { onMount, onDestroy } from 'svelte';
    import { page } from '$app/stores';
    import { routing, subscribeRouting } from '$lib/stores/routing';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { AccessPoliciesTab, RoutingRefreshButton } from '$lib/components/routing';

    let unsubRouting: (() => void) | null = null;
    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    // ?policy=Policy1 — прямой переход из настроек sing-box в редактор
    // конкретной политики (#573).
    let deepLinkPolicy = $derived($page.url.searchParams.get('policy'));
</script>

<svelte:head>
    <title>Политики доступа - AWGM</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Политики доступа">
        {#snippet actions()}
            <RoutingRefreshButton />
        {/snippet}
    </PageHeader>

    <AccessPoliciesTab
        accessPolicies={$routing.accessPolicies}
        policyDevices={$routing.policyDevices}
        policyInterfaces={$routing.policyInterfaces}
        missing={$routing.missing.includes('accessPolicies')}
        openPolicy={deepLinkPolicy}
    />
</PageContainer>
