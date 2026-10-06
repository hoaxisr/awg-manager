<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (MainExpertScreen)
  Главная композиция Expert вида (полный набор: правила, rule-sets, outbounds, DNS, движок, прокси).

  Адаптации от шаблона:
  - onSaved → onSave (реальный prop у всех 5 модалов)
  - Все модалы требуют outboundOptions: OutboundGroup[] — берём из store.options
  - RuleEditModal требует availableRuleSets + ruleSetUsage (excludeIndex для edit)
  - DNSServerEditModal требует servers: SingboxRouterDNSServer[]
  - DNSRuleEditModal требует servers + availableRuleSets + ruleSetUsage
  - DNS данные берём из store (dnsServers/dnsRules), не грузим отдельно
  - RuleSetAddModal поддерживает edit-mode через prop ruleSet (необязательный)
  - CompositeOutboundEditModal edit-mode через prop outbound (необязательный)
-->

<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { subscriptionGroupsStore, subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { singboxMemory } from '$lib/stores/singboxMemory';
  import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
  import { formatBytes, formatByteRate } from '$lib/utils/format';
  import { notifications } from '$lib/stores/notifications';
  import { api } from '$lib/api/client';
  import {
    computeRuleSetUsage,
    DNSChainPresetCard,
    DNSGlobalsEditModal,
  } from '$lib/components/routing/singboxRouter';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import type {
    CatalogPreset,
    SingboxRouterRule,
    SingboxRouterRuleSet,
    SingboxRouterOutbound,
    SingboxRouterDNSChainPreset,
    SingboxRouterDNSServer,
    SingboxRouterDNSRule,
    SingboxRouterDNSStrategy,
    DeviceProxyInstance,
    SingboxInboundEntry,
  } from '$lib/types';
  import { newDeviceProxyInstance } from '$lib/utils/deviceProxyInstance';
  import { inboundsPanelTotal } from '$lib/utils/singboxInbounds';
  import { deleteDeviceProxyInstanceWithNotice } from '$lib/utils/deviceProxyDeleteNotice';

  import StatStrip, { type StatCellData } from './StatStrip.svelte';
  import SidePanel from './SidePanel.svelte';
  import RoutingTable from './RoutingTable.svelte';
  import RuleSetsTable from './RuleSetsTable.svelte';
  import BulkSelectBar from './BulkSelectBar.svelte';
  import { isSystemRule, mapRuleAction } from './adapters';
  import { buildDownloadDetourOptions, outboundGroupLabel } from '$lib/components/routing/singboxRouter/outboundOptions';
  import SbRouterRuleSetCatalogModal from './SbRouterRuleSetCatalogModal.svelte';
  import SbRouterGeositeCatalogModal from './SbRouterGeositeCatalogModal.svelte';
  import { addGeositeRuleSets, applyCatalogPresetsAsRuleSets } from './rulesetCatalogActions';
  import OutboundsCompact from './OutboundsCompact.svelte';
  import DnsServersCompact from './DnsServersCompact.svelte';
  import DeviceProxyCompact from './DeviceProxyCompact.svelte';
  import InboundsMirror from './InboundsMirror.svelte';
  import { expertPanelCollapse } from './expertPanelCollapseStore';
  import InboundSettingsDrawer from './InboundSettingsDrawer.svelte';
  import { ensureLanNamesRule, isLanNamesRule, removeLanNamesRule } from './emptyStateActions';
  import EngineFatalModal from './EngineFatalModal.svelte';

  import RuleEditModal from '$lib/components/routing/singboxRouter/RuleEditModal.svelte';
  import RuleSetAddModal from '$lib/components/routing/singboxRouter/RuleSetAddModal.svelte';
  import CompositeOutboundEditModal from '$lib/components/routing/singboxRouter/CompositeOutboundEditModal.svelte';
  import DNSServerEditModal from '$lib/components/routing/singboxRouter/DNSServerEditModal.svelte';
  import DNSRuleEditModal from '$lib/components/routing/singboxRouter/DNSRuleEditModal.svelte';
  import { DNSRewritesList } from '$lib/components/routing/singboxRouter';
  import { ConfirmModal, Dropdown, Button, type DropdownOption } from '$lib/components/ui';
  import { Check, LayoutGrid, Library } from 'lucide-svelte';
  import { browser } from '$app/environment';

  // Store subscriptions
  const storeStatus = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const storeRules = singboxRouterStore.rules;
  const storeRuleSets = singboxRouterStore.ruleSets;
  const storeOutbounds = singboxRouterStore.outbounds;
  const storeDnsServers = singboxRouterStore.dnsServers;
  const storeDnsRules = singboxRouterStore.dnsRules;
  const storeDnsRewrites = singboxRouterStore.dnsRewrites;
  const storeDnsGlobals = singboxRouterStore.dnsGlobals;
  const storeOptions = singboxRouterStore.options;

  // Сортировка наборов rule-sets по алфавиту (только отображение, UI-преференс)
  const RULESET_SORT_KEY = 'awg.sb-router.ruleset-sort-alpha';
  function readAlphaSortRuleSets(): boolean {
    if (!browser) return false;
    try {
      return localStorage.getItem(RULESET_SORT_KEY) === 'true';
    } catch {
      return false;
    }
  }
  function persistAlphaSortRuleSets(on: boolean): void {
    if (!browser) return;
    try {
      localStorage.setItem(RULESET_SORT_KEY, on ? 'true' : 'false');
    } catch {
      /* приватный режим / quota — игнор */
    }
  }
  let alphaSortRuleSets = $state(readAlphaSortRuleSets());
  const sortedRuleSets = $derived(
    alphaSortRuleSets
      ? [...$storeRuleSets].sort((a, b) => a.tag.localeCompare(b.tag))
      : $storeRuleSets,
  );

  // ── Globals (route-final + DNS final/strategy) ──────────────────────
  // route-final: direct + все outbounds, кроме группы 'special'
  const routeFinalOptions = $derived<DropdownOption[]>([
    { value: 'direct', label: m.sb_router_expert_route_final_direct() },
    ...$storeOptions
      .filter((g) => g.id !== 'special')
      .flatMap((g) => g.items.map((i) => ({ value: i.value, label: i.label, group: outboundGroupLabel(g.id) }))),
  ]);

  let draftRouteFinal = $state('direct');
  let routeFinalBusy = $state(false);

  // draft синхронизируется со стором
  $effect(() => {
    draftRouteFinal = $storeStatus?.final || 'direct';
  });

  const routeFinalDirty = $derived(draftRouteFinal !== ($storeStatus?.final || 'direct'));

  async function saveRouteFinal() {
    if (!routeFinalDirty || routeFinalBusy) return;
    routeFinalBusy = true;
    try {
      await api.singboxRouterPutRouteFinal(draftRouteFinal);
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      routeFinalBusy = false;
    }
  }

  function openDnsGlobalsModal() {
    dnsGlobalsModalOpen = true;
  }

  let activeProxyCount = $state<number | null>(null);
  let totalProxyCount = $state<number | null>(null);
  let deviceProxyInstances = $state<DeviceProxyInstance[]>([]);

  async function loadActiveProxyCount() {
    try {
      const proxyInstances = await api.listDeviceProxyInstances();
      deviceProxyInstances = proxyInstances;
      totalProxyCount = proxyInstances.length;

        const runtimeEntries = await Promise.all(
          proxyInstances.map(async (in_) => {
            const runtime = await api.getDeviceProxyInstanceRuntime(in_.id).catch(() => null);
            return { instance: in_, runtime };
        }),
      );

        activeProxyCount = runtimeEntries.filter(({ instance, runtime }) => {
          return instance.enabled && runtime?.alive === true;
        }).length;
      } catch {
        activeProxyCount = null;
      totalProxyCount = null;
      deviceProxyInstances = [];
    }
  }

  const outboundUsageContext = $derived({
    rules: $storeRules,
    routeFinal: $storeStatus?.final || 'direct',
    outbounds: $storeOutbounds,
    dnsServers: $storeDnsServers,
    ruleSets: $storeRuleSets,
    deviceProxyOutbounds: deviceProxyInstances
      .map((in_) => in_.selectedOutbound)
      .filter((tag): tag is string => !!tag),
  });

  const dnsServerUsageContext = $derived({
    rules: $storeDnsRules,
    servers: $storeDnsServers,
    dnsFinal: $storeDnsGlobals.final || '',
  });

  const activeProxyCountLabel = $derived(
    activeProxyCount === null || totalProxyCount === null ? '—' : `${activeProxyCount}/${totalProxyCount}`,
  );

  // ── Зеркало inbound'ов merged-конфига (GET /api/singbox/inbounds) ────
  // Панель «Inbounds» показывает ВСЕ inbound'ы, а не только device-proxy:
  // интерактивная часть device-proxy остаётся в DeviceProxyCompact, остальные
  // источники — read-only списком (InboundsMirror).
  let allInbounds = $state<SingboxInboundEntry[] | null>(null);
  let inboundWarnings = $state<string[]>([]);

  async function loadAllInbounds() {
    try {
      const res = await api.listSingboxInbounds();
      allInbounds = res.inbounds;
      inboundWarnings = res.warnings ?? [];
    } catch {
      allInbounds = null;
      inboundWarnings = [];
    }
  }

  const mirrorInbounds = $derived(
    (allInbounds ?? []).filter((e) => e.source !== 'deviceproxy'),
  );
  // Счётчик панели = все видимые элементы: inbound'ы merged-конфига плюс
  // выключенные инстансы device-proxy (их нет в слоте 30, но карточки
  // рендерятся). При недоступном endpoint'е деградируем к прежней метке.
  const inboundsPanelCountLabel = $derived(
    allInbounds === null
      ? activeProxyCountLabel
      : String(inboundsPanelTotal(allInbounds, deviceProxyInstances.map((in_) => in_.id))),
  );

  // Refetch при разворачивании панели «Inbounds» (mount покрыт в onMount).
  let prevInboundsCollapsed = $expertPanelCollapse.inbounds;
  $effect(() => {
    const collapsed = $expertPanelCollapse.inbounds;
    if (prevInboundsCollapsed && !collapsed) void loadAllInbounds();
    prevInboundsCollapsed = collapsed;
  });

  // Modal state
  let ruleEditIdx = $state<number | null>(null);
  let ruleAddOpen = $state(false);
  let rewriteAddMode = $state(false);
  let rsEditTag = $state<string | null>(null);
  let rsAddOpen = $state(false);
  let rsCatalogOpen = $state(false);
  let rsCatalogBusy = $state(false);
  let geositeCatalogOpen = $state(false);
  let geositeCatalogBusy = $state(false);
  let outboundEditTag = $state<string | null>(null);
  let outboundAddOpen = $state(false);

  // DNS-серверы
  let dnsServerEditTag = $state<string | null>(null);
  let dnsServerAddOpen = $state(false);
  let dnsRuleEditIdx = $state<number | null>(null);
  let dnsRuleAddOpen = $state(false);
  let dnsGlobalsModalOpen = $state(false);
  let engineFatalOpen = $state(false);

  let inboundDrawerInstance = $state<DeviceProxyInstance | null>(null);
  let inboundDrawerOpen = $state(false);
  let dpReloadKey = $state(0);

  // Унифицированное подтверждение удаления (rule / rule-set / inbound)
  let pendingConfirm = $state<{ title: () => string; message: () => string; run: () => Promise<void> } | null>(null);
  let confirmBusy = $state(false);

  async function runConfirm() {
    if (!pendingConfirm) return;
    confirmBusy = true;
    try {
      await pendingConfirm.run();
      pendingConfirm = null;
    } finally {
      confirmBusy = false;
    }
  }

  function openInbound(in_: DeviceProxyInstance) {
    inboundDrawerInstance = in_;
    inboundDrawerOpen = true;
  }
  async function addInbound() {
    let existing: DeviceProxyInstance[] = [];
    try {
      existing = await api.listDeviceProxyInstances();
    } catch {
      existing = [];
    }
    inboundDrawerInstance = newDeviceProxyInstance(existing);
    inboundDrawerOpen = true;
  }
  function onInboundSaved() {
    inboundDrawerOpen = false;
    dpReloadKey += 1;
    void loadActiveProxyCount();
    void loadAllInbounds();
  }
  function deleteInbound(in_: DeviceProxyInstance) {
    pendingConfirm = {
      title: () => m.sb_router_expert_inbound_delete_title(),
      message: () => m.sb_router_expert_inbound_delete_message({ name: in_.name || in_.id }),
      run: async () => {
        try {
          await deleteDeviceProxyInstanceWithNotice(in_.id, {
            successMessage: m.sb_router_expert_inbound_deleted(),
            pendingApplyMessage: m.sb_router_expert_inbound_deleted_pending(),
          });
          dpReloadKey += 1;
          await loadActiveProxyCount();
          await loadAllInbounds();
        } catch (e) {
          notifications.error(m.sb_router_expert_delete_failed({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  // DNS-пресет цепочки хранится в настройках, а не в конфиге роутера, — грузим
  // отдельно от store.loadAll. Ошибка (старый бэкенд/мок) оставляет «Выкл».
  let dnsChainPreset = $state<SingboxRouterDNSChainPreset>({ mode: '' });

  async function loadDnsChainPreset(): Promise<void> {
    try {
      dnsChainPreset = await api.singboxRouterGetDNSChainPreset();
    } catch {
      /* пресет недоступен — карточка остаётся выключенной */
    }
  }

  // Ошибку намеренно не глушим: её показывает сама карточка.
  async function handleDnsChainPresetApply(preset: SingboxRouterDNSChainPreset) {
    await api.singboxRouterSetDNSChainPreset(preset);
    await loadDnsChainPreset();
    await singboxRouterStore.loadAll();
  }

  onMount(() => {
    void singboxRouterStore.loadAll();
    void loadDnsChainPreset();
    void loadActiveProxyCount();
    void loadAllInbounds();
  });

  // Derived modal targets
  const ruleEditTarget = $derived<SingboxRouterRule | undefined>(
    ruleEditIdx !== null ? $storeRules[ruleEditIdx] : undefined
  );
  const rsEditTarget = $derived<SingboxRouterRuleSet | undefined>(
    rsEditTag !== null ? $storeRuleSets.find((rs) => rs.tag === rsEditTag) : undefined
  );
  const outboundEditTarget = $derived<SingboxRouterOutbound | undefined>(
    outboundEditTag !== null ? $storeOutbounds.find((o) => o.tag === outboundEditTag) : undefined
  );
  const dnsServerEditTarget = $derived<SingboxRouterDNSServer | undefined>(
    dnsServerEditTag !== null ? $storeDnsServers.find((s) => s.tag === dnsServerEditTag) : undefined
  );
  const dnsRuleEditTarget = $derived<SingboxRouterDNSRule | undefined>(
    dnsRuleEditIdx !== null ? $storeDnsRules[dnsRuleEditIdx] : undefined
  );

  // ruleSetUsage for RuleEditModal: exclude currently edited index
  const ruleSetUsageForRuleAdd = $derived(computeRuleSetUsage($storeRules));
  const ruleSetUsageForRuleEdit = $derived(
    ruleEditIdx === null
      ? new Map<string, number>()
      : computeRuleSetUsage($storeRules, ruleEditIdx)
  );
  // Catalog «добавлено»-tile differentiation: a set counts as used when ANY rule —
  // route or DNS — references it (route-only usage would mislabel DNS-only sets).
  const ruleSetUsageForCatalog = $derived(
    computeRuleSetUsage([...$storeRules, ...$storeDnsRules])
  );
  // ruleSetUsage for DNSRuleEditModal: exclude currently edited index
  const ruleSetUsageForDnsAdd = $derived(computeRuleSetUsage($storeDnsRules));
  const ruleSetUsageForDnsEdit = $derived(
    dnsRuleEditIdx === null
      ? new Map<string, number>()
      : computeRuleSetUsage($storeDnsRules, dnsRuleEditIdx)
  );

  // Engine badge keys on the live interception state, not the persisted
  // toggle: enabled+active → работает (ON); enabled but jumps gone → СБОЙ;
  // disabled → OFF. XOR-режимы: когда активен режим FakeIP, общий sing-box
  // крутит fakeip-слот и TPROXY-перехват не установлен — это НЕАКТИВЕН (muted),
  // НЕ ложный СБОЙ. Оценка active/СБОЙ только когда TProxy и есть активный режим.
  const mihomoOwnsRouting = $derived($storeSettings?.routingEngine === 'mihomo');
  // kind — код состояния для логики; value — подпись ячейки (переводится).
  const engineStat = $derived.by<{
    kind: 'off' | 'inactive' | 'mihomo' | 'on' | 'fail';
    value: string;
    tone: StatCellData['tone'];
  }>(() => {
    if (!$storeStatus?.enabled) return { kind: 'off', value: 'OFF', tone: 'muted' };
    if (mihomoOwnsRouting) return { kind: 'mihomo', value: 'MIHOMO', tone: 'muted' };
    if ($storeSettings?.routingMode === 'fakeip-tun') {
      return { kind: 'inactive', value: m.sb_router_expert_engine_inactive(), tone: 'muted' };
    }
    return $storeStatus.active
      ? { kind: 'on', value: 'ON', tone: 'success' }
      : { kind: 'fail', value: m.sb_router_expert_engine_fail(), tone: 'error' };
  });

  // Живые ресурсы движка: память из SSE singbox:memory, скорость и объём —
  // кумулятивные totals Clash (singbox:traffic-totals). Значения честные:
  // без работающего TProxy-движка (в т.ч. в режиме FakeIP, где ячейка
  // «Движок» показывает НЕАКТИВЕН) или до второго снимка — «—», а не
  // протухшие числа.
  const engineRunning = $derived(
    ($storeStatus?.enabled ?? false)
    && ($storeStatus?.active ?? false)
    && !mihomoOwnsRouting
    && $storeSettings?.routingMode !== 'fakeip-tun',
  );
  const liveStats = $derived($singboxTrafficLive);
  const memCellValue = $derived(
    engineRunning && $singboxMemory > 0 ? formatBytes($singboxMemory) : '—',
  );
  const rateCellValue = $derived(
    engineRunning && liveStats.rate.hasRate
      ? formatByteRate(liveStats.rate.downloadRate)
      : '—',
  );

  const statCells: StatCellData[] = $derived([
    {
      label: m.sb_router_expert_stat_engine(),
      value: engineStat.value,
      tone: engineStat.tone,
      helpTitle:
        engineStat.kind === 'mihomo'
          ? m.sb_router_expert_stat_engine_mihomo_title()
          : engineStat.kind === 'inactive'
          ? m.sb_router_expert_stat_engine_inactive_title()
          : undefined,
      helpText:
        engineStat.kind === 'mihomo'
          ? m.sb_router_expert_stat_engine_mihomo_text()
          : engineStat.kind === 'inactive'
          ? m.sb_router_expert_stat_engine_inactive_text()
          : undefined,
      onClick:
        engineStat.kind === 'fail' && $storeStatus?.lastError
          ? () => (engineFatalOpen = true)
          : undefined,
      actionLabel: m.sb_router_expert_stat_details(),
    },
    {
      label: m.sb_router_expert_stat_memory(),
      value: memCellValue,
      compact: true,
      helpTitle: m.sb_router_expert_stat_memory_title(),
      helpText: m.sb_router_expert_stat_memory_text(),
    },
    {
      label: m.sb_router_expert_stat_traffic(),
      value: rateCellValue,
      compact: true,
      helpTitle: m.sb_router_expert_stat_traffic_title(),
      helpText: m.sb_router_expert_stat_traffic_text(),
      helpItems: [
        m.sb_router_expert_stat_traffic_upload({ value: engineRunning && liveStats.rate.hasRate ? formatByteRate(liveStats.rate.uploadRate) : '—' }),
        m.sb_router_expert_stat_traffic_session({ value: engineRunning ? formatBytes(liveStats.totals.downloadBytes + liveStats.totals.uploadBytes) : '—' }),
      ],
    },
    {
      label: m.sb_router_expert_stat_rules(),
      value: String($storeRules.length),
      helpTitle: m.sb_router_expert_stat_rules_title(),
      helpText: m.sb_router_expert_stat_rules_text(),
      helpItems: [
        m.sb_router_expert_stat_rules_item_order(),
        m.sb_router_expert_stat_rules_item_default(),
      ],
    },
    {
      label: 'Rule-sets',
      value: String($storeRuleSets.length),
      helpTitle: m.sb_router_expert_stat_rulesets_title(),
      helpText: m.sb_router_expert_stat_rulesets_text(),
      helpItems: [
        m.sb_router_expert_stat_rulesets_remote(),
        m.sb_router_expert_stat_rulesets_local(),
        m.sb_router_expert_stat_rulesets_inline(),
      ],
    },
    {
      label: 'OUTBOUNDS',
      value: String($storeOutbounds.length),
      helpTitle: 'Outbounds',
      helpText: m.sb_router_expert_stat_outbounds_text(),
      helpItems: [
        m.sb_router_expert_stat_outbounds_item_route(),
        m.sb_router_expert_stat_outbounds_item_dns(),
      ],
    },
    {
      label: 'DNS',
      value: String($storeDnsRules.length),
      helpTitle: m.sb_router_expert_stat_dns_title(),
      helpText: m.sb_router_expert_stat_dns_text(),
      helpItems: [
        m.sb_router_expert_stat_dns_item_separate(),
        m.sb_router_expert_stat_dns_item_domains(),
      ],
    },
    {
      label: 'Rewrite',
      value: String($storeDnsRewrites.length),
      helpTitle: m.sb_router_expert_stat_rewrite_title(),
      helpText: m.sb_router_expert_stat_rewrite_text(),
      helpItems: [
        m.sb_router_expert_stat_rewrite_item_override(),
        m.sb_router_expert_stat_rewrite_item_before(),
      ],
    },
      {
        label: m.sb_router_expert_stat_proxy(),
        value: activeProxyCountLabel,
        helpTitle: 'Device Proxy / Inbounds',
        helpText: m.sb_router_expert_stat_proxy_text(),
        helpItems: [
          m.sb_router_expert_stat_proxy_item_active(),
          m.sb_router_expert_stat_proxy_item_off(),
      ],
    },
  ]);

  // ── Bulk-выбор правил (Эксперт: RoutingTable) ─────────────────────────
  let rulesSelectMode = $state(false);
  let rulesSelected = $state<Set<number>>(new Set());
  let rulesBulkBusy = $state(false);
  let prevRulesRef: SingboxRouterRule[] | undefined;

  function isSelectableRule(r: SingboxRouterRule): boolean {
    const action = mapRuleAction(r);
    return !isSystemRule(r) && (action === 'route' || action === 'direct');
  }

  const selectableRuleIndices = $derived(
    $storeRules.reduce<number[]>((acc, r, i) => {
      if (isSelectableRule(r)) acc.push(i);
      return acc;
    }, []),
  );
  const selectableRuleIndexSet = $derived(new Set(selectableRuleIndices));

  // Тот же каталог outbound'ов, что у RuleEditModal ($storeOptions), сплющенный
  // в плоский список для BulkSelectBar.
  const bulkOutboundOptions = $derived(
    $storeOptions.flatMap((g) => g.items.map((i) => ({ value: i.value, label: i.label, group: outboundGroupLabel(g.id) }))),
  );

  function toggleRulesSelectMode(): void {
    rulesSelectMode = true;
    rulesSelected = new Set();
  }

  function cancelRulesSelectMode(): void {
    rulesSelectMode = false;
    rulesSelected = new Set();
  }

  function toggleRuleSelect(index: number): void {
    const next = new Set(rulesSelected);
    if (next.has(index)) next.delete(index);
    else next.add(index);
    rulesSelected = next;
  }

  function selectAllRules(): void {
    rulesSelected = new Set(selectableRuleIndices);
  }

  async function applyRulesBulkOutbound(value: string): Promise<void> {
    if (rulesSelected.size === 0 || rulesBulkBusy) return;
    rulesBulkBusy = true;
    try {
      const { updated } = await api.singboxRouterBulkOutbound([...rulesSelected], value);
      notifications.success(m.sb_router_expert_changed({ count: updated }));
      rulesSelectMode = false;
      rulesSelected = new Set();
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      rulesBulkBusy = false;
    }
  }

  // Список правил мог перезагрузиться из другого источника, пока шло
  // выделение (SSE-инвалидация, чужое действие) — индексы больше не
  // гарантированно соответствуют выбранным чекбоксам. bulkBusy гейтит
  // собственный apply (см. applyRulesBulkOutbound — selectMode уже false
  // к моменту прихода нового $storeRules).
  $effect(() => {
    const current = $storeRules;
    if (rulesSelectMode && !rulesBulkBusy && prevRulesRef !== undefined && current !== prevRulesRef) {
      rulesSelectMode = false;
      rulesSelected = new Set();
      notifications.info(m.sb_router_expert_list_changed());
    }
    prevRulesRef = current;
  });

  // ── Bulk-выбор rule-set'ов (Эксперт: RuleSetsTable) ────────────────────
  let rsSelectMode = $state(false);
  let rsSelected = $state<Set<string>>(new Set());
  let rsBulkBusy = $state(false);
  let prevRuleSetsRef: SingboxRouterRuleSet[] | undefined;

  // Видимые-выбираемые теги (remote среди отфильтрованных RuleSetsTable) —
  // «Выбрать все» и видимость кнопки должны идти от активного тип-фильтра
  // таблицы, а не от полного $storeRuleSets (#558 fix-волна, finding F2).
  let rsFilteredSelectableTags = $state<string[]>([]);

  const rsBulkDetourOptions = $derived(buildDownloadDetourOptions($storeOptions, m.sb_router_expert_bulk_reset()));

  function toggleRsSelectMode(): void {
    rsSelectMode = true;
    rsSelected = new Set();
  }

  function cancelRsSelectMode(): void {
    rsSelectMode = false;
    rsSelected = new Set();
  }

  function toggleRsSelect(tag: string): void {
    const next = new Set(rsSelected);
    if (next.has(tag)) next.delete(tag);
    else next.add(tag);
    rsSelected = next;
  }

  function selectAllRs(): void {
    rsSelected = new Set(rsFilteredSelectableTags);
  }

  async function applyRsBulkDetour(value: string): Promise<void> {
    if (rsSelected.size === 0 || rsBulkBusy) return;
    rsBulkBusy = true;
    try {
      const { updated } = await handleRsBulkDetour([...rsSelected], value);
      notifications.success(m.sb_router_expert_changed({ count: updated }));
      rsSelectMode = false;
      rsSelected = new Set();
    } catch (e) {
      notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      rsBulkBusy = false;
    }
  }

  // Аналогичный сброс выделения rule-set'ов при внешней инвалидации списка.
  $effect(() => {
    const current = $storeRuleSets;
    if (rsSelectMode && !rsBulkBusy && prevRuleSetsRef !== undefined && current !== prevRuleSetsRef) {
      rsSelectMode = false;
      rsSelected = new Set();
      notifications.info(m.sb_router_expert_list_changed());
    }
    prevRuleSetsRef = current;
  });

  // Смена тип-фильтра в RuleSetsTable могла скрыть уже выбранные теги —
  // держим rsSelected ⊆ видимых-выбираемых, иначе count в баре и Apply
  // затрагивали бы невидимые наборы (#558 fix-волна 2, Minor 4).
  $effect(() => {
    const visible = new Set(rsFilteredSelectableTags);
    if (rsSelected.size === 0) return;
    let changed = false;
    const next = new Set<string>();
    for (const tag of rsSelected) {
      if (visible.has(tag)) next.add(tag);
      else changed = true;
    }
    if (changed) rsSelected = next;
  });

  // Rule handlers
  function handleDeleteRule(idx: number) {
    pendingConfirm = {
      title: () => m.sb_router_expert_rule_delete_title(),
      message: () => m.sb_router_expert_rule_delete_message({ n: idx }),
      run: async () => {
        try {
          await api.singboxRouterDeleteRule(idx);
          await singboxRouterStore.loadAll();
          notifications.success(m.sb_router_expert_rule_deleted());
        } catch (e) {
          notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  function handleDeleteDNSRule(idx: number) {
    pendingConfirm = {
      title: () => m.sb_router_expert_dns_rule_delete_title(),
      message: () => m.sb_router_expert_dns_rule_delete_message({ n: idx + 1 }),
      run: async () => {
        try {
          await api.singboxRouterDeleteDNSRule(idx);
          await singboxRouterStore.loadAll();
          notifications.success(m.sb_router_expert_dns_rule_deleted());
        } catch (e) {
          notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  function handleDeleteDnsServer(tag: string) {
    pendingConfirm = {
      title: () => m.sb_router_expert_dns_server_delete_title(),
      message: () => m.sb_router_expert_dns_server_delete_message({ tag }),
      run: async () => {
        try {
          await api.singboxRouterDeleteDNSServer(tag);
          await singboxRouterStore.loadAll();
          notifications.success(m.sb_router_expert_dns_server_deleted());
        } catch (e) {
          notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  async function handleMoveRule(idx: number, dir: 'up' | 'down') {
    const to = dir === 'up' ? idx - 1 : idx + 1;
    if (to < 0 || to >= $storeRules.length) return;
    try {
      await api.singboxRouterMoveRule(idx, to);
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
    }
  }

  // Rule save handlers (called by modals)
  async function handleRuleSave(rule: SingboxRouterRule) {
    if (ruleEditIdx !== null) {
      await api.singboxRouterUpdateRule(ruleEditIdx, rule);
    } else {
      await api.singboxRouterAddRule(rule);
    }
    ruleEditIdx = null;
    ruleAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  // RuleSet handlers
  function handleDeleteRs(tag: string) {
    pendingConfirm = {
      title: () => m.sb_router_expert_ruleset_delete_title(),
      message: () => m.sb_router_expert_ruleset_delete_message({ tag }),
      run: async () => {
        try {
          await api.singboxRouterDeleteRuleSet(tag);
          await singboxRouterStore.loadAll();
          notifications.success(m.sb_router_expert_ruleset_deleted());
        } catch (e) {
          notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  async function handleRsAddSave(rs: SingboxRouterRuleSet) {
    await api.singboxRouterAddRuleSet(rs);
    rsAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleRsEditSave(rs: SingboxRouterRuleSet) {
    if (rsEditTag !== null) {
      await api.singboxRouterUpdateRuleSet(rsEditTag, rs);
    }
    rsEditTag = null;
    await singboxRouterStore.loadAll();
  }

  async function handleRsBulkDetour(tags: string[], downloadDetour: string) {
    const result = await api.singboxRouterBulkDetour(tags, downloadDetour);
    await singboxRouterStore.loadAll();
    return result;
  }

  async function handleRsCatalogConfirm(presets: CatalogPreset[]) {
    if (rsCatalogBusy || presets.length === 0) return;
    rsCatalogBusy = true;
    try {
      const result = await applyCatalogPresetsAsRuleSets(presets, $storeRuleSets);
      await singboxRouterStore.loadAll();

      if (result.added.length > 0) {
        notifications.success(m.sb_router_expert_catalog_added({ count: result.added.length }));
      } else if (result.failures.length === 0 && result.emptyPresets.length > 0) {
        notifications.error(m.sb_router_expert_catalog_no_sets());
      } else if (result.failures.length === 0) {
        notifications.info(m.sb_router_expert_catalog_already());
      }

      if (result.failures.length > 0) {
        const msg = result.failures.map((f) => `${f.tag}: ${f.error}`).join('; ');
        notifications.error(m.sb_router_expert_add_failed({ message: msg }));
      } else if (result.added.length > 0 || result.emptyPresets.length === 0) {
        rsCatalogOpen = false;
      }
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      rsCatalogBusy = false;
    }
  }

  async function handleGeositeCatalogConfirm(names: string[], baseUrl: string) {
    if (geositeCatalogBusy || names.length === 0) return;
    geositeCatalogBusy = true;
    try {
      const result = await addGeositeRuleSets(names, baseUrl, $storeRuleSets);
      await singboxRouterStore.loadAll();

      if (result.added.length > 0) {
        notifications.success(m.sb_router_expert_catalog_sagernet_added({ count: result.added.length }));
      } else if (result.failures.length === 0) {
        notifications.info(m.sb_router_expert_catalog_already());
      }

      if (result.failures.length > 0) {
        const msg = result.failures.map((f) => `${f.tag}: ${f.error}`).join('; ');
        notifications.error(m.sb_router_expert_add_failed({ message: msg }));
      } else {
        geositeCatalogOpen = false;
      }
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      geositeCatalogBusy = false;
    }
  }

  // Outbound handlers
  async function handleOutboundAddSave(o: SingboxRouterOutbound) {
    await api.singboxRouterAddOutbound(o);
    outboundAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleOutboundEditSave(o: SingboxRouterOutbound) {
    if (outboundEditTag !== null) {
      await api.singboxRouterUpdateOutbound(outboundEditTag, o);
    }
    outboundEditTag = null;
    await singboxRouterStore.loadAll();
  }

  function handleDeleteOutbound(tag: string) {
    const outbound = $storeOutbounds.find((o) => o.tag === tag);
    if (!outbound) return;
    pendingConfirm = {
      title: () => m.sb_router_expert_outbound_delete_title(),
      message: () => m.sb_router_expert_outbound_delete_message({ tag }),
      run: async () => {
        try {
          await api.singboxRouterDeleteOutbound(tag);
          await singboxRouterStore.loadAll();
          notifications.success(m.sb_router_expert_outbound_deleted());
        } catch (e) {
          notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
        }
      },
    };
  }

  // DNS server handlers
  async function handleDnsServerAddSave(server: SingboxRouterDNSServer) {
    await api.singboxRouterAddDNSServer(server);
    dnsServerAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleDnsServerEditSave(server: SingboxRouterDNSServer) {
    if (dnsServerEditTag !== null) {
      await api.singboxRouterUpdateDNSServer(dnsServerEditTag, server);
    }
    dnsServerEditTag = null;
    await singboxRouterStore.loadAll();
  }

  // DNS rule handlers
  async function handleDnsRuleAddSave(rule: SingboxRouterDNSRule) {
    await api.singboxRouterAddDNSRule(rule);
    dnsRuleAddOpen = false;
    await singboxRouterStore.loadAll();
  }

  async function handleDnsRuleEditSave(rule: SingboxRouterDNSRule) {
    if (dnsRuleEditIdx !== null) {
      await api.singboxRouterUpdateDNSRule(dnsRuleEditIdx, rule);
    }
    dnsRuleEditIdx = null;
    await singboxRouterStore.loadAll();
  }

  let lanNamesBusy = $state(false);
  const lanNamesOn = $derived($storeDnsRules.some(isLanNamesRule));

  async function toggleLanNamesRule() {
    const on = lanNamesOn;
    lanNamesBusy = true;
    try {
      let created: 'created' | 'exists' | null = null;
      if (on) await removeLanNamesRule();
      else created = (await ensureLanNamesRule()) === 'created' ? 'created' : 'exists';
      await singboxRouterStore.loadAll();
      notifications.success(
        created === null
          ? m.sb_router_expert_lan_names_removed()
          : created === 'created'
            ? m.sb_router_expert_lan_names_created()
            : m.sb_router_expert_lan_names_exists()
      );
    } catch (e) {
      notifications.error(m.sb_router_expert_action_failed({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      lanNamesBusy = false;
    }
  }

  // Перестановка DNS-правила перетаскиванием: оптимистика в стор, при ошибке
  // бэкенда (например move ломает цепочку evaluate/match_response) — откат
  // снапшота + тост, как в fakeip/DnsTab.
  async function handleMoveDnsRule(from: number, to: number) {
    const snapshot = get(singboxRouterStore.dnsRules);
    const next = snapshot.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    singboxRouterStore.applyDNSRules(next);
    try {
      await api.singboxRouterMoveDNSRule(from, to);
      await singboxRouterStore.loadAll();
    } catch (e) {
      singboxRouterStore.applyDNSRules(snapshot);
      notifications.error(m.sb_router_expert_move_failed({ message: e instanceof Error ? e.message : String(e) }));
    }
  }

  async function handleDnsGlobalsSave(globals: {
    final: string;
    strategy: SingboxRouterDNSStrategy;
    timeout: string;
  }) {
    await api.singboxRouterPutDNSGlobals(globals);
    dnsGlobalsModalOpen = false;
    await singboxRouterStore.loadAll();
  }
</script>


<div class="wrap">
  <StatStrip cells={statCells} />

  <div class="main-grid">
    <div class="col-main">
      <SidePanel
        section="rules"
        title={m.sb_router_expert_rules_title()}
        count={String($storeRules.length)}
      >
        {#snippet actions()}
          {#if rulesSelectMode}
            <Button
              variant="ghost"
              size="sm"
              disabled={rulesBulkBusy || selectableRuleIndices.length === 0}
              onclick={selectAllRules}
            >
              {m.sb_router_expert_select_all()}
            </Button>
            <Button variant="ghost" size="sm" disabled={rulesBulkBusy} onclick={cancelRulesSelectMode}>
              {m.common_cancel()}
            </Button>
          {:else}
            {#if selectableRuleIndices.length > 0}
              <Button variant="ghost" size="sm" onclick={toggleRulesSelectMode}>{m.common_select()}</Button>
            {/if}
            <Button variant="primary" size="sm" onclick={() => (ruleAddOpen = true)}>{m.sb_router_expert_add_rule()}</Button>
          {/if}
        {/snippet}
        <div class="globals-bar">
          <span class="gb-label gb-label-full">{m.sb_router_expert_first_match_full()}</span>
          <span class="gb-label gb-label-mobile">{m.sb_router_expert_first_match_mobile()}</span>
          <div class="route-final-select">
            <Dropdown bind:value={draftRouteFinal} options={routeFinalOptions} fullWidth />
          </div>
          {#if routeFinalDirty}
            <button class="gb-save" onclick={saveRouteFinal} disabled={routeFinalBusy} type="button">
              {m.common_save()}
            </button>
          {/if}
        </div>
        <RoutingTable
          bare
          rules={$storeRules}
          outbounds={$storeOutbounds}
          outboundOptions={$storeOptions}
          subscriptions={$subscriptionsStore.data}
          proxyGroups={$singboxProxies.data ?? []}
          singboxTunnels={$singboxTunnels.data ?? []}
          onEdit={(idx) => (ruleEditIdx = idx)}
          onDelete={handleDeleteRule}
          onMove={handleMoveRule}
          selectMode={rulesSelectMode}
          selected={rulesSelected}
          onToggleSelect={toggleRuleSelect}
          isSelectable={(i) => selectableRuleIndexSet.has(i)}
        />
        {#if rulesSelectMode}
          <BulkSelectBar
            count={rulesSelected.size}
            options={bulkOutboundOptions}
            applyLabel={m.common_apply()}
            onapply={applyRulesBulkOutbound}
            oncancel={cancelRulesSelectMode}
            busy={rulesBulkBusy}
          />
        {/if}
      </SidePanel>

      <SidePanel
        section="ruleSets"
        title="Rule-sets"
        count={String($storeRuleSets.length)}
      >
        {#snippet actions()}
          <div class="rs-head-actions">
            {#if rsSelectMode}
              <Button
                variant="ghost"
                size="sm"
                disabled={rsBulkBusy || rsFilteredSelectableTags.length === 0}
                onclick={selectAllRs}
              >
                {m.sb_router_expert_select_all()}
              </Button>
              <Button variant="ghost" size="sm" disabled={rsBulkBusy} onclick={cancelRsSelectMode}>
                {m.common_cancel()}
              </Button>
            {:else if rsFilteredSelectableTags.length > 0}
              <Button variant="ghost" size="sm" onclick={toggleRsSelectMode}>{m.common_select()}</Button>
            {/if}
            <Button variant="secondary" size="sm" onclick={() => (rsCatalogOpen = true)}>
              {#snippet iconBefore()}
                <LayoutGrid size={14} aria-hidden="true" />
              {/snippet}
              {m.sb_router_expert_catalog()}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onclick={() => (geositeCatalogOpen = true)}
              title={m.sb_router_expert_geosite_title()}
            >
              {#snippet iconBefore()}
                <Library size={14} aria-hidden="true" />
              {/snippet}
              SagerNet
            </Button>
            <Button variant="primary" size="sm" onclick={() => (rsAddOpen = true)}>{m.sb_router_expert_add_set()}</Button>
          </div>
        {/snippet}
        <div class="panel-cap">{m.sb_router_expert_rulesets_cap()}</div>
        <RuleSetsTable
          bare
          ruleSets={sortedRuleSets}
          alphaSort={alphaSortRuleSets}
          onToggleAlphaSort={() => {
            alphaSortRuleSets = !alphaSortRuleSets;
            persistAlphaSortRuleSets(alphaSortRuleSets);
          }}
          onEdit={(tag) => (rsEditTag = tag)}
          onDelete={handleDeleteRs}
          selectMode={rsSelectMode}
          selected={rsSelected}
          onToggleSelect={toggleRsSelect}
          onSelectableChange={(tags) => (rsFilteredSelectableTags = tags)}
        />
        {#if rsSelectMode}
          <BulkSelectBar
            count={rsSelected.size}
            options={rsBulkDetourOptions}
            applyLabel={m.common_apply()}
            onapply={applyRsBulkDetour}
            oncancel={cancelRsSelectMode}
            busy={rsBulkBusy}
            allowEmpty
          />
        {/if}
      </SidePanel>
    </div>

    <div class="col-sidebar">
      <SidePanel
        section="outbounds"
        title="Outbounds"
        count={String($storeOutbounds.length)}
        actionLabel="+ Outbound"
        actionVariant="filled"
        onAction={() => (outboundAddOpen = true)}
      >
        <OutboundsCompact
          outbounds={$storeOutbounds}
          subscriptions={$subscriptionsStore.data ?? []}
          groups={$subscriptionGroupsStore.data ?? []}
          usage={outboundUsageContext}
          proxyGroups={$singboxProxies.data ?? []}
          outboundOptions={$storeOptions}
          onEdit={(tag) => (outboundEditTag = tag)}
          onDelete={handleDeleteOutbound}
        />
      </SidePanel>

      <SidePanel
        section="dnsServers"
        title={m.sb_router_expert_dns_servers_title()}
        count={String($storeDnsServers.length)}
      >
        {#snippet actions()}
          {#snippet lanNamesCheck()}
            <Check size={14} aria-hidden="true" />
          {/snippet}
          <Button
            variant={lanNamesOn ? 'secondary' : 'ghost'}
            size="sm"
            disabled={lanNamesBusy}
            iconBefore={lanNamesOn ? lanNamesCheck : undefined}
            title={lanNamesOn
              ? m.sb_router_expert_lan_names_active_title()
              : m.sb_router_expert_lan_names_create_title()}
            onclick={toggleLanNamesRule}
          >
            {m.sb_router_expert_lan_names_button()}
          </Button>
          <Button variant="primary" size="sm" onclick={() => (dnsServerAddOpen = true)}>{m.sb_router_expert_add_server()}</Button>
        {/snippet}
        <button
          type="button"
          class="globals-summary"
          onclick={openDnsGlobalsModal}
        >
          <div>
            <span class="gb-label">{m.sb_router_expert_dns_default()}</span>
            <div class="globals-summary-values">
              <span>Final: <strong>{$storeDnsGlobals.final || '—'}</strong></span>
              <span>Strategy: <strong>{$storeDnsGlobals.strategy || 'default'}</strong></span>
            </div>
          </div>
          <span class="globals-summary-action">{m.sb_router_expert_configure()}</span>
        </button>
        <DNSChainPresetCard
          servers={$storeDnsServers}
          rules={$storeDnsRules}
          preset={dnsChainPreset}
          finalServer={$storeDnsGlobals.final}
          fakeipMode={$storeSettings?.routingMode === 'fakeip-tun'}
          onApply={handleDnsChainPresetApply}
        />
        <DnsServersCompact
          servers={$storeDnsServers}
          rules={$storeDnsRules}
          outbounds={$storeOutbounds}
          outboundOptions={$storeOptions}
          subscriptions={$subscriptionsStore.data}
          proxyGroups={$singboxProxies.data ?? []}
          singboxTunnels={$singboxTunnels.data ?? []}
          dnsUsage={dnsServerUsageContext}
          onEditServer={(tag) => (dnsServerEditTag = tag)}
          onDeleteServer={handleDeleteDnsServer}
          onEditRule={(idx) => (dnsRuleEditIdx = idx)}
          onDeleteRule={handleDeleteDNSRule}
          onMoveRule={handleMoveDnsRule}
          onAddRule={() => (dnsRuleAddOpen = true)}
        />
      </SidePanel>

      <SidePanel
        section="dnsRewrite"
        title="DNS Rewrite"
        count={String($storeDnsRewrites.length)}
        actionLabel={m.sb_router_expert_add()}
        actionVariant="filled"
        onAction={() => (rewriteAddMode = true)}
      >
        <DNSRewritesList
          rewrites={$storeDnsRewrites}
          onChange={() => singboxRouterStore.loadAll()}
          showHeader={false}
          hideColumnHeader={true}
          bind:addMode={rewriteAddMode}
        />
      </SidePanel>

        <SidePanel
          section="inbounds"
          title="Inbounds"
          count={inboundsPanelCountLabel}
          actionLabel={m.sb_router_expert_add()}
          actionVariant="filled"
          onAction={addInbound}
        >
        {#key dpReloadKey}
          <DeviceProxyCompact bare onSelect={openInbound} onDelete={deleteInbound} />
        {/key}
        {#if mirrorInbounds.length > 0 || inboundWarnings.length > 0}
          <div class="inbounds-mirror-wrap">
            <InboundsMirror entries={mirrorInbounds} warnings={inboundWarnings} />
          </div>
        {/if}
      </SidePanel>
    </div>
  </div>
</div>

<!-- RuleEditModal: add -->
{#if ruleAddOpen}
  <RuleEditModal
    outboundOptions={$storeOptions}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForRuleAdd}
    onClose={() => (ruleAddOpen = false)}
    onSave={handleRuleSave}
  />
{/if}

<!-- RuleEditModal: edit -->
{#if ruleEditIdx !== null && ruleEditTarget !== undefined}
  <RuleEditModal
    rule={ruleEditTarget}
    outboundOptions={$storeOptions}
    availableRuleSets={$storeRuleSets}
    ruleSetUsage={ruleSetUsageForRuleEdit}
    onClose={() => (ruleEditIdx = null)}
    onSave={handleRuleSave}
  />
{/if}

<SbRouterRuleSetCatalogModal
  open={rsCatalogOpen}
  existingRuleSetTags={$storeRuleSets.map((rs) => rs.tag)}
  ruleSetUsage={ruleSetUsageForCatalog}
  submitting={rsCatalogBusy}
  onclose={() => {
    if (!rsCatalogBusy) rsCatalogOpen = false;
  }}
  onconfirm={handleRsCatalogConfirm}
/>

<SbRouterGeositeCatalogModal
  open={geositeCatalogOpen}
  existingRuleSetTags={$storeRuleSets.map((rs) => rs.tag)}
  submitting={geositeCatalogBusy}
  onclose={() => {
    if (!geositeCatalogBusy) geositeCatalogOpen = false;
  }}
  onconfirm={handleGeositeCatalogConfirm}
/>

<!-- RuleSetAddModal: add -->
{#if rsAddOpen}
  <RuleSetAddModal
    outboundOptions={$storeOptions}
    onClose={() => (rsAddOpen = false)}
    onSave={handleRsAddSave}
  />
{/if}

<!-- RuleSetAddModal: edit (ruleSet prop activates edit-mode) -->
{#if rsEditTag !== null && rsEditTarget !== undefined}
  <RuleSetAddModal
    ruleSet={rsEditTarget}
    outboundOptions={$storeOptions}
    onClose={() => (rsEditTag = null)}
    onSave={handleRsEditSave}
  />
{/if}

<!-- CompositeOutboundEditModal: add -->
{#if outboundAddOpen}
  <CompositeOutboundEditModal
    outbounds={$storeOutbounds}
    outboundOptions={$storeOptions}
    onClose={() => (outboundAddOpen = false)}
    onSave={handleOutboundAddSave}
  />
{/if}

<!-- CompositeOutboundEditModal: edit -->
{#if outboundEditTag !== null && outboundEditTarget !== undefined}
  <CompositeOutboundEditModal
    outbound={outboundEditTarget}
    outbounds={$storeOutbounds}
    outboundOptions={$storeOptions}
    onClose={() => (outboundEditTag = null)}
    onSave={handleOutboundEditSave}
  />
{/if}

<!-- DNSServerEditModal: add -->
{#if dnsServerAddOpen}
  <DNSServerEditModal
    servers={$storeDnsServers}
    outboundOptions={$storeOptions}
    onClose={() => (dnsServerAddOpen = false)}
    onSave={handleDnsServerAddSave}
  />
{/if}

<!-- DNSServerEditModal: edit -->
{#if dnsServerEditTag !== null && dnsServerEditTarget !== undefined}
  <DNSServerEditModal
    server={dnsServerEditTarget}
    servers={$storeDnsServers}
    outboundOptions={$storeOptions}
    onClose={() => (dnsServerEditTag = null)}
    onSave={handleDnsServerEditSave}
  />
{/if}

<!-- DNSRuleEditModal: add -->
{#if dnsRuleAddOpen}
  <DNSRuleEditModal
    servers={$storeDnsServers}
    availableRuleSets={sortedRuleSets}
    ruleSetUsage={ruleSetUsageForDnsAdd}
    rules={$storeDnsRules}
    onClose={() => (dnsRuleAddOpen = false)}
    onSave={handleDnsRuleAddSave}
  />
{/if}

<!-- DNSRuleEditModal: edit -->
{#if dnsRuleEditIdx !== null && dnsRuleEditTarget !== undefined}
  <DNSRuleEditModal
    rule={dnsRuleEditTarget}
    servers={$storeDnsServers}
    availableRuleSets={sortedRuleSets}
    ruleSetUsage={ruleSetUsageForDnsEdit}
    rules={$storeDnsRules}
    ruleIndex={dnsRuleEditIdx ?? undefined}
    onClose={() => (dnsRuleEditIdx = null)}
    onSave={handleDnsRuleEditSave}
  />
{/if}

{#if dnsGlobalsModalOpen}
  <DNSGlobalsEditModal
    servers={$storeDnsServers}
    final={$storeDnsGlobals.final}
    strategy={$storeDnsGlobals.strategy}
    timeout={$storeDnsGlobals.timeout ?? ''}
    onClose={() => (dnsGlobalsModalOpen = false)}
    onSave={handleDnsGlobalsSave}
  />
{/if}

{#if inboundDrawerInstance}
  <InboundSettingsDrawer
    instance={inboundDrawerInstance}
    open={inboundDrawerOpen}
    onClose={() => (inboundDrawerOpen = false)}
    onSaved={onInboundSaved}
  />
{/if}

<ConfirmModal
  open={pendingConfirm !== null}
  title={pendingConfirm?.title() ?? ''}
  message={pendingConfirm?.message() ?? ''}
  busy={confirmBusy}
  onConfirm={runConfirm}
  onClose={() => { if (!confirmBusy) pendingConfirm = null; }}
/>

<EngineFatalModal
  open={engineFatalOpen}
  lastError={$storeStatus?.lastError ?? ''}
  onclose={() => (engineFatalOpen = false)}
/>

<style>
  .wrap {
    max-width: none;
    margin: 0 auto;
    /* Компактный режим держит колонку контента в 960px при любом вьюпорте —
       ломать сетку надо по ширине контейнера, а не окна (см. @container ниже). */
    container-type: inline-size;
  }
  /* Caption внутри SidePanel body — sub-title строкой над контентом */
  .rs-head-actions {
    display: flex;
    align-items: center;
    /* Три кнопки не влезают в шапку панели на узких телефонах — перенос
       вместо молчаливого обрезания overflow:hidden родителя. */
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 8px;
  }
  .panel-cap {
    padding: 8px 14px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  /* Read-only зеркало остальных inbound'ов под интерактивным device-proxy */
  .inbounds-mirror-wrap {
    padding: 10px 14px;
    border-top: 1px solid var(--border);
  }
  /* Globals-бар route-final (шапка панели «Правила») */
  .globals-bar {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 8px;
    padding: 8px 14px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
  }
  .gb-label {
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .gb-label-mobile {
    display: none;
  }
  .route-final-select {
    min-width: 0;
    width: 100%;
  }
  /* Globals-секция DNS (шапка панели «DNS-серверы») */
  .globals-summary {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.75rem 0.875rem;
    background: var(--bg-tertiary);
    border: 0;
    border-bottom: 1px solid var(--border);
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .globals-summary-values {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem 0.75rem;
    margin-top: 0.25rem;
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .globals-summary-action {
    flex: 0 0 auto;
    font-size: 0.72rem;
    font-weight: 700;
    color: var(--accent);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .main-grid {
    display: grid;
    grid-template-columns: minmax(0, 8fr) minmax(0, 4fr);
    gap: 14px;
  }
  .col-main {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }
  .col-sidebar {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
  }
  /* 1280px — ниже этого сайдбар (4/12) уже́ шапки панели: заголовок + две
     кнопки не влезают и наезжают друг на друга. */
  @container (max-width: 1280px) {
    .main-grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  @media (max-width: 768px) {
    .globals-bar {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      gap: 0.45rem;
      padding: 0.625rem 0.875rem;
    }
    .gb-label-full {
      display: none;
    }
    .gb-label-mobile {
      display: block;
      min-width: 0;
      font-size: 10px;
      line-height: 1.2;
      letter-spacing: 0.04em;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .route-final-select {
      width: 100%;
      min-width: 0;
    }
    .globals-summary {
      align-items: flex-start;
    }
    .globals-summary-values {
      display: grid;
      gap: 0.25rem;
    }
    .globals-summary-action {
      padding-top: 0.1rem;
    }
  }
</style>
