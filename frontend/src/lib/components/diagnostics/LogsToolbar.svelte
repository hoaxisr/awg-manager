<script lang="ts" module>
  export interface LogsFilter {
    search: string;
    group: string;
    subgroup: string;
    levels: string[];
  }

  export const ALL_LEVELS = ['error', 'warn', 'info', 'full', 'debug'] as const;
  export const ALL_GROUPS = ['tunnel', 'routing', 'server', 'system'] as const;
</script>

<script lang="ts">
  import { Badge, StatusDot, Modal, Button } from '$lib/components/ui';

  interface Props {
    filter: LogsFilter;
    onFilterChange: (filter: LogsFilter) => void;
    paused: boolean;
    bufferCount: number;
    onTogglePause: () => void;
    onResume?: () => void;
    onCopy: () => void;
    onDownload: () => void;
    onClear: () => void;
    totalEntries: number;
    visibleEntries: number;
    downloading?: boolean;
    clearing?: boolean;
    searchInputRef?: (el: HTMLInputElement) => void;
  }

  let {
    filter = $bindable(),
    onFilterChange,
    paused,
    bufferCount,
    onTogglePause,
    onResume,
    onCopy,
    onDownload,
    onClear,
    totalEntries,
    visibleEntries,
    downloading = false,
    clearing = false,
    searchInputRef,
  }: Props = $props();

  const levelLabel: Record<string, string> = {
    error: 'ERROR',
    warn: 'WARN',
    info: 'INFO',
    full: 'FULL',
    debug: 'DEBUG',
  };

  function toggleLevel(lvl: string) {
    const set = new Set(filter.levels);
    if (set.has(lvl)) set.delete(lvl);
    else set.add(lvl);
    filter.levels = Array.from(set);
    onFilterChange({ ...filter });
  }

  function selectGroup(g: string) {
    if (filter.group === g) return;
    filter.group = g;
    filter.subgroup = '';
    onFilterChange({ ...filter });
  }

  let searchTimeout: ReturnType<typeof setTimeout> | null = null;
  function handleSearchInput(v: string) {
    filter.search = v;
    if (searchTimeout) clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => onFilterChange({ ...filter }), 300);
  }

  let confirmClearOpen = $state(false);

  function handleClear() {
    confirmClearOpen = true;
  }

  function confirmClear() {
    confirmClearOpen = false;
    onClear();
  }

  let searchEl = $state<HTMLInputElement | null>(null);
  $effect(() => {
    if (searchEl && searchInputRef) searchInputRef(searchEl);
  });
</script>

<div class="toolbar">
  <div class="row row-chips">
    <span class="live-cell">
      {#if paused}
        <Badge variant="warning" size="sm">PAUSED</Badge>
        {#if bufferCount > 0}
          <button type="button" class="buffer-chip" onclick={onResume}>
            +{bufferCount} ↑
          </button>
        {/if}
      {:else}
        <StatusDot variant="success" pulse size="sm" />
        <span class="live-label">LIVE</span>
      {/if}
    </span>

    <span class="divider" aria-hidden="true"></span>

    <span class="chip-row" role="group" aria-label="Фильтр по уровню">
      {#each ALL_LEVELS as lvl (lvl)}
        {@const active = filter.levels.includes(lvl)}
        <button
          type="button"
          class="chip chip-level-{lvl}"
          class:chip-active={active}
          aria-pressed={active}
          onclick={() => toggleLevel(lvl)}
        >
          {levelLabel[lvl]}
        </button>
      {/each}
    </span>

    <span class="divider" aria-hidden="true"></span>

    <span class="chip-row" role="group" aria-label="Фильтр по группе">
      <button
        type="button"
        class="chip chip-group-pill"
        class:chip-active={!filter.group}
        aria-pressed={!filter.group}
        onclick={() => selectGroup('')}
      >
        ALL
      </button>
      {#each ALL_GROUPS as g (g)}
        {@const active = filter.group === g}
        <button
          type="button"
          class="chip chip-group-pill"
          class:chip-active={active}
          aria-pressed={active}
          onclick={() => selectGroup(g)}
        >
          {g}
        </button>
      {/each}
    </span>
  </div>

  <div class="row row-actions">
    <input
      bind:this={searchEl}
      type="search"
      placeholder="Поиск..."
      bind:value={filter.search}
      oninput={(e) => handleSearchInput((e.currentTarget as HTMLInputElement).value)}
      class="search"
    />

    <span class="actions">
      <button type="button" class="chip" onclick={onTogglePause}>
        {paused ? 'Resume' : 'Pause'}
      </button>
      <button type="button" class="chip" onclick={onCopy} disabled={visibleEntries === 0}>
        Copy
      </button>
      <button type="button" class="chip" onclick={onDownload} disabled={totalEntries === 0 || downloading}>
        {downloading ? 'Downloading…' : 'Download'}
      </button>
      <button type="button" class="chip chip-danger" onclick={handleClear} disabled={totalEntries === 0 || clearing}>
        {clearing ? 'Clearing…' : 'Clear'}
      </button>
    </span>
  </div>
</div>

<Modal
  open={confirmClearOpen}
  title="Очистить журнал"
  size="sm"
  onclose={() => (confirmClearOpen = false)}
>
  <p class="confirm-text">
    Удалить <strong>{totalEntries}</strong> {totalEntries === 1 ? 'запись' : (totalEntries < 5 ? 'записи' : 'записей')} из журнала? Это действие нельзя отменить.
  </p>
  <p class="confirm-hint">
    Логирование продолжится: новые события появятся по мере работы приложения.
  </p>
  {#snippet actions()}
    <Button variant="ghost" size="md" onclick={() => (confirmClearOpen = false)}>Отмена</Button>
    <Button variant="danger" size="md" onclick={confirmClear}>Очистить</Button>
  {/snippet}
</Modal>

<style>
  /* Layout-only. Chip colors / states are design-system primitives in app.css. */
  .confirm-text {
    margin: 0 0 0.5rem;
    font-size: 13px;
    color: var(--color-text-primary);
  }
  .confirm-hint {
    margin: 0;
    font-size: 12px;
    color: var(--color-text-muted);
  }

  .toolbar {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    background: var(--color-bg-secondary);
    border-bottom: 1px solid var(--color-border);
  }

  .row {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    flex-wrap: wrap;
  }

  .live-cell {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 12px;
  }

  .live-label {
    color: var(--color-text-muted);
    font-weight: 600;
    font-size: 11px;
    letter-spacing: 0.05em;
    text-transform: uppercase;
  }

  .divider {
    width: 1px;
    align-self: stretch;
    background: var(--color-border);
    margin: 0 0.125rem;
  }

  .chip-row {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    flex-wrap: wrap;
  }

  .buffer-chip {
    background: var(--color-accent);
    color: white;
    border: none;
    border-radius: var(--radius-pill);
    padding: 0.125rem 0.5rem;
    font: inherit;
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
  }
  .buffer-chip:hover { filter: brightness(1.1); }

  .search {
    flex: 1;
    min-width: 200px;
    background: var(--color-bg-primary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-pill);
    color: var(--color-text-primary);
    font: inherit;
    font-size: 12px;
    padding: 0.3125rem 0.75rem;
    line-height: 1.4;
  }
  .search:focus {
    outline: none;
    border-color: var(--color-accent);
  }
  .search::placeholder { color: var(--color-text-muted); }

  .actions {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    margin-left: auto;
    flex-wrap: wrap;
  }

  @media (max-width: 640px) {
    .search {
      flex-basis: 100%;
      min-width: 0;
    }
  }
</style>
