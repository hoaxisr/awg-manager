<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/api/client';
  import { m } from '$lib/i18n';
  import type {
    FircStatus,
    FircSettingValue,
    FircExtConfig,
    FircInterfaceOption,
    FircProxyGroup,
    FircInboundServerItem,
    FircMigrationOptions,
    FircMigrationSummary,
    FircTunnel,
    FircGroup,
  } from '$lib/types/firc';
  import { reloadFircAll } from '$lib/stores/firc';
  import { openFircMigrationModal } from '$lib/stores/fircMigrationModal';
  import { StatusDot, Badge, Toggle, Button, Modal } from '$lib/components/ui';
  import { notifications } from '$lib/stores/notifications';

  const fallbackCatalog: FircInboundServerItem[] = [
    { id: 'awg_server', name: 'Сервер AmneziaWG (Wireguard2)', category: 'awgm', running: false, detail: 'nwg2 · 10.10.0.0/24' },
    { id: 'wdtt_server', name: 'Сервер WDTT', category: 'awgm', running: false, detail: 'opkgtun17/19 · 10.66.0.0/16' },
    { id: 'freeturn_server', name: 'Сервер FreeTurn', category: 'awgm', running: false, detail: 'nwg2 · 10.10.0.2/32' },
    { id: 'vless_xhttp_server', name: 'Сервер VLESS XHTTP', category: 'awgm', running: false, detail: 'router · XHTTP' },
    { id: 'openflux_server', name: 'Сервер OpenFlux', category: 'awgm', running: false, detail: 'opkgtun · 10.77.0.0/16' },
    { id: 'keenetic_wg', name: 'Keenetic WireGuard (Wireguard0)', category: 'system', running: false, detail: 'nwg0 · 172.16.6.0/24' },
    { id: 'keenetic_sstp', name: 'Keenetic SSTP', category: 'system', running: false, detail: 'sstp+ · 172.16.7.0/24' },
    { id: 'keenetic_l2tp', name: 'Keenetic L2TP/IPsec', category: 'system', running: false, detail: 'l2tp+ · 172.16.2.0/24' },
    { id: 'keenetic_ikev2', name: 'Keenetic IKEv2/IPsec', category: 'system', running: false, detail: 'xfrms2 · 172.20.8.0/24' },
    { id: 'keenetic_ikev1', name: 'Keenetic Virtual IP (IKEv1)', category: 'system', running: false, detail: 'xfrms1 · 172.20.0.0/24' },
    { id: 'keenetic_pptp', name: 'Keenetic PPTP', category: 'system', running: false, detail: 'vpn+ · 172.16.1.0/24' },
    { id: 'keenetic_oc', name: 'Keenetic OpenConnect', category: 'system', running: false, detail: 'oc+ · 172.16.3.0/24' },
  ];

  const defaultExt: FircExtConfig = {
    proxyGroups: [],
    keenDnsCloud: { enabled: false, proxyGroupId: '', interfaces: [] },
    serverRouting: {
      enabledServers: [],
      awgServer: false,
      wdttServer: false,
      freeTurnServer: false,
      vlessXhttpServer: false,
    },
  };

  let status = $state<FircStatus | null>(null);
  let settings = $state<Record<string, FircSettingValue>>({});
  let originalSettings = $state<string>('{}');
  let extConfig = $state<FircExtConfig>(structuredClone(defaultExt));
  let originalExt = $state<string>(JSON.stringify(defaultExt));
  let keenDnsSyncError = $state('');
  let availableServers = $state<FircInboundServerItem[]>(structuredClone(fallbackCatalog));
  let serverPickerOpen = $state(false);
  let interfaces = $state<FircInterfaceOption[]>([]);
  let pgModalOpen = $state(false);
  let pgDraft = $state<FircProxyGroup[]>([]);
  let migratePreviewOpen = $state(false);
  let migratePreview = $state<FircMigrationSummary | null>(null);
  let migrateLoadingPreview = $state(false);
  let migrateApplying = $state(false);
  let migrateMode = $state<'merge' | 'replace'>('merge');
  let migrateRouterSource = $state<'auto' | 'tproxy' | 'fakeip' | 'both'>('auto');
  let migrateIncludeFakeIP = $state(false);
  let selectedMigrateTunnelIds = $state<string[]>([]);
  let selectedMigrateGroupIds = $state<string[]>([]);
  let pendingRestart = $state<string[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let busyAction = $state<string | null>(null);
  let pollTimer: ReturnType<typeof setInterval> | null = null;

  interface Props {
    hasUnsaved?: boolean;
  }

  let { hasUnsaved = $bindable(false) }: Props = $props();

  const settingsDirty = $derived(JSON.stringify(settings) !== originalSettings);
  const extDirty = $derived(JSON.stringify(extConfig) !== originalExt);
  const isDirty = $derived(settingsDirty || extDirty);
  const restartRequired = $derived(pendingRestart.length > 0);

  $effect(() => {
    hasUnsaved = isDirty;
  });

  function strVal(key: string, fallback = ''): string {
    const v = settings[key];
    if (v === undefined || v === null) return fallback;
    return String(v);
  }

  function numVal(key: string, fallback = 0): number {
    const v = settings[key];
    if (typeof v === 'number' && Number.isFinite(v)) return v;
    const parsed = Number(v);
    return Number.isFinite(parsed) ? parsed : fallback;
  }

  function boolVal(key: string, fallback = false): boolean {
    const v = settings[key];
    if (typeof v === 'boolean') return v;
    if (v === 'true' || v === 1) return true;
    if (v === 'false' || v === 0) return false;
    return fallback;
  }

  function setField(key: string, val: FircSettingValue) {
    settings = { ...settings, [key]: val };
  }

  function invertedBool(key: string): boolean {
    return !boolVal(key, false);
  }

  function setInvertedBool(key: string, enabled: boolean) {
    setField(key, !enabled);
  }

  let lanLinksText = $derived.by(() => {
    const raw = settings['app.link'];
    if (Array.isArray(raw)) return raw.join(', ');
    if (typeof raw === 'string') return raw;
    return 'br0';
  });

  function updateLanLinks(text: string) {
    const list = text
      .split(/[\s,;]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    setField('app.link', list);
  }

  function normalizeExtFromApi(raw: FircExtConfig): FircExtConfig {
    let enabled = Array.isArray(raw.serverRouting?.enabledServers)
      ? [...raw.serverRouting.enabledServers]
      : [];
    if (!Array.isArray(raw.serverRouting?.enabledServers)) {
      if (raw.serverRouting?.awgServer) enabled.push('awg_server');
      if (raw.serverRouting?.wdttServer) enabled.push('wdtt_server');
      if (raw.serverRouting?.freeTurnServer) enabled.push('freeturn_server');
      if (raw.serverRouting?.vlessXhttpServer) enabled.push('vless_xhttp_server');
    }
    return {
      proxyGroups: raw.proxyGroups ?? [],
      globalTarget: raw.globalTarget ?? '',
      keenDnsCloud: {
        enabled: Boolean(raw.keenDnsCloud?.enabled),
        proxyGroupId: raw.keenDnsCloud?.proxyGroupId ?? '',
        interfaces: raw.keenDnsCloud?.interfaces ?? [],
      },
      serverRouting: {
        enabledServers: enabled,
        awgServer: enabled.includes('awg_server'),
        wdttServer: enabled.includes('wdtt_server'),
        freeTurnServer: enabled.includes('freeturn_server'),
        vlessXhttpServer: enabled.includes('vless_xhttp_server'),
      },
    };
  }

  function isInboundServerSelected(id: string): boolean {
    return (extConfig.serverRouting.enabledServers ?? []).includes(id);
  }

  function toggleInboundServer(id: string) {
    const cur = extConfig.serverRouting.enabledServers ?? [];
    const next = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id];
    extConfig.serverRouting = {
      enabledServers: next,
      awgServer: next.includes('awg_server'),
      wdttServer: next.includes('wdtt_server'),
      freeTurnServer: next.includes('freeturn_server'),
      vlessXhttpServer: next.includes('vless_xhttp_server'),
    };
  }

  function getServerById(id: string): FircInboundServerItem {
    return (
      availableServers.find((s) => s.id === id) ?? {
        id,
        name: id,
        category: 'system',
        running: false,
        detail: '',
      }
    );
  }

  async function loadAll(silent = false) {
    if (!silent) loading = true;
    try {
      const [st, cfg, extRes, ifacesRes] = await Promise.all([
        api.fircGetStatus().catch(() => null),
        api.fircGetDaemonSettings().catch(() => null),
        api.fircGetExt().catch(() => null),
        api.fircGetInterfaces().catch(() => null),
      ]);
      if (st) status = st;
      if (ifacesRes) interfaces = ifacesRes;
      if (cfg && cfg.settings && !settingsDirty) {
        settings = { ...cfg.settings };
        originalSettings = JSON.stringify(cfg.settings);
        pendingRestart = cfg.pendingRestart ?? [];
      } else if (cfg) {
        pendingRestart = cfg.pendingRestart ?? [];
      }
      if (extRes) {
        if (Array.isArray(extRes.availableServers) && extRes.availableServers.length > 0) {
          availableServers = extRes.availableServers;
        }
        keenDnsSyncError = extRes.keenDnsCloud?.enabled ? (extRes.keenDnsCloud?.lastSyncError ?? '') : '';
        if (!extDirty) {
          const normalizedExt = normalizeExtFromApi(extRes);
          extConfig = normalizedExt;
          originalExt = JSON.stringify(normalizedExt);
        }
      }
    } catch (e: unknown) {
      if (!silent) notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка загрузки параметров FIRC');
    } finally {
      if (!silent) loading = false;
    }
  }

  function keenDnsTargetSelectValue(): string {
    if (extConfig.keenDnsCloud.proxyGroupId) {
      return `pg:${extConfig.keenDnsCloud.proxyGroupId}`;
    }
    const firstIface = extConfig.keenDnsCloud.interfaces?.[0] ?? '';
    return firstIface ? `iface:${firstIface}` : '';
  }

  function handleKeenDnsTargetChange(val: string) {
    if (val.startsWith('pg:')) {
      const pgId = val.slice(3);
      const pg = extConfig.proxyGroups.find((x) => x.id === pgId);
      extConfig.keenDnsCloud.proxyGroupId = pgId;
      extConfig.keenDnsCloud.interfaces = pg ? [...pg.interfaces] : [];
    } else if (val.startsWith('iface:')) {
      const iface = val.slice(6);
      extConfig.keenDnsCloud.proxyGroupId = '';
      extConfig.keenDnsCloud.interfaces = iface ? [iface] : [];
    } else {
      extConfig.keenDnsCloud.proxyGroupId = '';
      extConfig.keenDnsCloud.interfaces = [];
    }
  }

  function formatIfaceLabel(opt: FircInterfaceOption): string {
    const desc = (opt.name || opt.ndmsName || '').split(' (')[0].trim();
    const base = desc && desc !== opt.id ? `${opt.id} — ${desc}` : opt.id;
    return opt.role === 'wan' ? `Direct (${base})` : base;
  }

  function isSelectableTunnelOrWan(opt: FircInterfaceOption): boolean {
    if (!opt.id || opt.id.startsWith('t2s')) return false;
    const lower = (opt.name || opt.ndmsName || '').toLowerCase();
    if (opt.id === 'opkgtun17' || opt.id === 'opkgtun19' || lower.includes('awgm wdtt') || lower.includes('vpn server') || (opt.name || opt.ndmsName) === 'AWGM') {
      return false;
    }
    return opt.role === 'tunnel' || opt.role === 'wan';
  }

  function openPgModal() {
    pgDraft = extConfig.proxyGroups.map((pg) => ({
      id: pg.id,
      name: pg.name,
      interfaces: [...pg.interfaces],
    }));
    if (pgDraft.length === 0) {
      pgDraft = [
        {
          id: Math.random().toString(16).slice(2, 10),
          name: 'KeenDNS Fallback + Direct',
          interfaces: [],
        },
      ];
    }
    pgModalOpen = true;
  }

  function addPgDraft() {
    pgDraft = [
      ...pgDraft,
      {
        id: Math.random().toString(16).slice(2, 10),
        name: `Fallback #${pgDraft.length + 1}`,
        interfaces: [],
      },
    ];
  }

  function removePgDraft(idx: number) {
    pgDraft = pgDraft.filter((_, i) => i !== idx);
  }

  function togglePgDraftIface(pgIdx: number, ifaceId: string) {
    const pg = pgDraft[pgIdx];
    if (!pg) return;
    if (pg.interfaces.includes(ifaceId)) {
      pg.interfaces = pg.interfaces.filter((x) => x !== ifaceId);
    } else if (pg.interfaces.length < 4) {
      pg.interfaces = [...pg.interfaces, ifaceId];
    }
  }

  function applyPgModal() {
    for (const pg of pgDraft) {
      if (!pg.name.trim() || pg.interfaces.length === 0) {
        notifications.error('Укажите название и выберите хотя бы один интерфейс для прокси-группы');
        return;
      }
    }
    extConfig.proxyGroups = pgDraft.map((pg) => ({
      id: pg.id,
      name: pg.name.trim(),
      interfaces: [...pg.interfaces],
    }));
    if (!extConfig.keenDnsCloud.proxyGroupId && extConfig.proxyGroups.length > 0) {
      extConfig.keenDnsCloud.proxyGroupId = extConfig.proxyGroups[0].id;
      extConfig.keenDnsCloud.interfaces = [...extConfig.proxyGroups[0].interfaces];
    }
    pgModalOpen = false;
  }

  onMount(() => {
    loadAll();
    pollTimer = setInterval(() => loadAll(true), 4000);
  });

  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
  });

  function resetChanges() {
    try {
      settings = JSON.parse(originalSettings);
      extConfig = JSON.parse(originalExt);
    } catch {
      loadAll();
    }
  }

  async function saveAll() {
    if (extConfig.keenDnsCloud.enabled) {
      const hasTarget =
        Boolean(extConfig.keenDnsCloud.proxyGroupId) ||
        (extConfig.keenDnsCloud.interfaces?.length ?? 0) > 0;
      if (!hasTarget) {
        notifications.error('Выберите целевую прокси-группу или туннель для KeenDNS');
        return;
      }
    }
    saving = true;
    try {
      if (settingsDirty) {
        const res = await api.fircSaveDaemonSettings(settings);
        originalSettings = JSON.stringify(settings);
        pendingRestart = res?.pendingRestart ?? [];
      }
      if (extDirty) {
        const savedExt = await api.fircSaveExt(extConfig);
        if (Array.isArray(savedExt.availableServers) && savedExt.availableServers.length > 0) {
          availableServers = savedExt.availableServers;
        }
        keenDnsSyncError = savedExt.keenDnsCloud?.enabled ? (savedExt.keenDnsCloud?.lastSyncError ?? '') : '';
        const normalizedExt = normalizeExtFromApi(savedExt);
        extConfig = normalizedExt;
        originalExt = JSON.stringify(normalizedExt);
      }
      notifications.success('Параметры FIRC сохранены');
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка сохранения параметров FIRC');
    } finally {
      saving = false;
    }
  }

  async function handleControl(action: 'start' | 'stop' | 'restart' | 'migrate' | 'restartV1') {
    busyAction = action;
    try {
      if (action === 'start') {
        await api.fircStart();
        notifications.success('Демон fircd запущен');
      } else if (action === 'stop') {
        await api.fircStop();
        notifications.info('Демон fircd остановлен');
      } else if (action === 'restart') {
        await api.fircRestart();
        pendingRestart = [];
        notifications.success('Демон fircd перезапущен');
      } else if (action === 'restartV1') {
        await api.fircRestartDaemonV1();
        pendingRestart = [];
        notifications.success('fircd перезапущен с новыми параметрами');
      } else if (action === 'migrate') {
        openFircMigrationModal('forward');
      }
      await loadAll(true);
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка выполнения команды');
    } finally {
      busyAction = null;
    }
  }

  function isSubscriptionTunnel(tun: FircTunnel): boolean {
    return (tun.sources ?? []).some((s) => s.kind === 'subscription');
  }

  function isClientRouteGroup(grp: FircGroup): boolean {
    return (grp.devices?.allow?.length ?? 0) > 0 && grp.id.startsWith('cr');
  }

  async function loadMigratePreview(
    routerSource: 'auto' | 'tproxy' | 'fakeip' | 'both' = migrateRouterSource,
    resetSelection = false,
  ) {
    migrateLoadingPreview = true;
    try {
      const res = await api.fircMigrateFromSingbox(true, {
        mode: migrateMode,
        routerSource,
        includeTunnels: true,
        includeSubscriptions: true,
        includeGroups: true,
        includeClientRoutes: true,
      });
      migratePreview = res;
      if (resetSelection) {
        selectedMigrateTunnelIds = (res.tunnels ?? []).map((t) => t.id);
        selectedMigrateGroupIds = (res.groups ?? []).map((g) => g.id);
        migrateIncludeFakeIP = false;
      } else {
        const validGroupIds = new Set((res.groups ?? []).map((g) => g.id));
        const kept = selectedMigrateGroupIds.filter((id) => validGroupIds.has(id));
        selectedMigrateGroupIds =
          kept.length > 0 ? kept : (res.groups ?? []).map((g) => g.id);
      }
    } finally {
      migrateLoadingPreview = false;
    }
  }

  async function handleRouterSourceChange(val: 'auto' | 'tproxy' | 'fakeip' | 'both') {
    migrateRouterSource = val;
    try {
      await loadMigratePreview(val, false);
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка обновления предпросмотра миграции');
    }
  }

  function toggleMigrateTunnel(id: string) {
    if (selectedMigrateTunnelIds.includes(id)) {
      selectedMigrateTunnelIds = selectedMigrateTunnelIds.filter((x) => x !== id);
    } else {
      selectedMigrateTunnelIds = [...selectedMigrateTunnelIds, id];
    }
  }

  function selectMigrateTunnelsFilter(filter: 'all' | 'subs' | 'nodes' | 'none') {
    const all = migratePreview?.tunnels ?? [];
    if (filter === 'all') {
      selectedMigrateTunnelIds = all.map((t) => t.id);
    } else if (filter === 'subs') {
      selectedMigrateTunnelIds = all.filter(isSubscriptionTunnel).map((t) => t.id);
    } else if (filter === 'nodes') {
      selectedMigrateTunnelIds = all.filter((t) => !isSubscriptionTunnel(t)).map((t) => t.id);
    } else {
      selectedMigrateTunnelIds = [];
    }
  }

  function toggleMigrateGroup(id: string) {
    if (selectedMigrateGroupIds.includes(id)) {
      selectedMigrateGroupIds = selectedMigrateGroupIds.filter((x) => x !== id);
    } else {
      selectedMigrateGroupIds = [...selectedMigrateGroupIds, id];
    }
  }

  function selectMigrateGroupsFilter(filter: 'all' | 'rules_only' | 'devices_only' | 'none') {
    const all = migratePreview?.groups ?? [];
    if (filter === 'all') {
      selectedMigrateGroupIds = all.map((g) => g.id);
    } else if (filter === 'rules_only') {
      selectedMigrateGroupIds = all.filter((g) => !isClientRouteGroup(g)).map((g) => g.id);
    } else if (filter === 'devices_only') {
      selectedMigrateGroupIds = all.filter(isClientRouteGroup).map((g) => g.id);
    } else {
      selectedMigrateGroupIds = [];
    }
  }

  async function applyMigrationConfirm() {
    if (migrateApplying) return;
    if (
      selectedMigrateTunnelIds.length === 0 &&
      selectedMigrateGroupIds.length === 0 &&
      !migrateIncludeFakeIP
    ) {
      notifications.error('Выберите хотя бы один туннель, группу или настройку FakeIP для миграции');
      return;
    }
    migrateApplying = true;
    try {
      const options: FircMigrationOptions = {
        mode: migrateMode,
        routerSource: migrateRouterSource,
        includeTunnels: selectedMigrateTunnelIds.length > 0,
        includeSubscriptions: selectedMigrateTunnelIds.length > 0,
        includeGroups: selectedMigrateGroupIds.length > 0,
        includeClientRoutes: selectedMigrateGroupIds.length > 0,
        includeFakeIPConfig: migrateIncludeFakeIP,
        selectedTunnelIds:
          selectedMigrateTunnelIds.length > 0 ? selectedMigrateTunnelIds : ['__none__'],
        selectedGroupIds:
          selectedMigrateGroupIds.length > 0 ? selectedMigrateGroupIds : ['__none__'],
      };
      const res = await api.fircMigrateFromSingbox(false, options);
      const tCount = res.tunnels?.length ?? res.tunnelsCreated ?? 0;
      const gCount = res.groups?.length ?? res.groupsCreated ?? 0;
      const sCount = res.subscriptionsMigrated ?? 0;
      migratePreviewOpen = false;
      await Promise.all([loadAll(true), reloadFircAll()]);
      notifications.success(
        `Миграция завершена: туннелей ${tCount} (подписок ${sCount}), групп маршрутизации ${gCount}`,
      );
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка применения миграции');
    } finally {
      migrateApplying = false;
    }
  }
