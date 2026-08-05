<script lang="ts" module>
  import type { Snippet } from "svelte";
</script>

<script lang="ts">
  import { FileJson, FilePen, ScrollText, Search, Settings } from "lucide-svelte";
  import { mode, setMode, type RouterMode } from "./modeStore";
  import { openDrawer } from "./drawerStore";
  import StatusDrawer from "./StatusDrawer.svelte";
  import SourceDrawer from "./SourceDrawer.svelte";
  import LiveConnectionsChip from "./LiveConnectionsChip.svelte";
  import { SegmentedControl } from "$lib/components/ui";

  interface Props {
    /** Дополнительный subtitle под title (опционально). */
    subtitle?: string;
    /** Открыть инспектор маршрута (кнопка в шапке рендерится только если задан). */
    onOpenInspector?: () => void;
    /** Открыть JSON-конфиг (кнопка в шапке рендерится только если задан). */
    onOpenJson?: () => void;
    /** Открыть редактор конфигурации config.d (эксперт; рендерится только если задан). */
    onOpenConfigEditor?: () => void;
    /** Открыть/закрыть логи sing-box (кнопка в шапке рендерится только если задан). */
    onOpenLogs?: () => void;
    /** Лог-вью сейчас открыт — кнопка «Логи» подсвечивается как активная. */
    logsActive?: boolean;
    /** Дочерний контент страницы. */
    children: Snippet;
  }

  let { subtitle, onOpenInspector, onOpenJson, onOpenConfigEditor, onOpenLogs, logsActive = false, children }: Props = $props();
  let currentMode = $derived($mode);

  // Clash-WS живых соединений биндит layout группы Sing-box
  // (routes/sb/+layout.svelte): счётчик соединений нужен бейджам сайдбара на
  // всех страницах группы, а не только там, где смонтирован этот каркас.

  function selectMode(next: RouterMode) {
    setMode(next);
  }
</script>

