<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { goto } from '$app/navigation';
    import { page } from '$app/stores';
    import {
        routing,
        subscribeRouting,
        invalidateAllRouting,
        routingDnsNdmsTabReady,
        routingIpTabReady,
        routingClientVpnTabReady,
        hydrarouteStatusStore,
    } from '$lib/stores/routing';
    import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
    import { systemInfo } from '$lib/stores/system';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { Tabs, Button, Modal } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';
    import { HrNeoTab } from '$lib/components/hrneo';
    import { SingboxRoutingPage } from '$lib/components/singbox-routing';
    import { isRoutingSubTabVisible, type RoutingSubTab, type UsageLevel } from '$lib/types/usageLevel';
    import { usageLevel } from '$lib/stores/settings';

    // Per-section polling stores — subscribe here so all 8 fetch while
    // the routing page is open. Unsubscribed on destroy to stop polling.
    let unsubRouting: (() => void) | null = null;

    onMount(() => {
        // Legacy URL redirect: the standalone "Прокси для устройств" tab
        // moved into the Sing-box page as a sub-tab. Preserve old links.
        const sp = new URLSearchParams($page.url.search);
        if (sp.get('tab') === 'deviceproxy') {
            sp.set('tab', 'singbox');
            sp.set('sub', 'deviceproxy');
            goto(`?${sp.toString()}`, { replaceState: true });
        }
        unsubRouting = subscribeRouting();
        // Prime sing-box router status so the tab badge count is correct
        // immediately on page load instead of waiting for the next polling
        // tick after the user actually clicks into the sing-box sub-tab.
        void singboxRouterStore.reloadStatus();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    let activeTab = $state<'hrneo' | 'dns' | 'ip' | 'policy' | 'clientvpn' | 'singbox'>('dns');

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);
    let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);

    // Search → edit rule integration
    let editRuleId = $state('');
    let editRuleCounter = $state(0);
    let searchOpen = $state(false);

    function handleSearchRuleClick(id: string, type: 'dns' | 'ip') {
        if (type === 'dns') {
            // dnsRoutes mixes NDMS and hydraroute backends in one array;
            // route hydraroute hits to the HR NEO tab so the edit modal
            // actually opens (DnsRoutesTab filters those out).
            const route = dnsRoutes.find(r => r.id === id);
            activeTab = route?.backend === 'hydraroute' ? 'hrneo' : 'dns';
        } else {
            activeTab = 'ip';
        }
        editRuleId = id;
        editRuleCounter++;
        searchOpen = false;
    }

    // NDMS tab is OS5-only (see tabItems gate). On OS4, bounce off `dns`
    // to HR NEO when hydraroute is installed, otherwise IP.
    $effect(() => {
        if (!$systemInfo.data) return;
        const hr = $hydrarouteStatusStore;
        if (hr.lastFetchedAt === 0 && hr.status !== 'error') return;

        if (!isOS5 && activeTab === 'dns') {
            activeTab = hydrarouteInstalled ? 'hrneo' : 'ip';
        }
    });

    // Data from SSE-driven store
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

    type TabItem = {
        id: string;
        label: string;
        badge?: number | string;
        badgeTone?: 'default' | 'success' | 'warning' | 'muted';
        separatorBefore?: boolean;
    };

    const TAB_TO_SUBTAB: Record<string, RoutingSubTab> = {
        policy: 'accessPolicies',
        clientvpn: 'clientRoutes',
        dns: 'dnsRoutes',
        ip: 'ipRoutes',
        hrneo: 'hrNeo',
        singbox: 'singboxRouter',
    };

    function tabVisible(localId: string, level?: UsageLevel): boolean {
        const sub = TAB_TO_SUBTAB[localId];
        const lvl = level ?? $usageLevel;
        return sub ? isRoutingSubTabVisible(lvl, sub) : true;
    }

    const singboxRouterStatus = singboxRouterStore.status;
    let singboxRuleCount = $derived($singboxRouterStatus?.ruleCount ?? 0);

    let tabItems = $derived(
        ([
            // NDMS dns-proxy with object-group fqdn is OS5-only — gate the
            // tab on isOS5 so OS4 routers don't see an unusable NDMS tab
            // (hydraroute users on OS4 use the HR NEO tab instead).
            isOS5 ? { id: 'dns', label: 'NDMS', badge: dnsActiveCount } : null,
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientRouteCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            // Visual gap separates the NDMS-stack tabs above from the
            // sing-box / hydraroute stack below.
            singboxInstalled ? { id: 'singbox', label: 'Sing-box Router', badge: singboxRuleCount, separatorBefore: true } : null,
            hydrarouteInstalled ? { id: 'hrneo', label: 'HR NEO', badge: hrRuleCount, separatorBefore: !singboxInstalled } : null,
        ] as (TabItem | null)[])
            .filter((t): t is TabItem => t !== null)
            .filter((t) => tabVisible(t.id))
    );

    // If the user deep-linked / had the tab active and sing-box disappeared
    // (uninstall while the page is open), bounce them off.
    $effect(() => {
        if (!$systemInfo.data) return;
        if (!singboxInstalled && activeTab === 'singbox') {
            activeTab = 'dns';
        }
    });

    // Пока список вкладок меняется (systemInfo, HR, уровень), не держим
    // active на id, которого ещё нет в tabItems — иначе пустой контент.
    // Не сбрасываем NDMS/политики/sing-box до прихода systemInfo: до fetch
    // isOS5=false и вкладки dns|policy ещё нет в списке — иначе F5 с NDMS
    // уводил на IP. Аналогично HR NEO — ждём hydraroute-status.
    $effect(() => {
        const items = tabItems;
        if (items.length === 0) return;

        const si = $systemInfo;
        const systemKnown = si.lastFetchedAt > 0 || si.status === 'error';
        const hr = $hydrarouteStatusStore;
        const hrKnown = hr.lastFetchedAt > 0 || hr.status === 'error';

        if (
            !systemKnown &&
            (activeTab === 'dns' || activeTab === 'policy' || activeTab === 'singbox') &&
            !items.some((it) => it.id === activeTab)
        ) {
            return;
        }
        if (!hrKnown && activeTab === 'hrneo' && !items.some((it) => it.id === 'hrneo')) {
            return;
        }

        if (!items.some((it) => it.id === activeTab)) {
            activeTab = items[0].id as typeof activeTab;
        }
    });

