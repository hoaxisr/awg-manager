<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (SidePanel)
-->

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ChevronDown } from 'lucide-svelte';
  import { Button } from '$lib/components/ui';
  import {
    expertPanelCollapse,
    toggleExpertPanelSection,
    type ExpertPanelSection,
  } from './expertPanelCollapseStore';

  interface Props {
    title: string;
    count?: string;
    /** Ключ секции — включает сворачивание и читает состояние из expertPanelCollapse. */
    section?: ExpertPanelSection;
    actionLabel?: string;
    /** 'link' — текст-ссылка (навигация); 'filled' — заливная кнопка (add-действия). */
    actionVariant?: 'link' | 'filled';
    actionDisabled?: boolean;
    actionTitle?: string;
    onAction?: () => void;
    /** Если задан — заменяет одиночную кнопку (для множественных actions). */
    actions?: Snippet;
    children: Snippet;
  }

  let {
    title, count, section, actionLabel, actionVariant = 'link', actionDisabled = false, actionTitle, onAction, actions, children,
  }: Props = $props();

  let collapsed = $derived(section ? $expertPanelCollapse[section] : false);

  function toggleCollapse() {
    if (!section) return;
    toggleExpertPanelSection(section);
  }
</script>

<div class="panel" class:panel-collapsed={collapsed}>
  <header class="head" class:head-collapsed={collapsed}>
    <div class="left">
      {#if section}
        <button
          type="button"
          class="collapse-btn"
          onclick={toggleCollapse}
          aria-expanded={!collapsed}
          aria-label={collapsed ? `Развернуть «${title}»` : `Свернуть «${title}»`}
        >
          <span class="collapse-chevron" class:open={!collapsed}>
            <ChevronDown size={14} aria-hidden={true} />
          </span>
        </button>
      {/if}
      <button
        type="button"
        class="title-btn"
        class:title-static={!section}
        onclick={section ? toggleCollapse : undefined}
        disabled={!section}
        aria-expanded={section ? !collapsed : undefined}
      >
        <span class="title">{title}</span>
        {#if count}<span class="count">· {count}</span>{/if}
      </button>
    </div>
    <div class="actions">
      {#if actions}
        {@render actions()}
      {:else if actionLabel && onAction}
        {#if actionVariant === 'filled'}
          <Button variant="primary" size="sm" onclick={onAction} disabled={actionDisabled}>{actionLabel}</Button>
        {:else}
          <button type="button" class="action" onclick={onAction} disabled={actionDisabled} title={actionTitle}>{actionLabel}</button>
        {/if}
      {/if}
    </div>
  </header>
  {#if !collapsed}
    <div class="body">
      {@render children()}
    </div>
  {/if}
</div>

<style>
  .panel {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    background: var(--bg-tertiary);
  }
  .head-collapsed {
    border-bottom: 0;
  }
  .left {
    display: flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
  }
  .collapse-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    width: 22px;
    height: 22px;
    padding: 0;
    border: 0;
    border-radius: var(--radius-sm, 4px);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    font-family: inherit;
  }
  .collapse-btn:hover {
    color: var(--text-primary);
    background: var(--bg-secondary);
  }
  .collapse-chevron {
    display: inline-flex;
    align-items: center;
    transition: transform 0.15s ease;
    transform: rotate(-90deg);
  }
  .collapse-chevron.open {
    transform: rotate(0deg);
  }
  .title-btn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font-family: inherit;
    cursor: pointer;
    text-align: left;
  }
  .title-btn:disabled,
  .title-static {
    cursor: default;
  }
  .title {
    font-weight: 600;
    font-size: 13px;
    color: var(--text-primary);
  }
  .count {
    font-size: 11px;
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .action, :global(.panel .head .action) {
    background: transparent;
    border: 0;
    color: var(--accent);
    font-size: 11.5px;
    cursor: pointer;
    font-family: inherit;
    padding: 0;
  }
  .action:hover:not(:disabled), :global(.panel .head .action:hover:not(:disabled)) {
    text-decoration: underline;
  }
  .action:disabled, :global(.panel .head .action:disabled) {
    cursor: not-allowed;
    opacity: 0.55;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 0;
  }
</style>