<div class="sb-shell">
  <header class="sb-header">
    <div class="title-block">
      <h1 class="title">Маршрутизация · sing-box</h1>
      {#if subtitle}<div class="subtitle">{subtitle}</div>{/if}
    </div>

    <div class="header-tools">
      <LiveConnectionsChip />

      <button
        type="button"
        class="params-btn"
        onclick={openDrawer}
        aria-label="Параметры sing-box"
      >
        <Settings size={16} aria-hidden="true" />
        <span class="params-text">Параметры sing-box</span>
      </button>

      <div class="header-actions">
        {#if onOpenInspector}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenInspector}
            aria-label="Инспектор маршрута"
            title="Инспектор маршрута"
          >
            <span class="action-icon"><Search size={16} /></span>
            <span class="action-text">Инспектор</span>
          </button>
        {/if}
        {#if onOpenJson}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenJson}
            aria-label="JSON-конфиг"
            title="JSON-конфиг"
          >
            <span class="action-icon"><FileJson size={16} /></span>
            <span class="action-text">Конфиг</span>
          </button>
        {/if}
        {#if onOpenConfigEditor}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenConfigEditor}
            aria-label="Редактор конфигурации"
            title="Редактор конфигурации (config.d)"
          >
            <span class="action-icon"><FilePen size={16} /></span>
            <span class="action-text">Редактор</span>
          </button>
        {/if}
        {#if onOpenLogs}
          <button
            type="button"
            class="icon-btn"
            class:icon-btn-active={logsActive}
            onclick={onOpenLogs}
            aria-pressed={logsActive}
            aria-label={logsActive ? 'Закрыть логи sing-box' : 'Логи sing-box'}
            title={logsActive ? 'Закрыть логи sing-box' : 'Логи sing-box'}
          >
            <span class="action-icon"><ScrollText size={16} /></span>
            <span class="action-text">Логи</span>
          </button>
        {/if}
      </div>

      <SegmentedControl
        value={currentMode}
        options={[
          { value: "beginner", label: "Простой" },
          { value: "expert", label: "Эксперт" },
        ] satisfies Array<{ value: RouterMode; label: string }>}
        ariaLabel="Режим интерфейса"
        onchange={selectMode}
      />
    </div>
  </header>

  <div class="sb-body">
    {@render children()}
  </div>
</div>

<StatusDrawer />
<SourceDrawer />

<style>
  .sb-shell {
    display: flex;
    flex-direction: column;
  }

  .sb-header {
    display: flex;
    align-items: center;
    gap: var(--sp-4);
    padding-bottom: var(--sp-3);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--sp-3);
    flex-wrap: wrap;
  }

  .title-block {
    flex: 1 1 auto;
    min-width: 0;
  }
  .title {
    margin: 0;
    font-size: var(--fs-h3);
    font-weight: 600;
    color: var(--text-primary);
    line-height: var(--lh-tight);
  }
  .subtitle {
    margin-top: 2px;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }

  .header-tools {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
    flex-wrap: wrap;
  }

  .header-tools :global(.chip),
  .params-btn,
  .header-actions .icon-btn,
  .header-tools :global(.segmented-control) {
    height: 32px;
    box-sizing: border-box;
  }

  .params-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 0 10px;
    border-radius: var(--radius-sm);
    background: var(--color-bg-secondary, var(--bg-secondary));
    border: 1px solid var(--color-border, var(--border));
    color: var(--color-text-secondary, var(--text-secondary));
    font-size: 12px;
    font-weight: 500;
    font-family: inherit;
    cursor: pointer;
    white-space: nowrap;
    flex-shrink: 0;
  }
  .params-btn:hover {
    color: var(--color-text-primary, var(--text-primary));
    background: var(--color-bg-hover, var(--bg-tertiary));
    border-color: var(--border-hover, var(--accent-line));
  }
  .params-btn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }

  .icon-btn {
    background: var(--color-bg-secondary, var(--bg-secondary));
    border: 1px solid var(--color-border, var(--border));
    color: var(--color-text-secondary, var(--text-secondary));
    padding: 0 8px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .action-text {
    display: none;
  }
  .icon-btn:hover {
    color: var(--color-text-primary, var(--text-primary));
    background: var(--color-bg-hover, var(--bg-tertiary));
  }
  .icon-btn-active,
  .icon-btn-active:hover {
    color: var(--color-text-primary, var(--text-primary));
    border-color: var(--accent);
  }

  .header-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  .sb-body {
    width: 100%;
  }

  @media (max-width: 768px) {
    .sb-header {
      flex-direction: column;
      align-items: stretch;
      gap: 10px;
    }
    .title-block {
      text-align: left;
      padding-bottom: 12px;
      border-bottom: 1px solid var(--border);
    }
    .title {
      font-size: 18px;
    }
    .header-tools {
      width: 100%;
      flex-direction: column;
      align-items: stretch;
      gap: 8px;
    }
    .params-btn,
    .header-tools :global(.chip),
    .header-tools :global(.segmented-control) {
      width: 100%;
      flex: none;
      justify-content: center;
    }
    .header-tools :global(.segmented-control-btn) {
      flex: 1;
      min-width: 0;
    }
    .header-actions {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      align-items: stretch;
      gap: 0.5rem;
      width: 100%;
      min-width: 0;
    }

    .header-actions .icon-btn {
      width: 100%;
      min-width: 0;
      max-width: 100%;
      justify-content: center;
      gap: 0.375rem;
      padding-inline: 0.625rem;
      white-space: nowrap;
    }

    .header-actions .action-text {
      display: inline;
      font-size: var(--fs-sm);
      font-weight: 500;
    }
  }

  @media (max-width: 700px) {
    .sb-header {
      display: grid;
      grid-template-columns: 1fr;
      align-items: stretch;
      gap: 0.625rem;
    }

    .header-tools {
      width: 100%;
      min-width: 0;
      flex-direction: column;
      align-items: stretch;
      gap: 0.5rem;
    }

    .header-tools :global(.chip),
    .params-btn,
    .header-tools :global(.segmented-control) {
      width: 100%;
      flex: none;
      min-width: 0;
      justify-content: center;
    }

    .header-tools :global(.segmented-control-btn) {
      flex: 1;
      min-width: 0;
    }
  }
</style>
