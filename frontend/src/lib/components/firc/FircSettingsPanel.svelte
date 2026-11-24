<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/api/client';
  import { onResourceInvalidated } from '$lib/stores/storeRegistry';
  import { m } from '$lib/i18n';
  import type {
    FircStatus,
    FircSettingValue,
    FircExtConfig,
    FircInterfaceOption,
    FircProxyGroup,
    FircInboundServerItem,
  } from '$lib/types/firc';
  import { fircInterfaceProbeResults, fircProbeResults, fircTunnelsState } from '$lib/stores/firc';
  import { friendlyIfaceLabel } from '$lib/utils/fircFallback';
  import {
    normalizeProxyGroupForSave,
    proxyGroupsDraft,
    proxyGroupsDraftError,
    type FircGroupsRuntime,
  } from '$lib/utils/fircGroups';
  import { openFircMigrationModal } from '$lib/stores/fircMigrationModal';
  import { Button } from '$lib/components/ui';
  import { notifications } from '$lib/stores/notifications';
  import FircSettingsServiceBar from './FircSettingsServiceBar.svelte';
  import FircSettingsKeenDnsSection from './FircSettingsKeenDnsSection.svelte';
  import FircSettingsInboundServers from './FircSettingsInboundServers.svelte';
  import FircSettingsDaemonSections from './FircSettingsDaemonSections.svelte';
  import FircProxyGroupsModal from './FircProxyGroupsModal.svelte';
  import { createFircProbes } from './fircProbes.svelte';

  const defaultExt: FircExtConfig = {
    proxyGroups: [],
    keenDnsCloud: { enabled: false, cloudHealthCheck: false, proxyGroupId: '', interfaces: [] },
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
  // null — каталог от бэкенда ещё не пришёл, FircSettingsInboundServers
  // показывает встроенный.
  let availableServers = $state<FircInboundServerItem[] | null>(null);
  let interfaces = $state<FircInterfaceOption[]>([]);
  let pgModalOpen = $state(false);
  let pgDraft = $state<FircProxyGroup[]>([]);
  const probes = createFircProbes((id) => friendlyIfaceLabel(id, interfaces, $fircTunnelsState));
  // Групп маршрутизации и их живого состояния здесь нет: активный интерфейс
  // цепочки определяется только по замерам, как и раньше в этой панели.
  let pgRt = $derived<FircGroupsRuntime>({
    groups: [],
    liveMap: {},
    tunnelsState: $fircTunnelsState,
    interfaces,
    resolvers: [],
    proxyGroups: extConfig.proxyGroups,
    ifaceProbes: $fircInterfaceProbeResults,
    tunnelProbes: $fircProbeResults,
  });
  let destroyed = false;
  let pendingRestart = $state<string[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let busyAction = $state<string | null>(null);
  let unsubFirc: (() => void) | null = null;

  interface Props {
    hasUnsaved?: boolean;
  }

  let { hasUnsaved = $bindable(false) }: Props = $props();

  const settingsDirty = $derived(JSON.stringify(settings) !== originalSettings);
  const extDirty = $derived(JSON.stringify(extConfig) !== originalExt);
  const isDirty = $derived(settingsDirty || extDirty);

  $effect(() => {
    hasUnsaved = isDirty;
  });

  function setField(key: string, val: FircSettingValue) {
    settings = { ...settings, [key]: val };
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
        cloudHealthCheck: Boolean(raw.keenDnsCloud?.cloudHealthCheck),
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
        if (Array.isArray(extRes.availableServers)) {
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
      if (!silent) notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_settings_load_error());
    } finally {
      if (!silent) loading = false;
    }
  }

  async function openPgModal() {
    // Без списка групп черновик не выводит проверку: чужие настройки не затираются.
    const groups = await api.fircGetGroups().catch(() => []);
    if (destroyed) return;
    pgDraft = proxyGroupsDraft(extConfig.proxyGroups, groups);
    pgModalOpen = true;
  }

  function applyPgModal() {
    const draftErr = proxyGroupsDraftError(pgDraft, extConfig.proxyGroups);
    if (draftErr) {
      notifications.error(draftErr);
      return;
    }
    extConfig.proxyGroups = pgDraft.map(normalizeProxyGroupForSave);
    if (!extConfig.keenDnsCloud.proxyGroupId && extConfig.proxyGroups.length > 0) {
      extConfig.keenDnsCloud.proxyGroupId = extConfig.proxyGroups[0].id;
      extConfig.keenDnsCloud.interfaces = [...extConfig.proxyGroups[0].interfaces];
    }
    pgModalOpen = false;
  }

  onMount(() => {
    loadAll();
    if ($fircTunnelsState.length === 0) {
      void api
        .fircGetTunnelsState()
        .then((st) => {
          if (!destroyed && st) fircTunnelsState.set(st);
        })
        .catch(() => {});
    }
    // Статус, настройки демона и ext перечитываем по подсказке firc
    // (мутации FIRC и смена живости демона, замеченная сторожем) — без
    // 4-секундного таймера. Несохранённые правки loadAll не затирает.
    unsubFirc = onResourceInvalidated('firc', () => void loadAll(true));
  });

  onDestroy(() => {
    destroyed = true;
    probes.destroy();
    unsubFirc?.();
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
        notifications.error(m.firc_settings_keendns_target_required());
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
      notifications.success(m.firc_settings_saved());
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_settings_save_error());
    } finally {
      saving = false;
    }
  }

  async function handleControl(action: 'start' | 'stop' | 'restart' | 'migrate' | 'restartV1') {
    busyAction = action;
    try {
      if (action === 'start') {
        await api.fircStart();
        notifications.success(m.firc_settings_daemon_started());
      } else if (action === 'stop') {
        await api.fircStop();
        notifications.info(m.firc_settings_daemon_stopped());
      } else if (action === 'restart') {
        await api.fircRestart();
        pendingRestart = [];
        notifications.success(m.firc_settings_daemon_restarted());
      } else if (action === 'restartV1') {
        await api.fircRestartDaemonV1();
        pendingRestart = [];
        notifications.success(m.firc_settings_daemon_restarted_params());
      } else if (action === 'migrate') {
        openFircMigrationModal('forward');
      }
      await loadAll(true);
    } catch (e: unknown) {
      notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_settings_control_error());
    } finally {
      busyAction = null;
    }
  }
