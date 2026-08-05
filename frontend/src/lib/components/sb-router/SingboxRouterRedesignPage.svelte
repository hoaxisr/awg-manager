<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { ArrowLeft, TriangleAlert } from 'lucide-svelte';
  import { LoadingSpinner } from '$lib/components/layout';
  import { Button } from '$lib/components/ui';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { RouteInspector, JsonConfigDrawer, ConfigSlotsDrawer } from '$lib/components/singbox-routing';
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

  // Баннер черновика и окно пересборки ipset (SelectiveRebuildModal) живут в
  // layout группы движка (routes/sb/(engine)/+layout.svelte): черновик один на
  // весь слот маршрутизации, применить его можно с любой страницы группы, и
  // прогресс пересборки обязан пережить уход с этой страницы.

  let activeSingboxSub = $derived($page.url.searchParams.get('sub'));
  let inspectorOpen = $state(false);
  let jsonOpen = $state(false);
  // Эксперт-редактор config.d (слоты + 90-user.json).
  let configEditorOpen = $state(false);
  const singboxRulesStore = singboxRouterStore.rules;
  // Спиннер холодной загрузки снимается по attempted (попытка завершена), а не
  // по initialized (загрузка удалась): initialized теперь остаётся false после
  // неудачи, чтобы mount мог повторить запрос, и спиннер на нём крутился бы
  // вечно.
  const singboxAttempted = singboxRouterStore.attempted;
  // Экран ошибки показываем ТОЛЬКО пока ни одной удачной загрузки не было
  // (initialized === false): без него упавший бэкенд давал пустой список
  // правил, неотличимый от «ничего не настроено», и страница предлагала
  // мастер первичной настройки, который тут же падал бы на записи. Сужение по
  // initialized обязательно: иначе сбойный фоновый loadAll после мутации
  // сносил бы уже показанные данные.
  const singboxError = singboxRouterStore.error;
  const singboxInitialized = singboxRouterStore.initialized;
  const singboxLoading = singboxRouterStore.loading;
  // ...и НЕ отпускаем экран, пока идёт повтор. loadAll синхронно сбрасывает
  // error в null, поэтому без `|| $singboxLoading` клик по «Повторить» на
  // секунду возвращал бы ровно тот мастер настройки, который мы отсюда
  // убрали, а его onMount слал бы второй круг из десяти запросов.
  let loadFailed = $derived(
    $singboxAttempted && !$singboxInitialized && ($singboxError !== null || $singboxLoading),
  );
  let singboxRulesCount = $derived($singboxRulesStore.length);
  // Текст последней ошибки переживает повтор: на время запроса error пуст, а
  // блок остаётся на экране и обязан что-то показывать.
  let lastError = $state<string | null>(null);
  $effect(() => {
    if ($singboxError !== null) lastError = $singboxError;
  });

  function retryLoad(): void {
    void singboxRouterStore.loadAll();
  }

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
  {:else if loadFailed}
    <div class="load-failed">
      <TriangleAlert size={20} aria-hidden={true} />
      <p class="load-failed-title">Не удалось загрузить конфигурацию маршрутизации</p>
      <p class="load-failed-msg">{$singboxError ?? lastError ?? ''}</p>
      <Button variant="secondary" size="sm" onclick={retryLoad} disabled={$singboxLoading}>
        {$singboxLoading ? 'Повтор…' : 'Повторить'}
      </Button>
    </div>
  {:else if $sbMode === 'beginner'}
    {#if $addWizardOpen}
      <AddWizardPanel />
    {:else if $traceOpen}
      <TracePanel />
    {:else if !$singboxAttempted}
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

  .load-failed {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 48px 16px;
    text-align: center;
    color: var(--color-error, var(--text-primary));
  }
  .load-failed-title {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .load-failed-msg {
    margin: 0;
    max-width: 46ch;
    font-size: 13px;
    color: var(--text-secondary);
    overflow-wrap: anywhere;
  }
</style>
