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
    import { systemInfo } from '$lib/stores/system';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { Search } from 'lucide-svelte';
    import { Tabs, Button, Modal } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';
    import { HrNeoTab } from '$lib/components/hrneo';

    // Per-section polling stores — subscribe here so all 8 fetch while
    // the routing page is open. Unsubscribed on destroy to stop polling.
    let unsubRouting: (() => void) | null = null;

    // Вкладки sing-box уехали на свои маршруты (навигация v3) — старые
    // deep-links держим редиректом. `deviceproxy` — ещё более старый адрес
    // отдельной страницы «Прокси для устройств» (Expert Inbounds).
    const LEGACY_TAB_ROUTES: Record<string, string> = {
        singbox: '/sb/routing',
        fakeip: '/sb/routing?view=fakeip',
        geodata: '/sb/geodata',
        deviceproxy: '/sb/routing?mode=expert',
    };

    onMount(() => {
        const legacyTab = $page.url.searchParams.get('tab') ?? '';
        const legacy = LEGACY_TAB_ROUTES[legacyTab];
        if (legacy) {
            const target = new URL(legacy, window.location.origin);
            // Параметры самой поверхности (mode/sub/add/edit/trace/q) переносим:
            // deep-link вида ?tab=singbox&mode=expert должен доехать целиком.
            // Умер только `tab` — и `sub` старой страницы «Прокси для устройств».
            for (const [k, v] of $page.url.searchParams) {
                if (k === 'tab' || (k === 'sub' && legacyTab === 'deviceproxy')) continue;
                if (!target.searchParams.has(k)) target.searchParams.set(k, v);
            }
            void goto(`${target.pathname}${target.search}`, { replaceState: true });
            return;
        }
        unsubRouting = subscribeRouting();
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    let activeTab = $state<'hrneo' | 'dns' | 'ip' | 'policy' | 'clientvpn'>('dns');

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);

    // Search → edit rule integration
    let editRuleId = $state('');
    let editRuleCounter = $state(0);
    let searchOpen = $state(false);

    function handleSearchRuleClick(id: string, type: 'dns' | 'ip') {
        if (type === 'dns') {
            // dnsRoutes mixes NDMS and hydraroute backends in one array;
            // route hydraroute hits to the HR Neo tab so the edit modal
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
    // to HR Neo when hydraroute is installed, otherwise IP.
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
    let clientActiveCount = $derived(clientRoutes.filter(r => r.enabled).length);
    let policyCount = $derived(accessPolicies.length);

    type TabItem = {
        id: string;
        label: string;
        badge?: number | string;
        badgeTone?: 'default' | 'success' | 'warning' | 'muted';
        separatorBefore?: boolean;
        muted?: boolean;
    };

    let tabItems = $derived(
        ([
            // NDMS dns-proxy with object-group fqdn is OS5-only — gate the
            // tab on isOS5 so OS4 routers don't see an unusable NDMS tab
            // (hydraroute users on OS4 use the HR Neo tab instead).
            isOS5 ? { id: 'dns', label: 'NDMS', badge: dnsActiveCount } : null,
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientActiveCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            // HR Neo is a separate routing engine (not sing-box) — divider before it.
            hydrarouteInstalled ? { id: 'hrneo', label: 'HR Neo', badge: hrRuleCount, separatorBefore: true } : null,
        ] as (TabItem | null)[])
            .filter((t): t is TabItem => t !== null)
    );

    // Пока список вкладок меняется (systemInfo, HR, уровень), не держим
    // active на id, которого ещё нет в tabItems — иначе пустой контент.
    // Не сбрасываем NDMS/политики до прихода systemInfo: до fetch
    // isOS5=false и вкладки dns|policy ещё нет в списке — иначе F5 с NDMS
    // уводил на IP. Аналогично HR Neo — ждём hydraroute-status.
    $effect(() => {
        const items = tabItems;
        if (items.length === 0) return;

        const si = $systemInfo;
        const systemKnown = si.lastFetchedAt > 0 || si.status === 'error';
        const hr = $hydrarouteStatusStore;
        const hrKnown = hr.lastFetchedAt > 0 || hr.status === 'error';

        if (
            !systemKnown &&
            (activeTab === 'dns' || activeTab === 'policy') &&
            !items.some((it) => it.id === activeTab)
        ) {
            return;
        }
        if (!hrKnown && activeTab === 'hrneo' && !items.some((it) => it.id === activeTab)) {
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
    <div class="routing-page">
    <PageHeader title="Маршрутизация">
        {#snippet actions()}
            <Button
                variant="secondary"
                size="sm"
                onclick={() => (searchOpen = true)}
                iconBefore={searchIcon}
            >
                Поиск
            </Button>
            <!-- TODO Phase 1: warning variant for missing>0 -->
            <Button
                variant="secondary"
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
    {/if}
    </div>
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
    <Search size={16} strokeWidth={2} aria-hidden="true" />
{/snippet}

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
