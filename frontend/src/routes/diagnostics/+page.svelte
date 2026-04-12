<script lang="ts">
    import { page } from '$app/stores';
    import { onMount } from 'svelte';
    import { api } from '$lib/api/client';
    import type { TunnelListItem } from '$lib/types';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { OverflowTabs } from '$lib/components/ui';
    import DiagnosticsTestsTab from './DiagnosticsTestsTab.svelte';
    import ConnectionsTab from './ConnectionsTab.svelte';
    import LogsTab from './LogsTab.svelte';

    let activeTab = $state<'tests' | 'connections' | 'logs'>('tests');
    let tunnels = $state<TunnelListItem[]>([]);

    // Deep link: ?tab=connections or ?tab=logs (from redirects)
    $effect(() => {
        const tabParam = $page.url.searchParams.get('tab');
        if (tabParam === 'connections' || tabParam === 'logs' || tabParam === 'tests') {
            activeTab = tabParam;
        }
    });

    onMount(async () => {
        try {
            tunnels = await api.listTunnels();
        } catch {
            tunnels = [];
        }
    });

    const tabItems = [
        { id: 'tests', label: 'Тесты' },
        { id: 'connections', label: 'Соединения' },
        { id: 'logs', label: 'Журнал' },
    ];
</script>

<svelte:head>
    <title>Диагностика - AWG Manager</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Диагностика" />

    <OverflowTabs
        tabs={tabItems}
        active={activeTab}
        onchange={(id) => activeTab = id as typeof activeTab}
    />

    {#if activeTab === 'tests'}
        <DiagnosticsTestsTab {tunnels} />
    {:else if activeTab === 'connections'}
        <ConnectionsTab />
    {:else if activeTab === 'logs'}
        <LogsTab />
    {/if}
</PageContainer>
