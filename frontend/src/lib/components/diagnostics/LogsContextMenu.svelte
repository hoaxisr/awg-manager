<script lang="ts">
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
      Скопировать строку
    </button>
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onCopyMessage?.(); closeContextMenu(); }}>
      Скопировать сообщение
    </button>
    <hr />
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onFilterScope?.(); closeContextMenu(); }}>
      Фильтр по scope
    </button>
    <button type="button" role="menuitem" onclick={() => { $contextMenu.onFilterLevel?.(); closeContextMenu(); }}>
      Фильтр по уровню
    </button>
  </div>
{/if}

<style>
  .menu {
    position: fixed;
    z-index: 1000;
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