</script>

<div class="settings-panel">
  <!-- Top Service & Save Bar -->
  <FircSettingsServiceBar {status} {busyAction} {pendingRestart} oncontrol={handleControl} />

  {#if loading}
    <div class="loading-box">{m.firc_settings_loading()}</div>
  {:else}
    <div class="sections-grid">
      <FircSettingsKeenDnsSection
        bind:extConfig
        bind:keenDnsSyncError
        {interfaces}
        onopenproxygroups={openPgModal}
      />

      <FircSettingsInboundServers
        {availableServers}
        enabledServers={extConfig.serverRouting.enabledServers ?? []}
        ontoggle={toggleInboundServer}
      />

      <FircSettingsDaemonSections {settings} onfield={setField} />
    </div>

    <!-- Bottom Save Footer -->
    <div class="save-bar" class:is-dirty={isDirty}>
      <div class="save-bar-status">
        {#if isDirty}
          <span class="dirty-dot"></span>
          <span>{m.firc_settings_dirty()}</span>
        {:else}
          <span class="clean-text">{m.firc_settings_clean()}</span>
        {/if}
      </div>
      <div class="save-bar-actions">
        <Button variant="secondary" size="sm" disabled={!isDirty || saving} onclick={resetChanges}>
          {m.firc_settings_reset()}
        </Button>
        <Button variant="primary" size="sm" loading={saving} disabled={!isDirty} onclick={saveAll}>
          {m.firc_settings_save()}
        </Button>
      </div>
    </div>
  {/if}
</div>

<FircProxyGroupsModal
  open={pgModalOpen}
  bind:draft={pgDraft}
  rt={pgRt}
  probingIfaces={probes.ifaces}
  probingGroups={probes.groups}
  deferredSave
  onprobe={probes.probe}
  onclose={() => (pgModalOpen = false)}
  onsave={applyPgModal}
/>

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
</style>
