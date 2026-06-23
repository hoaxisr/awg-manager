<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { get } from 'svelte/store';
    import { goto } from '$app/navigation';
    import { browser } from '$app/environment';
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
    import { Search } from 'lucide-svelte';
    import { Tabs, Button, Modal } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';
    import { HrNeoTab } from '$lib/components/hrneo';
    import { SingboxRouterRedesignPage } from '$lib/components/sb-router';
    import FakeIPTab from '$lib/components/fakeip/FakeIPTab.svelte';
    import ModeSwitchHost from '$lib/components/routing/ModeSwitchHost.svelte';
    import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
    import GeoDataTab from './GeoDataTab.svelte';
    import { isRoutingSubTabVisible, type RoutingSubTab, type UsageLevel } from '$lib/types/usageLevel';
    import { usageLevel } from '$lib/stores/settings';

    // Per-section polling stores — subscribe here so all 8 fetch while
    // the routing page is open. Unsubscribed on destroy to stop polling.
    let unsubRouting: (() => void) | null = null;

    onMount(() => {
        // Legacy URL: standalone «Прокси для устройств» → Expert Inbounds в Sing-box Router.
        const sp = new URLSearchParams($page.url.search);
        if (sp.get('tab') === 'deviceproxy') {
            sp.set('tab', 'singbox');
            sp.set('mode', 'expert');
            sp.delete('sub');
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

    let activeTab = $state<'hrneo' | 'geodata' | 'dns' | 'ip' | 'policy' | 'clientvpn' | 'singbox' | 'fakeip'>('dns');

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let hydrarouteInstalled = $derived($routing.hydrarouteStatus?.installed ?? false);
    let hasDnsEngine = $derived(isOS5 || hydrarouteInstalled);
    let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);

    let pendingTab = $state<string | null>(null);

    function requestTab(id: string): void {
        if (modeSwitchBusy(get(modeSwitch))) return;
        const hasDraft = get(singboxRouterStore.staging)?.hasDraft ?? false;
        if (activeTab === 'singbox' && id !== 'singbox' && hasDraft) {
            pendingTab = id;
            return;
        }
        activeTab = id as typeof activeTab;
    }
    function confirmLeave(): void {
        if (pendingTab) activeTab = pendingTab as typeof activeTab;
        pendingTab = null;
    }

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

    // In fakeip-tun mode, land on the FakeIP tab instead of the tproxy-
    // oriented default — the tproxy view would show the engine as "running"
    // while the tproxy slot is disabled, which is misleading. The FakeIP UI
    // now lives as a tab on THIS page, so we just select it (activeTab is
    // the page's tab source-of-truth; the Tabs component syncs ?tab=fakeip
    // outbound). We deliberately do NOT goto('/fakeip') — that route now
    // bounces back to /routing?tab=fakeip and would create an infinite loop.
    //
    // One-shot (fakeipAutoSelected) so a manual switch to another tab sticks,
    // and skipped when the URL already carries an explicit ?tab= (deep-link)
    // so we never override a user's chosen tab. Guarded on singboxInstalled
    // (the same condition that renders the tab) so we never select a tab that
    // isn't there — fakeip-tun implies sing-box installed, but this keeps the
    // selection from racing ahead of systemInfo arriving.
    const singboxInitializedStore = singboxRouterStore.initialized;
    const singboxSettings = singboxRouterStore.settings;
    let fakeipAutoSelected = false;
    $effect(() => {
        if (!browser) return;
        if (!$singboxInitializedStore) return;
        if (!singboxInstalled) return;
        if (fakeipAutoSelected) return;
        if ($singboxSettings?.routingMode === 'fakeip-tun') {
            fakeipAutoSelected = true;
            const explicitTab = new URL(window.location.href).searchParams.get('tab');
            if (!explicitTab) {
                activeTab = 'fakeip';
            }
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
    let geoFileCount = $state(0);

    async function loadGeoFileCount() {
        if (!hydrarouteInstalled && !singboxInstalled) {
            geoFileCount = 0;
            return;
        }
        try {
            const files = await api.getGeoFiles();
            geoFileCount = files?.length ?? 0;
        } catch {
            geoFileCount = 0;
        }
    }

    $effect(() => {
        if (hydrarouteInstalled || singboxInstalled) void loadGeoFileCount();
        else geoFileCount = 0;
    });
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

    const TAB_TO_SUBTAB: Record<string, RoutingSubTab> = {
        policy: 'accessPolicies',
        clientvpn: 'clientRoutes',
        dns: 'dnsRoutes',
        ip: 'ipRoutes',
        hrneo: 'hrNeo',
        geodata: 'geoData',
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
            // (hydraroute users on OS4 use the HR Neo tab instead).
            isOS5 ? { id: 'dns', label: 'NDMS', badge: dnsActiveCount } : null,
            { id: 'ip', label: 'IP-адреса', badge: ipActiveCount },
            { id: 'clientvpn', label: 'VPN для устройств', badge: clientActiveCount },
            isOS5 ? { id: 'policy', label: 'Политики доступа', badge: policyCount } : null,
            // Visual gap separates the NDMS-stack tabs above from the
            // sing-box / hydraroute stack below. TProxy + FakeIP are the two
            // mutually-exclusive sing-box routing modes — kept adjacent (no
            // separator between them) and muted when the OTHER mode is the active
            // one (XOR), so the dormant mode reads as dormant, not broken.
            singboxInstalled
                ? { id: 'singbox', label: 'Sing-box: TProxy', badge: singboxRuleCount, separatorBefore: true,
                    muted: !!$singboxRouterStatus?.enabled && $singboxSettings?.routingMode === 'fakeip-tun' }
                : null,
            // FakeIP is expert-gated (mirrors the 'singbox' tab's 'expert'
            // level) BUT stays visible whenever the engine is actually in
            // fakeip-tun mode — that's the in-use case the auto-select effect
            // lands on, and hiding the chip there would strand activeTab on a
            // tab with no chip to navigate back from.
            (singboxInstalled && (tabVisible('singbox') || $singboxSettings?.routingMode === 'fakeip-tun'))
                ? { id: 'fakeip', label: 'Sing-box: FakeIP', badge: undefined, separatorBefore: false,
                    muted: !!$singboxRouterStatus?.enabled && $singboxSettings?.routingMode === 'tproxy' }
                : null,
            // HR Neo is a separate routing engine (not sing-box) — divider before it.
            hydrarouteInstalled ? { id: 'hrneo', label: 'HR Neo', badge: hrRuleCount, separatorBefore: true } : null,
            (hydrarouteInstalled || singboxInstalled)
                ? { id: 'geodata', label: 'Гео-данные', badge: geoFileCount, separatorBefore: true }
                : null,
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
            (activeTab === 'dns' || activeTab === 'policy' || activeTab === 'singbox') &&
            !items.some((it) => it.id === activeTab)
        ) {
            return;
        }
        if (!hrKnown && (activeTab === 'hrneo' || activeTab === 'geodata') && !items.some((it) => it.id === activeTab)) {
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
        onchange={(id) => requestTab(id)}
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
    {:else if activeTab === 'geodata'}
        <GeoDataTab />
    {:else if activeTab === 'singbox'}
        <SingboxRouterRedesignPage />
    {:else if activeTab === 'fakeip'}
        <FakeIPTab />
    {/if}
    <ModeSwitchHost />
    </div>
</PageContainer>

<Modal
    open={pendingTab !== null}
    title="Несохранённые правки маршрутизации"
    size="sm"
    onclose={() => (pendingTab = null)}
>
    <p>Правки sing-box сохранены как черновик, но <strong>ещё не применены</strong>. Если уйти с вкладки — маршрутизация не изменится, пока вы не нажмёте «Применить».</p>
    {#snippet actions()}
        <Button variant="ghost" size="md" onclick={() => (pendingTab = null)}>Остаться</Button>
        <Button variant="primary" size="md" onclick={confirmLeave}>Уйти всё равно</Button>
    {/snippet}
</Modal>

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
