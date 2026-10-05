<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { SideDrawer, Toggle, Button, Badge, StatusDot, SegmentedControl, Modal } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
  import { singboxStatus } from '$lib/stores/singbox';
  import { singboxMemory } from '$lib/stores/singboxMemory';
  import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
  import { formatBytes, formatByteRate } from '$lib/utils/format';
  import { systemInfo } from '$lib/stores/system';
  import { opkgTunUnsupportedReason, opkgTunSupported } from '$lib/utils/opkgTunSupport';
  import { notifications } from '$lib/stores/notifications';
  import { drawerOpen, closeDrawer } from './drawerStore';
  import { openSourceDrawer } from './sourceDrawerStore';
  import { mode } from './modeStore';
  import DepRow from './DepRow.svelte';
  import IssueRow from './IssueRow.svelte';
  import PortChipsInput from './PortChipsInput.svelte';
  import SubnetChipsInput from './SubnetChipsInput.svelte';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import QosSettingsCard from './QosSettingsCard.svelte';
  import PolicyTunCard from './PolicyTunCard.svelte';
  import BypassGeoIPTags from './BypassGeoIPTags.svelte';
  import OutboundOption from './OutboundOption.svelte';
  import { deriveDeps, deriveIssues } from './drawerData';
  import { formatSuppressedUntil } from './crashInfo';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
  import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface, type WanAutoOverride } from './wanMode';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import type { SingboxRouterSettings, SingboxRouterWANInterface, MihomoNativeGroup, MihomoNativeSubscription, MihomoNativeProxy } from '$lib/types';
  import { lookupIpKnowledge } from '$lib/utils/ipKnowledge';
  import { Globe, Shield, RefreshCw } from 'lucide-svelte';

  type AdaptiveRoutingSettings = {
    alwaysEntries?: string[];
    [key: string]: unknown;
  };

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;
  const storeOptions = singboxRouterStore.options;

  let open = $derived($drawerOpen);
  let s = $derived($status);
  // Эффективный путь cache.db: при незаданной настройке это может быть
  // рукописный путь из 00-base.json, который селектор выразить не может.
  let cfg = $derived($storeSettings);
  let isExpert = $derived($mode === 'expert');
  let tunSupported = $derived(opkgTunSupported($systemInfo.data));

  // ── Mihomo Traffic Mode (rule / global / direct) ──
  let currentMihomoMode = $state<'rule' | 'global' | 'direct'>('rule');
  let currentGlobalTarget = $state<string>('DIRECT');
  let clashGlobalAll = $state<string[]>([]);
  let mihomoNativeGroupsList = $state<MihomoNativeGroup[]>([]);
  let mihomoNativeSubsList = $state<MihomoNativeSubscription[]>([]);
  let mihomoNativeProxiesList = $state<MihomoNativeProxy[]>([]);
  let clashModeLoading = $state(false);

  const availableGlobalOutbounds = $derived.by(() => {
    const list: Array<{ value: string; label: string }> = [
      { value: 'DIRECT', label: m.sb_router_direct_option() },
      { value: 'REJECT', label: m.sb_router_reject_option() },
    ];

    // 1. Mihomo native proxy groups
    for (const g of mihomoNativeGroupsList) {
      if (g.enabled && !list.some((i) => i.value === g.name)) {
        list.push({ value: g.name, label: `${g.name} (${g.type})` });
      }
    }

    // 2. Legacy proxy groups in cfg
    const pGroups = cfg?.proxyGroups ?? [];
    for (const g of pGroups) {
      if (!list.some((i) => i.value === g.name)) {
        list.push({ value: g.name, label: `${g.name} (${g.type})` });
      }
    }

    // 3. AWG / System WireGuard
    const awgList = $awgTagsStore?.data ?? [];
    for (const t of awgList) {
      if (!list.some((i) => i.value === t.tag)) {
        list.push({ value: t.tag, label: t.label ? `${t.label} (${t.tag})` : t.tag });
      }
    }

    // 4. Native Mihomo subscriptions
    for (const s of mihomoNativeSubsList) {
      if (s.enabled && s.groupName && !list.some((i) => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name });
      }
    }

    // 5. Native Mihomo standalone proxies
    for (const p of mihomoNativeProxiesList) {
      if (p.enabled && !list.some((i) => i.value === p.name)) {
        list.push({ value: p.name, label: p.name });
      }
    }

    // 6. Sing-box tunnels
    const sbList = $singboxTunnels?.data ?? [];
    for (const t of sbList) {
      if (!list.some((i) => i.value === t.tag)) {
        list.push({ value: t.tag, label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag });
      }
    }

    // 7. Sing-box subscriptions
    const subList = $subscriptionsStore?.data ?? [];
    for (const sub of subList) {
      const tag = sub.selectorTag || sub.id;
      if (tag && !list.some((i) => i.value === tag)) {
        list.push({ value: tag, label: `${sub.label || tag} (${tag})` });
      }
    }

    // 8. Singbox standalone proxies
    const sbProxies = $singboxProxies?.data ?? [];
    for (const p of sbProxies) {
      if (!list.some((i) => i.value === p.tag)) {
        list.push({ value: p.tag, label: p.tag });
      }
    }

    // 9. If Clash GLOBAL returns any proxies not in list, add them
    for (const item of clashGlobalAll) {
      if (!list.some((i) => i.value === item)) {
        list.push({ value: item, label: item });
      }
    }

    return list;
  });

  const availableCloudOutbounds = $derived.by(() => {
    const filtered = availableGlobalOutbounds.filter(
      (o) => o.value !== 'DIRECT' && o.value !== 'REJECT'
    );
    if (filtered.length > 0) return filtered;
    return availableGlobalOutbounds;
  });

  function toggleCloudTunnel() {
    const nextState = !cfg?.keeneticCloudTunnel;
    let nextOutbound = cfg?.keeneticCloudOutbound;
    if (nextState && (!nextOutbound || nextOutbound === 'DIRECT')) {
      const firstTarget = availableCloudOutbounds[0]?.value || 'DIRECT';
      nextOutbound = firstTarget;
    }
    void applyPatch({
      keeneticCloudTunnel: nextState,
      keeneticCloudOutbound: nextOutbound,
    });
  }

  function toggleSusanin() {
    const nextState = !cfg?.susaninEnabled;
    let nextOutbound = cfg?.susaninOutbound;
    if (nextState && (!nextOutbound || nextOutbound === 'DIRECT')) {
      const firstTarget = availableCloudOutbounds[0]?.value || 'DIRECT';
      nextOutbound = firstTarget;
    }
    void applyPatch({
      susaninEnabled: nextState,
      susaninOutbound: nextOutbound,
    });
  }

  let susaninModalOpen = $state(false);
  let susaninModalTab = $state<'learned' | 'always'>('learned');
  let susaninIPList = $state<string[]>([]);
  let susaninKnowledge = $state<Record<string, { title: string; org?: string; country?: string; cc?: string }>>({});
  let susaninLoading = $state(false);
  let susaninSearch = $state('');
  let susaninIPCount = $state(0);

  // Susanin Always (Whitelist) state
  let susaninAlwaysList = $state<string[]>([]);
  let susaninAlwaysInput = $state('');
  let susaninAlwaysTextMode = $state(false);
  let susaninAlwaysText = $state('');
  let susaninSavingAlways = $state(false);
  let susaninSettings = $state<AdaptiveRoutingSettings | null>(null);

  const filteredSusaninIPs = $derived(
    susaninSearch.trim()
      ? susaninIPList.filter((ip) => {
          const q = susaninSearch.trim().toLowerCase();
          const k = susaninKnowledge[ip];
          const localK = lookupIpKnowledge(ip);
          return (
            ip.toLowerCase().includes(q) ||
            (k?.title && k.title.toLowerCase().includes(q)) ||
            (localK?.title && localK.title.toLowerCase().includes(q))
          );
        })
      : susaninIPList
  );

  const filteredSusaninAlways = $derived(
    susaninSearch.trim()
      ? susaninAlwaysList.filter((entry) => {
          const q = susaninSearch.trim().toLowerCase();
          const localK = lookupIpKnowledge(entry);
          return (
            entry.toLowerCase().includes(q) ||
            (localK?.title && localK.title.toLowerCase().includes(q))
          );
        })
      : susaninAlwaysList
  );

  type SusaninApi = {
    getAdaptiveRoutingLearned?: () => Promise<{
      okTcp?: string[];
      okUdp?: string[];
      okNet?: string[];
      knowledge?: Record<string, { title: string; org?: string; country?: string; cc?: string }>;
    }>;
    getAdaptiveRoutingSettings?: () => Promise<AdaptiveRoutingSettings>;
    clearAdaptiveRoutingCache?: () => Promise<void>;
    applyAdaptiveRouting?: (settings: AdaptiveRoutingSettings) => Promise<void>;
  };

  async function loadSusaninData() {
    try {
      const sApi = api as unknown as SusaninApi;
      if (!sApi.getAdaptiveRoutingLearned) return;
      const [learnedRes, settingsRes] = await Promise.all([
        sApi.getAdaptiveRoutingLearned(),
        sApi.getAdaptiveRoutingSettings ? sApi.getAdaptiveRoutingSettings().catch(() => null) : Promise.resolve(null)
      ]);
      const set = new Set<string>();
      for (const ip of learnedRes?.okTcp || []) set.add(ip);
      for (const ip of learnedRes?.okUdp || []) set.add(ip);
      for (const ip of learnedRes?.okNet || []) set.add(ip);
      susaninIPList = Array.from(set).sort();
      susaninIPCount = susaninIPList.length;

      if (learnedRes?.knowledge) {
        susaninKnowledge = learnedRes.knowledge;
      }

      if (settingsRes) {
        susaninSettings = settingsRes;
        susaninAlwaysList = (settingsRes.alwaysEntries || []).map((e: string) => e.trim()).filter(Boolean);
        susaninAlwaysText = susaninAlwaysList.join('\n');
      }
    } catch {
      susaninIPList = [];
      susaninIPCount = 0;
    }
  }

  function openSusaninModal() {
    susaninModalOpen = true;
    susaninSearch = '';
    void loadSusaninData();
  }

  async function handleClearSusanin() {
    try {
      susaninLoading = true;
      const sApi = api as unknown as SusaninApi;
      if (sApi.clearAdaptiveRoutingCache) {
        await sApi.clearAdaptiveRoutingCache();
      }
      notifications.success(m.sb_router_susanin_cleared());
      await loadSusaninData();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.sb_router_susanin_clear_failed());
    } finally {
      susaninLoading = false;
    }
  }

  async function saveSusaninAlways(newList?: string[]) {
    try {
      susaninSavingAlways = true;
      const entriesToSave = (newList ?? (susaninAlwaysTextMode
        ? susaninAlwaysText.split('\n').map((s) => s.trim()).filter(Boolean)
        : susaninAlwaysList
      ));

      const sApi = api as unknown as SusaninApi;
      let currentSettings = susaninSettings;
      if (!currentSettings && sApi.getAdaptiveRoutingSettings) {
        currentSettings = await sApi.getAdaptiveRoutingSettings();
      }

      const updated: AdaptiveRoutingSettings = {
        ...currentSettings,
        alwaysEntries: entriesToSave,
      };

      if (sApi.applyAdaptiveRouting) {
        await sApi.applyAdaptiveRouting(updated);
      }
      susaninSettings = updated;
      susaninAlwaysList = entriesToSave;
      susaninAlwaysText = entriesToSave.join('\n');
      notifications.success(m.sb_router_susanin_rules_saved());
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.sb_router_susanin_clear_failed());
    } finally {
      susaninSavingAlways = false;
    }
  }

  function addAlwaysEntry() {
    let val = susaninAlwaysInput.trim();
    if (!val) return;
    val = val.replace(/^https?:\/\//i, '').split('/')[0].trim().toLowerCase();
    if (!val) return;

    if (susaninAlwaysList.some((e) => e.toLowerCase() === val)) {
      notifications.info(m.sb_router_susanin_rule_added({ val }));
      susaninAlwaysInput = '';
      return;
    }

    const nextList = [...susaninAlwaysList, val];
    susaninAlwaysList = nextList;
    susaninAlwaysText = nextList.join('\n');
    susaninAlwaysInput = '';
    void saveSusaninAlways(nextList);
  }

  function removeAlwaysEntry(entry: string) {
    const nextList = susaninAlwaysList.filter((e) => e !== entry);
    susaninAlwaysList = nextList;
    susaninAlwaysText = nextList.join('\n');
    void saveSusaninAlways(nextList);
  }

  function pinToWhitelist(ipOrCidr: string) {
    if (susaninAlwaysList.includes(ipOrCidr)) {
      notifications.info(m.sb_router_susanin_rule_added({ val: ipOrCidr }));
      return;
    }
    const nextList = [...susaninAlwaysList, ipOrCidr];
    susaninAlwaysList = nextList;
    susaninAlwaysText = nextList.join('\n');
    void saveSusaninAlways(nextList);
    notifications.success(m.sb_router_susanin_promoted({ target: ipOrCidr }));
  }

  $effect(() => {
    if (open && cfg?.susaninEnabled) {
      void loadSusaninData();
    }
  });

  async function loadMihomoClashMode() {
    if (cfg?.routingEngine !== 'mihomo') return;
    try {
      clashModeLoading = true;
      const [configs, globalProxy, groupsRes, subsRes, proxiesRes] = await Promise.all([
        api.mihomoGetClashConfigs().catch(() => null),
        api.mihomoGetGlobalProxy().catch(() => null),
        api.mihomoNativeGroups().catch(() => []),
        api.mihomoNativeSubscriptions().catch(() => []),
        api.mihomoNativeProxies().catch(() => []),
      ]);
      if (configs?.mode) {
        currentMihomoMode = configs.mode;
      } else if (cfg?.mihomoTrafficMode) {
        currentMihomoMode = cfg.mihomoTrafficMode;
      }
      if (globalProxy?.now) {
        currentGlobalTarget = globalProxy.now;
      } else if (cfg?.mihomoGlobalTarget) {
        currentGlobalTarget = cfg.mihomoGlobalTarget;
      }
      if (globalProxy?.all) {
        clashGlobalAll = globalProxy.all;
      }
      mihomoNativeGroupsList = groupsRes || [];
      mihomoNativeSubsList = subsRes || [];
      mihomoNativeProxiesList = proxiesRes || [];
    } catch (e) {
      console.error('Failed to load Mihomo clash mode:', e);
    } finally {
      clashModeLoading = false;
    }
  }

  $effect(() => {
    if (open && cfg?.routingEngine === 'mihomo') {
      void loadMihomoClashMode();
    }
  });

  async function handleMihomoModeChange(newMode: 'rule' | 'global' | 'direct') {
    currentMihomoMode = newMode;
    try {
      await api.mihomoPatchClashConfigs({ mode: newMode });
      await applyPatch({ mihomoTrafficMode: newMode });
      const labels = {
        rule: m.sb_router_status_mihomo_mode_rule(),
        global: m.sb_router_status_mihomo_mode_global(),
        direct: m.sb_router_status_mihomo_mode_direct(),
      };
      notifications.success(m.sb_router_mihomo_mode_switched({ mode: labels[newMode] }));
    } catch (e) {
      notifications.error(m.sb_router_mihomo_mode_switch_failed({ error: e instanceof Error ? e.message : String(e) }));
    }
  }

  async function handleGlobalTargetChange(newTarget: string) {
    currentGlobalTarget = newTarget;
    try {
      await api.mihomoSetGlobalProxy(newTarget);
      await applyPatch({ mihomoGlobalTarget: newTarget });
      notifications.success(m.sb_router_mihomo_target_switched({ target: newTarget }));
    } catch (e) {
      notifications.error(m.sb_router_mihomo_target_switch_failed({ error: e instanceof Error ? e.message : String(e) }));
    }
  }

  let singboxInstallStatus = $derived($singboxStatus.data);
  let sysInfo = $derived($systemInfo.data);

  let deps = $derived(deriveDeps(s));
  let engineEnabled = $derived(s?.enabled ?? false);
  // Реальная работа перехвата (цепочки + PREROUTING-jump'ы), не просто
  // persisted-тумблер. Заголовок различает «включён, но не работает».
  let engineActive = $derived(engineEnabled && (s?.active ?? false));

  // Тумблер/кнопка управляют режимом через общий modeSwitch (детерминированно
  // <выбранный режим>↔off), а не enable/disable «текущего» режима. checked —
  // mode-aware: «вкл» только когда активен один из режимов этого дровера
  // (а не голый enabled) — FakeIP живёт на своей вкладке.
  const settings = singboxRouterStore.settings;
  const switchBusy = $derived(modeSwitchBusy($modeSwitch));

  // Режимы захвата, которыми управляет этот дровер.
  type CaptureMode = 'tproxy' | 'policy-tun';
  let activeMode = $derived.by<CaptureMode | null>(() => {
    if (!(s?.enabled ?? false)) return null;
    const rm = $settings?.routingMode;
    if (rm === undefined || rm === 'tproxy') return 'tproxy';
    return rm === 'policy-tun' ? rm : null;
  });
  // This is the main engine switch, so it must reflect the same global
  // enabled state as the status title and the core-switch guard.  Previously
  // it was tied only to TPROXY/policy-tun, which rendered it OFF while FakeIP
  // (or a legacy payload) was actually running.
  let engineOn = $derived(engineEnabled);
  // Выбор пользователя в этой сессии — что включит тумблер, пока движок
  // выключен. Пусто → persisted routingMode (легаси/пустой = tproxy).
  let pickedMode = $state<CaptureMode | null>(null);
  let targetMode = $derived<CaptureMode>(
    activeMode ?? pickedMode ?? ($settings?.routingMode === 'policy-tun' ? 'policy-tun' : 'tproxy'),
  );
  // While FakeIP is active neither of this drawer's capture cards is active.
  // When the engine is off, show the mode that the next enable will start.
  let displayedMode = $derived<CaptureMode | null>(activeMode ?? (engineOn ? null : targetMode));
  let policyTunMode = $derived(targetMode === 'policy-tun');

  // policy-tun-unbound показывает карточка режима (там же ссылка на политики) —
  // в общем списке замечаний он был бы вторым экземпляром той же строки.
  // При активном Mihomo не показываем нерелевантные замечания sing-box конфигурации.
  let issues = $derived(
    cfg?.routingEngine === 'mihomo'
      ? (s?.issues ?? [])
          .filter((i) => i.kind !== 'policy-tun-unbound' && !i.message?.includes('rule_set'))
          .map((i) => ({
            tone: i.severity === 'error' ? ('error' as const) : ('warning' as const),
            text: i.message,
            ctaHint: m.sb_router_cta_apply(),
          }))
      : deriveIssues(
          policyTunMode && s
            ? { ...s, issues: (s.issues ?? []).filter((i) => i.kind !== 'policy-tun-unbound') }
            : s,
        ),
  );
  let issueCount = $derived(issues.length);

  let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
  let saving = $state(false);
  let restarting = $state(false);
  let lastError = $state<string | null>(null);
  let wanAutoOverride = $state<WanAutoOverride>(null);
  let wanAuto = $derived(resolveWanAuto(wanAutoOverride, cfg?.wanAutoDetect));
  function versionLabel(value?: string | null): string {
    const v = (value ?? '').trim();
    return v ? `v${v}` : '—';
  }
  let mihomoStatusData = $state<import('$lib/types').MihomoStatus | null>(null);
  onMount(() => {
    api.mihomoStatus().then((res) => {
      mihomoStatusData = res;
    }).catch(() => {});
  });
  let mihomoVersionLabel = $derived(versionLabel(
    mihomoStatusData?.version ?? '1.19.16'
  ));
  let sbVersionLabel = $derived(versionLabel(
    singboxInstallStatus?.version ?? singboxInstallStatus?.currentVersion ?? sysInfo?.singbox?.version,
  ));

  async function selectEngine(engine: 'sing-box' | 'mihomo'): Promise<void> {
    const current = cfg?.routingEngine === 'mihomo' ? 'mihomo' : 'sing-box';
    if (current === engine) return;
    if (engineEnabled || switchBusy) {
      notifications.error(m.sb_router_status_engine_switch_stop_first());
      return;
    }
    saving = true;
    try {
      await mergeAndSaveSettings({ routingEngine: engine });
      notifications.success(m.sb_router_status_engine_switched({ engine: engine === 'mihomo' ? 'Mihomo' : 'Sing-box' }));
    } catch (e) {
      notifications.error(m.sb_router_status_engine_switch_failed({ error: String(e) }));
    } finally {
      saving = false;
    }
  }

  let bigTitle = $derived.by(() => {
    if (!engineEnabled) return m.sb_router_status_engine_off();
    return engineActive ? m.sb_router_status_engine_running() : m.sb_router_status_engine_not_running();
  });
  let bigSubtitle = $derived.by(() => {
    if (!engineEnabled) return m.sb_router_status_sub_inactive();
    if (!engineActive) return m.sb_router_status_sub_no_intercept();
    const n = s?.ruleCount ?? 0;
    return m.sb_router_status_sub_traffic_via({ count: n });
  });

  let engineState = $derived.by<'off' | 'warn' | 'on'>(() => {
    if (!engineEnabled) return 'off';
    if (!engineActive) return 'warn';
    return 'on';
  });

  let engineDotVariant = $derived(
    engineState === 'on' ? 'success' as const :
    engineState === 'warn' ? 'warning' as const :
    'muted' as const,
  );

  // ── Падения движка (#456): счётчик за окно backoff'а, причина последнего
  // падения и пауза авто-перезапуска. Блок виден, пока падения не выйдут из
  // 10-минутного окна; escape hatch — кнопка «Перезапустить» в футере.
  let crashCount = $derived(s?.crashCount ?? 0);
  let crashSuppressedLabel = $derived(formatSuppressedUntil(s?.restartSuppressedUntil));
  let showCrashInfo = $derived(crashCount > 0 || crashSuppressedLabel !== null);

  // ── Ресурсы: живая память (SSE singbox:memory, Go-рантайм по Clash API) и
  // агрегатный трафик (кумулятивные totals Clash, singbox:traffic-totals).
  // Секция видна только при работающем режиме этого дровера (tproxy/policy-tun):
  // в режиме FakeIP или после остановки движка SSE замолкает и сторы держат
  // протухшие числа (окно до ближайшего тика watchdog'а ~30 с — принятая задержка).
  let resourcesVisible = $derived(engineActive && activeMode !== null);
  let liveStats = $derived($singboxTrafficLive);
  let memoryLabel = $derived($singboxMemory > 0 ? formatBytes($singboxMemory) : '—');
  let rateLabel = $derived(
    liveStats.rate.hasRate
      ? `↓ ${formatByteRate(liveStats.rate.downloadRate)} · ↑ ${formatByteRate(liveStats.rate.uploadRate)}`
      : '—',
  );
  let sessionLabel = $derived(
    `↓ ${formatBytes(liveStats.totals.downloadBytes)} · ↑ ${formatBytes(liveStats.totals.uploadBytes)}`,
  );

  onMount(async () => {
    void singboxRouterStore.loadAll();
    try {
      wanInterfaces = await api.singboxRouterListWANInterfaces();
    } catch (_e) {
      // ignore
    }
  });

  // Новичку TPROXY-настройки живут в SourceDrawer (узел «Источник» во FlowGraph);
  // здесь — сводка и переход, чтобы под выбором режима не было пусто (#730).
  let sourceSummary = $derived.by(() => {
    if (cfg?.deviceMode === 'all') return m.sb_router_status_source_all();
    const name = (cfg?.policyName ?? '').trim();
    return name
      ? m.sb_router_status_source_policy({ name })
      : m.sb_router_status_source_none();
  });
  function goToSourceSettings() {
    closeDrawer();
    openSourceDrawer();
  }

  // ── Engine control ──
  function toggleEngine(turnOn: boolean) {
    modeSwitch.request(turnOn ? targetMode : 'off');
  }
  function handleToggleClick(_e: MouseEvent) {
    toggleEngine(!engineOn);
  }
  // Выбор режима: при выключенном движке только запоминаем цель тумблера,
  // при включённом — сразу просим переключение (общий confirm + прогресс).
  function selectMode(next: CaptureMode) {
    if (switchBusy || next === targetMode) return;
    if (next === 'policy-tun' && !tunSupported) return;
    pickedMode = next;
    if (activeMode !== null) modeSwitch.request(next);
  }
  async function restartEngine(_e: MouseEvent) {
    if (restarting) return;
    restarting = true;
    try {
      await api.singboxControl('restart');
      await singboxRouterStore.reloadStatus();
      notifications.success(m.sb_router_status_restarted());
    } catch (e) {
      notifications.error(m.sb_router_status_restart_failed({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      restarting = false;
    }
  }

  let resetConfirmOpen = $state(false);
  let resetting = $state(false);

  async function handleResetMihomo() {
    if (resetting) return;
    resetting = true;
    try {
      await api.mihomoResetConfig();
      await singboxRouterStore.loadAll();
      notifications.success(m.sb_router_status_mihomo_reset_done());
      resetConfirmOpen = false;
      closeDrawer();
      if (typeof window !== 'undefined') {
        window.location.reload();
      }
    } catch (e) {
      notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      resetting = false;
    }
  }

  // ── Settings (expert, auto-save) ──
  async function applyPatch(patch: Partial<SingboxRouterSettings>) {
    if (!cfg) return;
    saving = true;
    lastError = null;
    try {
      await mergeAndSaveSettings(patch);
    } catch (e) {
      lastError = e instanceof Error ? e.message : String(e);
      notifications.error(m.sb_router_common_save_failed({ message: lastError }));
    } finally {
      saving = false;
    }
  }
  function toggleAutoDetect(checked: boolean) {
    const { override, patch } = planToggleAutoDetect(checked);
    wanAutoOverride = override;
    if (patch) void applyPatch(patch);
  }
  function onCacheLocationChange(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    if (v === 'flash' || v === 'tmp') void applyPatch({ cacheFileLocation: v });
  }

  function onWanInterfaceChange(e: Event) {
    const action = planSelectWanInterface((e.currentTarget as HTMLSelectElement).value);
    if (!action) return;
    wanAutoOverride = action.override;
    if (action.patch) void applyPatch(action.patch);
  }
  function toggleSniffer(checked: boolean) { void applyPatch({ snifferEnabled: checked }); }
  function togglePreset(id: string) {
    const current = cfg?.bypassPresets ?? [];
    const next = current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
    void applyPatch({ bypassPresets: next });
  }

  const UDP_TIMEOUT_OPTIONS = $derived([
    { value: '', label: m.sb_router_status_udp_timeout_default() },
    { value: '5m0s', label: m.sb_router_status_udp_timeout_5m() },
    { value: '10m0s', label: m.sb_router_status_udp_timeout_10m() },
    { value: '15m0s', label: m.sb_router_status_udp_timeout_15m() },
    { value: '30m0s', label: m.sb_router_status_udp_timeout_30m() },
    { value: '1h0m0s', label: m.sb_router_status_udp_timeout_1h() },
    { value: '3h0m0s', label: m.sb_router_status_udp_timeout_3h() },
  ]);

  const UDP_NAT_MAX_OPTIONS = $derived([
    { value: '', label: m.sb_router_status_udp_nat_auto() },
    { value: '2048', label: '2048' },
    { value: '4096', label: '4096' },
    { value: '8192', label: '8192' },
    { value: '16384', label: '16384' },
  ]);
</script>

<SideDrawer {open} onClose={closeDrawer} title={cfg?.routingEngine === 'mihomo' ? m.sb_router_status_title_mihomo() : m.sb_router_status_title()} width={420}>
  <div class="sections">
    <!-- Состояние -->
    <section class="sec">
      <div class="sec-cap">{m.sb_router_status_sec_state()}</div>
      <div class="engine-status" class:state-off={engineState === 'off'} class:state-warn={engineState === 'warn'} class:state-on={engineState === 'on'}>
        <div class="engine-main">
          <Toggle checked={engineOn} controlled loading={switchBusy} ariaLabel={m.sb_router_status_toggle_aria()} onchange={toggleEngine} />
          <div class="engine-text">
            <div class="engine-head">
              <StatusDot variant={engineDotVariant} size="sm" />
              <div class="engine-title">{bigTitle}</div>
            </div>
            <div class="engine-sub">{bigSubtitle}</div>
          </div>
        </div>
        <div class="engine-meta">
          <span>{cfg?.routingEngine === 'mihomo' ? m.sb_router_status_version_mihomo() : m.sb_router_status_version()}</span>
          <span class="engine-version">{cfg?.routingEngine === 'mihomo' ? mihomoVersionLabel : sbVersionLabel}</span>
        </div>
      </div>

      <div class="sec-cap mt-4">{m.sb_router_status_sec_routing_core()}</div>
      <div class="card-grid">
        <SegmentedControl
          ariaLabel={m.sb_router_status_sec_routing_core()}
          options={[
            { label: 'Sing-box', value: 'sing-box' },
            { label: 'Mihomo', value: 'mihomo' }
          ]}
          value={cfg?.routingEngine === 'mihomo' ? 'mihomo' : 'sing-box'}
          onchange={(val) => selectEngine(val as 'sing-box' | 'mihomo')}
        />
      </div>
      <p class="hint mt-1">{m.sb_router_status_engine_switch_hint()}</p>

      <div class="sec-cap mt-4">{m.sb_router_status_sec_capture()}</div>
      <div class="card-grid">
        <OutboundOption
          label={m.sb_router_status_mode_tproxy()}
          sub={m.sb_router_status_mode_tproxy_sub()}
          tone="accent"
          selected={displayedMode === 'tproxy'}
          onclick={() => selectMode('tproxy')}
        />
        <OutboundOption
          label={m.sb_router_status_mode_policy_tun()}
          sub={m.sb_router_status_mode_policy_tun_sub()}
          tone="accent"
          selected={targetMode === 'policy-tun'}
          disabled={!tunSupported}
          title={tunSupported ? undefined : opkgTunUnsupportedReason()}
          onclick={() => selectMode('policy-tun')}
        />
      </div>
      {#if cfg?.routingEngine !== 'mihomo'}
        <p class="hint">{m.sb_router_status_fakeip_hint()}</p>
        {#if !tunSupported}
          <p class="hint">{opkgTunUnsupportedReason()}</p>
        {/if}
      {:else}
        <div class="sec-cap mt-4">{m.sb_router_status_mihomo_traffic_mode()}</div>
        <div class="mode-segmented">
          <SegmentedControl
            ariaLabel={m.sb_router_status_mihomo_traffic_mode()}
            options={[
              { label: m.sb_router_status_mihomo_mode_rule(), value: 'rule' },
              { label: m.sb_router_status_mihomo_mode_global(), value: 'global' },
              { label: m.sb_router_status_mihomo_mode_direct(), value: 'direct' }
            ]}
            value={currentMihomoMode}
            onchange={(val) => handleMihomoModeChange(val as 'rule' | 'global' | 'direct')}
          />
        </div>

        {#if currentMihomoMode === 'rule'}
          <p class="hint">
            <strong>{m.sb_router_status_mihomo_mode_rule()}:</strong> {m.sb_router_status_mihomo_mode_rule_hint()}
          </p>
        {:else if currentMihomoMode === 'global'}
          <div class="global-target-card">
            <p class="hint hint-warning">
              <strong>{m.sb_router_status_mihomo_mode_global()}:</strong> {m.sb_router_status_mihomo_mode_global_hint()}
            </p>
            <div class="field mt-2">
              <label class="lbl" for="ed-global-target">{m.sb_router_status_mihomo_global_target()}</label>
              <select
                id="ed-global-target"
                class="inp"
                value={currentGlobalTarget}
                onchange={(e) => handleGlobalTargetChange(e.currentTarget.value)}
              >
                {#each availableGlobalOutbounds as ob}
                  <option value={ob.value}>{ob.label}</option>
                {/each}
              </select>
            </div>
          </div>
        {:else if currentMihomoMode === 'direct'}
          <p class="hint hint-warning">
            <strong>{m.sb_router_status_mihomo_mode_direct()}:</strong> {m.sb_router_status_mihomo_mode_direct_hint()}
          </p>
        {/if}
      {/if}

      {#if showCrashInfo}
        <div class="crash-info">
          <!-- FIX-D: при crashCount 0 (например, серия неудачных стартов до
               grace-периода без записанных падений) строка счётчика скрыта —
               «Падений: 0» рядом с активным подавлением только путает. -->
          {#if crashCount > 0}
            <div class="crash-line">
              <span class="crash-label">{m.sb_router_status_crash_label()}</span>
              <span class="crash-value">{crashCount}</span>
            </div>
          {/if}
          {#if s?.lastCrashReason}
            <p class="crash-reason">{m.sb_router_status_crash_reason({ reason: s.lastCrashReason })}</p>
          {/if}
          {#if crashSuppressedLabel}
            <p class="crash-suppressed">
              {#if crashCount > 0}
                {m.sb_router_status_crash_suppressed_count({ time: crashSuppressedLabel, count: crashCount })}
              {:else}
                {m.sb_router_status_crash_suppressed({ time: crashSuppressedLabel })}
              {/if}
            </p>
          {/if}
        </div>
      {/if}
    </section>

    <!-- Ресурсы: живая память и трафик движка -->
    {#if resourcesVisible}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_resources()}</div>
        <div class="stat-line" title={m.sb_router_status_memory_title()}>
          <span class="stat-label">{m.sb_router_status_memory()}</span>
          <span class="stat-value">{memoryLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">{m.sb_router_status_speed()}</span>
          <span class="stat-value">{rateLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">{m.sb_router_status_session()}</span>
          <span class="stat-value">{sessionLabel}</span>
        </div>
      </section>
    {/if}

    <!-- Зависимости -->
    <section class="sec">
      <div class="sec-cap">{m.sb_router_status_sec_deps()}</div>
      {#each deps as dep}
        <DepRow tone={dep.tone} label={dep.label} hint={dep.hint} />
      {/each}
    </section>

    <!-- Замечания -->
    {#if issueCount > 0}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_issues()} <Badge variant="warning" size="sm">{issueCount}</Badge></div>
        {#each issues as issue}
          <IssueRow tone={issue.tone} text={issue.text} ctaHint={issue.ctaHint} />
        {/each}
      </section>
    {/if}

    <!-- Карточка режима «Политики + tun»: статус интерфейса + source-preserve.
         Видна и новичку — это состояние режима, а не эксперт-настройка. -->
    {#if policyTunMode && cfg}
      <PolicyTunCard {cfg} status={s} onPatch={(patch) => applyPatch(patch)} />
    {/if}

    <!-- TPROXY у новичка: сводка источника + переход в SourceDrawer. Иначе под
         выбором режима пусто, тогда как policy-tun показывает свою карточку. -->
    {#if !policyTunMode && !isExpert && cfg}
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_source()}</div>
        <p class="hint">{sourceSummary}</p>
        <Button variant="ghost" size="sm" onclick={goToSourceSettings}>{m.sb_router_status_configure_source()}</Button>
      </section>
    {/if}

    {#if cfg}
      <!-- WAN-интерфейс -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_wan()}</div>
        <div class="field-row">
          <span>{m.sb_router_status_wan_auto()}</span>
          <Toggle checked={wanAuto} onchange={(checked) => toggleAutoDetect(checked)} />
        </div>
        {#if !wanAuto}
          <div class="field">
            <label class="lbl" for="ed-wan">{m.sb_router_status_wan_interface()}</label>
            <select id="ed-wan" class="inp" value={cfg.wanInterface ?? ''} onchange={onWanInterfaceChange}>
              <option value="">{m.sb_router_status_wan_choose()}</option>
              {#each wanInterfaces as iface (iface.name)}
                <option value={iface.name}>{iface.name}{iface.label ? ` — ${iface.label}` : ''}</option>
              {/each}
            </select>
          </div>
        {/if}
        <p class="hint">{m.sb_router_status_wan_hint()}</p>
      </section>

      <!-- Кэш sing-box (issue #842): единственное место настройки, вне expert-гейта —
           износ флеша касается любого режима с fakeip. -->
      {#if cfg?.routingEngine !== 'mihomo'}
        <section class="sec">
          <div class="sec-cap">{m.sb_router_status_sec_cache()}</div>
          <div class="field">
            <label class="lbl" for="ed-cache-location">{m.sb_router_status_cache_location()}</label>
            <select
              id="ed-cache-location"
              class="inp"
              value={cfg.cacheFileLocation ?? ''}
              onchange={onCacheLocationChange}
            >
              {#if !cfg.cacheFileLocation}
                <option value="">{m.sb_router_status_cache_unset()}</option>
              {/if}
              <option value="flash">{m.sb_router_status_cache_flash()}</option>
              <option value="tmp">{m.sb_router_status_cache_tmp()}</option>
            </select>
          </div>
          <p class="hint">{s?.cacheDbPath ? m.sb_router_status_cache_hint_now({ path: s.cacheDbPath }) : m.sb_router_status_cache_hint()}</p>
        </section>
      {/if}
    {/if}

    {#if isExpert && cfg}
      <!-- Источник трафика (deviceMode/policy) — только TPROXY: в policy-tun
           захват задаётся привязкой интерфейса к политике доступа NDMS. -->
      {#if !policyTunMode}
        <TrafficSourceSettings
          {cfg}
          deviceCount={s?.deviceCount ?? 0}
          policyExists={s?.policyExists !== false}
          variant="expert"
          onPatch={(patch) => void applyPatch(patch)}
        />
      {/if}

      <!-- Анализ трафика -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_sniff()}</div>
        <div class="field-row">
          <span>{m.sb_router_status_sniff_enable()}</span>
          <Toggle checked={cfg.snifferEnabled} onchange={(checked) => toggleSniffer(checked)} />
        </div>
        <p class="hint">{m.sb_router_status_sniff_hint()}</p>
        <div class="field">
          <label class="lbl" for="ed-udp-timeout">{m.sb_router_status_udp_timeout()}</label>
          <div class="udp-timeout-row">
            <select
              id="ed-udp-timeout"
              class="inp"
              value={cfg.udpTimeout ?? ''}
              onchange={(e) => void applyPatch({ udpTimeout: (e.currentTarget as HTMLSelectElement).value || undefined })}
            >
              {#each UDP_TIMEOUT_OPTIONS as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
        </div>
        <p class="hint">{m.sb_router_status_udp_timeout_hint()}</p>
        <div class="field">
          <label class="lbl" for="ed-udp-nat-max">{m.sb_router_status_udp_nat_max()}</label>
          <div class="udp-timeout-row">
            <select
              id="ed-udp-nat-max"
              class="inp"
              value={cfg.udpNatMax ? String(cfg.udpNatMax) : ''}
              onchange={(e) => {
                const v = (e.currentTarget as HTMLSelectElement).value;
                void applyPatch({ udpNatMax: v ? Number(v) : undefined });
              }}
            >
              {#each UDP_NAT_MAX_OPTIONS as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
        </div>
        <p class="hint">{m.sb_router_status_udp_nat_max_hint()}</p>
      </section>

      {#if cfg?.routingEngine !== 'mihomo'}
        <!-- QoS-маршрутизация (DSCP): onPatch возвращает Promise — карточка
             сериализует свои PUT-ы и ресинкается со стором после дренажа очереди. -->
        <QosSettingsCard
          {cfg}
          status={s}
          outboundOptions={$storeOptions}
          onPatch={(patch) => applyPatch(patch)}
        />
      {/if}

      <!-- Службы Keenetic / KeenDNS в туннель -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_cloud_sec_title()}</div>
        <div class="feature-chips">
          <button type="button" class="feature-chip" class:active={!!cfg?.keeneticCloudTunnel} onclick={toggleCloudTunnel}>
            <div class="feature-chip-head">
              <span class="feature-chip-label">{m.sb_router_cloud_chip_label()}</span>
              <span class="feature-chip-status-badge" class:active={!!cfg?.keeneticCloudTunnel}>
                {cfg?.keeneticCloudTunnel ? m.sb_router_cloud_chip_active() : m.sb_router_cloud_chip_direct()}
              </span>
            </div>
            <span class="feature-chip-desc">
              {m.sb_router_cloud_desc()}
            </span>
          </button>
        </div>

        {#if cfg?.keeneticCloudTunnel}
          <div class="field" style="margin-top: 10px;">
            <label class="lbl" for="cloud-outbound-sel">{m.sb_router_cloud_outbound_lbl()}</label>
            <select
              id="cloud-outbound-sel"
              class="sel"
              value={cfg?.keeneticCloudOutbound || (availableCloudOutbounds[0]?.value ?? '')}
              onchange={(e) => void applyPatch({ keeneticCloudOutbound: (e.currentTarget as HTMLSelectElement).value })}
            >
              {#each availableCloudOutbounds as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>
        {/if}

        <p class="hint">
          {@html m.sb_router_cloud_hint()}
        </p>
      </section>

      <!-- Адаптивное обнаружение блокировок (радар Susanin) -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_susanin_sec_title()}</div>
        <div class="feature-chips">
          <button type="button" class="feature-chip" class:active={!!cfg?.susaninEnabled} onclick={toggleSusanin}>
            <div class="feature-chip-head">
              <span class="feature-chip-label">{m.sb_router_susanin_chip_label()}</span>
              <span class="feature-chip-status-badge" class:active={!!cfg?.susaninEnabled}>
                {cfg?.susaninEnabled ? m.sb_router_susanin_chip_on() : m.sb_router_susanin_chip_off()}
              </span>
            </div>
            <span class="feature-chip-desc">
              {m.sb_router_susanin_chip_desc()}
            </span>
          </button>
        </div>

        {#if cfg?.susaninEnabled}
          <div class="field" style="margin-top: 10px;">
            <label class="lbl" for="susanin-outbound-sel">{m.sb_router_susanin_outbound_lbl()}</label>
            <select
              id="susanin-outbound-sel"
              class="sel"
              value={cfg?.susaninOutbound || (availableCloudOutbounds[0]?.value ?? '')}
              onchange={(e) => void applyPatch({ susaninOutbound: (e.currentTarget as HTMLSelectElement).value })}
            >
              {#each availableCloudOutbounds as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </div>

          <div style="display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 8px;">
            <span style="font-size: 13px; color: var(--text-secondary);">
              {m.sb_router_susanin_active_count()} <strong style="color: var(--text-primary);">{susaninIPCount}</strong>
            </span>
            <Button variant="secondary" size="sm" onclick={openSusaninModal}>
              {m.sb_router_susanin_active_ips_btn({ count: susaninIPCount })}
            </Button>
          </div>
        {/if}

        <p class="hint">
          {@html m.sb_router_susanin_drawer_hint()}
        </p>
      </section>

      <!-- Исключения: порт-пресеты + IP-пресеты (keendns) + ручные порты/подсети -->
      <section class="sec">
        <div class="sec-cap">{m.sb_router_status_sec_bypass()}</div>
        <div class="bypass-presets">
          {#each BYPASS_PRESETS as p (p.id)}
            {@const active = (cfg.bypassPresets ?? []).includes(p.id)}
            <button type="button" class="bypass-preset" class:active onclick={() => togglePreset(p.id)}>
              <span class="preset-label">{p.label}</span>
              <span class="preset-desc">
                {p.id === 'keendns' ? m.sb_router_keendns_warn({ engine: cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box' }) : p.desc}
              </span>
            </button>
          {/each}
        </div>
        <div class="field">
          <label class="lbl" for="ed-ports-input">{m.sb_router_status_extra_ports()}</label>
          <PortChipsInput inputId="ed-ports-input" value={cfg.bypassExtraPorts ?? ''} onChange={(v) => void applyPatch({ bypassExtraPorts: v })} />
        </div>
        <p class="hint">{m.sb_router_status_ports_hint_pre()}<code class="mono">443 TCP</code>{m.sb_router_status_ports_hint_mid()}<code class="mono">5000-5500 UDP</code>{m.sb_router_status_ports_hint_post()}</p>
        <!-- В «Политики + tun» перехвата netfilter нет вовсе, поэтому исключения
             работают иначе, чем в TPROXY: они влияют только на классы QoS и на
             перехват DNS. Про 53 сказано отдельно — там выключатель СОЗНАТЕЛЬНО
             грубее, чем в TPROXY (пер-протокольный там, общий здесь), потому что
             сам перехват 53-го неделим: на усечённый ответ клиент переспрашивает
             по TCP, и половинчатый перехват дал бы резолвинг, зависящий от
             размера ответа. -->
        {#if policyTunMode}
          <p class="hint">{m.sb_router_status_policy_tun_bypass_pre()} <code class="mono">53</code> {m.sb_router_status_policy_tun_bypass_post()}</p>
        {/if}
        <div class="field">
          <label class="lbl" for="ed-subnets-input">{m.sb_router_status_extra_subnets()}</label>
          <SubnetChipsInput inputId="ed-subnets-input" value={cfg.bypassExtraSubnets ?? ''} onChange={(v) => void applyPatch({ bypassExtraSubnets: v })} />
        </div>
        <p class="hint">{m.sb_router_status_subnets_hint()}</p>
        <!-- Набор AWGM-BYPASS живёт только в TPROXY-перехвате: в policy-tun
             (DSCPOnly) правило обхода не эмитится — обходить нечего. -->
        {#if !policyTunMode}
          <BypassGeoIPTags {cfg} onPatch={(patch) => applyPatch(patch)} />
        {/if}
      </section>

      {#if cfg?.routingEngine === 'mihomo'}
        <!-- Локальные порты прокси Mihomo -->
        <section class="sec">
          <div class="sec-cap">{m.sb_router_status_mihomo_local_ports()}</div>
          <p class="hint">{m.sb_router_status_mihomo_local_ports_hint()}</p>
          <div class="card-grid">
            <div class="field">
              <label class="lbl" for="ed-mh-mixed">Mixed (SOCKS+HTTP)</label>
              <input
                id="ed-mh-mixed"
                type="number"
                min="0"
                max="65535"
                class="inp"
                value={cfg.mihomoMixedPort ?? 0}
                onchange={(e) => void applyPatch({ mihomoMixedPort: Number((e.currentTarget as HTMLInputElement).value) || 0 })}
              />
            </div>
            <div class="field">
              <label class="lbl" for="ed-mh-http">HTTP</label>
              <input
                id="ed-mh-http"
                type="number"
                min="0"
                max="65535"
                class="inp"
                value={cfg.mihomoHttpPort ?? 0}
                onchange={(e) => void applyPatch({ mihomoHttpPort: Number((e.currentTarget as HTMLInputElement).value) || 0 })}
              />
            </div>
            <div class="field">
              <label class="lbl" for="ed-mh-socks">SOCKS5</label>
              <input
                id="ed-mh-socks"
                type="number"
                min="0"
                max="65535"
                class="inp"
                value={cfg.mihomoSocksPort ?? 0}
                onchange={(e) => void applyPatch({ mihomoSocksPort: Number((e.currentTarget as HTMLInputElement).value) || 0 })}
              />
            </div>
          </div>
        </section>
      {/if}
    {/if}

    {#if cfg?.routingEngine === 'mihomo'}
      <!-- Опасная зона / Сброс настроек Mihomo -->
      <section class="sec danger-sec">
        <div class="sec-cap text-danger">{m.sb_router_status_mihomo_danger_zone()}</div>
        <p class="hint">{m.sb_router_status_mihomo_reset_hint()}</p>
        <Button variant="danger" size="sm" fullWidth onclick={() => (resetConfirmOpen = true)}>
          {m.sb_router_status_mihomo_reset_btn()}
        </Button>
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <div class="footer-actions">
      <div class="footer-btns">
        <Button variant={engineOn ? 'danger' : 'primary'} size="sm" fullWidth disabled={switchBusy} onclick={handleToggleClick}>
          {engineOn ? m.sb_router_status_turn_off() : m.sb_router_status_turn_on()}
        </Button>
        <Button variant="ghost" size="sm" fullWidth loading={restarting} onclick={restartEngine}>{m.common_restart()}</Button>
      </div>
      {#if isExpert}
        <span class="save-status" class:err={lastError}>
          {saving ? m.sb_router_status_saving() : lastError ? m.common_error() : m.sb_router_status_saved()}
        </span>
      {/if}
    </div>
  {/snippet}
</SideDrawer>

<Modal
  open={resetConfirmOpen}
  title={m.sb_router_reset_modal_title()}
  size="sm"
  onclose={() => (resetConfirmOpen = false)}
>
  <div class="reset-confirm-body">
    <p class="reset-confirm-text">{m.sb_router_reset_modal_question()}</p>
    <p class="reset-confirm-warn">{m.sb_router_reset_modal_warning()}</p>
    <div class="reset-modal-actions">
      <Button variant="ghost" size="sm" onclick={() => (resetConfirmOpen = false)}>
        {m.common_cancel()}
      </Button>
      <Button variant="danger" size="sm" loading={resetting} onclick={handleResetMihomo}>
        {m.sb_router_status_mihomo_reset_btn()}
      </Button>
    </div>
  </div>
</Modal>

<Modal
  open={susaninModalOpen}
  title={m.sb_router_susanin_modal_title()}
  size="lg"
  onclose={() => (susaninModalOpen = false)}
>
  <div class="susanin-modal-body">
    <!-- Tab navigation -->
    <div class="susanin-tabs">
      <button
        type="button"
        class="susanin-tab-btn"
        class:active={susaninModalTab === 'learned'}
        onclick={() => (susaninModalTab = 'learned')}
      >
        <span style="display: flex; align-items: center; gap: 6px;">
          <Globe style="width: 14px; height: 14px;" />
          {m.sb_router_susanin_tab_radar({ count: susaninIPList.length })}
        </span>
      </button>
      <button
        type="button"
        class="susanin-tab-btn"
        class:active={susaninModalTab === 'always'}
        onclick={() => (susaninModalTab = 'always')}
      >
        <span style="display: flex; align-items: center; gap: 6px;">
          <Shield style="width: 14px; height: 14px;" />
          {m.sb_router_susanin_tab_always({ count: susaninAlwaysList.length })}
        </span>
      </button>
    </div>

    {#if susaninModalTab === 'learned'}
      <p class="hint" style="margin-bottom: 8px;">
        {m.sb_router_susanin_radar_desc_1()}
        {@html m.sb_router_susanin_radar_desc_2()}
      </p>

      <div style="display: flex; gap: 8px; margin-bottom: 8px;">
        <input
          type="search"
          class="inp"
          placeholder={m.sb_router_susanin_search_placeholder()}
          bind:value={susaninSearch}
          style="flex: 1;"
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={susaninIPList.length === 0}
          onclick={() => {
            navigator.clipboard.writeText(susaninIPList.join('\n'));
            notifications.success(m.sb_router_susanin_copied());
          }}
        >
          {m.sb_router_susanin_copy_all()}
        </Button>
        <Button
          variant="danger"
          size="sm"
          disabled={susaninIPList.length === 0 || susaninLoading}
          loading={susaninLoading}
          onclick={handleClearSusanin}
        >
          {m.sb_router_susanin_cleared()}
        </Button>
      </div>

      <div class="susanin-ip-scrollbox">
        {#if susaninIPList.length === 0}
          <div style="text-align: center; padding: 32px; color: var(--text-muted);">
            {m.sb_router_susanin_empty()}
          </div>
        {:else if filteredSusaninIPs.length === 0}
          <div style="text-align: center; padding: 32px; color: var(--text-muted);">
            {m.sb_router_susanin_not_found({ query: susaninSearch })}
          </div>
        {:else}
          <div class="susanin-card-grid">
            {#each filteredSusaninIPs as ip}
              {@const k = susaninKnowledge[ip] || lookupIpKnowledge(ip)}
              {@const isPinned = susaninAlwaysList.includes(ip)}
              <div class="susanin-card">
                <div class="susanin-card-top">
                  <span class="font-mono" style="font-size: 11px; font-weight: 600; user-select: all;">{ip}</span>
                  {#if isPinned}
                    <Badge variant="success" size="sm">{m.sb_router_susanin_auto_radar_badge()}</Badge>
                  {:else}
                    <button
                      type="button"
                      class="pin-btn"
                      title={m.sb_router_susanin_promote_title()}
                      onclick={() => pinToWhitelist(ip)}
                    >
                      {m.sb_router_susanin_promote_btn()}
                    </button>
                  {/if}
                </div>
                <div class="susanin-card-bottom">
                  {#if k?.title && k.title !== m.sb_router_susanin_unknown_host()}
                    <span class="service-chip" title="{k.org || ''} {k.country ? `(${k.country})` : ''}">
                      {k.title}
                    </span>
                  {:else}
                    <span class="service-chip muted">{m.sb_router_susanin_other_service()}</span>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>

    {:else}
      <!-- Whitelist Tab -->
      <p class="hint" style="margin-bottom: 8px;">
        {@html m.sb_router_susanin_always_desc_1()}
        {@html m.sb_router_susanin_always_desc_2()}
      </p>

      <div style="display: flex; gap: 8px; margin-bottom: 8px; align-items: center;">
        {#if !susaninAlwaysTextMode}
          <input
            type="text"
            class="inp font-mono"
            placeholder={m.sb_router_susanin_add_placeholder()}
            bind:value={susaninAlwaysInput}
            onkeydown={(e) => e.key === 'Enter' && addAlwaysEntry()}
            style="flex: 1;"
          />
          <Button
            variant="primary"
            size="sm"
            disabled={!susaninAlwaysInput.trim() || susaninSavingAlways}
            onclick={addAlwaysEntry}
          >
            {m.sb_router_susanin_add_btn()}
          </Button>
        {:else}
          <div style="flex: 1; font-size: 12px; color: var(--text-muted);">
            {m.sb_router_susanin_always_list_label()}
          </div>
        {/if}

        <Button
          variant="secondary"
          size="sm"
          onclick={() => {
            if (susaninAlwaysTextMode) {
              const parsed = susaninAlwaysText.split('\n').map((s) => s.trim()).filter(Boolean);
              susaninAlwaysList = parsed;
            } else {
              susaninAlwaysText = susaninAlwaysList.join('\n');
            }
            susaninAlwaysTextMode = !susaninAlwaysTextMode;
          }}
        >
          {susaninAlwaysTextMode ? m.sb_router_susanin_mode_list() : m.sb_router_susanin_mode_text()}
        </Button>
      </div>

      {#if susaninAlwaysTextMode}
        <textarea
          class="inp font-mono"
          rows="12"
          bind:value={susaninAlwaysText}
          placeholder={m.sb_router_susanin_text_placeholder()}
          style="width: 100%; resize: vertical; margin-bottom: 8px;"
        ></textarea>
        <div style="display: flex; justify-content: flex-end; gap: 8px;">
          <Button
            variant="primary"
            size="sm"
            loading={susaninSavingAlways}
            onclick={() => void saveSusaninAlways()}
          >
            {m.sb_router_susanin_save_rules()}
          </Button>
        </div>
      {:else}
        <div style="margin-bottom: 8px;">
          <input
            type="search"
            class="inp"
            placeholder={m.sb_router_susanin_search_rules_placeholder()}
            bind:value={susaninSearch}
            style="width: 100%;"
          />
        </div>

        <div class="susanin-ip-scrollbox">
          {#if susaninAlwaysList.length === 0}
            <div style="text-align: center; padding: 32px; color: var(--text-muted);">
              {m.sb_router_susanin_always_empty()}
            </div>
          {:else if filteredSusaninAlways.length === 0}
            <div style="text-align: center; padding: 32px; color: var(--text-muted);">
              {m.sb_router_susanin_not_found({ query: susaninSearch })}
            </div>
          {:else}
            <div class="susanin-always-list">
              {#each filteredSusaninAlways as entry}
                {@const isCidr = entry.includes('/')}
                {@const isIp = !isCidr && /^[0-9.]+$/.test(entry)}
                {@const k = isCidr || isIp ? lookupIpKnowledge(entry) : null}
                <div class="susanin-always-item">
                  <div style="display: flex; align-items: center; gap: 8px; min-width: 0;">
                    <span class="font-mono" style="font-size: 11px; font-weight: 600; user-select: all;">{entry}</span>
                    {#if isCidr}
                      <Badge variant="muted" size="sm">{m.sb_router_susanin_badge_cidr()}</Badge>
                    {:else if isIp}
                      <Badge variant="muted" size="sm">{m.sb_router_susanin_badge_single_ip()}</Badge>
                    {:else}
                      <Badge variant="accent" size="sm">{m.sb_router_susanin_badge_domain()}</Badge>
                    {/if}
                    {#if k?.title && k.title !== m.sb_router_susanin_unknown_host()}
                      <span class="service-chip" title="{k.org || ''}">{k.title}</span>
                    {/if}
                  </div>
                  <button
                    type="button"
                    class="del-btn"
                    title={m.sb_router_susanin_remove_title()}
                    disabled={susaninSavingAlways}
                    onclick={() => removeAlwaysEntry(entry)}
                  >
                    ✕
                  </button>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
    {/if}
  </div>

  {#snippet actions()}
    <div style="display: flex; justify-content: space-between; align-items: center; width: 100%;">
      <span style="font-size: 12px; color: var(--text-muted);">
        {#if susaninModalTab === 'learned'}
          {m.sb_router_susanin_stats_radar({ count: susaninIPList.length })}
        {:else}
          {m.sb_router_susanin_stats_always({ count: susaninAlwaysList.length })}
        {/if}
      </span>
      <div style="display: flex; gap: 8px;">
        {#if susaninModalTab === 'always' && !susaninAlwaysTextMode}
          <Button
            variant="secondary"
            size="sm"
            onclick={() => {
              navigator.clipboard.writeText(susaninAlwaysList.join('\n'));
              notifications.success(m.sb_router_susanin_copied());
            }}
          >
            {m.sb_router_susanin_copy_all()}
          </Button>
        {/if}
        <Button variant="ghost" size="sm" onclick={() => (susaninModalOpen = false)}>
          {m.common_close()}
        </Button>
      </div>
    </div>
  {/snippet}
</Modal>

<style>
  .sections { display: flex; flex-direction: column; }
  .sec {
    padding: 14px var(--sp-4);
    border-bottom: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 10px;
  }
  .sec:last-of-type { border-bottom: 0; }
  .sec-cap {
    font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em;
    color: var(--text-muted); display: flex; align-items: center; gap: 8px;
  }

  .engine-status {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
  }
  .engine-status.state-on {
    border-left: 3px solid var(--color-success, #22c55e);
  }
  .engine-status.state-warn {
    border-left: 3px solid var(--color-warning, #dab856);
  }
  .engine-status.state-off {
    border-left: 3px solid color-mix(in srgb, var(--text-muted) 55%, var(--border));
  }
  .engine-main {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  .engine-text {
    flex: 1;
    min-width: 0;
    padding-top: 2px;
  }
  .engine-head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .engine-title {
    font-weight: 600;
    font-size: 14px;
    color: var(--text-primary);
    line-height: 1.25;
  }
  .engine-sub {
    font-size: 11.5px;
    color: var(--text-muted);
    margin-top: 4px;
    line-height: 1.4;
  }
  .engine-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-muted);
  }
  .engine-version {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-secondary);
  }

  .card-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  @media (max-width: 480px) { .card-grid { grid-template-columns: 1fr; } }

  .field { display: flex; flex-direction: column; gap: 4px; }
  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 13px;
  }
  .field-row > span {
    flex: 1;
    min-width: 0;
  }
  .field-row > :global([role='switch']),
  .field-row > :global(.toggle-container) {
    flex-shrink: 0;
  }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .inp {
    padding: 6px 10px; border-radius: var(--radius-sm); background: var(--bg-primary);
    border: 1px solid var(--border); color: var(--text-primary); font-size: 12.5px; font-family: inherit;
  }
  .udp-timeout-row { display: flex; gap: 6px; }
  .udp-timeout-row .inp { flex: 1; }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .feature-chips { display: flex; flex-direction: column; gap: 8px; }
  .feature-chip {
    text-align: left;
    padding: 9px 12px;
    border-radius: var(--radius-md, 8px);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    cursor: pointer;
    font-family: inherit;
    color: inherit;
    display: flex;
    flex-direction: column;
    gap: 4px;
    transition: all 0.15s ease;
  }
  .feature-chip:hover {
    border-color: var(--accent);
    background: var(--bg-tertiary);
  }
  .feature-chip.active {
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .feature-chip-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .feature-chip-label {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .feature-chip-status-badge {
    font-size: 10.5px;
    padding: 1px 6px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    color: var(--text-muted);
    font-weight: 500;
    border: 1px solid var(--border);
  }
  .feature-chip-status-badge.active {
    background: color-mix(in srgb, var(--accent) 18%, transparent);
    color: var(--accent);
    border-color: color-mix(in srgb, var(--accent) 35%, transparent);
    font-weight: 600;
  }
  .feature-chip-desc {
    font-size: 11.5px;
    color: var(--text-secondary);
    line-height: 1.35;
    word-break: break-word;
    white-space: normal;
  }

  .bypass-presets { display: flex; flex-direction: column; gap: 6px; }
  /* Не .chip: имя занято утилитой Skeleton и app.css (nowrap + центровка). */
  .bypass-preset {
    text-align: left; padding: 8px 10px; border-radius: var(--radius-sm); background: var(--bg-tertiary);
    border: 1px solid var(--border); cursor: pointer; font-family: inherit; color: inherit;
    display: flex; flex-direction: column; gap: 2px;
    transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
  }
  .bypass-preset:hover { color: var(--color-text-primary); border-color: var(--color-border-hover); }
  .bypass-preset.active { background: var(--accent-soft); border-color: var(--accent); }
  .preset-label { font-size: 12.5px; font-weight: 600; }
  .preset-desc { font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); }

  .footer-actions { display: flex; flex-direction: column; gap: 6px; width: 100%; }
  .footer-btns {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
    width: 100%;
  }
  .save-status { align-self: flex-end; font-size: 11px; color: var(--text-muted); }
  .save-status.err { color: var(--color-error, #dc2626); }
  code.mono {
    font-family: var(--font-mono);
    font-size: 10.5px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
    color: var(--text-secondary);
  }
  .crash-info {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-left: 3px solid var(--color-warning, #dab856);
  }
  .crash-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
  }
  .crash-label { color: var(--text-muted); }
  .crash-value {
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .crash-reason {
    margin: 0;
    font-size: 11.5px;
    color: var(--text-secondary);
    line-height: 1.4;
    word-break: break-word;
  }
  .crash-suppressed {
    margin: 0;
    font-size: 11.5px;
    color: var(--color-warning, #dab856);
    line-height: 1.4;
  }
  .stat-line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
  }
  .stat-label { color: var(--text-muted); }
  .stat-value {
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: 11.5px;
    text-align: right;
  }
  .danger-sec {
    background: rgba(220, 38, 38, 0.04);
    border-top: 1px dashed rgba(220, 38, 38, 0.25);
  }
  .text-danger {
    color: var(--color-error, #dc2626) !important;
  }
  .reset-confirm-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .reset-confirm-text {
    margin: 0;
    font-size: 13px;
    color: var(--text-primary);
    line-height: 1.4;
  }
  .reset-confirm-warn {
    margin: 0;
    font-size: 12px;
    color: var(--color-error, #dc2626);
    font-weight: 500;
  }
  .reset-modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 6px;
  }
  .mode-segmented {
    margin-top: 6px;
    margin-bottom: 6px;
  }
  .global-target-card {
    background: rgba(218, 184, 86, 0.08);
    border: 1px solid rgba(218, 184, 86, 0.25);
    border-radius: var(--radius-md, 8px);
    padding: 10px 12px;
    margin-top: 6px;
  }
  .susanin-modal-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .susanin-tabs {
    display: flex;
    gap: 6px;
    border-bottom: 1px solid var(--border);
    padding-bottom: 8px;
    margin-bottom: 4px;
  }
  .susanin-tab-btn {
    padding: 6px 12px;
    font-size: 12px;
    font-weight: 500;
    border-radius: var(--radius-sm, 6px);
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition: all 0.15s ease;
  }
  .susanin-tab-btn:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }
  .susanin-tab-btn.active {
    color: var(--text-primary);
    background: var(--bg-tertiary);
    border-color: var(--border);
    font-weight: 600;
  }
  .susanin-ip-scrollbox {
    max-height: 380px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    padding: 8px;
  }
  .susanin-card-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 6px;
  }
  .susanin-card {
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    transition: border-color 0.15s ease;
  }
  .susanin-card:hover {
    border-color: var(--color-accent, #6366f1);
  }
  .susanin-card-top {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 6px;
  }
  .susanin-card-bottom {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .pin-btn {
    font-size: 10px;
    font-weight: 600;
    padding: 2px 6px;
    border-radius: 4px;
    background: rgba(99, 102, 241, 0.12);
    color: var(--color-accent, #6366f1);
    border: 1px solid rgba(99, 102, 241, 0.3);
    cursor: pointer;
    white-space: nowrap;
    transition: all 0.15s ease;
  }
  .pin-btn:hover {
    background: var(--color-accent, #6366f1);
    color: #fff;
  }
  .service-chip {
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 4px;
    background: var(--bg-secondary);
    color: var(--text-muted);
    border: 1px solid var(--border);
    max-width: 100%;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .service-chip.muted {
    opacity: 0.6;
  }
  .susanin-always-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .susanin-always-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 10px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 6px;
    gap: 8px;
  }
  .del-btn {
    width: 20px;
    height: 20px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
    border: 0;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 12px;
    transition: all 0.15s ease;
  }
  .del-btn:hover {
    background: rgba(220, 38, 38, 0.15);
    color: var(--color-error, #dc2626);
  }
</style>
