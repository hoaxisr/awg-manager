<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { get } from 'svelte/store';
  import { api } from '$lib/api/client';
  import type {
    FircEnginePurgeResult,
    FircMigrationOptions,
    FircMigrationOverview,
    FircMigrationSummary,
    FircInstallStatus,
    FircSnapshotMeta,
  } from '$lib/types/firc';
  import { m } from '$lib/i18n';
  import { acquireFircStatusWatch, fircForeign, fircStatus, reloadFircAll } from '$lib/stores/firc';
  import { reloadSettings } from '$lib/stores/settings';
  import { singboxRouter } from '$lib/stores/singboxRouter';
  import { singboxStatus, singboxTunnels } from '$lib/stores/singbox';
  import { subscriptionsStore, subscriptionGroupsStore } from '$lib/stores/subscriptions';
  import { awg3Tunnels } from '$lib/stores/awg3';
  import { tunnels } from '$lib/stores/tunnels';
  import {
    fircMigrationModalOpen,
    fircMigrationModalTab,
    openFircMigrationModal,
    closeFircMigrationModal,
    notifyFircMigrationApplied,
    isFircMigrationPopupLocallyDismissed,
    setFircMigrationPopupLocallyDismissed,
    type FircMigrationWizardTab,
  } from '$lib/stores/fircMigrationModal';
  import { LoadingSpinner } from '$lib/components/layout';
  import { Modal, Button, Tabs } from '$lib/components/ui';
  import type { Tab } from '$lib/components/ui/tabs-model';
  import { notifications } from '$lib/stores/notifications';
  import './fircMigrationShared.css';
  import FircForeignNotice from './FircForeignNotice.svelte';
  import FircMigrationEngineStrip from './FircMigrationEngineStrip.svelte';
  import FircMigrationExecNotice from './FircMigrationExecNotice.svelte';
  import FircMigrationForwardTab from './FircMigrationForwardTab.svelte';
  import FircMigrationSnapshotsTab from './FircMigrationSnapshotsTab.svelte';
  import {
    engineLabel,
    forwardDoneText,
    forwardNotice,
    purgeItemsSummary,
    purgeNotice,
    restoreNotice,
    type ExecNotice,
    type MigrationEngine,
  } from './fircMigrationNotices';

  let { onApplied }: { onApplied?: () => void } = $props();

  let activeTab = $derived($fircMigrationModalTab);
  let isOpen = $derived($fircMigrationModalOpen);

  let loadingOverview = $state(false);
  let activeEngine = $state<string>('sing-box');
  let fircRunning = $state(false);
  let fircInstalled = $state(true);
  let singboxRunning = $state(true);
  let singboxInstalled = $state(true);
  let singboxTunnelsCount = $state(0);
  let singboxSubscriptionsCount = $state(0);
  let fircTunnelsCount = $state(0);
  let fircGroupsCount = $state(0);
  let dontShowAgain = $state(false);
  let installStatus = $state<FircInstallStatus | null>(null);
  // Свежий статус важнее снимка на момент открытия: сторонний firc могли
  // поставить или снять, пока окно открыто.
  let foreign = $derived($fircForeign === undefined ? installStatus?.foreign : ($fircForeign ?? undefined));
  let overviewFailed = $state(false);
  let overviewLoaded = $state(false);
  let overviewReady = $derived(overviewLoaded && installStatus !== null);
  let installingRuntime = $state(false);
  let installingSingbox = $state(false);
  let purgingEngine = $state<MigrationEngine | null>(null);
  let confirmPurgeEngine = $state<MigrationEngine | null>(null);
  let runtimeMissing = $derived(
    installStatus !== null && (!installStatus.fircdPresent || !installStatus.tunvlessPresent),
  );
  let fircMissing = $derived(!fircInstalled || runtimeMissing);
  let singboxMissing = $derived(!singboxInstalled);
  let canPurgeSingbox = $derived(
    activeEngine === 'firc' &&
      (singboxInstalled || singboxTunnelsCount > 0 || singboxSubscriptionsCount > 0),
  );
  let canPurgeFirc = $derived(
    activeEngine === 'sing-box' &&
      (fircInstalled || fircTunnelsCount > 0 || fircGroupsCount > 0),
  );
  let lastExecutionNotice = $state<ExecNotice | null>(null);

  // Forward migration (Sing-box -> FIRC) state
  let fwdPreview = $state<FircMigrationSummary | null>(null);
  let fwdLoading = $state(false);
  let fwdApplying = $state(false);
  let fwdRouterSource = $state<'auto' | 'tproxy' | 'fakeip' | 'both'>('auto');
  let fwdPostAction = $state<'switch_standby' | 'switch_and_stop' | 'switch_and_uninstall' | 'keep_current'>('switch_standby');
  let fwdIncludeFakeIP = $state(false);
  let fwdSelectedTunnelIds = $state<string[]>([]);
  let fwdSelectedGroupIds = $state<string[]>([]);
  let fwdInitializedSelection = $state(false);
  let fwdConfirmReplace = $state(false);
  // Импорт заменяет выбранные категории: подтверждение нужно, только если есть что терять.
  let fwdReplacesExisting = $derived(fircTunnelsCount + fircGroupsCount > 0);

  // Snapshots state
  let snapshots = $state<FircSnapshotMeta[]>([]);
  let snapLabelInput = $state('');
  let snapCreating = $state(false);
  let snapRestoringId = $state<string | null>(null);
  let snapDeletingId = $state<string | null>(null);
  let snapConfirmRestoreId = $state<string | null>(null);

  // Sequence counters to discard stale async responses
  let overviewReqSeq = 0;
  let prefetchedAutoOverview: FircMigrationOverview | null = null;
  let fwdPreviewReqSeq = 0;

  let isBusy = $derived(
    loadingOverview ||
      installingRuntime ||
      installingSingbox ||
      purgingEngine !== null ||
      Boolean(installStatus?.busy) ||
      fwdLoading ||
      fwdApplying ||
      snapCreating ||
      snapRestoringId !== null ||
      snapDeletingId !== null,
  );

  const tabs = $derived.by<Tab[]>(() => {
    const fwdCount = (fwdPreview?.tunnels?.length ?? 0) + (fwdPreview?.groups?.length ?? 0);
    return [
      { id: 'forward', label: m.firc_migration_tab_forward(), badge: fwdCount > 0 ? fwdCount : undefined },
      {
        id: 'snapshots',
        label: m.firc_migration_tab_snapshots(),
        badge: snapshots.length,
        badgeTone: snapshots.length > 0 ? 'success' : 'muted',
      },
    ];
  });

  function setTab(tab: FircMigrationWizardTab) {
    if (isBusy) return;
    fircMigrationModalTab.set(tab);
  }

  const errorText = (e: unknown, fallback: string) => (e instanceof Error ? e.message : undefined) ?? fallback;

  /** Перечитать всё, что могла поменять миграция, установка или восстановление. */
  function reloadAfterChange() {
    return Promise.allSettled([
      reloadFircAll(),
      reloadSettings(),
      singboxRouter.reloadSettings(),
      singboxStatus.refetch(),
      singboxTunnels.refetch(),
      subscriptionsStore.refetch(),
      subscriptionGroupsStore.refetch(),
      awg3Tunnels.refetch(),
      tunnels.refetch(),
      loadFullWizardData(true),
    ]);
  }

  async function installFircRuntimeFromModal() {
    if (isBusy || !installStatus?.available) return;
    installingRuntime = true;
    try {
      await api.fircInstall();
      const deadline = Date.now() + 90_000;
      while (Date.now() < deadline) {
        const next = await api.fircGetInstallStatus().catch(() => null);
        if (next) {
          installStatus = next;
          if (!next.busy) {
            if (next.fircdPresent && next.tunvlessPresent) {
              fwdPostAction = 'switch_standby';
              await Promise.allSettled([reloadFircAll(), reloadSettings(), loadFullWizardData(true)]);
              notifications.success(m.firc_runtime_installed());
            } else if (next.error) {
              notifications.error(next.error);
            }
            break;
          }
        }
        await new Promise((r) => setTimeout(r, 1500));
      }
    } catch (e: unknown) {
      notifications.error(e instanceof Error ? e.message : m.firc_runtime_unavailable());
    } finally {
      installingRuntime = false;
    }
  }

  async function installSingboxFromModal() {
    if (isBusy) return;
    installingSingbox = true;
    try {
      await api.singboxInstall();
      await Promise.allSettled([
        reloadFircAll(),
        reloadSettings(),
        singboxRouter.reloadSettings(),
        singboxStatus.refetch(),
        singboxTunnels.refetch(),
        subscriptionsStore.refetch(),
        subscriptionGroupsStore.refetch(),
        awg3Tunnels.refetch(),
        loadFullWizardData(true),
      ]);
      notifyFircMigrationApplied();
      onApplied?.();
      notifications.success(m.firc_migration_singbox_installed());
    } catch (e: unknown) {
      notifications.error(e instanceof Error ? e.message : m.firc_migration_singbox_install_failed());
    } finally {
      installingSingbox = false;
    }
  }

  function snapRequiresMissingEngine(snap: FircSnapshotMeta): MigrationEngine | null {
    const eng = (snap.activeEngine || '').toLowerCase();
    if (eng === 'sing-box' && singboxMissing) return 'sing-box';
    if (eng === 'firc' && fircMissing) return 'firc';
    return null;
  }

  async function handlePurgeInactiveEngine(engine: MigrationEngine) {
    if (isBusy) return;
    if (confirmPurgeEngine !== engine) {
      confirmPurgeEngine = engine;
      return;
    }
    purgingEngine = engine;
    lastExecutionNotice = null;
    try {
      const res: FircEnginePurgeResult = await api.fircPurgeEngine(engine);
      confirmPurgeEngine = null;
      await reloadAfterChange();
      notifyFircMigrationApplied();
      onApplied?.();
      lastExecutionNotice = purgeNotice(engine, res, activeEngine);
      notifications.success(
        m.firc_migration_purge_done({ engine: engineLabel(engine), items: purgeItemsSummary(engine, res) }),
      );
    } catch (e: unknown) {
      notifications.error(e instanceof Error ? e.message : m.firc_migration_purge_failed({ engine }));
    } finally {
      purgingEngine = null;
    }
  }

  function applyFwdPreview(preview: FircMigrationSummary, resetSelection: boolean) {
    fwdPreview = preview;
    if (resetSelection || !fwdInitializedSelection) {
      fwdSelectedTunnelIds = (preview.tunnels ?? []).map((t) => t.id);
      fwdSelectedGroupIds = (preview.groups ?? []).map((g) => g.id);
      fwdInitializedSelection = true;
    } else {
      const validTun = new Set((preview.tunnels ?? []).map((t) => t.id));
      const validGrp = new Set((preview.groups ?? []).map((g) => g.id));
      fwdSelectedTunnelIds = fwdSelectedTunnelIds.filter((id) => validTun.has(id));
      fwdSelectedGroupIds = fwdSelectedGroupIds.filter((id) => validGrp.has(id));
    }
  }

  async function loadFullWizardData(preserveUserSelections = false) {
    const seq = ++overviewReqSeq;
    loadingOverview = true;
    const prefetched = preserveUserSelections ? null : prefetchedAutoOverview;
    if (!preserveUserSelections) {
      prefetchedAutoOverview = null;
    }
    try {
      const [ov, nextInstallStatus] = await Promise.all([
        prefetched ? Promise.resolve(prefetched) : api.fircGetMigrationState(),
        api.fircGetInstallStatus().catch(() => null),
      ]);
      if (seq !== overviewReqSeq) return;
      // Открытие окна прошлый ответ не оставляет; перечитывание посреди работы —
      // оставляет, чтобы сбой запроса не стёр показанный итог операции.
      overviewFailed = nextInstallStatus === null && !overviewLoaded;
      if (nextInstallStatus !== null) {
        installStatus = nextInstallStatus;
        if (
          (!nextInstallStatus.fircdPresent || !nextInstallStatus.tunvlessPresent) &&
          fwdPostAction !== 'keep_current'
        ) {
          fwdPostAction = 'keep_current';
        }
      }
      activeEngine = ov.activeEngine || 'sing-box';
      if (nextInstallStatus?.foreign) return; // мигрировать некуда: предпросмотр вернул бы отказ
      fircRunning = Boolean(ov.fircRunning);
      singboxRunning = Boolean(ov.singboxRunning);
      fircInstalled =
        ov.fircInstalled ??
        (nextInstallStatus !== null
          ? nextInstallStatus.fircdPresent && nextInstallStatus.tunvlessPresent
          : true);
      singboxInstalled = ov.singboxInstalled ?? true;
      singboxTunnelsCount = ov.singboxTunnelsCount ?? 0;
      singboxSubscriptionsCount = ov.singboxSubscriptionsCount ?? 0;
      fircTunnelsCount = ov.fircTunnelsCount ?? 0;
      fircGroupsCount = ov.fircGroupsCount ?? 0;
      dontShowAgain = Boolean(ov.wizardState?.dismissedAutoPopup);
      snapshots = ov.snapshots ?? [];

      if (ov.forwardPreview && fwdRouterSource === 'auto') {
        applyFwdPreview(ov.forwardPreview, !preserveUserSelections);
      } else {
        await refreshForwardPreview(fwdRouterSource, !preserveUserSelections || !fwdInitializedSelection);
      }
    } catch (e: unknown) {
      if (seq === overviewReqSeq) {
        overviewFailed = !overviewLoaded;
        notifications.error(errorText(e, m.firc_migration_load_failed()));
      }
    } finally {
      if (seq === overviewReqSeq) {
        loadingOverview = false;
        overviewLoaded = !overviewFailed;
      }
    }
  }

  onMount(() => {
    let unmounted = false;
    void Promise.resolve().then(async () => {
      if (unmounted || get(fircMigrationModalOpen) || isFircMigrationPopupLocallyDismissed()) {
        return;
      }
      try {
        const ov = await api.fircGetMigrationState();
        if (unmounted || get(fircMigrationModalOpen) || isFircMigrationPopupLocallyDismissed()) {
          return;
        }
        if (ov?.shouldShowPopup && !ov.wizardState?.dismissedAutoPopup) {
          prefetchedAutoOverview = ov;
          openFircMigrationModal('forward');
        }
      } catch {
        // Ignore background auto-popup probe errors.
      }
    });
    return () => {
      unmounted = true;
    };
  });

  $effect(() => {
    if (isOpen) {
      lastExecutionNotice = null;
      snapConfirmRestoreId = null;
      confirmPurgeEngine = null;
      installStatus = null;
      overviewFailed = false;
      overviewLoaded = false;
      void loadFullWizardData(false);
      return acquireFircStatusWatch();
    }
  });

  // Сторонний firc ушёл при открытом окне: данных для разделов ещё нет.
  let wasForeign = false;
  $effect(() => {
    const now = Boolean($fircForeign); // не foreign: тот гаснет и на каждом открытии
    if (isOpen && wasForeign && !now) void untrack(() => loadFullWizardData(false));
    wasForeign = now;
  });

  async function handleToggleDontShow(checked: boolean) {
    dontShowAgain = checked;
    setFircMigrationPopupLocallyDismissed(checked);
    try {
      await api.fircUpdateMigrationState({ dismissedAutoPopup: checked });
    } catch {
      // ignore network error; local storage already saved
    }
  }

  async function handleCloseModal() {
    if (isBusy) return;
    if (dontShowAgain) {
      setFircMigrationPopupLocallyDismissed(true);
      try {
        await api.fircUpdateMigrationState({ dismissedAutoPopup: true });
      } catch {
        // ignore
      }
    }
    closeFircMigrationModal();
  }

  // ── Forward Migration (Sing-box -> FIRC) ──

  async function refreshForwardPreview(
    routerSource: 'auto' | 'tproxy' | 'fakeip' | 'both' = fwdRouterSource,
    resetSelection = false,
  ) {
    const seq = ++fwdPreviewReqSeq;
    fwdLoading = true;
    try {
      const res = await api.fircMigrateFromSingbox(true, {
        routerSource,
        includeTunnels: true,
        includeSubscriptions: true,
        includeGroups: true,
        includeClientRoutes: true,
      });
      if (seq !== fwdPreviewReqSeq) return;
      applyFwdPreview(res, resetSelection);
    } catch (e: unknown) {
      if (seq === fwdPreviewReqSeq) {
        notifications.error(errorText(e, m.firc_migration_fwd_preview_failed()));
      }
    } finally {
      if (seq === fwdPreviewReqSeq) {
        fwdLoading = false;
      }
    }
  }

  async function applyForwardMigration() {
    if (isBusy) return;
    if (fwdSelectedTunnelIds.length === 0 && fwdSelectedGroupIds.length === 0 && !fwdIncludeFakeIP) {
      notifications.error(m.firc_migration_fwd_nothing_selected());
      return;
    }
    if (fwdReplacesExisting && !fwdConfirmReplace) {
      notifications.error(m.firc_migration_fwd_confirm_replace_required());
      return;
    }
    fwdApplying = true;
    lastExecutionNotice = null;
    try {
      // Категория без выбранных элементов не трогается; выбранные заменяют её целиком.
      const options: FircMigrationOptions = {
        routerSource: fwdRouterSource,
        includeTunnels: fwdSelectedTunnelIds.length > 0,
        includeSubscriptions: fwdSelectedTunnelIds.length > 0,
        includeGroups: fwdSelectedGroupIds.length > 0,
        includeClientRoutes: fwdSelectedGroupIds.length > 0,
        includeFakeIPConfig: fwdIncludeFakeIP,
        selectedTunnelIds: fwdSelectedTunnelIds.length > 0 ? fwdSelectedTunnelIds : ['__none__'],
        selectedGroupIds: fwdSelectedGroupIds.length > 0 ? fwdSelectedGroupIds : ['__none__'],
        postSingboxAction: fwdPostAction,
      };
      const res = await api.fircMigrateFromSingbox(false, options);
      if (dontShowAgain) {
        setFircMigrationPopupLocallyDismissed(true);
        await api.fircUpdateMigrationState({ dismissedAutoPopup: true }).catch(() => {});
      }
      await reloadAfterChange();
      notifyFircMigrationApplied();
      onApplied?.();

      const hasPostErr =
        Boolean(res.switchError || res.startError || res.postActionError) ||
        res.postActionApplied !== fwdPostAction ||
        (fwdPostAction !== 'keep_current' && res.engineSwitched === false);
      lastExecutionNotice = forwardNotice(res, {
        postAction: fwdPostAction,
        activeEngine,
        hasPostErr,
      });

      if (hasPostErr || res.recoveryRequired) {
        notifications.error(
          m.firc_migration_fwd_post_failed({
            error: res.startError || res.switchError || res.postActionError || m.firc_migration_action_unconfirmed(),
          }),
        );
      } else {
        notifications.success(forwardDoneText(res));
        closeFircMigrationModal();
      }
    } catch (e: unknown) {
      notifications.error(errorText(e, m.firc_migration_fwd_failed()));
    } finally {
      fwdApplying = false;
    }
  }

  // ── System Snapshots ──

  async function handleCreateSnapshot() {
    if (isBusy) return;
    snapCreating = true;
    try {
      const created = await api.fircCreateSnapshot(snapLabelInput.trim());
      snapLabelInput = '';
      snapshots = [created, ...snapshots.filter((s) => s.id !== created.id)];
      notifications.success(m.firc_migration_snap_create_done({ label: created.label, id: created.id }));
    } catch (e: unknown) {
      notifications.error(errorText(e, m.firc_migration_snap_create_failed()));
    } finally {
      snapCreating = false;
    }
  }

  async function handleRestoreSnapshot(snap: FircSnapshotMeta) {
    if (isBusy) return;
    const missingEng = snapRequiresMissingEngine(snap);
    if (missingEng) {
      notifications.error(m.firc_migration_restore_need_engine({ engine: engineLabel(missingEng) }));
      return;
    }
    if (snapConfirmRestoreId !== snap.id) {
      snapConfirmRestoreId = snap.id;
      return;
    }
    snapRestoringId = snap.id;
    lastExecutionNotice = null;
    try {
      const restored = await api.fircRestoreSnapshot(snap.id, true);
      snapConfirmRestoreId = null;
      await reloadAfterChange();
      notifyFircMigrationApplied();
      onApplied?.();

      lastExecutionNotice = restoreNotice(restored);
      if (restored.postActionError || restored.recoveryRequired) {
        notifications.error(
          m.firc_migration_restore_post_failed({
            engine: restored.activeEngine,
            error: restored.postActionError ?? '',
          }),
        );
      } else {
        notifications.success(
          m.firc_migration_restore_done({ label: restored.label, engine: restored.activeEngine }),
        );
      }
    } catch (e: unknown) {
      notifications.error(errorText(e, m.firc_migration_restore_failed()));
    } finally {
      snapRestoringId = null;
    }
  }

  async function handleDeleteSnapshot(id: string) {
    if (isBusy) return;
    snapDeletingId = id;
    try {
      await api.fircDeleteSnapshot(id);
      snapshots = snapshots.filter((s) => s.id !== id);
      if (snapConfirmRestoreId === id) {
        snapConfirmRestoreId = null;
      }
      notifications.info(m.firc_migration_snap_deleted());
    } catch (e: unknown) {
      notifications.error(errorText(e, m.firc_migration_snap_delete_failed()));
    } finally {
      snapDeletingId = null;
    }
  }
