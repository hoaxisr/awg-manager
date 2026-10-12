<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount, onDestroy } from 'svelte';
  import { contextMenu, closeContextMenu } from './log-row-context-menu';

  let menuEl: HTMLDivElement | null = null;

  function handleDocClick(e: MouseEvent) {
    if (menuEl && !menuEl.contains(e.target as Node)) {
      closeContextMenu();
    }
  }

  function handleEsc(e: KeyboardEvent) {
    if (e.key === 'Escape') closeContextMenu();
  }

  onMount(() => {
    document.addEventListener('mousedown', handleDocClick);
    document.addEventListener('keydown', handleEsc);
  });

  onDestroy(() => {
    document.removeEventListener('mousedown', handleDocClick);
    document.removeEventListener('keydown', handleEsc);
  });
</script>

{#if $contextMenu.open}
  <div
    bind:this={menuEl}
    class="menu"
    style="left: {$contextMenu.x}px; top: {$contextMenu.y}px;"
    role="menu"
  >
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onCopyLine?.(); closeContextMenu(); }}>
      {m.diag_logs_menu_copy_line()}
    </button>
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onCopyMessage?.(); closeContextMenu(); }}>
      {m.diag_logs_menu_copy_message()}
    </button>
    <hr />
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onFilterScope?.(); closeContextMenu(); }}>
      {m.diag_logs_menu_filter_scope()}
    </button>
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onFilterLevel?.(); closeContextMenu(); }}>
      {m.diag_logs_menu_filter_level()}
    </button>
  </div>
{/if}

<style>
  .menu {
    position: fixed;
    z-index: var(--z-floating);
    min-width: 200px;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow);
    padding: 0.25rem 0;
    display: flex;
    flex-direction: column;
  }

  .menu button {
    background: transparent;
    border: none;
    text-align: left;
    padding: 0.4375rem 0.75rem;
    font: inherit;
    font-size: 13px;
    color: var(--color-text-primary);
    cursor: pointer;
  }
  .menu button:hover {
    background: var(--color-bg-hover);
  }

  .menu hr {
    border: none;
    border-top: 1px solid var(--color-border);
    margin: 0.25rem 0;
  }
</style>
