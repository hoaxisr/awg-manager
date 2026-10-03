<script lang="ts" module>
  import type { LogEntry } from '$lib/types';
</script>

<script lang="ts">
  import { m } from '$lib/i18n';
  import { openContextMenu } from './log-row-context-menu';
  import { formatDateTimeWithOffset, formatTime } from '$lib/utils/format';
  import { familyOf } from './subgroup-palette';
  import { stripAnsi } from '$lib/utils/ansi';

  interface Props {
    log: LogEntry;
    routerOffset?: number | null;
    showFullTimestamp?: boolean;
    expanded?: boolean;
    onToggleExpand?: () => void;
    onClickScope?: (group: string, subgroup: string) => void;
    onClickLevel?: (level: string) => void;
    onCopyLine?: (log: LogEntry) => void;
    onCopyMessage?: (text: string) => void;
  }

  let {
    log,
    routerOffset,
    showFullTimestamp = false,
    expanded = false,
    onToggleExpand,
    onClickScope,
    onClickLevel,
    onCopyLine,
    onCopyMessage,
  }: Props = $props();

  const isExpanded = $derived(expanded || log.level === 'error' || log.level === 'warn');
  const fullTimestamp = $derived(
    formatDateTimeWithOffset(log.timestamp, routerOffset ?? undefined),
  );
  function formatTimeWithOffset(timestamp: string, offsetMinutes?: number | null): string {
    const date = new Date(timestamp);
    if (isNaN(date.getTime())) return timestamp;

    const pad = (n: number) => String(n).padStart(2, '0');

    if (offsetMinutes === undefined || offsetMinutes === null || !Number.isFinite(offsetMinutes)) {
      return formatTime(timestamp);
    }

    const shifted = new Date(date.getTime() + offsetMinutes * 60_000);
    return `${pad(shifted.getUTCHours())}:${pad(shifted.getUTCMinutes())}:${pad(shifted.getUTCSeconds())}`;
  }
  const formattedTimestamp = $derived(
    showFullTimestamp
      ? fullTimestamp
      : formatTimeWithOffset(log.timestamp, routerOffset),
  );

  const subgroupFamily = $derived(familyOf(log.subgroup));

  // Схлопнутые повторы: бейдж «×N» = всего появлений записи; тултип —
  // время последнего повтора (timestamp строки — первое появление).
  const repeatTitle = $derived(
    log.lastSeen
      ? m.diag_log_repeated_last({ time: formatDateTimeWithOffset(log.lastSeen, routerOffset ?? undefined) })
      : m.diag_log_repeated(),
  );

  // Sing-box stderr lines (and any other ANSI-emitting source) may carry
  // raw colour escapes. Strip at the render boundary — sing-box has no
  // config-level switch to suppress colour, and its CLI --disable-color
  // is reportedly buggy (issue #423), so the backend keeps raw bytes and
  // the frontend decorates for display.
  const cleanMessage = $derived(stripAnsi(log.message));

  const levelLabel: Record<string, string> = {
    error: 'ERROR',
    warn: 'WARN',
    info: 'INFO',
    full: 'FULL',
    debug: 'DEBUG',
  };

  function handleClickScope(e: MouseEvent) {
    e.stopPropagation();
    onClickScope?.(log.group, log.subgroup);
  }

  function handleClickLevel(e: MouseEvent) {
    e.stopPropagation();
    onClickLevel?.(log.level);
  }

  function handleContextMenu(e: MouseEvent) {
    openContextMenu(e, log, {
      onCopyLine: () => onCopyLine?.(log),
      onCopyMessage: () => onCopyMessage?.(cleanMessage),
      onFilterScope: () => onClickScope?.(log.group, log.subgroup),
      onFilterLevel: () => onClickLevel?.(log.level),
    });
  }

  function handleRowClick() {
    onToggleExpand?.();
  }

  function handleRowKey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onToggleExpand?.();
    }
  }
</script>

<div
  class="row"
  class:level-error={log.level === 'error'}
  class:level-warn={log.level === 'warn'}
  class:level-info={log.level === 'info'}
  class:level-full={log.level === 'full'}
  class:level-debug={log.level === 'debug'}
  class:expanded={isExpanded}
  oncontextmenu={handleContextMenu}
  onclick={handleRowClick}
  onkeydown={handleRowKey}
  role="button"
  tabindex="0"
  aria-expanded={isExpanded}