</script>

<Modal open={isOpen} onclose={handleCloseModal} title={m.firc_migration_title()} width="740px">
  {#if foreign}
  <FircForeignNotice {foreign} selected={$fircStatus?.selected ?? activeEngine === 'firc'} bare />
  {:else if overviewFailed}
  <p class="wiz-loading" role="alert">{m.firc_migration_load_failed()}</p>
  {:else if !overviewReady}
  <div class="wiz-loading"><LoadingSpinner /></div>
  {:else}
  <div class="firc-mig-body">
    <Tabs {tabs} active={activeTab} disabled={isBusy} onchange={(id) => setTab(id as FircMigrationWizardTab)} />

    <FircMigrationEngineStrip
      {activeEngine}
      {isBusy}
      {singboxMissing}
      {singboxRunning}
      {singboxTunnelsCount}
      {singboxSubscriptionsCount}
      {fircMissing}
      {fircRunning}
      {fircTunnelsCount}
      {fircGroupsCount}
      {canPurgeSingbox}
      {canPurgeFirc}
      {confirmPurgeEngine}
      {purgingEngine}
      {installingSingbox}
      {installingRuntime}
      {installStatus}
      oninstallSingbox={installSingboxFromModal}
      oninstallFirc={installFircRuntimeFromModal}
      onpurge={handlePurgeInactiveEngine}
      oncancelPurge={() => (confirmPurgeEngine = null)}
    />

    {#if lastExecutionNotice}
      <FircMigrationExecNotice notice={lastExecutionNotice} />
    {/if}

    {#if loadingOverview}
      <div class="wiz-loading">{m.firc_migration_loading()}</div>
    {:else if activeTab === 'forward'}
      <FircMigrationForwardTab
        preview={fwdPreview}
        {isBusy}
        {runtimeMissing}
        {installStatus}
        {installingRuntime}
        {activeEngine}
        bind:postAction={fwdPostAction}
        bind:routerSource={fwdRouterSource}
        bind:includeFakeIP={fwdIncludeFakeIP}
        replacesExisting={fwdReplacesExisting}
        bind:confirmReplace={fwdConfirmReplace}
        bind:selectedTunnelIds={fwdSelectedTunnelIds}
        bind:selectedGroupIds={fwdSelectedGroupIds}
        oninstallRuntime={installFircRuntimeFromModal}
        onrefresh={(source) => void refreshForwardPreview(source, false)}
      />
    {:else}
      <FircMigrationSnapshotsTab
        {snapshots}
        {isBusy}
        {singboxMissing}
        {fircMissing}
        {installingSingbox}
        {installingRuntime}
        {installStatus}
        bind:labelInput={snapLabelInput}
        creating={snapCreating}
        restoringId={snapRestoringId}
        deletingId={snapDeletingId}
        bind:confirmRestoreId={snapConfirmRestoreId}
        missingEngineFor={snapRequiresMissingEngine}
        oninstallSingbox={installSingboxFromModal}
        oninstallFirc={installFircRuntimeFromModal}
        oncreate={handleCreateSnapshot}
        onrestore={handleRestoreSnapshot}
        ondelete={handleDeleteSnapshot}
      />
    {/if}
  </div>
  {/if}

  {#snippet actions()}
    {#if foreign || !overviewReady || overviewFailed}
    <Button variant="ghost" size="sm" onclick={closeFircMigrationModal}>{m.common_close()}</Button>
    {:else}
    <div class="wiz-footer">
      <label class="check-row">
        <input
          type="checkbox"
          checked={dontShowAgain}
          disabled={isBusy}
          onchange={(e) => handleToggleDontShow((e.currentTarget as HTMLInputElement).checked)}
        />
        <span>{m.firc_migration_dont_show()}</span>
      </label>

      <div class="wiz-footer-btns">
        <Button variant="ghost" size="sm" disabled={isBusy} onclick={handleCloseModal}>{m.common_close()}</Button>
        {#if activeTab === 'forward'}
          <Button
            variant="primary"
            size="sm"
            loading={fwdApplying || fwdLoading}
            disabled={isBusy || (fwdReplacesExisting && !fwdConfirmReplace)}
            onclick={applyForwardMigration}
          >
            {m.firc_migration_fwd_apply()}
          </Button>
        {/if}
      </div>
    </div>
    {/if}
  {/snippet}
</Modal>

<style>
  .firc-mig-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .wiz-loading {
    padding: 28px;
    text-align: center;
    font-size: 13px;
    color: var(--text-secondary);
  }

  .wiz-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    width: 100%;
    flex-wrap: wrap;
  }

  .wiz-footer-btns {
    display: flex;
    gap: 8px;
    align-items: center;
    justify-content: flex-end;
    /* На ≤640px Modal делает кнопки футера flex: 1 1 0 + nowrap: без роста
       контейнера они делят его ширину по содержимому (shrink-to-fit) и длинная
       подпись обрезается. Растягиваем контейнер на свободное место строки. */
    flex: 1 1 auto;
  }

  /* Равные доли (основа 0, width: 100%) из Modal режут «Выполнить миграцию в FIRC» на 390px:
     основа по содержимому, остаток делится поровну. */
  @media (max-width: 640px) {
    .wiz-footer .wiz-footer-btns :global(button) {
      flex: 1 1 auto;
      width: auto;
    }
  }

  .check-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-primary);
    cursor: pointer;
  }

  .check-row > input {
    flex-shrink: 0;
  }
</style>