</script>

<div class="settings-panel">
  <!-- Top Service & Save Bar -->
  <section class="sec">
    <div class="service-row">
      <div class="service-info">
        <StatusDot variant={status?.running ? 'success' : 'muted'} size="md" />
        <div class="service-meta">
          <div class="service-title-row">
            <span class="service-title">Демон маршрутизации fircd</span>
            <Badge variant={status?.running ? 'success' : 'muted'} size="sm">
              {status?.running ? 'Активен' : 'Остановлен'}
            </Badge>
            {#if status?.version}
              <span class="mono-meta">v{status.version}</span>
            {/if}
            {#if status?.dnsProxyEndpoint}
              <Badge variant="info" size="sm" mono>DNS {status.dnsProxyEndpoint}</Badge>
            {/if}
          </div>
          <span class="service-sub">
            Управляет правилами DNS-прокси, пулом FakeIP, таблицами маршрутизации и vless-туннелями
          </span>
        </div>
      </div>

      <div class="service-actions">
        {#if status?.running}
          <Button
            variant="secondary"
            size="sm"
            loading={busyAction === 'restart'}
            onclick={() => handleControl('restart')}
          >
            Перезапустить
          </Button>
          <Button
            variant="danger"
            size="sm"
            loading={busyAction === 'stop'}
            onclick={() => handleControl('stop')}
          >
            Остановить
          </Button>
        {:else}
          <Button
            variant="primary"
            size="sm"
            loading={busyAction === 'start'}
            onclick={() => handleControl('start')}
          >
            Запустить
          </Button>
        {/if}
        <Button
          variant="secondary"
          size="sm"
          loading={busyAction === 'migrate'}
          onclick={() => openFircMigrationModal('forward')}
        >
          Мастер миграции Sing-box ⇄ FIRC
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onclick={() => openFircMigrationModal('snapshots')}
        >
          Снапшоты системы
        </Button>
      </div>
    </div>

    {#if restartRequired}
      <div class="restart-banner">
        <div class="restart-banner-text">
          <strong>Требуется перезапуск fircd:</strong> изменённые системные параметры ({pendingRestart.join(', ')}) вступят в силу после перезапуска демона.
        </div>
        <Button
          variant="primary"
          size="sm"
          loading={busyAction === 'restartV1'}
          onclick={() => handleControl('restartV1')}
        >
          Перезапустить сейчас
        </Button>
      </div>
    {/if}
  </section>

  {#if loading}
    <div class="loading-box">Загрузка параметров FIRC…</div>
  {:else}
    <div class="sections-grid">
      <section class="sec">
        <label class="sec-cap" for="firc-global">{m.firc_global_routing_mode()}</label>
        <select id="firc-global" class="inp" bind:value={extConfig.globalTarget}>
          <option value="">{m.firc_global_mode_rules()}</option>
          {#each extConfig.proxyGroups as pg (pg.id)}
            <option value={'pg:' + pg.id}>Global · {pg.name}</option>
          {/each}
          {#each interfaces.filter(isSelectableTunnelOrWan) as opt (opt.id)}
            <option value={'iface:' + opt.id}>Global · {formatIfaceLabel(opt)}</option>
          {/each}
        </select>
      </section>

      <!-- KeenDNS / Облако Keenetic через туннель / Fallback -->
      <section class="sec">
        <div class="sec-cap">Облачные сервисы Keenetic / KeenDNS</div>
        <span class="hint">
          Направляет облачный трафик роутера (KeenDNS через облако, мобильное приложение Keenetic, RMM, автообновления) через выбранную прокси-группу Fallback (с резервом на Direct WAN) или отдельный туннель.
        </span>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Маршрутизировать KeenDNS и облако Keenetic через FIRC</span>
            <span class="hint">
              Создаёт приоритетную группу «KeenDNS / Облако Keenetic» и перехватывает исходящий облачный трафик роутера
            </span>
          </div>
          <Toggle
            checked={extConfig.keenDnsCloud.enabled}
            onchange={(v) => {
              extConfig.keenDnsCloud.enabled = v;
              if (!v) keenDnsSyncError = '';
            }}
          />
        </div>
        {#if extConfig.keenDnsCloud.enabled && keenDnsSyncError}
          <div class="restart-banner" role="alert">
            <span>{m.firc_keendns_sync_error({ error: keenDnsSyncError })}</span>
          </div>
        {/if}

        <div class="field">
          <label class="lbl" for="firc-keendns-target">Целевая прокси-группа Fallback или туннель</label>
          <select
            id="firc-keendns-target"
            class="inp"
            value={keenDnsTargetSelectValue()}
            onchange={(e) => handleKeenDnsTargetChange((e.currentTarget as HTMLSelectElement).value)}
          >
            <option value="">— Выберите прокси-группу или туннель —</option>
            {#if extConfig.proxyGroups.length > 0}
              <optgroup label="Прокси-группы (Fallback)">
                {#each extConfig.proxyGroups as pg (pg.id)}
                  <option value="pg:{pg.id}">
                    {pg.name} ({pg.interfaces.map((id) => (id === 'eth3' ? 'Direct (eth3)' : id)).join(' → ')})
                  </option>
                {/each}
              </optgroup>
            {/if}
            {#if interfaces.filter(isSelectableTunnelOrWan).length > 0}
              <optgroup label="Отдельные туннели и Direct WAN">
                {#each interfaces.filter(isSelectableTunnelOrWan) as opt (opt.id)}
                  <option value="iface:{opt.id}">{formatIfaceLabel(opt)}</option>
                {/each}
              </optgroup>
            {/if}
          </select>
        </div>
        <div style="display: flex; justify-content: flex-start;">
          <Button variant="secondary" size="sm" onclick={openPgModal}>
            Настроить прокси-группы Fallback ({extConfig.proxyGroups.length})
          </Button>
        </div>
      </section>

      <!-- Маршрутизация входных VPN-серверов через FIRC -->
      <section class="sec">
        <div class="sec-cap">Маршрутизация входных VPN-серверов через FIRC</div>
        <span class="hint">
          Выберите сервисы AWGM и системные VPN-серверы Keenetic, трафик клиентов которых должен проходить через DNS-прокси (порт 3553) и группы маршрутизации FIRC. Зелёный индикатор означает, что сервис сейчас запущен, серый — остановлен.
        </span>

        <div class="srv-picker-wrap">
          <div class="srv-chips-row">
            {#if (extConfig.serverRouting.enabledServers ?? []).length === 0}
              <span class="srv-empty-text">Сервисы не выбраны</span>
            {:else}
              {#each extConfig.serverRouting.enabledServers as srvId (srvId)}
                {@const srv = getServerById(srvId)}
                <span class="srv-chip" title="{srv.name} ({srv.detail})">
                  <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
                  <span class="srv-chip-name">{srv.name}</span>
                  {#if srv.detail}
                    <span class="srv-chip-meta">{srv.detail.split('·')[0].trim()}</span>
                  {/if}
                  <button
                    type="button"
                    class="srv-chip-remove"
                    title="Убрать сервис"
                    onclick={() => toggleInboundServer(srvId)}
                  >
                    ×
                  </button>
                </span>
              {/each}
            {/if}

            <button
              type="button"
              class="srv-picker-btn"
              class:open={serverPickerOpen}
              onclick={() => (serverPickerOpen = !serverPickerOpen)}
            >
              <span>+ Выбрать сервисы ({(extConfig.serverRouting.enabledServers ?? []).length})</span>
              <span class="srv-picker-caret">{serverPickerOpen ? '▴' : '▾'}</span>
            </button>
          </div>

          {#if serverPickerOpen}
            <div class="srv-popover">
              <div class="srv-popover-head">
                <span>Доступные VPN-серверы и сервисы</span>
                <button
                  type="button"
                  class="srv-popover-close"
                  onclick={() => (serverPickerOpen = false)}
                >
                  Готово
                </button>
              </div>

              <div class="srv-popover-group">Сервисы AWGM</div>
              {#each availableServers.filter((s) => s.category === 'awgm') as srv (srv.id)}
                {@const checked = isInboundServerSelected(srv.id)}
                <button
                  type="button"
                  class="srv-popover-item"
                  class:selected={checked}
                  title="{srv.name} — {srv.detail}"
                  onclick={() => toggleInboundServer(srv.id)}
                >
                  <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
                  <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
                  <span class="srv-item-name">{srv.name}</span>
                  <span class="srv-item-detail">{srv.detail.split('·')[0].trim()}</span>
                </button>
              {/each}

              <div class="srv-popover-group">Системные VPN-серверы Keenetic</div>
              {#each availableServers.filter((s) => s.category === 'system') as srv (srv.id)}
                {@const checked = isInboundServerSelected(srv.id)}
                <button
                  type="button"
                  class="srv-popover-item"
                  class:selected={checked}
                  title="{srv.name} — {srv.detail}"
                  onclick={() => toggleInboundServer(srv.id)}
                >
                  <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
                  <StatusDot variant={srv.running ? 'success' : 'muted'} size="sm" />
                  <span class="srv-item-name">{srv.name}</span>
                  <span class="srv-item-detail">{srv.detail.split('·')[0].trim()}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      </section>

      <!-- 1. DNS-прокси -->
      <section class="sec">
        <div class="sec-cap">DNS-прокси</div>
        <span class="hint">
          Встроенный DNS-прокси перехватывает DNS-запросы клиентов в локальной сети, сопоставляет домены со списками правил и подменяет ответы адресами из пула FakeIP.
        </span>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-dns-up-addr">Вышестоящий DNS-сервер</label>
            <input
              id="firc-dns-up-addr"
              class="inp mono"
              type="text"
              placeholder="127.0.0.1"
              value={strVal('app.dnsProxy.upstream.address', '127.0.0.1')}
              oninput={(e) => setField('app.dnsProxy.upstream.address', (e.currentTarget as HTMLInputElement).value)}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-dns-up-port">Порт вышестоящего DNS</label>
            <input
              id="firc-dns-up-port"
              class="inp mono"
              type="number"
              min="1"
              max="65535"
              value={numVal('app.dnsProxy.upstream.port', 53)}
              oninput={(e) => setField('app.dnsProxy.upstream.port', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
        <span class="hint">Системный DNS роутера (обычно 127.0.0.1:53 или порт локального резолвера).</span>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Отбрасывать записи AAAA (IPv6)</span>
            <span class="hint">Блокирует ответы с IPv6-адресами, чтобы клиенты всегда маршрутизировались по IPv4</span>
          </div>
          <Toggle
            checked={invertedBool('app.dnsProxy.disableDropAAAA')}
            onchange={(v) => setInvertedBool('app.dnsProxy.disableDropAAAA', v)}
          />
        </div>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Перехват порта 53 (NAT REDIRECT)</span>
            <span class="hint">Автоматически перенаправляет все исходящие DNS-запросы клиентов на встроенный прокси</span>
          </div>
          <Toggle
            checked={invertedBool('app.dnsProxy.disableRemap53')}
            onchange={(v) => setInvertedBool('app.dnsProxy.disableRemap53', v)}
          />
        </div>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Сбор статистики DNS</span>
            <span class="hint">Ведёт журнал разрешённых доменов и совпавших групп для вкладки «Журнал»</span>
          </div>
          <Toggle
            checked={invertedBool('app.dnsProxy.disableStats')}
            onchange={(v) => setInvertedBool('app.dnsProxy.disableStats', v)}
          />
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-dns-host-addr">Адрес прослушивания DNS-прокси</label>
            <input
              id="firc-dns-host-addr"
              class="inp mono"
              type="text"
              placeholder="0.0.0.0"
              value={strVal('app.dnsProxy.host.address', '0.0.0.0')}
              oninput={(e) => setField('app.dnsProxy.host.address', (e.currentTarget as HTMLInputElement).value)}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-dns-host-port">Внутренний порт DNS-прокси</label>
            <input
              id="firc-dns-host-port"
              class="inp mono"
              type="number"
              min="1"
              max="65535"
              value={numVal('app.dnsProxy.host.port', 3553)}
              oninput={(e) => setField('app.dnsProxy.host.port', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-dns-ttl">TTL ответов вне правил (с)</label>
            <input
              id="firc-dns-ttl"
              class="inp mono"
              type="number"
              min="0"
              max="86400"
              value={numVal('app.dnsProxy.unmatchedTtl', 60)}
              oninput={(e) => setField('app.dnsProxy.unmatchedTtl', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-dns-timeout">Таймаут DNS-запроса (мс)</label>
            <input
              id="firc-dns-timeout"
              class="inp mono"
              type="number"
              min="100"
              max="30000"
              value={numVal('app.dnsProxy.timeout', 5000)}
              oninput={(e) => setField('app.dnsProxy.timeout', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-dns-conc">Макс. одновременных запросов</label>
            <input
              id="firc-dns-conc"
              class="inp mono"
              type="number"
              min="1"
              max="4096"
              value={numVal('app.dnsProxy.maxConcurrent', 256)}
              oninput={(e) => setField('app.dnsProxy.maxConcurrent', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-dns-idle">Простаивающих соединений</label>
            <input
              id="firc-dns-idle"
              class="inp mono"
              type="number"
              min="0"
              max="512"
              value={numVal('app.dnsProxy.maxIdleConns', 32)}
              oninput={(e) => setField('app.dnsProxy.maxIdleConns', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
      </section>

      <!-- 2. Пул адресов (FakeIP) -->
      <section class="sec">
        <div class="sec-cap">Пул адресов (FakeIP)</div>
        <span class="hint">
          Каждому домену, попавшему под правило группы, выделяется виртуальный IP-адрес из пула, а трафик прозрачно перенаправляется в нужный интерфейс или туннель.
        </span>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-pool-ttl">Потолок TTL для правил (с)</label>
            <input
              id="firc-pool-ttl"
              class="inp mono"
              type="number"
              min="1"
              max="3600"
              value={numVal('app.addressPool.ttlClamp', 60)}
              oninput={(e) => setField('app.addressPool.ttlClamp', Number((e.currentTarget as HTMLInputElement).value))}
            />
            <span class="hint">Ограничивает время кеширования FakeIP на устройствах</span>
          </div>
          <div class="field">
            <label class="lbl" for="firc-pool-max">Максимум доменов в пуле</label>
            <input
              id="firc-pool-max"
              class="inp mono"
              type="number"
              min="256"
              max="262144"
              value={numVal('app.addressPool.maxNames', 16384)}
              oninput={(e) => setField('app.addressPool.maxNames', Number((e.currentTarget as HTMLInputElement).value))}
            />
            <span class="hint">Лимит одновременно отслеживаемых доменных имён</span>
          </div>
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-pool-v4">Пул IPv4 (CIDR)</label>
            <input
              id="firc-pool-v4"
              class="inp mono"
              type="text"
              placeholder="10.255.0.0/16"
              value={strVal('app.addressPool.v4.pool', '10.255.0.0/16')}
              oninput={(e) => setField('app.addressPool.v4.pool', (e.currentTarget as HTMLInputElement).value)}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-pool-v4-chunk">Маска подсети чанка IPv4 (/)</label>
            <input
              id="firc-pool-v4-chunk"
              class="inp mono"
              type="number"
              min="8"
              max="32"
              value={numVal('app.addressPool.v4.chunk', 24)}
              oninput={(e) => setField('app.addressPool.v4.chunk', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
        <span class="hint">Диапазон адресов не должен пересекаться с вашей локальной сетью или подсетями VPN.</span>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-pool-v6">Пул IPv6 (CIDR)</label>
            <input
              id="firc-pool-v6"
              class="inp mono"
              type="text"
              placeholder="авто"
              value={strVal('app.addressPool.v6.pool', '')}
              oninput={(e) => setField('app.addressPool.v6.pool', (e.currentTarget as HTMLInputElement).value)}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-pool-v6-chunk">Маска подсети чанка IPv6 (/)</label>
            <input
              id="firc-pool-v6-chunk"
              class="inp mono"
              type="number"
              min="32"
              max="128"
              value={numVal('app.addressPool.v6.chunk', 112)}
              oninput={(e) => setField('app.addressPool.v6.chunk', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
      </section>

      <!-- 3. Netfilter и маршрутизация -->
      <section class="sec">
        <div class="sec-cap">Netfilter и сетевой стек</div>
        <span class="hint">
          Управление правилами iptables, ipset, таблицами маршрутизации и LAN-интерфейсами, на которых перехватывается трафик.
        </span>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Правила маршрутизации IPv4</span>
            <span class="hint">Создавать цепочки iptables и правила policy routing для трафика IPv4</span>
          </div>
          <Toggle
            checked={invertedBool('app.netfilter.disableIPv4')}
            onchange={(v) => setInvertedBool('app.netfilter.disableIPv4', v)}
          />
        </div>

        <div class="field-row">
          <div class="field-row-text">
            <span class="lbl">Правила маршрутизации IPv6</span>
            <span class="hint">Создавать цепочки ip6tables и правила policy routing для трафика IPv6</span>
          </div>
          <Toggle
            checked={invertedBool('app.netfilter.disableIPv6')}
            onchange={(v) => setInvertedBool('app.netfilter.disableIPv6', v)}
          />
        </div>

        <div class="field">
          <label class="lbl" for="firc-lan-links">Интерфейсы локальной сети (через запятую)</label>
          <input
            id="firc-lan-links"
            class="inp mono"
            type="text"
            placeholder="br0"
            value={lanLinksText}
            oninput={(e) => updateLanLinks((e.currentTarget as HTMLInputElement).value)}
          />
          <span class="hint">На Keenetic основной бридж домашней сети — br0 (при необходимости добавьте br1 для гостевой сети).</span>
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-nf-prefix">Префикс цепочек iptables</label>
            <input
              id="firc-nf-prefix"
              class="inp mono"
              type="text"
              placeholder="FIRC_"
              value={strVal('app.netfilter.iptables.chainPrefix', 'FIRC_')}
              oninput={(e) => setField('app.netfilter.iptables.chainPrefix', (e.currentTarget as HTMLInputElement).value)}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-nf-mark">Начальный индекс fwmark / таблицы</label>
            <input
              id="firc-nf-mark"
              class="inp mono"
              type="number"
              min="100"
              max="60000"
              value={numVal('app.netfilter.startMarkTableIndex', 2100)}
              oninput={(e) => setField('app.netfilter.startMarkTableIndex', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
      </section>

      <!-- 4. Проверка интерфейсов -->
      <section class="sec">
        <div class="sec-cap">Проверка интерфейсов (Healthcheck)</div>
        <span class="hint">
          Периодический контроль связности туннелей и WAN-интерфейса для отображения статуса и автоматического переключения.
        </span>

        <div class="field">
          <label class="lbl" for="firc-chk-url">URL проверки туннелей</label>
          <input
            id="firc-chk-url"
            class="inp mono"
            type="text"
            placeholder="http://cp.cloudflare.com/generate_204"
            value={strVal('app.check.url', 'http://cp.cloudflare.com/generate_204')}
            oninput={(e) => setField('app.check.url', (e.currentTarget as HTMLInputElement).value)}
          />
          <span class="hint">Например: http://cp.cloudflare.com/generate_204 или http://connectivitycheck.gstatic.com/generate_204</span>
        </div>

        <div class="field">
          <label class="lbl" for="firc-chk-wan">URL проверки провайдера (WAN)</label>
          <input
            id="firc-chk-wan"
            class="inp mono"
            type="text"
            placeholder="http://api.browser.yandex.ru/generate_204"
            value={strVal('app.check.wanUrl', 'http://api.browser.yandex.ru/generate_204')}
            oninput={(e) => setField('app.check.wanUrl', (e.currentTarget as HTMLInputElement).value)}
          />
          <span class="hint">Рекомендуется российский ресурс (например, http://api.browser.yandex.ru/generate_204)</span>
        </div>

        <div class="field-grid-2">
          <div class="field">
            <label class="lbl" for="firc-chk-int">Интервал проверки (с)</label>
            <input
              id="firc-chk-int"
              class="inp mono"
              type="number"
              min="10"
              max="3600"
              value={numVal('app.check.interval', 60)}
              oninput={(e) => setField('app.check.interval', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
          <div class="field">
            <label class="lbl" for="firc-chk-to">Таймаут ответа (с)</label>
            <input
              id="firc-chk-to"
              class="inp mono"
              type="number"
              min="1"
              max="30"
              value={numVal('app.check.timeout', 5)}
              oninput={(e) => setField('app.check.timeout', Number((e.currentTarget as HTMLInputElement).value))}
            />
          </div>
        </div>
      </section>
    </div>

    <!-- Bottom Save Footer -->
    <div class="save-bar" class:is-dirty={isDirty}>
      <div class="save-bar-status">
        {#if isDirty}
          <span class="dirty-dot"></span>
          <span>Есть несохранённые изменения в параметрах</span>
        {:else}
          <span class="clean-text">Все параметры синхронизированы с fircd</span>
        {/if}
      </div>
      <div class="save-bar-actions">
        <Button variant="secondary" size="sm" disabled={!isDirty || saving} onclick={resetChanges}>
          Сбросить
        </Button>
        <Button variant="primary" size="sm" loading={saving} disabled={!isDirty} onclick={saveAll}>
          Сохранить параметры
        </Button>
      </div>
    </div>
  {/if}
</div>

<Modal
  open={pgModalOpen}
  onclose={() => (pgModalOpen = false)}
  title="Прокси-группы (Fallback)"
  width="540px"
>
  <div class="pg-modal-body">
    <p class="hint">
      Соберите цепочку из 1–4 интерфейсов (туннели VLESS / AmneziaWG и резервный <strong>Direct WAN</strong>).
      После нажатия «Применить» не забудьте нажать «Сохранить параметры».
    </p>
    {#each pgDraft as pg, pgIdx (pg.id)}
      <div class="pg-box">
        <div class="pg-box-head">
          <input
            type="text"
            class="inp"
            placeholder="Название прокси-группы"
            bind:value={pg.name}
          />
          <Button variant="danger" size="sm" onclick={() => removePgDraft(pgIdx)}>Удалить</Button>
        </div>
        <div class="hint">
          Выбрано ({pg.interfaces.length}/4):
          <strong>
            {pg.interfaces.length > 0
              ? pg.interfaces.map((id) => (id === 'eth3' ? 'Direct (eth3)' : id)).join(' → ')
              : 'ничего не выбрано'}
          </strong>
        </div>
        <div class="pg-cand-wrap">
          {#each interfaces.filter(isSelectableTunnelOrWan) as opt (opt.id)}
            {@const rank = pg.interfaces.indexOf(opt.id)}
            <button
              type="button"
              class="pg-cand-chip"
              class:active={rank >= 0}
              onclick={() => togglePgDraftIface(pgIdx, opt.id)}
            >
              {#if rank >= 0}
                <span class="pg-rank">#{rank + 1}</span>
              {/if}
              <span>{formatIfaceLabel(opt)}</span>
            </button>
          {/each}
        </div>
      </div>
    {/each}
    <div>
      <Button variant="secondary" size="sm" onclick={addPgDraft}>+ Добавить прокси-группу</Button>
    </div>
  </div>
  {#snippet actions()}
    <div style="display:flex; justify-content:flex-end; gap:8px; width:100%;">
      <Button variant="ghost" size="sm" onclick={() => (pgModalOpen = false)}>Отмена</Button>
      <Button variant="primary" size="sm" onclick={applyPgModal}>Применить</Button>
    </div>
  {/snippet}
</Modal>

<Modal
  open={migratePreviewOpen}
  onclose={() => (migratePreviewOpen = false)}
  title="Мастер выборочной миграции из Sing-box в FIRC"
  width="680px"
>
  {#if migratePreview}
    <div class="pg-modal-body">
      <p class="hint">
        Выберите режим импорта, источник правил роутера (TProxy <code>20-router.json</code> и/или FakeIP-TUN <code>21-fakeip.json</code>) и отметьте туннели, подписки и группы маршрутизации для переноса в FIRC.
      </p>

      <!-- Mode & Router Source selectors -->
      <div class="field-grid-2">
        <div class="field">
          <label class="lbl" for="firc-mig-mode">Режим миграции</label>
          <select
            id="firc-mig-mode"
            class="inp"
            value={migrateMode}
            onchange={(e) => (migrateMode = (e.currentTarget as HTMLSelectElement).value as 'merge' | 'replace')}
          >
            <option value="merge">Добавить / обновить в текущих (Merge)</option>
            <option value="replace">Заменить текущие выбранными (Replace)</option>
          </select>
          <span class="hint">
            {#if migrateMode === 'merge'}
              Сохраняет текущие туннели и группы FIRC, добавляя или обновляя отмеченные ниже элементы.
            {:else}
              Полностью заменяет список туннелей/групп FIRC отмеченными элементами.
            {/if}
          </span>
        </div>

        <div class="field">
          <label class="lbl" for="firc-mig-source">Источник правил роутера Sing-box</label>
          <select
            id="firc-mig-source"
            class="inp"
            value={migrateRouterSource}
            disabled={migrateLoadingPreview}
            onchange={(e) =>
              handleRouterSourceChange(
                (e.currentTarget as HTMLSelectElement).value as 'auto' | 'tproxy' | 'fakeip' | 'both',
              )}
          >
            <option value="auto">Авто (TProxy 20-router + FakeIP 21-fakeip)</option>
            <option value="tproxy">Только TProxy (20-router.json)</option>
            <option value="fakeip">Только FakeIP-TUN (21-fakeip.json)</option>
            <option value="both">Оба слота (20-router + 21-fakeip)</option>
          </select>
          <span class="hint">
            {#if (migratePreview.routerSourcesUsed?.length ?? 0) > 0}
              Найдены слоты: <code>{migratePreview.routerSourcesUsed?.join(', ')}</code>
            {:else}
              Слоты роутера в <code>config.d</code> не обнаружены
            {/if}
          </span>
        </div>
      </div>

      <!-- FakeIP Configuration Card -->
      {#if migratePreview.fakeip?.detected}
        <div class="pg-box mig-fakeip-box">
          <div class="mig-sec-head">
            <div class="lbl">Обнаружена конфигурация Sing-box FakeIP-TUN</div>
            {#if migratePreview.fakeip.slotFile}
              <Badge variant="info" size="sm" mono>{migratePreview.fakeip.slotFile}</Badge>
            {/if}
          </div>
          <div class="hint">
            Пул IPv4: <code>{migratePreview.fakeip.inet4Range || '198.18.0.0/15'}</code>
            {#if migratePreview.fakeip.inet6Range}
              · IPv6: <code>{migratePreview.fakeip.inet6Range}</code>
            {/if}
            {#if migratePreview.fakeip.realDns}
              · Реальный DNS: <code>{migratePreview.fakeip.realDns}</code>
            {/if}
            {#if migratePreview.fakeip.finalOutbound}
              · Outbound по умолчанию: <code>{migratePreview.fakeip.finalOutbound}</code>
            {/if}
            {#if (migratePreview.fakeip.dnsRulesCount ?? 0) > 0}
              · DNS-правил FakeIP: <strong>{migratePreview.fakeip.dnsRulesCount}</strong>
            {/if}
          </div>
          <label class="mig-check-row">
            <input type="checkbox" bind:checked={migrateIncludeFakeIP} />
            <span>
              Перенести диапазон пула FakeIP (<code>{migratePreview.fakeip.inet4Range || '198.18.0.0/15'}</code>)
              {#if migratePreview.fakeip.realDns}
                и апстрим DNS (<code>{migratePreview.fakeip.realDns}</code>)
              {/if}
              в настройки демона FIRC
            </span>
          </label>
        </div>
      {/if}

      <!-- Tunnels & Subscriptions Selection -->
      <div class="pg-box">
        <div class="mig-sec-head">
          <div class="lbl">
            Туннели и подписки ({selectedMigrateTunnelIds.length} из {migratePreview.tunnels?.length ?? 0})
          </div>
          <div class="mig-filter-btns">
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateTunnelsFilter('all')}>
              Все
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateTunnelsFilter('subs')}>
              Подписки
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateTunnelsFilter('nodes')}>
              Узлы
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateTunnelsFilter('none')}>
              Снять
            </button>
          </div>
        </div>

        {#if (migratePreview.tunnels?.length ?? 0) === 0}
          <div class="hint">Туннели и подписки для миграции не найдены.</div>
        {:else}
          <div class="mig-list">
            {#each migratePreview.tunnels ?? [] as tun (tun.id)}
              {@const checked = selectedMigrateTunnelIds.includes(tun.id)}
              {@const isSub = isSubscriptionTunnel(tun)}
              <button
                type="button"
                class="mig-item"
                class:selected={checked}
                onclick={() => toggleMigrateTunnel(tun.id)}
              >
                <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
                <span class="mig-item-main">
                  <strong>{tun.description || tun.id}</strong>
                  <span class="mig-item-sub mono">{tun.device} · id:{tun.id}</span>
                </span>
                <span class="mig-item-badges">
                  <Badge variant={isSub ? 'info' : 'muted'} size="sm">
                    {isSub ? 'Подписка' : 'Прокси-узел'}
                  </Badge>
                  <span class="srv-item-detail">ист.: {tun.sources?.length ?? 0}</span>
                </span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Routing Groups & Client Routes Selection -->
      <div class="pg-box">
        <div class="mig-sec-head">
          <div class="lbl">
            Группы маршрутизации и клиентские маршруты ({selectedMigrateGroupIds.length} из {migratePreview.groups?.length ?? 0})
          </div>
          <div class="mig-filter-btns">
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateGroupsFilter('all')}>
              Все
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateGroupsFilter('rules_only')}>
              Только группы
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateGroupsFilter('devices_only')}>
              Только устройства
            </button>
            <button type="button" class="mig-filter-btn" onclick={() => selectMigrateGroupsFilter('none')}>
              Снять
            </button>
          </div>
        </div>

        {#if (migratePreview.groups?.length ?? 0) === 0}
          <div class="hint">Группы маршрутизации для выбранного источника не найдены.</div>
        {:else}
          <div class="mig-list">
            {#each migratePreview.groups ?? [] as grp (grp.id)}
              {@const checked = selectedMigrateGroupIds.includes(grp.id)}
              {@const isDev = isClientRouteGroup(grp)}
              <button
                type="button"
                class="mig-item"
                class:selected={checked}
                onclick={() => toggleMigrateGroup(grp.id)}
              >
                <span class="srv-check" class:checked>{checked ? '✓' : ''}</span>
                <span class="mig-item-main">
                  <strong>{grp.name}</strong>
                  <span class="mig-item-sub mono">
                    → {Array.isArray(grp.interface) ? grp.interface.join(' → ') : grp.interface}
                    {#if (grp.devices?.allow?.length ?? 0) > 0}
                      · клиенты: {grp.devices?.allow.join(', ')}
                    {/if}
                  </span>
                </span>
                <span class="mig-item-badges">
                  {#if isDev}
                    <Badge variant="warning" size="sm">Устройство</Badge>
                  {/if}
                  {#if grp.list?.url}
                    <Badge variant="info" size="sm">.srs список</Badge>
                  {/if}
                  <span class="srv-item-detail">правил: {grp.rules?.length ?? 0}</span>
                </span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      {#if (migratePreview.warnings?.length ?? 0) > 0}
        <div class="pg-box">
          <div class="lbl">Примечания ({migratePreview.warnings?.length}):</div>
          {#each migratePreview.warnings ?? [] as w}
            <div class="hint">• {w}</div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
  {#snippet actions()}
    <div style="display:flex; justify-content:space-between; align-items:center; gap:8px; width:100%;">
      <span class="hint">
        К переносу: туннелей <strong>{selectedMigrateTunnelIds.length}</strong>, групп <strong>{selectedMigrateGroupIds.length}</strong>{migrateIncludeFakeIP ? ' + настройки FakeIP' : ''}
      </span>
      <div style="display:flex; gap:8px;">
        <Button variant="ghost" size="sm" onclick={() => (migratePreviewOpen = false)}>Отмена</Button>
        <Button
          variant="primary"
          size="sm"
          loading={migrateApplying || migrateLoadingPreview}
          onclick={applyMigrationConfirm}
        >
          Применить выбранное
        </Button>
      </div>
    </div>
  {/snippet}
</Modal>

<style>
  .settings-panel {
    display: flex;
    flex-direction: column;
    gap: 14px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .sections-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
    align-items: start;
    width: 100%;
    max-width: 100%;
    min-width: 0;
  }

  .sec {
    padding: 14px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    overflow: hidden;
  }

  .sec-cap {
    font-size: 11px;
    font-weight: 700;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .pg-modal-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .pg-box {
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .pg-box-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .pg-cand-wrap {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .pg-cand-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 5px 10px;
    border-radius: 6px;
    font-size: 12px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .pg-cand-chip.active {
    background: color-mix(in srgb, var(--color-accent) 14%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--color-accent) 45%, var(--border));
    color: var(--text-primary);
    font-weight: 500;
  }

  .pg-rank {
    font-size: 10px;
    font-weight: 700;
    padding: 1px 5px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--color-accent) 22%, transparent);
    color: var(--color-accent);
  }

  .srv-picker-wrap {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-chips-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    padding: 8px 10px;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-empty-text {
    font-size: 12px;
    color: var(--text-tertiary);
    padding: 2px 4px;
  }

  .srv-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 8px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--color-accent) 12%, var(--bg-secondary));
    border: 1px solid color-mix(in srgb, var(--color-accent) 32%, var(--border));
    font-size: 12px;
    color: var(--text-primary);
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-chip-name {
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  .srv-chip-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--text-secondary);
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    flex-shrink: 0;
  }

  .srv-chip-remove {
    border: none;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
    font-size: 13px;
    line-height: 1;
    padding: 0 2px;
    flex-shrink: 0;
  }

  .srv-chip-remove:hover {
    color: var(--color-danger);
  }

  .srv-picker-btn {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 10px;
    border-radius: 6px;
    border: 1px dashed var(--border-hover, var(--border));
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    max-width: 100%;
    box-sizing: border-box;
    transition: border-color 0.15s, background 0.15s;
  }

  .srv-picker-btn:hover,
  .srv-picker-btn.open {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 10%, var(--bg-secondary));
  }

  .srv-picker-caret {
    font-size: 10px;
    color: var(--text-secondary);
  }

  .srv-popover {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow);
    padding: 6px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 320px;
    overflow-y: auto;
    overflow-x: hidden;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .srv-popover-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 4px 6px 6px;
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    font-weight: 600;
    color: var(--text-secondary);
    min-width: 0;
  }

  .srv-popover-close {
    border: none;
    background: color-mix(in srgb, var(--color-accent) 14%, transparent);
    color: var(--color-accent);
    border-radius: 5px;
    padding: 3px 8px;
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
    flex-shrink: 0;
  }

  .srv-popover-group {
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    padding: 7px 6px 2px;
  }

  .srv-popover-item {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    text-align: left;
    padding: 5px 7px;
    border-radius: 6px;
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-primary);
    font-size: 12px;
    cursor: pointer;
    transition: background 0.12s;
  }

  .srv-popover-item:hover {
    background: var(--bg-tertiary);
  }

  .srv-popover-item.selected {
    background: var(--color-accent-tint, color-mix(in srgb, var(--color-accent) 10%, var(--bg-tertiary)));
    border-color: var(--color-accent-border, color-mix(in srgb, var(--color-accent) 28%, var(--border)));
  }

  .srv-check {
    width: 15px;
    height: 15px;
    border-radius: 4px;
    border: 1px solid var(--border-hover, var(--border));
    background: var(--bg-tertiary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 10px;
    font-weight: 700;
    color: transparent;
    flex-shrink: 0;
  }

  .srv-check.checked {
    background: color-mix(in srgb, var(--color-accent) 18%, var(--bg-tertiary));
    border-color: var(--color-accent);
    color: var(--color-accent);
  }

  .srv-item-name {
    font-weight: 500;
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .srv-item-detail {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--text-muted);
    background: var(--bg-tertiary);
    padding: 1px 5px;
    border-radius: 4px;
    flex-shrink: 0;
    max-width: 92px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .service-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }

  .service-info {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .service-meta {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }

  .service-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .service-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary);
  }

  .service-sub {
    font-size: 12px;
    color: var(--text-secondary);
  }

  .mono-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-secondary);
  }

  .service-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .restart-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border-radius: 8px;
    background: var(--color-warning-tint);
    border: 1px solid var(--color-warning-border);
    color: var(--text-primary);
    font-size: 12px;
    flex-wrap: wrap;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
    max-width: 100%;
  }

  .field-grid-2 {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 10px;
    min-width: 0;
    max-width: 100%;
  }

  @media (max-width: 560px) {
    .field-grid-2 {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid rgba(255, 255, 255, 0.04);
    min-width: 0;
    max-width: 100%;
  }

  .field-row-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    flex: 1 1 auto;
  }

  .lbl {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
  }

  .inp {
    padding: 7px 10px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-primary);
    font-size: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    transition: border-color 0.15s;
  }

  .inp:focus {
    outline: none;
    border-color: var(--accent);
  }

  .mono {
    font-family: var(--font-mono);
  }

  .hint {
    font-size: 11px;
    color: var(--text-secondary);
    line-height: 1.4;
  }

  .loading-box {
    padding: 32px;
    text-align: center;
    color: var(--text-secondary);
    font-size: 13px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
  }

  .save-bar {
    position: sticky;
    bottom: -16px;
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 16px;
    margin-bottom: -4px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
    box-shadow: 0 -4px 16px rgba(0, 0, 0, 0.25);
    flex-wrap: wrap;
    transition: border-color 0.15s ease, background 0.15s ease;
  }

  .save-bar.is-dirty {
    border-color: color-mix(in srgb, var(--color-warning) 50%, var(--border));
    background: color-mix(in srgb, var(--color-warning) 10%, var(--bg-secondary));
  }

  .save-bar-status {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-primary);
  }

  .dirty-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-warning);
  }

  .clean-text {
    color: var(--text-secondary);
  }

  .save-bar-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .mig-sec-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    flex-wrap: wrap;
  }

  .mig-filter-btns {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
  }

  .mig-filter-btn {
    padding: 2px 8px;
    border-radius: 5px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    font-size: 11px;
    cursor: pointer;
    transition: border-color 0.12s, color 0.12s;
  }

  .mig-filter-btn:hover {
    border-color: var(--color-accent);
    color: var(--text-primary);
  }

  .mig-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-height: 190px;
    overflow-y: auto;
    padding-right: 2px;
  }

  .mig-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    padding: 6px 8px;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-primary);
    font-size: 12px;
    cursor: pointer;
    box-sizing: border-box;
    transition: border-color 0.12s, background 0.12s;
  }

  .mig-item:hover {
    border-color: var(--border-hover, var(--border));
  }

  .mig-item.selected {
    background: color-mix(in srgb, var(--color-accent) 10%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--color-accent) 32%, var(--border));
  }

  .mig-item-main {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
  }

  .mig-item-main strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mig-item-sub {
    font-size: 10.5px;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mig-item-badges {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex-shrink: 0;
  }

  .mig-fakeip-box {
    border-color: color-mix(in srgb, var(--color-info, #3b82f6) 40%, var(--border));
    background: color-mix(in srgb, var(--color-info, #3b82f6) 6%, var(--bg-secondary));
  }

  .mig-check-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-primary);
    cursor: pointer;
    padding-top: 2px;
  }
</style>