>
  <span
    class="time"
    class:time-full={showFullTimestamp}
    title={fullTimestamp}
  >
    {formattedTimestamp}
  </span>
  <button
    type="button"
    class="level-chip level-chip-{log.level}"
    onclick={handleClickLevel}
    aria-label={m.diag_log_filter_level_aria({ level: levelLabel[log.level] ?? log.level })}
  >
    {levelLabel[log.level] ?? log.level.toUpperCase()}
  </button>
  <button
    type="button"
    class="scope-chip"
    onclick={handleClickScope}
    aria-label={m.diag_log_filter_scope_aria({ scope: log.group + (log.subgroup ? '/' + log.subgroup : '') })}
  >
    <span class="scope-group">{log.group}</span>
    {#if log.subgroup}
      <span class="subgroup-pill" data-family={subgroupFamily ?? 'unknown'}>{log.subgroup}</span>
    {/if}
  </button>
  <span class="action">{log.action}</span>
  <span class="target">{log.target}</span>
  <span class="arrow">→</span>
  <span class="message" class:truncate={!isExpanded}>{cleanMessage}</span>
  {#if (log.repeats ?? 0) > 0}
    <span class="repeat-badge" title={repeatTitle}>×{(log.repeats ?? 0) + 1}</span>
  {/if}
</div>

<style>
  .row {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    width: 100%;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    border-left: 2px solid transparent;
    padding: 0.125rem 0.25rem 0.125rem 0.5rem;
    cursor: pointer;
    text-align: left;
    color: inherit;
    animation: log-row-enter 200ms ease-out both;
  }

  @keyframes log-row-enter {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }

  @media (prefers-reduced-motion: reduce) {
    .row { animation: none; }
  }

  .row:hover {
    background: var(--color-bg-hover);
  }

  .row:focus-visible {
    outline: 2px solid var(--color-accent);
    outline-offset: -2px;
  }

  .row.level-error { border-left-color: var(--color-error); }
  .row.level-warn { border-left-color: var(--color-warning); }

  .repeat-badge {
    flex: 0 0 auto;
    font-size: 11px;
    line-height: 1.4;
    padding: 0 5px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--color-border);
    background: var(--color-bg-secondary);
    color: var(--color-text-muted);
    white-space: nowrap;
  }

  .time {
    color: var(--color-text-muted);
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }

  .time-full {
    min-width: 25ch;
  }

  .level-chip {
    display: inline-block;
    background: transparent;
    border: none;
    font: inherit;
    font-weight: 700;
    padding: 0;
    cursor: pointer;
    white-space: nowrap;
  }
  .level-chip:hover { text-decoration: underline; }

  .level-chip-error { color: var(--color-error); }
  .level-chip-warn { color: var(--color-warning); }
  .level-chip-info { color: var(--color-accent); }
  .level-chip-full { color: var(--color-info); }
  .level-chip-debug { color: var(--color-text-muted); }

  .scope-chip {
    display: inline-flex;
    align-items: baseline;
    gap: 0.25rem;
    background: transparent;
    border: none;
    font: inherit;
    color: var(--color-text-muted);
    padding: 0;
    cursor: pointer;
    white-space: nowrap;
  }
  .scope-chip:hover .scope-group { color: var(--color-accent); text-decoration: underline; }

  .scope-group {
    color: var(--color-text-muted);
  }

  .action { color: var(--color-text-secondary); white-space: nowrap; }
  .target { color: var(--color-text-primary); white-space: nowrap; }
  .arrow { color: var(--color-text-muted); }

  .message {
    flex: 1;
    color: var(--color-text-primary);
    word-break: break-word;
  }
  .row.level-debug .message { color: var(--color-text-muted); }

  .truncate {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row.expanded {
    background: rgba(255, 255, 255, 0.02);
    padding-bottom: 0.25rem;
  }

  :global(html.light) .row.expanded,
  :global([data-theme="light"]) .row.expanded {
    background: rgba(0, 0, 0, 0.03);
  }

  @media (max-width: 640px) {
    .row {
      flex-wrap: wrap;
    }
    .time-full {
      min-width: auto;
    }
    .arrow {
      display: none;
    }
    .message {
      flex-basis: 100%;
      padding-left: 0.25rem;
    }
  }
</style>
