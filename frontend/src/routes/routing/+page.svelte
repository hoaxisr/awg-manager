<script lang="ts">
    import { routing } from '$lib/stores/routing';
    import { systemInfo } from '$lib/stores/system';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { OverflowTabs } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';

    let activeTab = $state<'dns' | 'ip' | 'policy' | 'clientvpn'>('dns');
    let isOS5 = $derived($systemInfo?.isOS5 ?? false);
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);

    // Search → edit rule integration
    let editRuleId = $state('');
    let editRuleCounter = $state(0);

    function handleSearchRuleClick(id: string, type: 'dns' | 'ip') {
        if (type === 'dns') {
            activeTab = 'dns';
        } else {
            activeTab = 'ip';
        }
        editRuleId = id;
        editRuleCounter++;
    }

    // Data from SSE-driven store
    let loading = $derived(!$routing.loaded);
    let dnsRoutes = $derived($routing.dnsRoutes);
    let ipRoutes = $derived($routing.staticRoutes);
    let accessPolicies = $derived($routing.accessPolicies);
    let policyDevices = $derived($routing.policyDevices);
    let policyInterfaces = $derived($routing.policyInterfaces);
    let clientRoutes = $derived($routing.clientRoutes);
    let routingTunnels = $derived($routing.tunnels);

    // Derived: tab badges
    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled).length);
    let ipActiveCount = $derived(ipRoutes.filter(r => r.enabled).length);
    let policyCount = $derived(accessPolicies.length);
    let clientRouteCount = $derived(clientRoutes.length);

    // Default to IP tab when no DNS engine available
    $effect(() => {
        if (!hasDnsEngine && activeTab === 'dns') {
            activeTab = 'ip';
        }
    });

    let tabItems = $derived(
        [
            { id: 'dns', label: 'Домены', badge: dnsActiveCount },
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientRouteCount },
        ].filter((t): t is { id: string; label: string; badge: number } => t !== null)
    );
</script>

<svelte:head>
    <title>Маршрутизация - AWG Manager</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Маршрутизация" />

    <RoutingSearch dnsRoutes={dnsRoutes} staticRoutes={ipRoutes} tunnels={routingTunnels} onRuleClick={handleSearchRuleClick} />

    {#if loading}
        <LoadingSpinner />
    {:else}
        <!-- Tab bar -->
        <OverflowTabs
            tabs={tabItems}
            active={activeTab}
            onchange={(id) => activeTab = id as typeof activeTab}
        />

        {#if activeTab === 'dns'}
            <DnsRoutesTab
                {dnsRoutes}
                {routingTunnels}
                {editRuleId}
                {editRuleCounter}
                {isOS5}
                {hydrarouteInstalled}
                {hasDnsEngine}
            />
        {:else if activeTab === 'ip'}
            <IpRoutesTab
                {ipRoutes}
                {routingTunnels}
                {editRuleId}
                {editRuleCounter}
            />
        {:else if activeTab === 'policy'}
            <AccessPoliciesTab
                {accessPolicies}
                {policyDevices}
                {policyInterfaces}
            />
        {:else if activeTab === 'clientvpn'}
            <ClientRoutesTab
                {clientRoutes}
                {policyDevices}
                {routingTunnels}
            />
        {/if}
    {/if}
</PageContainer>

