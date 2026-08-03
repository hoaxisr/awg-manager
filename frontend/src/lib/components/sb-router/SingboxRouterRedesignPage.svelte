<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { ArrowLeft } from 'lucide-svelte';
  import { LoadingSpinner } from '$lib/components/layout';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { StagingBanner, RouteInspector, JsonConfigDrawer, ConfigSlotsDrawer } from '$lib/components/singbox-routing';
  import { ConnectionsSubTab } from '$lib/components/routing/singboxRouter';
  import { LogsTerminal } from '$lib/components/diagnostics';
  import {
    PageShell,
    RulesPanel,
    FlowGraph,
    TracePanel,
    traceOpen,
    AddWizardPanel,
    addWizardOpen,
    closeAddWizard,
    closeTrace,
    EmptyState,
    ExpertPanel,
    mode as sbMode,
    type RouterMode,
  } from '$lib/components/sb-router';

  import SelectiveRebuildModal from './SelectiveRebuildModal.svelte';
  import { selectiveBypass } from '$lib/stores/selectiveBypass';

  const { progress: globalSelectiveProgress, modalRequested: selectiveModalRequested } = selectiveBypass;

  let globalRebuildOpen = $state(false);

  // Open only when explicitly requested (Apply button or engine enable).
  $effect(() => {
    if ($selectiveModalRequested) {
      globalRebuildOpen = true;
    }
  });

  function minimizeGlobalRebuild() {
    globalRebuildOpen = false;
    selectiveBypass.clearModalRequest();
  }

  function dismissGlobalRebuild() {
    globalRebuildOpen = false;
    selectiveBypass.clearModalRequest();
    selectiveBypass.resetProgress();
  }

  let activeSingboxSub = $derived($page.url.searchParams.get('sub'));
  let inspectorOpen = $state(false);
  let jsonOpen = $state(false);
  // Эксперт-редактор config.d (слоты + 90-user.json).
  let configEditorOpen = $state(false);
  const singboxRulesStore = singboxRouterStore.rules;
  const singboxInitialized = singboxRouterStore.initialized;
  let singboxRulesCount = $derived($singboxRulesStore.length);

  const SUB_VIEWS = new Set(['connections', 'logs']);
  const LEGACY_SUBS = new Set(['deviceproxy', 'rules', 'rulesets', 'outbounds', 'dns', 'engine']);

  function resetSingboxOverlayState() {
    closeAddWizard();
    closeTrace();
  }

  onMount(() => {
    // Не восстанавливаем визард (?add=1) и sub=connections после ухода со
    // страницы. sub=logs — намеренное исключение: лог-вью должен переживать
    // F5 и открываться по прямой ссылке.
    resetSingboxOverlayState();
    const sub = $page.url.searchParams.get('sub');
    if (!sub || sub === 'logs') {
      void singboxRouterStore.loadAll();
      return;
    }

    const url = new URL(window.location.href);
    let shouldReplace = false;

    if (SUB_VIEWS.has(sub)) {
      url.searchParams.delete('sub');
      shouldReplace = true;
    } else if (LEGACY_SUBS.has(sub)) {
      url.searchParams.delete('sub');
      if (sub === 'deviceproxy') {
        url.searchParams.set('mode', 'expert');
      }
      shouldReplace = true;
    }

    if (shouldReplace) {
      const search = url.searchParams.toString();
      void goto(`${url.pathname}${search ? `?${search}` : ''}`, {
        replaceState: true,
        keepFocus: true,
        noScroll: true,
      });
    }

    void singboxRouterStore.loadAll();
  });

  // Явный переход в sub-вид (connections/logs) — закрыть визард/trace, но sub оставить.
  $effect(() => {
    const sub = activeSingboxSub;
    if (sub && SUB_VIEWS.has(sub)) {
      resetSingboxOverlayState();
    }
  });

  // Эксперт → простой: не возвращать в визард добавления, если правила уже есть.
  let prevMode = $state<RouterMode | null>(null);
  $effect(() => {
    const current = $sbMode;
    if (
      prevMode === 'expert'
      && current === 'beginner'
      && $addWizardOpen
      && singboxRulesCount > 0
    ) {
      closeAddWizard();
    }
    prevMode = current;
  });

  let inSubView = $derived(!!activeSingboxSub && SUB_VIEWS.has(activeSingboxSub));

  function clearSub() {
    const url = new URL(window.location.href);
    url.searchParams.delete('sub');
    void goto(`${url.pathname}${url.search}`, { keepFocus: true, noScroll: true });
  }

  // Toggle, как у чипа соединений: повторный клик закрывает вид, а не наслаивает
  // одинаковые записи в истории. Путь и чужие параметры не трогаем — страница
  // sb-router своя (/sb/routing).
  function toggleLogsSub() {
    const url = new URL(window.location.href);
    if (activeSingboxSub === 'logs') {
      url.searchParams.delete('sub');
    } else {
      url.searchParams.set('sub', 'logs');
    }
    void goto(`${url.pathname}${url.search}`, { keepFocus: true, noScroll: true });
  }
</script>

<PageShell
  onOpenInspector={() => (inspectorOpen = true)}
  onOpenJson={() => (jsonOpen = true)}
  onOpenConfigEditor={$sbMode === 'expert' ? () => (configEditorOpen = true) : undefined}
  onOpenLogs={toggleLogsSub}
  logsActive={activeSingboxSub === 'logs'}
>
  <StagingBanner />
  {#if inSubView}
    <button type="button" class="sub-back" onclick={clearSub}>
      <ArrowLeft size={14} /> Назад
    </button>
  {/if}
  {#if activeSingboxSub === 'connections'}
    <ConnectionsSubTab />
  {:else if activeSingboxSub === 'logs'}
    <!-- Логи sing-box (bucket singbox: stdout движка + process/runtime-события).
         Действия над конфигурацией остаются в разделе «Журнал» (bucket app). -->
    <LogsTerminal lockBucket="singbox" storagePrefix="awgm.sb-router" />
  {:else if $sbMode === 'beginner'}
    {#if $addWizardOpen}
      <AddWizardPanel />
    {:else if $traceOpen}
      <TracePanel />
    {:else if !$singboxInitialized}
      <div class="boot-loading"><LoadingSpinner size="sm" /></div>
    {:else if singboxRulesCount === 0}
      <EmptyState />
    {:else}
      <FlowGraph />
      <RulesPanel />
    {/if}
  {:else}
    <ExpertPanel />
  {/if}
</PageShell>

<RouteInspector open={inspectorOpen} onClose={() => (inspectorOpen = false)} />
<JsonConfigDrawer open={jsonOpen} onClose={() => (jsonOpen = false)} />
<ConfigSlotsDrawer
  open={configEditorOpen}
  onClose={() => (configEditorOpen = false)}
  onOpenMerged={() => (jsonOpen = true)}
/>

<SelectiveRebuildModal
  open={globalRebuildOpen}
  progress={$globalSelectiveProgress}
  onMinimize={minimizeGlobalRebuild}
  onDismiss={dismissGlobalRebuild}
/>

<style>
  .sub-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 12px;
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    font-size: 13px;
    font-family: inherit;
    cursor: pointer;
  }
  .sub-back:hover {
    color: var(--text-primary);
    border-color: var(--border-hover, var(--accent-line));
  }

  .boot-loading {
    display: flex;
    justify-content: center;
    padding: 48px 0;
  }
</style>
