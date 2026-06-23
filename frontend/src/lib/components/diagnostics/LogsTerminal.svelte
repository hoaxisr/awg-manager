<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { TriangleAlert } from 'lucide-svelte';
  import { appLogEntries, singboxLogEntries, logStoreFor, type LogBucket, type LogStore } from '$lib/stores/logs';
  import { LoadingSpinner, EmptyState } from '$lib/components/layout';
  import { Button } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { usageLevel, settings } from '$lib/stores/settings';
  import { systemInfo } from '$lib/stores/system';
  import { copyToClipboard } from '$lib/utils/clipboard';
  import { stripAnsi } from '$lib/utils/ansi';
  import { formatDateTimeWithOffset } from '$lib/utils/format';
  import { diagnosticsSanitized, toggleDiagnosticsSanitized } from '$lib/stores/diagnosticsPrivacy';
  import { sanitizeLogEntry } from '$lib/utils/log-privacy';
  import LogRow from './LogRow.svelte';
  import LogsToolbar, { ALL_LEVELS } from './LogsToolbar.svelte';
  import LogsContextMenu from './LogsContextMenu.svelte';
  import type { LogsFilter } from './LogsToolbar.svelte';
  import type { LogEntry } from '$lib/types';

  // lockBucket: фиксирует журнал на один bucket (напр. FakeIP-чип «Журнал» —
  // только 'singbox') и прячет переключатель app/singbox в тулбаре. Не задан —
  // прежнее поведение (переключаемый, как на странице Диагностики).
  let { lockBucket }: { lockBucket?: LogBucket } = $props();

  const STORAGE_KEY = 'awgm.diagnostics.logsFilter';
  const BUCKET_KEY = 'awgm.diagnostics.logsBucket';
  const FULL_TIMESTAMP_KEY = 'awgm.diagnostics.logsFullTimestamp';
  const PAGE_SIZE = 200;
  type LogsQueryParams = {
    bucket: 'app' | 'singbox';
    groups: string[];
    subgroups: string[];
    limit: number;
    offset: number;
  };
  /** Min distance from top before auto-pause; also scales with viewport (see scrollPauseThreshold). */
  const SCROLL_THRESHOLD_MIN = 80;

  function normalizeStringArray(v: unknown): string[] {
    if (!Array.isArray(v)) return [];
    return v.filter((x): x is string => typeof x === 'string' && x.length > 0);
  }

  function defaultFilter(): LogsFilter {
    return { search: '', groups: [], subgroups: [], levels: [...ALL_LEVELS] };
  }

  let filterLoadWarning = false;

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
        groups:
          Array.isArray(parsed.groups)
            ? normalizeStringArray(parsed.groups)
            : (typeof parsed.group === 'string' && parsed.group ? [parsed.group] : []),
        subgroups:
          Array.isArray(parsed.subgroups)
            ? normalizeStringArray(parsed.subgroups)
            : (typeof parsed.subgroup === 'string' && parsed.subgroup ? [parsed.subgroup] : []),
        levels,
      };
    } catch {
      filterLoadWarning = true;
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

  function loadFullTimestamp(): boolean {
    if (typeof localStorage === 'undefined') return false;
    return localStorage.getItem(FULL_TIMESTAMP_KEY) === '1';
  }

  function saveFullTimestamp(v: boolean) {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(FULL_TIMESTAMP_KEY, v ? '1' : '0');
  }

  let filter = $state<LogsFilter>(loadFilter());
  // lockBucket — фиксированный проп (не меняется в рантайме): захват начального
  // значения — намеренный.
  // svelte-ignore state_referenced_locally
  let bucket = $state<LogBucket>(lockBucket ?? loadBucket());
  let showFullTimestamp = $state(loadFullTimestamp());
  let paused = $state(false);
  /** User clicked Pause — do not auto-resume when scrolled back to the top. */
  let manualPause = $state(false);
  let bufferCount = $state(0);
  let anchorScrollHeight = 0;
  let anchorScrollTop = 0;
  let downloading = $state(false);
  let clearing = $state(false);
  let expanded = $state<Record<string, boolean>>({});
  let scrollEl = $state<HTMLDivElement | null>(null);
  let searchInput = $state<HTMLInputElement | null>(null);
  let initialFetchDone = $state(false);
  let prevLen = $state(0);
  let pageOffset = $state(0);
  let manualFrozenLogs = $state<LogEntry[] | null>(null);

  /** Subgroup `profiling` is expert-only and only when -slow-request-ms > 0 at daemon start. */
  $effect(() => {
    if (!$settings) return;
    const profilingEnabled = ($systemInfo.data?.slowRequestThresholdMs ?? 0) > 0;
    if ($usageLevel === 'expert' && profilingEnabled) return;
    if (!filter.subgroups.includes('profiling')) return;
    (async () => {
      try {
        await applyFilter({ ...filter, subgroups: filter.subgroups.filter((s) => s !== 'profiling') });
      } catch {
        notifications.error('Не удалось сбросить фильтр profiling');
      }
    })();
  });
  let loadingMore = $state(false);
  let availableSubgroups = $state<string[]>([]);
  const subgroupCache = new Map<string, string[]>();

  const activeStore = $derived<LogStore>(logStoreFor(bucket));
  const privacyRevealAvailable = $derived(true);

  // Reactive subscriptions to the active store. $derived re-runs each time
  // the store identity changes (bucket toggle), so we re-subscribe naturally
  // through reactive store-reads in the template ($activeStore, etc.).
  const enabledStore = $derived(activeStore.enabled);
  const totalStore = $derived(activeStore.total);
  const loadedStore = $derived(activeStore.loaded);
  const statsStore = $derived(activeStore.stats);

  // Stable per-LogEntry id so DOM rows survive store mutations. Without
  // this, prepending a new entry shifts all index-based keys and Svelte
  // re-renders every row — which kills active text selection during a
  // tail-style live log feed.
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
  function buildLogQuery(limit: number, offset = 0): LogsQueryParams {
    if (bucket === 'singbox') {
      return {
        bucket,
        groups: ['singbox'],
        subgroups: filter.groups,
        limit,
        offset,
      };
    }

    return {
      bucket,
      groups: filter.groups,
      subgroups: filter.subgroups,
      limit,
      offset,
    };
  }

  async function loadBucketFresh(b: LogBucket) {
    const store = logStoreFor(b);
    pageOffset = 0;
    try {
      const query = b === bucket
        ? buildLogQuery(PAGE_SIZE, 0)
        : (b === 'singbox'
          ? { bucket: b, groups: ['singbox'], subgroups: [], limit: PAGE_SIZE, offset: 0 }
          : { bucket: b, groups: [], subgroups: [], limit: PAGE_SIZE, offset: 0 });
      const resp = await api.getLogs(query);
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
    const resp = await api.getLogsSubgroups(group);
    subgroupCache.set(group, resp.subgroups);
    return resp.subgroups;
  }

  async function refreshSubgroups() {
    if (bucket === 'singbox') {
      // Sing-box bucket flattens subgroups as the user-facing "groups" in the
      // toolbar — no separate subgroup row needed.
      availableSubgroups = [];
      return;
    }
    if (filter.groups.length === 0) {
      availableSubgroups = [];
      return;
    }
    try {
      const lists = await Promise.all(filter.groups.map((g) => fetchSubgroups(g)));
      const seen = new Set<string>();
      const merged: string[] = [];

      for (const list of lists) {
        for (const s of list) {
          if (seen.has(s)) continue;
          seen.add(s);
          merged.push(s);
        }
      }

      availableSubgroups = merged;
      const allowed = new Set(merged);
      const nextSubgroups = filter.subgroups.filter((s) => allowed.has(s));
      if (nextSubgroups.length !== filter.subgroups.length) {
        filter = { ...filter, subgroups: nextSubgroups };
        saveFilter(filter);
      }
    } catch {
      availableSubgroups = [];
      notifications.error('Не удалось загрузить список подгрупп журнала');
    }
  }

  onMount(async () => {
    if (filterLoadWarning) {
      notifications.warning('Не удалось прочитать сохранённые фильтры журнала, применены значения по умолчанию');
    }
    await loadBucketFresh(bucket);
    await refreshSubgroups();
    setTimeout(() => (initialFetchDone = true), 100);
    window.addEventListener('keydown', handleKeydown);
  });

  onDestroy(() => {
    window.removeEventListener('keydown', handleKeydown);
  });

  function scrollPauseThreshold(): number {
    if (!scrollEl) return SCROLL_THRESHOLD_MIN;
    // ~¾ viewport: scrolling “a page or two” away from the live head pauses follow.
    return Math.max(SCROLL_THRESHOLD_MIN, scrollEl.clientHeight * 0.75);
  }

  function onScroll() {
    if (!scrollEl) return;
    if (scrollEl.scrollTop > scrollPauseThreshold()) {
      paused = true;
    } else if (!manualPause) {
      paused = false;
      bufferCount = 0;
    }
  }

  // Keep the viewport anchored while paused — new SSE rows prepend at the top and
  // would otherwise push content under the scroll position.
  $effect.pre(() => {
    void $activeStore.length;
    if (scrollEl && paused && initialFetchDone) {
      anchorScrollHeight = scrollEl.scrollHeight;
      anchorScrollTop = scrollEl.scrollTop;
    }
  });

  $effect(() => {
    void $activeStore.length;
    if (!initialFetchDone || !scrollEl || !paused) return;
    void tick().then(() => {
      if (!scrollEl || !paused) return;
      const delta = scrollEl.scrollHeight - anchorScrollHeight;
      if (delta > 0 && scrollEl.scrollTop > 0) {
        scrollEl.scrollTop = anchorScrollTop + delta;
      }
    });
  });

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
    if (paused) {
      resumeAndScroll();
    } else {
      manualPause = true;
      manualFrozenLogs = filteredLogs.slice();
      paused = true;
    }
  }

  function resumeAndScroll() {
    manualPause = false;
    paused = false;
    bufferCount = 0;
    manualFrozenLogs = null;
    scrollEl?.scrollTo({ top: 0, behavior: 'smooth' });
  }

  function normalizeLogForOutput(log: LogEntry): LogEntry {
    const clean = {
      ...log,
      target: stripAnsi(log.target),
      message: stripAnsi(log.message),
    };
    const shouldSanitize = $diagnosticsSanitized;
    return shouldSanitize ? sanitizeLogEntry(clean) : clean;
  }

  function logForPrivacy(log: LogEntry): LogEntry {
    return normalizeLogForOutput(log);
  }

  async function applyFilter(f: LogsFilter) {
    // Filter changes should be predictable: drop manual pause snapshot and return to live mode.
    manualPause = false;
    paused = false;
    bufferCount = 0;
    manualFrozenLogs = null;
    filter = f;
    saveFilter(f);
    // Group changed → refresh subgroup catalog; subgroup change keeps catalog.
    await refreshSubgroups();
    await loadBucketFresh(bucket);
  }

  async function setBucket(b: LogBucket) {
    if (lockBucket) return; // bucket зафиксирован — переключение запрещено
    if (b === bucket) return;
    manualPause = false;
    paused = false;
    bufferCount = 0;
    manualFrozenLogs = null;
    bucket = b;
    saveBucket(b);
    // Reset filters on bucket switch — group sets are disjoint per bucket.
    filter = { ...filter, groups: [], subgroups: [] };
    saveFilter(filter);
    await loadBucketFresh(b);
    await refreshSubgroups();
  }

  const filteredLogs = $derived.by(() => {
    let arr: LogEntry[] = $activeStore;
    if (filter.levels.length > 0 && filter.levels.length < ALL_LEVELS.length) {
      const set = new Set(filter.levels);
      arr = arr.filter((l) => set.has(l.level));
    }
    if (bucket === 'singbox') {
      if (filter.groups.length > 0) {
        const set = new Set(filter.groups);
        arr = arr.filter((l) => set.has(l.subgroup));
      }
    } else {
      if (filter.groups.length > 0) {
        const set = new Set(filter.groups);
        arr = arr.filter((l) => set.has(l.group));
      }
      if (filter.subgroups.length > 0) {
        const set = new Set(filter.subgroups);
        arr = arr.filter((l) => set.has(l.subgroup));
      }
    }
    if (filter.search) {
      const q = filter.search.toLowerCase();
      arr = arr.filter((l) => {
        const visible = logForPrivacy(l);
        return (
          visible.message.toLowerCase().includes(q) ||
          visible.target.toLowerCase().includes(q) ||
          visible.action.toLowerCase().includes(q)
        );
      });
    }
    return arr;
  });

  const displayLogs = $derived.by(() => {
    if (manualPause && manualFrozenLogs) return manualFrozenLogs;
    return filteredLogs;
  });

  async function handleClickScope(group: string, subgroup: string) {
    if (bucket === 'singbox') {
      filter = { ...filter, groups: subgroup ? [subgroup] : [], subgroups: [] };
    } else {
      filter = { ...filter, groups: group ? [group] : [], subgroups: subgroup ? [subgroup] : [] };
    }
    saveFilter(filter);
    await refreshSubgroups();
    await loadBucketFresh(bucket);
  }

  function handleClickLevel(level: string) {
    filter = { ...filter, levels: [level] };
    saveFilter(filter);
  }

  function toggleFullTimestamp() {
    showFullTimestamp = !showFullTimestamp;
    saveFullTimestamp(showFullTimestamp);
  }

  async function handleToggleSanitizeLogs() {
    toggleDiagnosticsSanitized();
    manualPause = false;
    paused = false;
    bufferCount = 0;
    manualFrozenLogs = null;
    await loadBucketFresh(bucket);
  }

  function formatLine(log: LogEntry, routerOffset: number): string {
    const visible = logForPrivacy(log);
    const scope = visible.subgroup ? `${visible.group}/${visible.subgroup}` : visible.group;
    const t = formatDateTimeWithOffset(visible.timestamp, routerOffset);
    return `[${t}] [${visible.level.toUpperCase()}] [${scope}] ${visible.action} ${visible.target}: ${visible.message}`;
  }

  function getRouterOffsetOrWarn(): number | null {
    const routerOffset = $systemInfo.data?.routerTimezoneOffsetMinutes;
    if (routerOffset === undefined || routerOffset === null || !Number.isFinite(routerOffset)) {
      notifications.warning('Время роутера ещё не загружено, попробуйте через несколько секунд');
      return null;
    }
    return routerOffset;
  }

  async function getFreshRouterClockOrWarn(): Promise<{ routerTime: string; routerOffset: number } | null> {
    await systemInfo.refetch();

    const routerTime = $systemInfo.data?.routerTime;
    const routerOffset = $systemInfo.data?.routerTimezoneOffsetMinutes;

    if (!routerTime || routerOffset === undefined || routerOffset === null || !Number.isFinite(routerOffset)) {
      notifications.warning('Время роутера ещё не загружено, попробуйте через несколько секунд');
      return null;
    }

    return { routerTime, routerOffset };
  }

  function formatRouterClockFilenameStamp(routerTime: string, routerOffset: number): string {
    return formatDateTimeWithOffset(routerTime, routerOffset)
      .replace(' ', '-')
      .replace(/:/g, '-');
  }

  async function copyText(text: string, successMsg: string) {
    if (await copyToClipboard(text)) {
      notifications.success(successMsg);
    } else {
      notifications.error('Не удалось скопировать');
    }
  }

  async function handleCopy() {
    const routerOffset = getRouterOffsetOrWarn();
    if (routerOffset === null) return;
    const text = displayLogs.map((log) => formatLine(log, routerOffset)).join('\n');
    await copyText(text, 'Скопировано в буфер обмена');
  }

  function handleCopyLine(log: LogEntry) {
    const routerOffset = getRouterOffsetOrWarn();
    if (routerOffset === null) return;
    copyText(formatLine(log, routerOffset), 'Строка скопирована');
  }

  function handleCopyMessage(text: string) {
    copyText(text, 'Сообщение скопировано');
  }

  function matchesCurrentVisibleFilters(log: LogEntry, currentFilter: LogsFilter): boolean {
    if (currentFilter.levels.length > 0 && currentFilter.levels.length < ALL_LEVELS.length) {
      const set = new Set(currentFilter.levels);
      if (!set.has(log.level)) return false;
    }

    if (bucket === 'singbox') {
      if (currentFilter.groups.length > 0) {
        const set = new Set(currentFilter.groups);
        if (!set.has(log.subgroup)) return false;
      }
    } else {
      if (currentFilter.groups.length > 0) {
        const set = new Set(currentFilter.groups);
        if (!set.has(log.group)) return false;
      }
      if (currentFilter.subgroups.length > 0) {
        const set = new Set(currentFilter.subgroups);
        if (!set.has(log.subgroup)) return false;
      }
    }

    if (currentFilter.search) {
      const q = currentFilter.search.toLowerCase();
      const visible = logForPrivacy(log);
      return (
        visible.message.toLowerCase().includes(q) ||
        visible.target.toLowerCase().includes(q) ||
        visible.action.toLowerCase().includes(q)
      );
    }

    return true;
  }

  async function handleDownload() {
    downloading = true;
    try {
      const clock = await getFreshRouterClockOrWarn();
      if (clock === null) return;

      const resp = await api.getLogs(buildLogQuery($totalStore || 10000, 0));
      const logs = resp.logs.filter((log) => matchesCurrentVisibleFilters(log, filter));
      const text = logs.map((log) => formatLine(log, clock.routerOffset)).join('\n');
      const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const stamp = formatRouterClockFilenameStamp(clock.routerTime, clock.routerOffset);
      const a = document.createElement('a');
      a.href = url;
      a.download = `awg-manager-${bucket}-logs-${stamp}.txt`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      notifications.success(`Скачано ${logs.length} записей`);
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
      const resp = await api.getLogs(buildLogQuery(PAGE_SIZE, pageOffset));
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
        <TriangleAlert size={48} aria-hidden="true" />
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
      bucketLocked={!!lockBucket}
      onBucketChange={setBucket}
      {paused}
      {bufferCount}
      onTogglePause={togglePause}
      onResume={resumeAndScroll}
      onCopy={handleCopy}
      onDownload={handleDownload}
      onClear={handleClear}
      {showFullTimestamp}
      onToggleFullTimestamp={toggleFullTimestamp}
      sanitizeLogs={$diagnosticsSanitized}
      onToggleSanitizeLogs={handleToggleSanitizeLogs}
      sanitizeToggleAvailable={privacyRevealAvailable}
      sanitizeToggleHint=""
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
        {@const k = logKey(log) /* WeakMap returns the same id; reuse for expanded[] */}
        {@const visibleLog = logForPrivacy(log)}
        <LogRow
          log={visibleLog}
          routerOffset={$systemInfo.data?.routerTimezoneOffsetMinutes}
          showFullTimestamp={showFullTimestamp}
          expanded={expanded[k] ?? false}
          onToggleExpand={() => (expanded = { ...expanded, [k]: !expanded[k] })}
          onClickScope={handleClickScope}
          onClickLevel={handleClickLevel}
          onCopyLine={() => handleCopyLine(log)}
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
