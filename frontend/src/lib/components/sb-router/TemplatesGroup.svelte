<!--
  Источник дизайна: singbox-router/project/parts/RuleSetModal.jsx (Group)
  При правках сверять с JSX напрямую.
-->

<script lang="ts">
  import { m } from '$lib/i18n';
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    hint?: string;
    selectedCount: number;
    totalCount: number;
    onSelectAll: () => void;
    children: Snippet;
  }

  let { title, hint, selectedCount, totalCount, onSelectAll, children }: Props = $props();
</script>

<div class="group">
  <header class="head">
    <div class="title-row">
      <span class="title">{title}</span>
      {#if hint}
        <span class="hint">· {hint}</span>
      {/if}
    </div>
    <div class="actions">
      {#if selectedCount > 0}
        <span class="counter">{selectedCount} / {totalCount}</span>
      {/if}
      <button type="button" class="link" onclick={onSelectAll}>
        {selectedCount === totalCount ? m.sb_router_templates_deselect_all() : m.sb_router_templates_select_all()}
      </button>
    </div>
  </header>
  <div class="body">
    {@render children()}
  </div>
</div>

<style>
  .group {
    margin-top: 14px;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
    padding: 0 2px;
  }
  .title-row {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .title {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .hint {
    font-size: 11px;
    color: var(--text-muted);
    font-style: italic;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .counter {
    font-size: 10.5px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 600;
  }
  .link {
    background: transparent;
    border: 0;
    color: var(--accent);
    font-size: 11px;
    cursor: pointer;
    font-family: inherit;
    font-weight: 500;
    padding: 0;
  }
  .link:hover {
    text-decoration: underline;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
