<script lang="ts" module>
  import { m } from '$lib/i18n';
  import type { Snippet } from "svelte";
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import { FileJson, FilePen, ScrollText, Search, Settings } from "lucide-svelte";
  import { mode, setMode, type RouterMode } from "./modeStore";
  import { bindLiveConnectionsStore } from "./liveConnectionsStore";
  import { openDrawer } from "./drawerStore";
  import { singboxRouter as singboxRouterStore } from "$lib/stores/singboxRouter";
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
  const routerSettings = singboxRouterStore.settings;
  let activeEngine = $derived($routerSettings?.routingEngine === 'mihomo' ? 'mihomo' : 'sing-box');
  let activeEngineLabel = $derived(activeEngine === 'mihomo' ? 'Mihomo' : 'Sing-box');

  // Отпуск на размонтировании: иначе поток соединений живёт до конца сессии.
  onMount(() => bindLiveConnectionsStore());

  function selectMode(next: RouterMode) {
    setMode(next);
  }
</script>

<div class="sb-shell">
  <header class="sb-header">
    <div class="title-block">
      <h1 class="title">{m.sb_router_shell_title({ engine: activeEngineLabel })}</h1>
      {#if subtitle}<div class="subtitle">{subtitle}</div>{/if}
    </div>

    <div class="header-tools">
      <LiveConnectionsChip />

      <button
        type="button"
        class="params-btn"
        onclick={openDrawer}
        aria-label={m.sb_router_shell_params({ engine: activeEngineLabel })}
      >
        <Settings size={16} aria-hidden="true" />
        <span class="params-text">{m.sb_router_shell_params({ engine: activeEngineLabel })}</span>
      </button>

      <div class="header-actions">
        {#if onOpenInspector}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenInspector}
            aria-label={m.sb_router_shell_inspector_label()}
            title={m.sb_router_shell_inspector_label()}
          >
            <span class="action-icon"><Search size={16} /></span>
            <span class="action-text">{m.sb_router_shell_inspector()}</span>
          </button>
        {/if}
        {#if onOpenJson}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenJson}
            aria-label={m.sb_router_shell_json_label()}
            title={m.sb_router_shell_json_label()}
          >
            <span class="action-icon"><FileJson size={16} /></span>
            <span class="action-text">{m.sb_router_shell_json()}</span>
          </button>
        {/if}
        {#if onOpenConfigEditor}
          <button
            type="button"
            class="icon-btn"
            onclick={onOpenConfigEditor}
            aria-label={m.sb_router_shell_editor_label()}
            title={m.sb_router_shell_editor_title()}
          >
            <span class="action-icon"><FilePen size={16} /></span>
            <span class="action-text">{m.sb_router_shell_editor()}</span>
          </button>
        {/if}
        {#if onOpenLogs}
          <button
            type="button"
            class="icon-btn"
            class:icon-btn-active={logsActive}
            onclick={onOpenLogs}
            aria-pressed={logsActive}
            aria-label={logsActive ? m.sb_router_shell_logs_close({ engine: activeEngineLabel }) : m.sb_router_shell_logs_label({ engine: activeEngineLabel })}
            title={logsActive ? m.sb_router_shell_logs_close({ engine: activeEngineLabel }) : m.sb_router_shell_logs_label({ engine: activeEngineLabel })}
          >
            <span class="action-icon"><ScrollText size={16} /></span>
            <span class="action-text">{m.sb_router_shell_logs()}</span>
          </button>
        {/if}
      </div>

      <SegmentedControl
        value={currentMode}
        options={[
          { value: "beginner", label: m.sb_router_shell_mode_simple() },
          { value: "expert", label: m.sb_router_shell_mode_expert() },
        ] satisfies Array<{ value: RouterMode; label: string }>}
        ariaLabel={m.sb_router_shell_mode_label()}
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

  .header-tools :global(.live-chip),
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
    .header-tools :global(.live-chip),
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

    .header-tools :global(.live-chip),
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
