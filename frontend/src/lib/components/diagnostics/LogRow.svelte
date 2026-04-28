<script lang="ts" module>
  import type { LogEntry } from '$lib/types';
</script>

<script lang="ts">
  import { openContextMenu } from './log-row-context-menu';
  import { formatTime } from '$lib/utils/format';

  interface Props {
    log: LogEntry;
    expanded?: boolean;
    onToggleExpand?: () => void;
    onClickScope?: (group: string, subgroup: string) => void;
    onClickLevel?: (level: string) => void;
    onCopyLine?: (text: string) => void;
    onCopyMessage?: (text: string) => void;
  }

  let {
    log,
    expanded = false,
    onToggleExpand,
    onClickScope,
    onClickLevel,
    onCopyLine,
    onCopyMessage,
  }: Props = $props();

  const isExpanded = $derived(expanded || log.level === 'error' || log.level === 'warn');

  const levelLabel: Record<string, string> = {
    error: 'ERROR',
    warn: 'WARN',
    info: 'INFO',
    full: 'FULL',
    debug: 'DEBUG',
  };

  const formattedLine = $derived(
    `[${formatTime(log.timestamp)}] [${(levelLabel[log.level] ?? log.level).toUpperCase()}] [${log.group}${log.subgroup ? '/' + log.subgroup : ''}] ${log.action} ${log.target}: ${log.message}`,
  );

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
      onCopyLine: () => onCopyLine?.(formattedLine),
      onCopyMessage: () => onCopyMessage?.(log.message),
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
  <span class="time">{formatTime(log.timestamp)}</span>
  <button
    type="button"
    class="level-chip level-chip-{log.level}"
    onclick={handleClickLevel}
    aria-label="Фильтр по уровню {levelLabel[log.level] ?? log.level}"
  >
    {levelLabel[log.level] ?? log.level.toUpperCase()}
  </button>
  <button
    type="button"
    class="scope-chip"
    onclick={handleClickScope}
    aria-label="Фильтр по scope {log.group}{log.subgroup ? '/' + log.subgroup : ''}"
  >
    {log.group}{log.subgroup ? '/' + log.subgroup : ''}
  </button>
  <span class="action">{log.action}</span>
  <span class="target">{log.target}</span>
  <span class="arrow">→</span>
  <span class="message" class:truncate={!isExpanded}>{log.message}</span>
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

  .time { color: var(--color-text-muted); white-space: nowrap; }

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
    background: transparent;
    border: none;
    font: inherit;
    color: var(--color-text-muted);
    padding: 0;
    cursor: pointer;
    white-space: nowrap;
  }
  .scope-chip:hover { color: var(--color-accent); text-decoration: underline; }

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
    .arrow {
      display: none;
    }
    .message {
      flex-basis: 100%;
      padding-left: 0.25rem;
    }
  }
</style>
