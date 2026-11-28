<script lang="ts">
    import { m } from '$lib/i18n';
    import { onMount, onDestroy, tick } from 'svelte';
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
    } from '$lib/stores/routing';
    import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
    import { zapretStatusIfExpert } from '$lib/stores/zapret';
    import { systemInfo } from '$lib/stores/system';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { PageContainer, PageHeader } from '$lib/components/layout';
    import { Search, Star } from 'lucide-svelte';
    import { Tabs, Button, IconButton, Modal } from '$lib/components/ui';
    import { RoutingSearch } from '$lib/components/routing';
    import DnsRoutesTab from './DnsRoutesTab.svelte';
    import IpRoutesTab from './IpRoutesTab.svelte';
    import AccessPoliciesTab from './AccessPoliciesTab.svelte';
    import ClientRoutesTab from './ClientRoutesTab.svelte';
    import { SingboxRouterRedesignPage } from '$lib/components/sb-router';
    import { ZapretTab } from '$lib/components/zapret';
    import { AutolistTab } from '$lib/components/autolist';
    import { autolistStatusIfExpert } from '$lib/stores/autolist';
    import FakeIPTab from '$lib/components/fakeip/FakeIPTab.svelte';
    import ModeSwitchHost from '$lib/components/routing/ModeSwitchHost.svelte';
    import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
    import GeoDataTab from './GeoDataTab.svelte';
    import FircRouterPanel from './FircRouterPanel.svelte';
    import { fircForeign, refreshFircStatus, reloadFircAll } from '$lib/stores/firc';
    import { routingStartTab, restoresRoutingTab } from '$lib/utils/routingStartTab';
    import { readTabMemory, tabMemoryKey } from '$lib/components/ui/tab-memory';
    import { LoadingSpinner } from '$lib/components/layout';
    import { fircHasDraft, clearFircGroupsDraft } from '$lib/stores/fircDraft';
    import { isRoutingSubTabVisible, type RoutingSubTab, type UsageLevel } from '$lib/types/usageLevel';
    import { usageLevel, settings, settingsUnavailable } from '$lib/stores/settings';

    // Per-section polling stores — subscribe here so all 8 fetch while
    // the routing page is open. Unsubscribed on destroy to stop polling.
    let unsubRouting: (() => void) | null = null;

    onMount(() => {
        // Legacy URL: Multi-WAN moved to Туннели (/?tab=uplink).
        const sp = new URLSearchParams($page.url.search);
        if (sp.get('tab') === 'uplink') {
            void goto('/?tab=uplink', { replaceState: true });
            return;
        }
        // Legacy URL: standalone «Прокси для устройств» → Expert Inbounds в Sing-box Router.
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
        // Settings must be primed too (issue #420): the TProxy/FakeIP chip
        // mute-XOR reads `enabled && routingMode`, and routingMode lives in
        // settings. Without this the dormant mode's chip rendered as active
        // until the user first visited a sing-box tab (which runs loadAll).
        void singboxRouterStore.reloadSettings();
        void loadGeoFileCount();
    });
    $effect(() => {
        if (!browser) return;
        const tab = $page.url.searchParams.get('tab');
        if (tab === 'uplink') {
            void goto('/?tab=uplink', { replaceState: true });
        } else if (tab !== null && !(ROUTING_TABS as readonly string[]).includes(tab)) {
            // Закладка на снятую вкладку (hrneo): пока параметр в адресе, Tabs
            // ждёт её появления и выбор пользователя в адрес не пишет.
            const url = new URL($page.url);
            url.searchParams.delete('tab');
            void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
        }
    });
    onDestroy(() => {
        unsubRouting?.();
    });

    const ROUTING_TABS = ['geodata', 'dns', 'ip', 'policy', 'clientvpn', 'firc', 'singbox', 'fakeip', 'zapret', 'autolist'] as const;
    type RoutingTab = (typeof ROUTING_TABS)[number];

    // «Любимая» вкладка — открывается первой вместо FIRC. Per-browser
    // удобство, поэтому localStorage, а не настройки роутера.
    const TAB_MEMORY_KEY = 'routing-sections';
    const FAVORITE_TAB_KEY = 'awgm.routing.favoriteTab';

    function loadFavoriteTab(): RoutingTab | null {
        try {
            const v = localStorage.getItem(FAVORITE_TAB_KEY);
            return ROUTING_TABS.includes(v as RoutingTab) ? (v as RoutingTab) : null;
        } catch {
            return null;
        }
    }

    // Снимок на старте: решает стартовую вкладку и автовыбор FakeIP;
    // клики по звезде в этом визите на них уже не влияют.
    const savedFavoriteTab = loadFavoriteTab();
    let favoriteTab = $state<RoutingTab | null>(savedFavoriteTab);

    function toggleFavoriteTab(): void {
        favoriteTab = favoriteTab === activeTab ? null : activeTab;
        try {
            if (favoriteTab) localStorage.setItem(FAVORITE_TAB_KEY, favoriteTab);
            else localStorage.removeItem(FAVORITE_TAB_KEY);
        } catch {
            /* quota / private mode — выбор живёт до перезагрузки */
        }
    }

    // Вкладка из ссылки — сразу: иначе сперва монтируется стартовая и шлёт
    // свои запросы (F890).
    function tabFromUrl(): RoutingTab | null {
        const v = browser ? new URL(window.location.href).searchParams.get('tab') : null;
        return ROUTING_TABS.includes(v as RoutingTab) ? (v as RoutingTab) : null;
    }

    const urlTab = tabFromUrl();
    const explicitTab = { url: urlTab, favorite: savedFavoriteTab };
    const tabPreset = urlTab !== null || savedFavoriteTab !== null;
    // Снимок на старте: вкладка не прыгает, если сторонний firc появится или уйдёт.
    const initialForeign = get(fircForeign);
    const initialTab: RoutingTab = routingStartTab(explicitTab, Boolean(initialForeign));
    let foreignAtStart = $state(Boolean(initialForeign));
    let startTab = $state(initialTab);
    let activeTab = $state(initialTab);
    // Пока не ясно, чей firc на роутере, стартовую вкладку по умолчанию не
    // показываем — иначе FIRC мелькнула бы перед sing-box.
    const unknownAtStart = !tabPreset && initialForeign === undefined;
    let startPending = $state(unknownAtStart);

    async function settleStartTab() {
        await Promise.race([
            refreshFircStatus().catch(() => undefined),
            new Promise((resolve) => setTimeout(resolve, 3000)),
        ]);
        foreignAtStart = Boolean(get(fircForeign));
        const untouched = activeTab === startTab;
        startTab = routingStartTab(explicitTab, foreignAtStart);
        if (untouched) activeTab = startTab;
        startPending = false;
    }
    if (browser && unknownAtStart) void settleStartTab();
    // Вкладка по умолчанию убирается из адреса. Пока статуса нет, не убираем ничего:
    // иначе явная ?tab=firc пропала бы и после F5 открылся бы sing-box.
    const defaultTab = $derived(
        favoriteTab ?? ($fircForeign === undefined ? '' : $fircForeign ? 'singbox' : 'firc'),
    );
    const restoreTabMemory = $derived(
        restoresRoutingTab(
            savedFavoriteTab,
            readTabMemory(tabMemoryKey(TAB_MEMORY_KEY), [...ROUTING_TABS]),
            foreignAtStart,
        ),
    );

    // ?policy=Policy1 — прямой переход из настроек sing-box в редактор
    // конкретной политики (#573).
    let deepLinkPolicy = $derived($page.url.searchParams.get('policy'));

    let isOS5 = $derived($systemInfo.data?.isOS5 ?? false);
    let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);
    // Zapret ставит пользователь, а не AWGM, поэтому вкладка появляется
    // только когда движок реально найден на роутере.
    let zapretInstalled = $derived($zapretStatusIfExpert?.data?.installed ?? false);

    // awgmautolist: вкладка видна expert-уровню всегда, когда статус получен, —
    // и неустановленному сателлиту (там кнопка установки), и отключённому.
    // Но автолист работает только с маршрутизацией sing-box: без установленного
    // sing-box кандидаты бессмысленны, поэтому вкладки не показываем.
    // Ниже expert статус не опрашивается (autolistStatusIfExpert в сторе).
    let autolistWaitingCount = $derived($autolistStatusIfExpert?.data?.waitingCandidates ?? 0);
    let autolistAvailable = $derived($autolistStatusIfExpert?.data != null && singboxInstalled);

    let pendingTab = $state<string | null>(null);

    function requestTab(id: string): void {
        if (modeSwitchBusy(get(modeSwitch))) return;
        const hasDraft = get(singboxRouterStore.staging)?.hasDraft ?? false;
        if ((activeTab === 'singbox' && id !== 'singbox' && hasDraft) || (activeTab === 'firc' && id !== 'firc' && get(fircHasDraft))) {
            pendingTab = id;
            return;
        }
        activeTab = id as typeof activeTab;
    }
    function confirmLeave(): void {
        if (activeTab === 'firc') clearFircGroupsDraft();
        if (pendingTab) activeTab = pendingTab as typeof activeTab;
        pendingTab = null;
    }

    // Search → edit rule integration
    let editRuleId = $state('');
    let editRuleCounter = $state(0);
    let searchOpen = $state(false);

    async function handleSearchRuleClick(id: string, type: 'dns' | 'ip') {
        activeTab = type;
        searchOpen = false;
        // A freshly mounted tab captures editRuleCounter at mount; bump it only
        // after the switched-to tab has mounted so its effect sees the change.
        await tick();
        editRuleId = id;
        editRuleCounter++;
    }

    // NDMS tab is OS5-only (see tabItems gate). On OS4, bounce off `dns`
    // to IP.
    $effect(() => {
        if (!$systemInfo.data) return;
        if (!isOS5 && activeTab === 'dns') activeTab = 'ip';
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
    //
    // Gated on settings (primed by reloadSettings() in onMount), not on
    // `initialized`: that flag is set only by loadAll() of the sing-box tabs,
    // so on F5 / direct entry the auto-select never fired (F641). Decided once,
    // on the first settings snapshot — a later switch to fakeip-tun must not
    // yank the user off the tab they are on.
    const singboxSettings = singboxRouterStore.settings;
    let fakeipAutoSelected = false;
    $effect(() => {
        if (!browser) return;
        if (!$singboxSettings) return;
        if (!singboxInstalled) return;
        if (fakeipAutoSelected) return;
        fakeipAutoSelected = true;
        if ($singboxSettings.routingMode === 'fakeip-tun') {
            const explicitTab = new URL(window.location.href).searchParams.get('tab');
            // Явно выбранная любимая вкладка важнее автовыбора.
            if (!explicitTab && savedFavoriteTab === null) {
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
            void reloadFircAll().catch(() => undefined);
            if (res.missing.length === 0) {
                notifications.success(m.routing_page_refreshed());
            } else {
                notifications.warning(m.routing_page_missing({ items: res.missing.join(', ') }));
            }
        } catch (e) {
            notifications.error(m.routing_page_refresh_error({ message: (e as Error).message }));
        } finally {
            refreshing = false;
        }
    }

    // Derived: tab badges
    // null — ещё не прочитано.
    let geoFileCount = $state<number | null>(null);

    async function loadGeoFileCount() {
        try {
            const files = await api.getGeoFiles();
            geoFileCount = files?.length ?? 0;
        } catch {
            geoFileCount = 0;
        }
    }
    let dnsActiveCount = $derived(dnsRoutes.filter(r => r.enabled).length);
    let ipActiveCount = $derived(ipRoutes.filter(r => r.enabled).length);
    let clientActiveCount = $derived(clientRoutes.filter(r => r.enabled).length);
    let policyCount = $derived(accessPolicies.length);

    type TabChildItem = {
        id: string;
        label: string;
        badge?: number | string;
        badgeTone?: 'default' | 'success' | 'warning' | 'muted';
    };

    type TabItem = {
        id: string;
        label: string;
        badge?: number | string;
        badgeTone?: 'default' | 'success' | 'warning' | 'muted';
        separatorBefore?: boolean;
        children?: TabChildItem[];
    };

    const TAB_TO_SUBTAB: Record<string, RoutingSubTab> = {
        policy: 'accessPolicies',
        clientvpn: 'clientRoutes',
        dns: 'dnsRoutes',
        ip: 'ipRoutes',
        zapret: 'zapret',
        autolist: 'autolist',
        geodata: 'geoData',
        singbox: 'singboxRouter',
    };

    function tabVisible(localId: string, level?: UsageLevel): boolean {
        const sub = TAB_TO_SUBTAB[localId];
        const lvl = level ?? $usageLevel;
        return sub ? isRoutingSubTabVisible(lvl, sub) : true;
    }

    function tabLeafIds(tab: TabItem): string[] {
        return tab.children?.map((c) => c.id) ?? [tab.id];
    }

    function tabsInclude(items: TabItem[], id: string): boolean {
        return items.some((it) => tabLeafIds(it).includes(id));
    }

    const singboxRouterStatus = singboxRouterStore.status;
    let singboxRuleCount = $derived($singboxRouterStatus?.ruleCount ?? 0);

    const showSingboxTproxy = $derived(singboxInstalled && tabVisible('singbox'));
    // FakeIP is expert-gated (mirrors the 'singbox' tab's 'expert' level) BUT
    // stays visible whenever the engine is actually in fakeip-tun mode — that's
    // the in-use case the auto-select effect lands on, and hiding the chip there
    // would strand activeTab on a tab with no chip to navigate back from.
    const showSingboxFakeip = $derived(
        singboxInstalled && (tabVisible('singbox') || $singboxSettings?.routingMode === 'fakeip-tun'),
    );
    const singboxMenuChildren = $derived(
        (
            [
                showSingboxTproxy
                    ? { id: 'singbox', label: 'TProxy', badge: singboxRuleCount }
                    : null,
                showSingboxFakeip ? { id: 'fakeip', label: 'FakeIP' } : null,
                // Автолист работает с маршрутизацией sing-box, поэтому его
                // вкладка живёт в той же группе: Sing-box · Автолист.
                autolistAvailable
                    ? { id: 'autolist', label: m.routing_page_tab_autolist(), badge: autolistWaitingCount }
                    : null,
            ] as (TabChildItem | null)[]
        ).filter((c): c is TabChildItem => c !== null),
    );

    let tabItems = $derived(
        ([
            {
                id: 'firc',
                label: 'AWGM Route',
                badge: $fircHasDraft ? '•' : undefined,
                badgeTone: $fircHasDraft ? 'warning' : undefined,
            },
            // Sing-box modes as one dropdown chip (same pattern as tunnels page).
            singboxMenuChildren.length > 0
                ? {
                        id: singboxMenuChildren[0].id,
                        label: 'Sing-box',
                        separatorBefore: true,
                        children: singboxMenuChildren,
                    }
                : null,
            { id: 'ip', label: m.routing_page_tab_ip(), badge: ipActiveCount, separatorBefore: true },
            { id: 'clientvpn', label: m.routing_page_tab_clientvpn(), badge: clientActiveCount },
            { id: 'policy', label: m.routing_page_tab_policy(), badge: policyCount },
            // Zapret — отдельный движок, и ставит его пользователь, а не
            // AWGM. Вкладка появляется только когда движок найден на роутере.
            zapretInstalled
                ? {
                      id: 'zapret',
                      label: 'Zapret',
                      separatorBefore: true,
                  }
                : null,
            // awgmautolist — сателлит автосписка: живёт в группе Sing-box
            // (см. singboxMenuChildren), отдельной вкладки нет.
            // Файлы нужны sing-box, но и без него скачанное должно быть видно:
            // только здесь их удаляют и выключают автообновление.
            singboxInstalled || (geoFileCount ?? 0) > 0
                ? { id: 'geodata', label: m.routing_page_tab_geodata(), badge: geoFileCount ?? 0, separatorBefore: true }
                : null,
            // NDMS dns-proxy with object-group fqdn is OS5-only — gate the
            // tab on isOS5 so OS4 routers don't see an unusable NDMS tab.
            isOS5 ? { id: 'dns', label: 'NDMS', badge: dnsActiveCount, separatorBefore: true } : null,
        ] as (TabItem | null)[])
            .filter((t): t is TabItem => t !== null)
            .filter((t) => (t.children ? true : tabVisible(t.id)))
    );

    // If the user deep-linked / had the tab active and sing-box disappeared
    // (uninstall while the page is open), bounce them off. Автолист без
    // sing-box тоже не работает — его вкладка уходит вместе с движком.
    $effect(() => {
        if (!$systemInfo.data) return;
        if (!singboxInstalled && (activeTab === 'singbox' || activeTab === 'fakeip' || activeTab === 'autolist')) {
            activeTab = 'firc';
        }
    });

    // Пока список вкладок меняется (systemInfo, уровень), не держим
    // active на id, которого ещё нет в tabItems — иначе пустой контент.
    // Не сбрасываем NDMS/sing-box до прихода systemInfo: до fetch
    // isOS5=false и вкладки dns ещё нет в списке — иначе F5 с NDMS
    // уводил на IP.
    $effect(() => {
        const items = tabItems;
        if (items.length === 0 || startPending) return;

        const si = $systemInfo;
        // Отказ system/info вкладку из ссылки не сбрасывает: сброс ушёл бы в
        // адрес, и после восстановления ссылка была бы потеряна.
        const systemKnown = si.lastFetchedAt > 0 || (si.status === 'error' && activeTab !== urlTab);

        if (
            !systemKnown &&
            (activeTab === 'dns' || activeTab === 'singbox' || activeTab === 'fakeip' || activeTab === 'autolist' || activeTab === 'geodata') &&
            !tabsInclude(items, activeTab)
        ) {
            return;
        }
        if (activeTab === 'geodata' && geoFileCount === null && !tabsInclude(items, activeTab)) return;
        // Стартовую (из ссылки или любимую) не сбрасываем, пока не пришло то,
        // от чего зависит её видимость: до загрузки настроек usageLevel = 'advanced' и
        // expert-вкладки скрыты; FakeIP ниже expert виден только по
        // routingMode из настроек sing-box. Ждём лишь эти вкладки и лишь
        // пока настройки не отказали, чтобы упавшая загрузка не держала
        // их вечно.
        if (activeTab === startTab && !tabsInclude(items, activeTab)) {
            const levelGated = activeTab === 'geodata' || activeTab === 'singbox' || activeTab === 'fakeip';
            if (levelGated && $settings === null && !$settingsUnavailable) return;
            if (activeTab === 'fakeip' && $singboxSettings === null) return;
        }

        // Вкладка Zapret гейтится уровнем и zapret-status. Пока не пришли
        // настройки или (у expert) статус, вкладки в списке ещё нет — иначе
        // deep-link ?tab=zapret (например, из Настроек) откатился бы на
        // первую вкладку до загрузки. Не загрузились настройки — не ждём.
        const zapretPending = $zapretStatusIfExpert !== null && !$zapretStatusIfExpert.lastFetchedAt;
        if (
            activeTab === 'zapret' &&
            !tabsInclude(items, activeTab) &&
            (($settings === null && !$settingsUnavailable) || zapretPending)
        ) {
            return;
        }

        // Autolist — тот же гейт, что у Zapret: ждём expert-статус сателлита,
        // пока deep-link ?tab=autolist не откатили на первую вкладку.
        const expert = $autolistStatusIfExpert;
        const autolistPending = expert !== null && expert.lastFetchedAt === 0 && expert.status !== 'error';
        if (activeTab === 'autolist' && !tabsInclude(items, activeTab) && (($settings === null && !$settingsUnavailable) || autolistPending)) {
            return;
        }

        if (!tabsInclude(items, activeTab)) {
            // При стороннем firc вместо скрытой вкладки TProxy — соседняя sing-box.
            const first = (foreignAtStart && items.find((t) => t.children)) || items[0];
            activeTab =
                savedFavoriteTab && tabsInclude(items, savedFavoriteTab)
                    ? savedFavoriteTab
                    : ((first.children?.[0]?.id ?? first.id) as typeof activeTab);
        }
    });

</script>

<svelte:head>
    <title>{m.nav_routing()} - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
    <div class="routing-page">
    <PageHeader title={m.nav_routing()}>
        {#snippet actions()}
            <Button
                variant="secondary"
                size="md"
                onclick={() => (searchOpen = true)}
                iconBefore={searchIcon}
            >
                {m.routing_page_search()}
            </Button>
            <!-- TODO Phase 1: warning variant for missing>0 -->
            <Button
                variant="secondary"
                size="md"
                onclick={handleRefresh}
                disabled={refreshing}
                loading={refreshing}
            >
                {#if missing.length > 0}
                    {m.routing_page_load_missing({ count: missing.length })}
                {:else}
                    {m.common_refresh()}
                {/if}
            </Button>
            <IconButton
                size="md"
                variant={favoriteTab === activeTab ? 'warm' : 'default'}
                ariaLabel={favoriteTab === activeTab ? m.routing_page_favorite_unset() : m.routing_page_favorite_set()}
                title={favoriteTab === activeTab ? m.routing_page_favorite_unset() : m.routing_page_favorite_set()}
                onclick={toggleFavoriteTab}
            >
                <Star size={16} strokeWidth={2} fill={favoriteTab === activeTab ? 'currentColor' : 'none'} aria-hidden="true" />
            </IconButton>
        {/snippet}
    </PageHeader>

    {#if startPending}
        <LoadingSpinner />
    {:else}
    <Tabs
        tabs={tabItems}
        active={activeTab}
        onchange={(id) => requestTab(id)}
        urlParam="tab"
        persistKey={TAB_MEMORY_KEY}
        skipMemorySearchParams={['policy', 'rule', 'edit']}
        memoryReady={$settings !== null && $systemInfo.lastFetchedAt > 0 && (!singboxInstalled || $singboxSettings !== null)}
        restoreMemory={restoreTabMemory}
        defaultTab={defaultTab}
    />

    {#if activeTab === 'dns'}
        <DnsRoutesTab
            {dnsRoutes}
            {routingTunnels}
            {editRuleId}
            {editRuleCounter}
            {isOS5}
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
                openPolicy={deepLinkPolicy}
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
    {:else if activeTab === 'firc'}
        <FircRouterPanel />
    {:else if activeTab === 'singbox'}
        <SingboxRouterRedesignPage />
    {:else if activeTab === 'fakeip'}
        <FakeIPTab />
    {:else if activeTab === 'zapret'}
        <ZapretTab />
    {:else if activeTab === 'autolist'}
        <AutolistTab {policyDevices} />
    {/if}
    {/if}
    <ModeSwitchHost />
    </div>
</PageContainer>

<Modal
    open={pendingTab !== null}
    title={m.routing_page_unsaved_title()}
    size="sm"
    onclose={() => (pendingTab = null)}
>
    <p>{m.routing_page_unsaved_pre()} <strong>{m.routing_page_unsaved_strong()}</strong>. {m.routing_page_unsaved_post()}</p>
    {#snippet actions()}
        <Button variant="ghost" size="md" onclick={() => (pendingTab = null)}>{m.routing_page_stay()}</Button>
        <Button variant="primary" size="md" onclick={confirmLeave}>{m.routing_page_leave()}</Button>
    {/snippet}
</Modal>

<Modal
    open={searchOpen}
    onclose={() => (searchOpen = false)}
    title={m.routing_page_search_title()}
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
    <Search size={14} strokeWidth={2} aria-hidden="true" />
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
			min-width: 0;
			justify-content: center;
		}
	}
</style>
