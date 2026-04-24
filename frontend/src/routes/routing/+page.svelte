<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { page } from '$app/stores';
    import { routing, subscribeRouting, invalidateAllRouting } from '$lib/stores/routing';
    import { systemInfo } from '$lib/stores/system';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
    import { OverflowTabs } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';
    import { HrNeoTab } from '$lib/components/hrneo';
    import { DeviceProxyTab } from '$lib/components/deviceproxy';

    // Per-section polling stores — subscribe here so all 8 fetch while
    // the routing page is open. Unsubscribed on destroy to stop polling.
    let unsubRouting: (() => void) | null = null;

    onMount(() => {
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => { unsubRouting?.(); });

    let activeTab = $state<'hrneo' | 'dns' | 'ip' | 'policy' | 'clientvpn' | 'deviceproxy'>('dns');

    // Deep link: ?tab=hrneo from the Settings page HR NEO card, etc.
    $effect(() => {
        const t = $page.url.searchParams.get('tab');
        if (t === 'hrneo' || t === 'dns' || t === 'ip' || t === 'policy' || t === 'clientvpn' || t === 'deviceproxy') {
            activeTab = t;
        }
    });
    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);
    let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);

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
    let missing = $derived($routing.missing);

    let refreshing = $state(false);
    async function handleRefresh() {
        if (refreshing) return;
        refreshing = true;
        try {
            const res = await api.refreshRouting();
            // Force every section store to refetch now (the backend also
            // posts resource:invalidated hints, but a local kick keeps the
            // UI responsive even if SSE happens to be lagging).
            invalidateAllRouting();
            if (res.missing.length === 0) {
                notifications.success('Данные получены');
            } else {
                notifications.warning(`Не удалось загрузить: ${res.missing.join(', ')}`);
            }
        } catch (e) {
            notifications.error(`Ошибка обновления: ${(e as Error).message}`);
        } finally {
            refreshing = false;
        }
    }

    // Derived: tab badges
    let hrRuleCount = $derived(dnsRoutes.filter(r => r.backend === 'hydraroute').length);
    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled && r.backend !== 'hydraroute').length);
    let ipActiveCount = $derived(ipRoutes.filter(r => r.enabled).length);
    let policyCount = $derived(accessPolicies.length);
    let clientRouteCount = $derived(clientRoutes.length);

    // Default to IP tab when no DNS engine available.
    // Gated on $routing.loaded: otherwise on cold load (direct URL hit)
    // hasDnsEngine is transiently false before systemInfo + routing stores
    // settle, and we'd silently kick an OS5 user off the NDMS tab.
    $effect(() => {
        if (!$routing.loaded) return;
        if (!hasDnsEngine && activeTab === 'dns') {
            activeTab = 'ip';
        }
    });

    let tabItems = $derived(
        [
            hydrarouteInstalled ? { id: 'hrneo', label: 'HR NEO', badge: hrRuleCount } : null,
            { id: 'dns', label: 'NDMS', badge: dnsActiveCount },
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientRouteCount },
            singboxInstalled ? { id: 'deviceproxy', label: 'Прокси для устройств', badge: 0 } : null,
        ].filter((t): t is { id: string; label: string; badge: number } => t !== null)
    );

    // If the user deep-linked / had the tab active and sing-box disappeared
    // (uninstall while the page is open), bounce them off.
    $effect(() => {
        if (!$systemInfo.data) return;
        if (!singboxInstalled && activeTab === 'deviceproxy') {
            activeTab = 'dns';
        }
    });

</script>

<svelte:head>
    <title>Маршрутизация - AWG Manager</title>
</svelte:head>

<PageContainer>
    <PageHeader title="Маршрутизация">
        {#snippet actions()}
            <button
                class="btn btn-sm"
                class:btn-warning={missing.length > 0}
                class:btn-ghost={missing.length === 0}
                onclick={handleRefresh}
                disabled={refreshing}
                title={missing.length > 0 ? `Не загружено: ${missing.join(', ')}` : 'Обновить данные маршрутизации'}
            >
                {#if refreshing}
                    Загрузка…
                {:else if missing.length > 0}
                    Загрузить недостающее ({missing.length})
                {:else}
                    Обновить
                {/if}
            </button>
        {/snippet}
    </PageHeader>

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

        {#if activeTab === 'hrneo'}
            <HrNeoTab
                {dnsRoutes}
                tunnels={routingTunnels}
                policies={accessPolicies}
                {policyInterfaces}
            />
        {:else if activeTab === 'dns'}
            <DnsRoutesTab
                {dnsRoutes}
                {routingTunnels}
                {editRuleId}
                {editRuleCounter}
                {isOS5}
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
                missing={missing.includes('accessPolicies')}
            />
        {:else if activeTab === 'clientvpn'}
            <ClientRoutesTab
                {clientRoutes}
                {policyDevices}
                {routingTunnels}
            />
        {:else if activeTab === 'deviceproxy'}
            <DeviceProxyTab />
        {/if}
    {/if}
</PageContainer>

