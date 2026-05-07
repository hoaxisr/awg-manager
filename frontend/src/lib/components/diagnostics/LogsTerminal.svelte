<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { appLogEntries, singboxLogEntries, logStoreFor, type LogBucket, type LogStore } from '$lib/stores/logs';
  import { LoadingSpinner, EmptyState } from '$lib/components/layout';
  import { Button } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { copyToClipboard } from '$lib/utils/clipboard';
  import LogRow from './LogRow.svelte';
  import LogsToolbar, { ALL_LEVELS } from './LogsToolbar.svelte';
  import LogsContextMenu from './LogsContextMenu.svelte';
  import type { LogsFilter } from './LogsToolbar.svelte';
  import type { LogEntry } from '$lib/types';

  const STORAGE_KEY = 'awgm.diagnostics.logsFilter';
  const BUCKET_KEY = 'awgm.diagnostics.logsBucket';
  const PAGE_SIZE = 200;
  const SCROLL_THRESHOLD = 80;

  function defaultFilter(): LogsFilter {
    return { search: '', group: '', subgroup: '', levels: [...ALL_LEVELS] };
  }

  function loadFilter(): LogsFilter {
    if (typeof localStorage === 'undefined') return defaultFilter();
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return defaultFilter();
    try {
      const parsed = JSON.parse(raw);
      let levels: string[];
      if (Array.isArray(parsed.levels)) {
        levels = parsed.levels.filter((l: unknown): l is string => typeof l === 'string');
      } else if (typeof parsed.level === 'string' && parsed.level) {
        levels = [...ALL_LEVELS];
      } else {
        levels = [...ALL_LEVELS];
      }
      return {
        search: parsed.search ?? '',
        group: parsed.group ?? '',
        subgroup: parsed.subgroup ?? '',
        levels,
      };
    } catch {
      return defaultFilter();
    }
  }

  function saveFilter(f: LogsFilter) {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(f));
  }

  function loadBucket(): LogBucket {
    if (typeof localStorage === 'undefined') return 'app';
    const raw = localStorage.getItem(BUCKET_KEY);
    return raw === 'singbox' ? 'singbox' : 'app';
  }

  function saveBucket(b: LogBucket) {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(BUCKET_KEY, b);
  }

  let filter = $state<LogsFilter>(loadFilter());
  let bucket = $state<LogBucket>(loadBucket());
  let paused = $state(false);
  let bufferCount = $state(0);
  let downloading = $state(false);
  let clearing = $state(false);
  let expanded = $state<Record<string, boolean>>({});
  let scrollEl = $state<HTMLDivElement | null>(null);
  let searchInput = $state<HTMLInputElement | null>(null);
  let initialFetchDone = $state(false);
  let prevLen = $state(0);
  let pageOffset = $state(0);
  let loadingMore = $state(false);
  let availableSubgroups = $state<string[]>([]);
  const subgroupCache = new Map<string, string[]>();

  const activeStore = $derived<LogStore>(logStoreFor(bucket));

  // Reactive subscriptions to the active store. $derived re-runs each time
  // the store identity changes (bucket toggle), so we re-subscribe naturally
  // through reactive store-reads in the template ($activeStore, etc.).
  const enabledStore = $derived(activeStore.enabled);
  const totalStore = $derived(activeStore.total);
  const loadedStore = $derived(activeStore.loaded);
  const statsStore = $derived(activeStore.stats);

  // Stable row ids: keep DOM nodes for existing lines when new logs are
  // prepended, so text selection/copy is not disrupted by full re-render.
  const rowIdByLog = new WeakMap<LogEntry, string>();
  let rowSeq = 0;
  function logKey(log: LogEntry): string {
    const known = rowIdByLog.get(log);
    if (known) return known;
    rowSeq += 1;
    const id = `log-row-${rowSeq}`;
    rowIdByLog.set(log, id);
    return id;
  }

  // Initial fetch + every bucket switch: replace the entire active store.
  async function loadBucketFresh(b: LogBucket) {
    const store = logStoreFor(b);
    pageOffset = 0;
    const groupParam = b === 'singbox' ? 'singbox' : (filter.group || undefined);
    const subgroupParam = b === 'singbox'
      ? (filter.group ? filter.group : (filter.subgroup || undefined))
      : (filter.subgroup || undefined);
    try {
      const resp = await api.getLogs({
        bucket: b,
        group: groupParam,
        subgroup: subgroupParam,
        limit: PAGE_SIZE,
        offset: 0,
      });
      store.setEntries(resp.logs);
      store.setTotal(resp.total);
      store.setEnabled(resp.enabled);
      store.setStats({
        size: resp.bufferSize,
        capacity: resp.bufferCapacity,
        oldest: resp.oldestTimestamp,
      });
    } catch {
      notifications.error('Не удалось загрузить журнал');
    } finally {
      store.setLoaded(true);
    }
  }

  async function fetchSubgroups(group: string): Promise<string[]> {
    if (!group) return [];
    if (subgroupCache.has(group)) return subgroupCache.get(group)!;
    try {
      const resp = await api.getLogsSubgroups(group);
      subgroupCache.set(group, resp.subgroups);
      return resp.subgroups;
    } catch {
      return [];
    }
  }

  async function refreshSubgroups() {
    if (bucket === 'singbox') {
      // Sing-box bucket flattens subgroups as the user-facing "groups" in the
      // toolbar — no separate subgroup row needed.
      availableSubgroups = [];
      return;
    }
    if (!filter.group) {
      availableSubgroups = [];
      return;
    }
    availableSubgroups = await fetchSubgroups(filter.group);
  }

  onMount(async () => {
    await loadBucketFresh(bucket);
    await refreshSubgroups();
    setTimeout(() => (initialFetchDone = true), 100);
    window.addEventListener('keydown', handleKeydown);
  });

  onDestroy(() => {
    window.removeEventListener('keydown', handleKeydown);
  });

  function onScroll() {
    if (!scrollEl) return;
    const top = scrollEl.scrollTop;
    if (top > SCROLL_THRESHOLD) {
      paused = true;
    } else {
      paused = false;
      bufferCount = 0;
    }
  }

  $effect(() => {
    const len = $activeStore.length;
    if (!initialFetchDone) {
      prevLen = len;
      return;
    }
    if (len > prevLen && paused) {
      bufferCount += len - prevLen;
    }
    prevLen = len;
  });

  function togglePause() {
    paused = !paused;
    if (!paused) {
      scrollEl?.scrollTo({ top: 0, behavior: 'smooth' });
      bufferCount = 0;
    }
  }

  function resumeAndScroll() {
    scrollEl?.scrollTo({ top: 0, behavior: 'smooth' });
    paused = false;
    bufferCount = 0;
  }

  async function applyFilter(f: LogsFilter) {
    filter = f;
    saveFilter(f);
    // Group changed → refresh subgroup catalog; subgroup change keeps catalog.
    await refreshSubgroups();
  }

  async function setBucket(b: LogBucket) {
    if (b === bucket) return;
    bucket = b;
    saveBucket(b);
    // Reset filters on bucket switch — group sets are disjoint per bucket.
    filter = { ...filter, group: '', subgroup: '' };
    saveFilter(filter);
    await loadBucketFresh(b);
    await refreshSubgroups();
  }

  const displayLogs = $derived.by(() => {
    let arr: LogEntry[] = $activeStore;
    if (filter.levels.length > 0 && filter.levels.length < ALL_LEVELS.length) {
      const set = new Set(filter.levels);
      arr = arr.filter((l) => set.has(l.level));
    }
    if (bucket === 'singbox') {
      // sing-box bucket: filter.group is the SUBGROUP selector, filter.subgroup unused
      if (filter.group) arr = arr.filter((l) => l.subgroup === filter.group);
    } else {
      if (filter.group) arr = arr.filter((l) => l.group === filter.group);
      if (filter.subgroup) arr = arr.filter((l) => l.subgroup === filter.subgroup);
    }
    if (filter.search) {
      const q = filter.search.toLowerCase();
      arr = arr.filter(
        (l) =>
          l.message.toLowerCase().includes(q) ||
          l.target.toLowerCase().includes(q) ||
          l.action.toLowerCase().includes(q),
      );
    }
    return arr;
  });

  function handleClickScope(group: string, subgroup: string) {
    if (bucket === 'singbox') {
      // For sing-box the "scope" click maps the subgroup into the group selector.
      filter = { ...filter, group: subgroup, subgroup: '' };
    } else {
      filter = { ...filter, group, subgroup };
    }
    saveFilter(filter);
    refreshSubgroups();
  }

  function handleClickLevel(level: string) {
    filter = { ...filter, levels: [level] };
    saveFilter(filter);
  }

  function formatLine(log: LogEntry): string {
    const scope = log.subgroup ? `${log.group}/${log.subgroup}` : log.group;
    return `[${log.timestamp}] [${log.level.toUpperCase()}] [${scope}] ${log.action} ${log.target}: ${log.message}`;
  }

  async function copyText(text: string, successMsg: string) {
    if (await copyToClipboard(text)) {
      notifications.success(successMsg);
    } else {
      notifications.error('Не удалось скопировать');
    }
  }

  async function handleCopy() {
    const text = displayLogs.map(formatLine).join('\n');
    await copyText(text, 'Скопировано в буфер обмена');
  }

  function handleCopyLine(text: string) {
    copyText(text, 'Строка скопирована');
  }

  function handleCopyMessage(text: string) {
    copyText(text, 'Сообщение скопировано');
  }

  async function handleDownload() {
    downloading = true;
    try {
      const resp = await api.getLogs({
        bucket,
        group: bucket === 'singbox' ? 'singbox' : (filter.group || undefined),
        subgroup: bucket === 'singbox' ? (filter.group || undefined) : (filter.subgroup || undefined),
        limit: $totalStore || 10000,
      });
      const text = resp.logs.map(formatLine).join('\n');
      const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const date = new Date().toISOString().slice(0, 10);
      const a = document.createElement('a');
      a.href = url;
      a.download = `awg-manager-${bucket}-logs-${date}.txt`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      notifications.success(`Скачано ${resp.logs.length} записей`);
    } catch {
      notifications.error('Не удалось скачать логи');
    } finally {
      downloading = false;
    }
  }

  async function handleClear() {
    clearing = true;
    try {
      await api.clearLogs(bucket);
      activeStore.clear();
      activeStore.setStats({
        size: 0,
        capacity: $statsStore.capacity,
      });
      notifications.success('Логи очищены');
    } catch {
      notifications.error('Не удалось очистить логи');
    } finally {
      clearing = false;
    }
  }

  async function loadMore() {
    if (loadingMore) return;
    loadingMore = true;
    pageOffset += PAGE_SIZE;
    try {
      const resp = await api.getLogs({
        bucket,
        group: bucket === 'singbox' ? 'singbox' : (filter.group || undefined),
        subgroup: bucket === 'singbox' ? (filter.group || undefined) : (filter.subgroup || undefined),
        limit: PAGE_SIZE,
        offset: pageOffset,
      });
      activeStore.appendPage(resp.logs);
      activeStore.setTotal(resp.total);
      activeStore.setStats({
        size: resp.bufferSize,
        capacity: resp.bufferCapacity,
        oldest: resp.oldestTimestamp,
      });
    } catch {
      notifications.error('Не удалось загрузить ещё');
      pageOffset -= PAGE_SIZE;
    } finally {
      loadingMore = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
      e.preventDefault();
      searchInput?.focus();
    }
  }

  const remaining = $derived(Math.max(0, $totalStore - $activeStore.length));
  const hasMore = $derived(remaining > 0);
  const nextBatch = $derived(Math.min(PAGE_SIZE, remaining));
