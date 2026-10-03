<!--
  Единое меню движка sing-box. Открывается кликом по движку/статус-pill в hero (drawerStore).
  beginner: состояние + здоровье + управление. expert: + редактируемые настройки (auto-save).
-->
<script lang="ts">
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
  import { formatSuppressedUntil, CRASH_WORDS } from './crashInfo';
  import { mergeAndSaveSettings, BYPASS_PRESETS } from './settingsActions';
  import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface, type WanAutoOverride } from './wanMode';
  import { pluralize, pluralForm, RULE_WORDS } from '$lib/utils/pluralize';
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
  let cacheDbNow = $derived(s?.cacheDbPath ? ` Сейчас: ${s.cacheDbPath}` : '');
  let cfg = $derived($storeSettings);
  let isExpert = $derived($mode === 'expert');

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
      { value: 'DIRECT', label: 'DIRECT (Прямое подключение / мимо VPN)' },
      { value: 'REJECT', label: 'REJECT (Блокировать)' },
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
      notifications.success('Накопленные адреса Susanin очищены');
      await loadSusaninData();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка очистки базы Susanin');
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
      notifications.success('Белый список Susanin обновлен');
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения белого списка');
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
      notifications.info(`Запись «${val}» уже есть в белом списке`);
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
      notifications.info(`Адрес ${ipOrCidr} уже в белом списке`);
      return;
    }
    const nextList = [...susaninAlwaysList, ipOrCidr];
    susaninAlwaysList = nextList;
    susaninAlwaysText = nextList.join('\n');
    void saveSusaninAlways(nextList);
    notifications.success(`Адрес ${ipOrCidr} зафиксирован в белом списке`);
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
        rule: 'По правилам (Rule)',
        global: 'Глобальный (Global)',
        direct: 'Напрямую (Direct)',
      };
      notifications.success(`Режим Mihomo: ${labels[newMode]}`);
    } catch (e) {
      notifications.error(`Ошибка переключения режима: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  async function handleGlobalTargetChange(newTarget: string) {
    currentGlobalTarget = newTarget;
    try {
      await api.mihomoSetGlobalProxy(newTarget);
      await applyPatch({ mihomoGlobalTarget: newTarget });
      notifications.success(`Весь трафик направлен через ${newTarget}`);
    } catch (e) {
      notifications.error(`Ошибка смены узла: ${e instanceof Error ? e.message : String(e)}`);
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
    const m = $settings?.routingMode;
    // Legacy settings did not contain routingMode; an enabled legacy engine
    // is TPROXY.  Keep FakeIP distinct: it is running, but neither capture
    // card in this drawer is its active mode.
    if (m === undefined || m === 'tproxy') return 'tproxy';
    return m === 'policy-tun' ? m : null;
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
            ctaHint: '(в Эксперт)',
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
      notifications.error('Остановите текущий движок перед сменой ядра');
      return;
    }
    saving = true;
    try {
      await mergeAndSaveSettings({ routingEngine: engine });
      notifications.success(`Ядро изменено на ${engine === 'mihomo' ? 'Mihomo' : 'Sing-box'}`);
    } catch (e) {
      notifications.error(`Ошибка при смене ядра: ${e}`);
    } finally {
      saving = false;
    }
  }

  let bigTitle = $derived.by(() => {
    if (!engineEnabled) return 'Движок выключен';
    return engineActive ? 'Движок работает' : 'Движок не работает';
  });
  let bigSubtitle = $derived.by(() => {
    if (!engineEnabled) return 'Не активен';
    if (!engineActive) return 'Перехват не активен — правила не применены';
    const n = s?.ruleCount ?? 0;
    return `Трафик идёт через ${pluralize(n, RULE_WORDS)}`;
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
    if (cfg?.deviceMode === 'all') return 'Весь LAN-трафик роутера.';
    const name = (cfg?.policyName ?? '').trim();
    return name
      ? `Только устройства политики «${name}».`
      : 'Политика не выбрана — трафик устройств не обрабатывается.';
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
  function selectMode(m: CaptureMode) {
    if (switchBusy) return;
    if (engineOn && activeMode === m) return;
    if (!engineOn && m === targetMode) return;
    pickedMode = m;
    if (engineOn) modeSwitch.request(m);
  }
  async function restartEngine(_e: MouseEvent) {
    if (restarting) return;
    restarting = true;
    try {
      await api.singboxControl('restart');
      await singboxRouterStore.reloadStatus();
      notifications.success('Движок перезапущен');
    } catch (e) {
      notifications.error(`Не удалось перезапустить: ${e instanceof Error ? e.message : String(e)}`);
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
      notifications.success('Конфигурация Mihomo сброшена к заводским настройкам');
      resetConfirmOpen = false;
      closeDrawer();
      if (typeof window !== 'undefined') {
        window.location.reload();
      }
    } catch (e) {
      notifications.error(`Не удалось сбросить настройки: ${e instanceof Error ? e.message : String(e)}`);
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
      notifications.error(`Не удалось сохранить: ${lastError}`);
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

  const UDP_TIMEOUT_OPTIONS = [
    { value: '', label: 'По умолчанию (5 мин)' },
    { value: '5m0s', label: '5 минут' },
    { value: '10m0s', label: '10 минут' },
    { value: '15m0s', label: '15 минут' },
    { value: '30m0s', label: '30 минут' },
    { value: '1h0m0s', label: '1 час' },
    { value: '3h0m0s', label: '3 часа' },
  ];

  const UDP_NAT_MAX_OPTIONS = [
    { value: '', label: 'Авто (по памяти)' },
    { value: '2048', label: '2048' },
    { value: '4096', label: '4096' },
    { value: '8192', label: '8192' },
    { value: '16384', label: '16384' },
  ];
</script>

<SideDrawer {open} onClose={closeDrawer} title={cfg?.routingEngine === 'mihomo' ? 'Движок Mihomo' : 'Движок sing-box'} width={420}>
  <div class="sections">
    <!-- Состояние -->
    <section class="sec">
      <div class="sec-cap">Состояние</div>
      <div class="engine-status" class:state-off={engineState === 'off'} class:state-warn={engineState === 'warn'} class:state-on={engineState === 'on'}>
        <div class="engine-main">
          <Toggle checked={engineOn} controlled loading={switchBusy} ariaLabel="Включить или выключить движок маршрутизации" onchange={toggleEngine} />
          <div class="engine-text">
            <div class="engine-head">
              <StatusDot variant={engineDotVariant} size="sm" />
              <div class="engine-title">{bigTitle}</div>
            </div>
            <div class="engine-sub">{bigSubtitle}</div>
          </div>
        </div>
        <div class="engine-meta">
          <span>{cfg?.routingEngine === 'mihomo' ? 'Версия Mihomo' : 'Версия sing-box'}</span>
          <span class="engine-version">{cfg?.routingEngine === 'mihomo' ? mihomoVersionLabel : sbVersionLabel}</span>
        </div>
      </div>

      <div class="sec-cap mt-4">Ядро маршрутизации</div>
      <div class="card-grid">
        <SegmentedControl
          ariaLabel="Ядро маршрутизации"
          options={[
            { label: 'Sing-box', value: 'sing-box' },
            { label: 'Mihomo', value: 'mihomo' }
          ]}
          value={cfg?.routingEngine === 'mihomo' ? 'mihomo' : 'sing-box'}
          onchange={(val) => selectEngine(val as 'sing-box' | 'mihomo')}
        />
      </div>
      <p class="hint mt-1">Остановите движок перед сменой ядра.</p>

      <div class="sec-cap mt-4">Режим захвата</div>
      <div class="card-grid">
        <OutboundOption
          label="TPROXY-правила"
          sub="перехват iptables на роутере"
          tone="accent"
          selected={displayedMode === 'tproxy'}
          onclick={() => selectMode('tproxy')}
        />
        <OutboundOption
          label="Политики + tun"
          sub="захват трафика через политику доступа Keenetic, без TPROXY-правил"
          tone="accent"
          selected={displayedMode === 'policy-tun'}
          onclick={() => selectMode('policy-tun')}
        />
      </div>
      {#if cfg?.routingEngine !== 'mihomo'}
        <p class="hint">Режим FakeIP включается на своей вкладке «Sing-box → FakeIP».</p>
      {:else}
        <div class="sec-cap mt-4">Режим обработки трафика</div>
        <div class="mode-segmented">
          <SegmentedControl
            ariaLabel="Режим обработки трафика"
            options={[
              { label: 'По правилам', value: 'rule' },
              { label: 'Глобальный', value: 'global' },
              { label: 'Напрямую', value: 'direct' }
            ]}
            value={currentMihomoMode}
            onchange={(val) => handleMihomoModeChange(val as 'rule' | 'global' | 'direct')}
          />
        </div>

        {#if currentMihomoMode === 'rule'}
          <p class="hint">
            <strong>По правилам (Rule):</strong> Штатный режим. Трафик каждого сайта и приложения проверяется по списку правил (GEOSITE, GEOIP, IP-CIDR и т.д.) и направляется в назначенный туннель или напрямую.
          </p>
        {:else if currentMihomoMode === 'global'}
          <div class="global-target-card">
            <p class="hint hint-warning">
              <strong>Глобальный режим (Global):</strong> Все правила маршрутизации игнорируются! Абсолютно весь интернет-трафик роутера принудительно направляется в выбранный узел:
            </p>
            <div class="field mt-2">
              <label class="lbl" for="ed-global-target">Целевой выход (GLOBAL)</label>
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
            <strong>Напрямую (Direct):</strong> Все правила игнорируются. Весь сетевой трафик идёт напрямую от провайдера мимо любых VPN и прокси.
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
              <span class="crash-label">Падений за 10 мин</span>
              <span class="crash-value">{crashCount}</span>
            </div>
          {/if}
          {#if s?.lastCrashReason}
            <p class="crash-reason">Причина: {s.lastCrashReason}</p>
          {/if}
          {#if crashSuppressedLabel}
            <p class="crash-suppressed">
              Автоперезапуск приостановлен до {crashSuppressedLabel}{#if crashCount > 0}&nbsp;({crashCount}
              {pluralForm(crashCount, CRASH_WORDS)} за 10 мин){/if}.
              Кнопка «Перезапустить» ниже запускает движок немедленно.
            </p>
          {/if}
        </div>
      {/if}
    </section>

    <!-- Ресурсы: живая память и трафик движка -->
    {#if resourcesVisible}
      <section class="sec">
        <div class="sec-cap">Ресурсы</div>
        <div class="stat-line" title={`Память Go-рантайма ${cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'} по данным Clash API; фактический RSS процесса выше`}>
          <span class="stat-label">Память {cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'}</span>
          <span class="stat-value">{memoryLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">Скорость</span>
          <span class="stat-value">{rateLabel}</span>
        </div>
        <div class="stat-line">
          <span class="stat-label">За сессию</span>
          <span class="stat-value">{sessionLabel}</span>
        </div>
      </section>
    {/if}

    <!-- Зависимости -->
    <section class="sec">
      <div class="sec-cap">Зависимости</div>
      {#each deps as dep}
        <DepRow tone={dep.tone} label={dep.label} hint={dep.hint} />
      {/each}
    </section>

    <!-- Замечания -->
    {#if issueCount > 0}
      <section class="sec">
        <div class="sec-cap">Замечания <Badge variant="warning" size="sm">{issueCount}</Badge></div>
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
        <div class="sec-cap">Источник трафика</div>
        <p class="hint">{sourceSummary}</p>
        <Button variant="ghost" size="sm" onclick={goToSourceSettings}>Настроить источник →</Button>
      </section>
    {/if}

    {#if cfg}
      <!-- WAN-интерфейс -->
      <section class="sec">
        <div class="sec-cap">WAN-интерфейс (выход в Интернет)</div>
        <div class="field-row">
          <span>Авто-определение</span>
          <Toggle checked={wanAuto} onchange={(checked) => toggleAutoDetect(checked)} />
        </div>
        {#if !wanAuto}
          <div class="field">
            <label class="lbl" for="ed-wan">Интерфейс провайдера</label>
            <select id="ed-wan" class="inp" value={cfg.wanInterface ?? ''} onchange={onWanInterfaceChange}>
              <option value="">— выберите интерфейс —</option>
              {#each wanInterfaces as iface (iface.name)}
                <option value={iface.name}>{iface.name}{iface.label ? ` — ${iface.label}` : ''}</option>
              {/each}
            </select>
          </div>
        {/if}
        <p class="hint">Через какой внешний интерфейс отправляется прямой трафик (direct).</p>
      </section>

      <!-- Кэш sing-box (issue #842): единственное место настройки, вне expert-гейта —
           износ флеша касается любого режима с fakeip. -->
      {#if cfg?.routingEngine !== 'mihomo'}
        <section class="sec">
          <div class="sec-cap">Кэш sing-box</div>
          <div class="field">
            <label class="lbl" for="ed-cache-location">Хранилище cache.db</label>
            <select
              id="ed-cache-location"
              class="inp"
              value={cfg.cacheFileLocation ?? ''}
              onchange={onCacheLocationChange}
            >
              {#if !cfg.cacheFileLocation}
                <option value="">Не задано — как в 00-base.json</option>
              {/if}
              <option value="flash">Флеш роутера (/opt)</option>
              <option value="tmp">Оперативная память (/tmp)</option>
            </select>
          </div>
          <p class="hint">В RAM записи FakeIP-карты и Clash не изнашивают флеш, но кэш не переживает перезагрузку. Выбор перезаписывает путь cache_file в 00-base.json, включая заданный вручную.{cacheDbNow}</p>
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
        <div class="sec-cap">Анализ трафика</div>
        <div class="field-row">
          <span>Включить sniff</span>
          <Toggle checked={cfg.snifferEnabled} onchange={(checked) => toggleSniffer(checked)} />
        </div>
        <p class="hint">Анализ HTTP/TLS/QUIC по содержимому. Улучшает срабатывание domain-based правил при IP-only matchers.</p>
        <div class="field">
          <label class="lbl" for="ed-udp-timeout">UDP таймаут сессии</label>
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
        <p class="hint">Как долго {cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'} держит UDP-сессии активными. Увеличьте если игры или другие UDP-приложения обрываются каждые несколько минут.</p>
        <div class="field">
          <label class="lbl" for="ed-udp-nat-max">Потолок UDP-сессий</label>
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
        <p class="hint">Сколько UDP-сессий движок держит одновременно; при переполнении вытесняется самая старая. Уменьшите на роутере с малой памятью, если {cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'} растёт под UDP-нагрузкой.</p>
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
        <div class="sec-cap">Службы Keenetic в туннель</div>
        <div class="feature-chips">
          <button type="button" class="feature-chip" class:active={!!cfg?.keeneticCloudTunnel} onclick={toggleCloudTunnel}>
            <div class="feature-chip-head">
              <span class="feature-chip-label">Облако Keenetic & KeenDNS</span>
              <span class="feature-chip-status-badge" class:active={!!cfg?.keeneticCloudTunnel}>
                {cfg?.keeneticCloudTunnel ? 'В туннеле' : 'Напрямую'}
              </span>
            </div>
            <span class="feature-chip-desc">
              исходящая связь роутера с облаком Keenetic (KeenDNS, SSTP, приложение) идет через туннель
            </span>
          </button>
        </div>

        {#if cfg?.keeneticCloudTunnel}
          <div class="field" style="margin-top: 10px;">
            <label class="lbl" for="cloud-outbound-sel">Куда направить службы Keenetic</label>
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
          Направляет трафик роутера к серверам Keenetic Cloud (KeenDNS, облачный реле, аутентификация, приложение Keenetic и SSTP VPN) через выбранный туннель. Позволяет сохранить удаленный доступ к роутеру по доменному имени извне даже при блокировках провайдером или включении «белых списков». Локальный доступ дома (<code>my.keenetic.net</code>) остается прямым.
        </p>
      </section>

      <!-- Адаптивное обнаружение блокировок (радар Susanin) -->
      <section class="sec">
        <div class="sec-cap">Адаптивный радар Susanin</div>
        <div class="feature-chips">
          <button type="button" class="feature-chip" class:active={!!cfg?.susaninEnabled} onclick={toggleSusanin}>
            <div class="feature-chip-head">
              <span class="feature-chip-label">Радар Susanin (автообход блокировок)</span>
              <span class="feature-chip-status-badge" class:active={!!cfg?.susaninEnabled}>
                {cfg?.susaninEnabled ? 'Включен' : 'Выключен'}
              </span>
            </div>
            <span class="feature-chip-desc">
              автоматически обнаруживает заблокированные IP-адреса и накапливает их в правиле susanin
            </span>
          </button>
        </div>

        {#if cfg?.susaninEnabled}
          <div class="field" style="margin-top: 10px;">
            <label class="lbl" for="susanin-outbound-sel">Куда направить заблокированные IP</label>
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
              Накоплено адресов: <strong style="color: var(--text-primary);">{susaninIPCount}</strong>
            </span>
            <Button variant="secondary" size="sm" onclick={openSusaninModal}>
              Просмотреть список IP ({susaninIPCount})
            </Button>
          </div>
        {/if}

        <p class="hint">
          Радар Susanin непрерывно отслеживает сетевые сбои (таймауты TLS handshake, сбросы TCP RST) при прямых подключениях к сайтам. Все адреса с признаками блокировки DPI/РКН динамически накапливаются в правиле <code>susanin</code> активного движка и направляются через выбранный туннель или группу прокси без перезапуска.
        </p>
      </section>

      <!-- Исключения: порт-пресеты + IP-пресеты (keendns) + ручные порты/подсети -->
      <section class="sec">
        <div class="sec-cap">Исключения</div>
        <div class="bypass-presets">
          {#each BYPASS_PRESETS as p (p.id)}
            {@const active = (cfg.bypassPresets ?? []).includes(p.id)}
            <button type="button" class="bypass-preset" class:active onclick={() => togglePreset(p.id)}>
              <span class="preset-label">{p.label}</span>
              <span class="preset-desc">
                {p.id === 'keendns' ? `имена роутера резолвит сам роутер, его адреса — мимо ${cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'}` : p.desc}
              </span>
            </button>
          {/each}
        </div>
        <div class="field">
          <label class="lbl" for="ed-ports-input">Доп. порты</label>
          <PortChipsInput inputId="ed-ports-input" value={cfg.bypassExtraPorts ?? ''} onChange={(v) => void applyPatch({ bypassExtraPorts: v })} />
        </div>
        <p class="hint">Эти порты пойдут мимо {cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'} (прямо в WAN). Полезно для L2TP/NTP/SMB не ломая LAN-сервисы. Поддерживаются одиночные порты (<code class="mono">443 TCP</code>) и диапазоны (<code class="mono">5000-5500 UDP</code>).</p>
        <!-- В «Политики + tun» перехвата netfilter нет вовсе, поэтому исключения
             работают иначе, чем в TPROXY: они влияют только на классы QoS и на
             перехват DNS. Про 53 сказано отдельно — там выключатель СОЗНАТЕЛЬНО
             грубее, чем в TPROXY (пер-протокольный там, общий здесь), потому что
             сам перехват 53-го неделим: на усечённый ответ клиент переспрашивает
             по TCP, и половинчатый перехват дал бы резолвинг, зависящий от
             размера ответа. -->
        {#if policyTunMode}
          <p class="hint">В режиме «Политики + tun» исключения влияют только на классы QoS и на перехват DNS. Порт <code class="mono">53</code> в любом из списков — UDP или TCP — выключает перехват DNS целиком, для обоих протоколов сразу.</p>
        {/if}
        <div class="field">
          <label class="lbl" for="ed-subnets-input">Доп. подсети</label>
          <SubnetChipsInput inputId="ed-subnets-input" value={cfg.bypassExtraSubnets ?? ''} onChange={(v) => void applyPatch({ bypassExtraSubnets: v })} />
        </div>
        <p class="hint">IP или подсети, чей трафик целиком пойдёт мимо {cfg?.routingEngine === 'mihomo' ? 'Mihomo' : 'sing-box'} (прямо в WAN). Нужно для корпоративных VPN (Cisco AnyConnect и т.п.), чтобы их трафик не перехватывался.</p>
        <!-- Набор AWGM-BYPASS живёт только в TPROXY-перехвате: в policy-tun
             (DSCPOnly) правило обхода не эмитится — обходить нечего. -->
        {#if !policyTunMode}
          <BypassGeoIPTags {cfg} onPatch={(patch) => applyPatch(patch)} />
        {/if}
      </section>

      {#if cfg?.routingEngine === 'mihomo'}
        <!-- Локальные порты прокси Mihomo -->
        <section class="sec">
          <div class="sec-cap">Локальные порты прокси Mihomo</div>
          <p class="hint">Локальные proxy-порты позволяют внешним программам на роутере использовать прокси Mihomo напрямую (0 = отключено).</p>
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
              <label class="lbl" for="ed-mh-http">HTTP порт</label>
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
              <label class="lbl" for="ed-mh-socks">SOCKS5 порт</label>
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
        <div class="sec-cap text-danger">Опасная зона</div>
        <p class="hint">Очистить созданные правила маршрутизации и группы прокси Mihomo. Туннели, прокси и подписки сохранятся.</p>
        <Button variant="danger" size="sm" fullWidth onclick={() => (resetConfirmOpen = true)}>
          Сбросить правила и группы Mihomo
        </Button>
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <div class="footer-actions">
      <div class="footer-btns">
        <Button variant={engineOn ? 'danger' : 'primary'} size="sm" fullWidth disabled={switchBusy} onclick={handleToggleClick}>
          {engineOn ? 'Выключить' : 'Включить'}
        </Button>
        <Button variant="ghost" size="sm" fullWidth loading={restarting} onclick={restartEngine}>Перезапустить</Button>
      </div>
      {#if isExpert}
        <span class="save-status" class:err={lastError}>
          {saving ? 'Сохраняем…' : lastError ? `Ошибка` : '✓ Сохранено'}
        </span>
      {/if}
    </div>
  {/snippet}
</SideDrawer>

<Modal
  open={resetConfirmOpen}
  title="Сброс маршрутов и групп Mihomo"
  size="sm"
  onclose={() => (resetConfirmOpen = false)}
>
  <div class="reset-confirm-body">
    <p class="reset-confirm-text">Вы действительно хотите сбросить правила маршрутизации и группы прокси Mihomo?</p>
    <p class="reset-confirm-warn">Все ваши туннели, нативные прокси и подписки сохранятся. Будут удалены только созданные правила и группы прокси.</p>
    <div class="reset-modal-actions">
      <Button variant="ghost" size="sm" onclick={() => (resetConfirmOpen = false)}>
        Отмена
      </Button>
      <Button variant="danger" size="sm" loading={resetting} onclick={handleResetMihomo}>
        Сбросить маршруты
      </Button>
    </div>
  </div>
</Modal>

<Modal
  open={susaninModalOpen}
  title="База данных и белый список Susanin"
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
          Обнаруженные IP ({susaninIPList.length})
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
          Белый список ({susaninAlwaysList.length})
        </span>
      </button>
    </div>

    {#if susaninModalTab === 'learned'}
      <p class="hint" style="margin-bottom: 8px;">
        Адреса и подсети, к которым зафиксирован сбой прямого подключения (DPI/РКН).
        Маршрутизируются через туннель по правилу <code>susanin</code>. Любой адрес можно зафиксировать в белом списке навсегда.
      </p>

      <div style="display: flex; gap: 8px; margin-bottom: 8px;">
        <input
          type="search"
          class="inp"
          placeholder="Поиск по IP, сервису (YouTube, CDN77)..."
          bind:value={susaninSearch}
          style="flex: 1;"
        />
        <Button
          variant="secondary"
          size="sm"
          disabled={susaninIPList.length === 0}
          onclick={() => {
            navigator.clipboard.writeText(susaninIPList.join('\n'));
            notifications.success('Список IP скопирован в буфер');
          }}
        >
          Скопировать
        </Button>
        <Button
          variant="danger"
          size="sm"
          disabled={susaninIPList.length === 0 || susaninLoading}
          loading={susaninLoading}
          onclick={handleClearSusanin}
        >
          Очистить кэш
        </Button>
      </div>

      <div class="susanin-ip-scrollbox">
        {#if susaninIPList.length === 0}
          <div style="text-align: center; padding: 32px; color: var(--text-muted);">
            Динамический кэш пуст. При обнаружении сбоев IP появятся здесь автоматически.
          </div>
        {:else if filteredSusaninIPs.length === 0}
          <div style="text-align: center; padding: 32px; color: var(--text-muted);">
            Ничего не найдено по запросу «{susaninSearch}»
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
                    <Badge variant="success" size="sm">В белом</Badge>
                  {:else}
                    <button
                      type="button"
                      class="pin-btn"
                      title="Зафиксировать в белом списке навсегда"
                      onclick={() => pinToWhitelist(ip)}
                    >
                      + В белый
                    </button>
                  {/if}
                </div>
                <div class="susanin-card-bottom">
                  {#if k?.title && k.title !== 'Внешний узел'}
                    <span class="service-chip" title="{k.org || ''} {k.country ? `(${k.country})` : ''}">
                      {k.title}
                    </span>
                  {:else}
                    <span class="service-chip muted">Интернет-сервис</span>
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
        Статический список доменов, IP-адресов и подсетей CIDR, которые <strong>всегда</strong> направляются через туннель. 
        Домены (напр. <code>example.com</code>, <code>service.net</code>) автоматически разрешаются в IP демоном Susanin.
      </p>

      <div style="display: flex; gap: 8px; margin-bottom: 8px; align-items: center;">
        {#if !susaninAlwaysTextMode}
          <input
            type="text"
            class="inp font-mono"
            placeholder="Домен (напр. example.com) или IP/CIDR (198.51.100.0/24)..."
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
            + Добавить
          </Button>
        {:else}
          <div style="flex: 1; font-size: 12px; color: var(--text-muted);">
            Редактирование текстом (по одной записи на строку):
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
          {susaninAlwaysTextMode ? 'Режим списка' : 'Режим текста'}
        </Button>
      </div>

      {#if susaninAlwaysTextMode}
        <textarea
          class="inp font-mono"
          rows="12"
          bind:value={susaninAlwaysText}
          placeholder="По одной записи на строку:
example.com
198.51.100.0/24
gemini.google.com"
          style="width: 100%; resize: vertical; margin-bottom: 8px;"
        ></textarea>
        <div style="display: flex; justify-content: flex-end; gap: 8px;">
          <Button
            variant="primary"
            size="sm"
            loading={susaninSavingAlways}
            onclick={() => void saveSusaninAlways()}
          >
            Сохранить белый список
          </Button>
        </div>
      {:else}
        <div style="margin-bottom: 8px;">
          <input
            type="search"
            class="inp"
            placeholder="Поиск по белому списку..."
            bind:value={susaninSearch}
            style="width: 100%;"
          />
        </div>

        <div class="susanin-ip-scrollbox">
          {#if susaninAlwaysList.length === 0}
            <div style="text-align: center; padding: 32px; color: var(--text-muted);">
              Белый список пуст. Добавьте домен или IP адрес выше.
            </div>
          {:else if filteredSusaninAlways.length === 0}
            <div style="text-align: center; padding: 32px; color: var(--text-muted);">
              Ничего не найдено по запросу «{susaninSearch}»
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
                      <Badge variant="muted" size="sm">Подсеть CIDR</Badge>
                    {:else if isIp}
                      <Badge variant="muted" size="sm">IP адрес</Badge>
                    {:else}
                      <Badge variant="accent" size="sm">Домен</Badge>
                    {/if}
                    {#if k?.title && k.title !== 'Внешний узел'}
                      <span class="service-chip" title="{k.org || ''}">{k.title}</span>
                    {/if}
                  </div>
                  <button
                    type="button"
                    class="del-btn"
                    title="Удалить из белого списка"
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
          Всего в кэше: {susaninIPList.length} IP
        {:else}
          Всего в белом списке: {susaninAlwaysList.length} записей
        {/if}
      </span>
      <div style="display: flex; gap: 8px;">
        {#if susaninModalTab === 'always' && !susaninAlwaysTextMode}
          <Button
            variant="secondary"
            size="sm"
            onclick={() => {
              navigator.clipboard.writeText(susaninAlwaysList.join('\n'));
              notifications.success('Белый список скопирован');
            }}
          >
            Скопировать список
          </Button>
        {/if}
        <Button variant="ghost" size="sm" onclick={() => (susaninModalOpen = false)}>
          Закрыть
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
