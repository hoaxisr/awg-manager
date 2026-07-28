<!--
  Источник дизайна: singbox-router/project/parts/RuleSetModal.jsx (RsRow)
-->

<script lang="ts">
  import { Check } from 'lucide-svelte';
  import RuleSetTypeBadge from './RuleSetTypeBadge.svelte';
  import type { RuleSetDisplayType } from '$lib/utils/ruleSetType';

  interface Props {
    templateId: string;
    tag: string;
    type: RuleSetDisplayType;
    desc?: string;
    count?: string;
    selected: boolean;
    onclick: () => void;
  }

  let { tag, type, desc, count, selected, onclick }: Props = $props();
</script>

<button type="button" class="row" class:selected aria-pressed={selected} {onclick}>
  <div class="check">
    {#if selected}
      <Check size={10} />
    {/if}
  </div>
  <div class="meta">
    <div class="line">
      <span class="tag">{tag}</span>
      <RuleSetTypeBadge {type} />
    </div>
    {#if desc}
      <div class="desc">{desc}</div>
    {/if}
  </div>
  {#if count}
    <div class="count">{count}</div>
  {/if}
</button>

<style>
  .row {
    display: grid;
    grid-template-columns: 20px 1fr auto;
    gap: var(--sp-3);
    align-items: center;
    padding: 9px var(--sp-3);
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    cursor: pointer;
    text-align: left;
    font-family: inherit;
    color: inherit;
    width: 100%;
  }
  .row.selected {
    background: var(--accent-soft);
    border-color: var(--accent-line);
  }
  .check {
    width: 16px;
    height: 16px;
    border-radius: 4px;
    background: transparent;
    border: 1.5px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .row.selected .check {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--color-accent-contrast, #fff);
  }
  .meta {
    min-width: 0;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .desc {
    font-size: 11.5px;
    color: var(--text-muted);
    margin-top: 2px;
  }
  .count {
    font-size: 11px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    white-space: nowrap;
  }
</style>