</script>

{#if !$loadedStore}
  <div class="terminal-loading">
    <LoadingSpinner size="lg" message="Загрузка журнала..." />
  </div>
{:else if !$enabledStore}
  <div class="terminal-empty">
    <EmptyState
      title="Логирование отключено"
      description="Включите логирование в настройках для записи событий."
    >
      {#snippet icon()}
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
          <path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
          <line x1="12" y1="9" x2="12" y2="13" />
          <circle cx="12" cy="17" r="1" fill="currentColor" />
        </svg>
      {/snippet}
      {#snippet action()}
        <Button variant="primary" size="md" href="/settings">Открыть настройки</Button>
      {/snippet}
    </EmptyState>
  </div>
{:else}
  <div class="terminal">
    <LogsToolbar
      bind:filter
      onFilterChange={applyFilter}
      {bucket}
      onBucketChange={setBucket}
      {paused}
      {bufferCount}
      onTogglePause={togglePause}
      onResume={resumeAndScroll}
      onCopy={handleCopy}
      onDownload={handleDownload}
      onClear={handleClear}
      totalEntries={$totalStore}
      visibleEntries={displayLogs.length}
      bufferStats={$statsStore}
      {availableSubgroups}
      {downloading}
      {clearing}
      searchInputRef={(el) => (searchInput = el)}
    />
    <div class="feed" bind:this={scrollEl} onscroll={onScroll}>
      {#if !paused}
        <div class="prompt-row" aria-hidden="true">
          <span class="prompt-sym">&gt;</span>
          <span class="cursor"></span>
        </div>
      {/if}
      {#each displayLogs as log (logKey(log))}
        {@const k = logKey(log)}
        <LogRow
          {log}
          expanded={expanded[k] ?? false}
          onToggleExpand={() => (expanded = { ...expanded, [k]: !expanded[k] })}
          onClickScope={handleClickScope}
          onClickLevel={handleClickLevel}
          onCopyLine={handleCopyLine}
          onCopyMessage={handleCopyMessage}
        />
      {/each}
      {#if displayLogs.length === 0}
        <div class="empty-feed">Нет записей по текущим фильтрам</div>
      {/if}
      {#if hasMore && displayLogs.length > 0}
        <div class="load-more-row">
          <button type="button" class="chip load-more" onclick={loadMore} disabled={loadingMore}>
            {loadingMore ? 'Загрузка…' : `Загрузить ещё ${nextBatch}`}
          </button>
        </div>
      {/if}
    </div>
  </div>
  <LogsContextMenu />
{/if}

<style>
  .terminal {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--color-border);
    border-radius: var(--radius);
    overflow: hidden;
    background: var(--color-bg-secondary);
  }

  .feed {
    flex: 1;
    background: var(--color-bg-primary);
    padding: 0.5rem 0.75rem;
    min-height: 60vh;
    max-height: 75vh;
    overflow-y: auto;
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: 12px;
  }

  .empty-feed {
    color: var(--color-text-muted);
    padding: 3rem 1rem;
    text-align: center;
    font-family: var(--font-sans);
  }

  .load-more-row {
    display: flex;
    justify-content: center;
    padding: 0.75rem 0 0.5rem;
    font-family: var(--font-sans);
  }

  .load-more {
    cursor: pointer;
  }

  .prompt-row {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.125rem 0.25rem 0.25rem 0.5rem;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
  }

  .prompt-sym {
    color: var(--color-accent);
    font-weight: 700;
  }

  .cursor {
    display: inline-block;
    width: 8px;
    height: 14px;
    background: var(--color-accent);
    animation: blink 1s steps(2) infinite;
    vertical-align: middle;
  }

  @keyframes blink {
    50% { opacity: 0; }
  }

  .terminal-loading,
  .terminal-empty {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 50vh;
  }
</style>