</script>

<svelte:head>
    <title>Маршрутизация - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
    <PageHeader title="Маршрутизация">
        {#snippet actions()}
            <Button
                variant="ghost"
                size="sm"
                onclick={() => (searchOpen = true)}
                iconBefore={searchIcon}
            >
                Поиск
            </Button>
            <!-- TODO Phase 1: warning variant for missing>0 -->
            <Button
                variant={missing.length > 0 ? 'secondary' : 'ghost'}
                size="sm"
                onclick={handleRefresh}
                disabled={refreshing}
                loading={refreshing}
            >
                {#if missing.length > 0}
                    Загрузить недостающее ({missing.length})
                {:else}
                    Обновить
                {/if}
            </Button>
        {/snippet}
    </PageHeader>

    <Tabs
        tabs={tabItems}
        active={activeTab}
        onchange={(id) => (activeTab = id as typeof activeTab)}
        urlParam="tab"
        defaultTab="dns"
    />

    {#if activeTab === 'hrneo'}
        <HrNeoTab
            {dnsRoutes}
            tunnels={routingTunnels}
            policies={accessPolicies}
            {policyInterfaces}
            {editRuleId}
            {editRuleCounter}
        />
    {:else if activeTab === 'dns'}
        <DnsRoutesTab
            {dnsRoutes}
            {routingTunnels}
            {editRuleId}
            {editRuleCounter}
            {isOS5}
            {hasDnsEngine}
            bodyLoading={!$routingDnsNdmsTabReady}
        />
    {:else if activeTab === 'ip'}
        <IpRoutesTab
            {ipRoutes}
            {routingTunnels}
            {editRuleId}
            {editRuleCounter}
            bodyLoading={!$routingIpTabReady}
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
            bodyLoading={!$routingClientVpnTabReady}
        />
    {:else if activeTab === 'singbox'}
        <SingboxRoutingPage />
    {/if}
</PageContainer>

<Modal
    open={searchOpen}
    onclose={() => (searchOpen = false)}
    title="Поиск по правилам маршрутизации NDMS"
    size="xl"
>
    <RoutingSearch
        {dnsRoutes}
        staticRoutes={ipRoutes}
        tunnels={routingTunnels}
        onRuleClick={handleSearchRuleClick}
    />
</Modal>

{#snippet searchIcon()}
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
        <circle cx="11" cy="11" r="8"/>
        <line x1="21" y1="21" x2="16.65" y2="16.65"/>
    </svg>
{/snippet}

