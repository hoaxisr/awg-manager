<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { ArrowLeft } from 'lucide-svelte';
  import { LoadingSpinner } from '$lib/components/layout';
  import { api } from '$lib/api/client';
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
  import MihomoBeginnerView from './mihomo/MihomoBeginnerView.svelte';
  import MihomoExpertView from './mihomo/MihomoExpertView.svelte';
  import MihomoRecoveryBanner from './mihomo/MihomoRecoveryBanner.svelte';
  import MihomoYamlViewerDrawer from './mihomo/MihomoYamlViewerDrawer.svelte';
  import type {
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeRule,
    MihomoNativeRuleProvider,
    MihomoNativeSubscription,
    MihomoRuntimeProxy,
    MihomoRuntimeProvider,
  } from '$lib/types';

  let activeSingboxSub = $derived($page.url.searchParams.get('sub'));
  let inspectorOpen = $state(false);
  let jsonOpen = $state(false);
  let configEditorOpen = $state(false);

  const singboxRulesStore = singboxRouterStore.rules;
  const singboxInitialized = singboxRouterStore.initialized;
  const singboxLoadError = singboxRouterStore.error;
  const routerSettings = singboxRouterStore.settings;
  let singboxRulesCount = $derived($singboxRulesStore.length);

  let currentRouterMode = $derived($sbMode);
  let activeEngine = $derived($routerSettings?.routingEngine === 'mihomo' ? 'mihomo' : 'sing-box');
  let isMihomo = $derived(activeEngine === 'mihomo');

  // Mihomo data state
  let mihomoRules = $state<MihomoNativeRule[]>([]);
  let mihomoGroups = $state<MihomoNativeGroup[]>([]);
  let mihomoRuleProviders = $state<MihomoNativeRuleProvider[]>([]);
  let mihomoProxies = $state<MihomoNativeProxy[]>([]);
  let mihomoSubscriptions = $state<MihomoNativeSubscription[]>([]);
  let mihomoRuntime = $state<MihomoRuntimeProxy[]>([]);
  let mihomoRuntimeProviders = $state<Record<string, MihomoRuntimeProvider>>({});
  let mihomoLoading = $state(false);
  let mihomoInitialized = $state(false);
  let mihomoDegraded = $state(false);

  async function fetchRuntimeProviders(): Promise<Record<string, MihomoRuntimeProvider>> {
    const [proxyRes, ruleRes] = await Promise.all([
      api.mihomoRuntimeProviders().catch(() => null),
      api.mihomoRuntimeRuleProviders().catch(() => null),
    ]);
    return {
      ...(proxyRes?.providers || {}),
      ...(ruleRes?.providers || {}),
    };
  }

  async function loadMihomoData() {
    mihomoLoading = true;
    try {
      const [r, g, rp, p, s, runtime, rProviders, mStatus] = await Promise.all([
        api.mihomoNativeRules(),
        api.mihomoNativeGroups(),
        api.mihomoNativeRuleProviders(),
        api.mihomoNativeProxies(),
        api.mihomoNativeSubscriptions(),
        api.mihomoRuntimeProxies().then(res => Object.values(res.proxies || {})).catch(() => []),
        fetchRuntimeProviders(),
        api.mihomoStatus().catch(() => null),
      ]);
      mihomoRules = r;
      mihomoGroups = g;
      mihomoRuleProviders = rp;
      mihomoProxies = p;
      mihomoSubscriptions = s;
      mihomoRuntime = runtime;
      mihomoRuntimeProviders = rProviders;
      mihomoDegraded = !!mStatus?.degraded;
      mihomoInitialized = true;
      void singboxRouterStore.reloadStatus();
    } catch (e) {
      console.error('Failed to load Mihomo router data:', e);
    } finally {
      mihomoLoading = false;
      mihomoInitialized = true;
    }
  }

  const SUB_VIEWS = new Set(['connections', 'logs']);
  const LEGACY_SUBS = new Set(['deviceproxy', 'rules', 'rulesets', 'outbounds', 'dns', 'engine']);

  function resetSingboxOverlayState() {
    closeAddWizard();
    closeTrace();
  }

  async function pollRuntimeData() {
    if (!isMihomo) return;
    try {
      const [runtime, rProviders] = await Promise.all([
        api.mihomoRuntimeProxies().then(res => Object.values(res.proxies || {})).catch(() => []),
        fetchRuntimeProviders(),
      ]);
      if (runtime && runtime.length > 0) {
        mihomoRuntime = runtime;
      }
      if (rProviders && Object.keys(rProviders).length > 0) {
        mihomoRuntimeProviders = rProviders;
      }
    } catch {
      // Background poll failure is non-fatal
    }
  }

  onMount(() => {
    resetSingboxOverlayState();
    const sub = $page.url.searchParams.get('sub');
    if (!sub || sub === 'logs') {
      void singboxRouterStore.loadAll();
      void loadMihomoData();
    } else {
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
      void loadMihomoData();
    }

    const pollInterval = setInterval(() => {
      if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
        void pollRuntimeData();
      }
    }, 6000);

    return () => {
      clearInterval(pollInterval);
    };
  });

  $effect(() => {
    if (isMihomo) {
      void loadMihomoData();
    }
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
    const current = currentRouterMode;
    if (
      prevMode === 'expert'
      && current === 'beginner'
      && $addWizardOpen
      && (isMihomo ? mihomoRules.length > 0 : singboxRulesCount > 0)
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

  function toggleLogsSub() {
    const url = new URL(window.location.href);
    if (activeSingboxSub === 'logs') {
      url.searchParams.delete('sub');
    } else {
      url.searchParams.set('tab', 'singbox');
      url.searchParams.set('sub', 'logs');
    }
    void goto(`${url.pathname}${url.search}`, { keepFocus: true, noScroll: true });
  }
</script>

<PageShell
  onOpenInspector={() => (inspectorOpen = true)}
  onOpenJson={() => (jsonOpen = true)}
  onOpenConfigEditor={!isMihomo && currentRouterMode === 'expert' ? () => (configEditorOpen = true) : undefined}
  onOpenLogs={toggleLogsSub}
  logsActive={activeSingboxSub === 'logs'}
>
  {#if !isMihomo}
    <StagingBanner />
  {:else if mihomoDegraded}
    <MihomoRecoveryBanner onRestored={loadMihomoData} />
  {/if}
  {#if inSubView}
    <button type="button" class="sub-back" onclick={clearSub}>
      <ArrowLeft size={14} /> {m.common_back()}
    </button>
  {/if}
  {#if activeSingboxSub === 'connections'}
    <ConnectionsSubTab />
  {:else if activeSingboxSub === 'logs'}
    <LogsTerminal lockBucket={isMihomo ? 'mihomo' : 'singbox'} storagePrefix="awgm.sb-router" />
  {:else if $addWizardOpen}
    <AddWizardPanel isMihomo={isMihomo} onReloadMihomo={loadMihomoData} />
  {:else if $traceOpen}
    <TracePanel />
  {:else if currentRouterMode === 'beginner'}
    {#if isMihomo}
      {#if !mihomoInitialized}
        <div class="boot-loading"><LoadingSpinner size="sm" /></div>
      {:else if mihomoRules.length === 0}
        <EmptyState isMihomo={true} onReloadMihomo={loadMihomoData} />
      {:else}
        <MihomoBeginnerView
          rules={mihomoRules}
          groups={mihomoGroups}
          proxies={mihomoProxies}
          subscriptions={mihomoSubscriptions}
          ruleProviders={mihomoRuleProviders}
          runtimeProxies={mihomoRuntime}
          loading={mihomoLoading}
          onReload={loadMihomoData}
        />
      {/if}
    {:else if !$singboxInitialized}
      <div class="boot-loading"><LoadingSpinner size="sm" /></div>
    {:else if $singboxLoadError}
      <div class="boot-error" role="alert">
        <strong>Не удалось загрузить маршрутизацию</strong>
        <span>{$singboxLoadError}</span>
        <button type="button" onclick={() => void singboxRouterStore.loadAll()}>Повторить</button>
      </div>
    {:else if singboxRulesCount === 0}
      <EmptyState />
    {:else}
      <FlowGraph />
      <RulesPanel />
    {/if}
  {:else if isMihomo}
    <MihomoExpertView
      rules={mihomoRules}
      groups={mihomoGroups}
      proxies={mihomoProxies}
      subscriptions={mihomoSubscriptions}
      ruleProviders={mihomoRuleProviders}
      runtimeProxies={mihomoRuntime}
      runtimeProviders={mihomoRuntimeProviders}
      loading={mihomoLoading}
      onReload={loadMihomoData}
    />
  {:else}
    <ExpertPanel />
  {/if}
</PageShell>

<RouteInspector open={inspectorOpen} engine={isMihomo ? 'mihomo' : 'sing-box'} onClose={() => (inspectorOpen = false)} />
<JsonConfigDrawer open={jsonOpen && !isMihomo} onClose={() => (jsonOpen = false)} />
<MihomoYamlViewerDrawer open={jsonOpen && isMihomo} onClose={() => (jsonOpen = false)} />
<ConfigSlotsDrawer
  open={configEditorOpen}
  onClose={() => (configEditorOpen = false)}
  onOpenMerged={() => (jsonOpen = true)}
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

  .boot-error {
    display: grid;
    gap: 10px;
    max-width: 680px;
    margin: 48px auto;
    padding: 18px;
    border: 1px solid var(--warning-border, var(--border));
    border-radius: var(--radius-md);
    background: var(--warning-bg, var(--bg-secondary));
  }

  .boot-error span {
    color: var(--text-secondary);
  }

  .boot-error button {
    justify-self: start;
    padding: 7px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--accent);
    color: var(--accent-contrast, white);
    cursor: pointer;
  }
</style>
