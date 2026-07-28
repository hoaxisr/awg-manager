<!--
  Источник дизайна: singbox-router/project/parts/RuleSetModal.jsx (ServiceCheck)
  Reuse PresetIcon из routing/singboxRouter — те же brand icons.
-->

<script lang="ts">
  import PresetIcon from '$lib/components/routing/singboxRouter/PresetIcon.svelte';
  import { Check } from 'lucide-svelte';

  interface Props {
    templateId: string;
    presetId: string;
    iconSlug?: string;
    name: string;
    sub?: string;
    selected: boolean;
    onclick: () => void;
  }

  let { presetId, iconSlug, name, sub, selected, onclick }: Props = $props();
</script>

<button type="button" class="tile" class:selected aria-pressed={selected} {onclick}>
  <div class="check">
    {#if selected}
      <Check size={10} />
    {/if}
  </div>
  <div class="icon">
    <PresetIcon slug={iconSlug ?? presetId} label={name} size={26} />
  </div>
  <div class="text">
    <div class="name">{name}</div>
    {#if sub}
      <div class="sub">{sub}</div>
    {/if}
  </div>
</button>

<style>
  .tile {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 11px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    cursor: pointer;
    text-align: left;
    font-family: inherit;
    color: inherit;
    width: 100%;
    min-width: 0;
  }
  .tile.selected {
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .check {
    width: 16px;
    height: 16px;
    border-radius: 4px;
    flex-shrink: 0;
    background: transparent;
    border: 1.5px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .tile.selected .check {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--color-accent-contrast, #fff);
  }
  .icon {
    width: 26px;
    height: 26px;
    flex-shrink: 0;
  }
  .text {
    min-width: 0;
    line-height: 1.2;
  }
  .name {
    font-weight: 600;
    font-size: 12.5px;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    font-size: 10.5px;
    color: var(--text-muted);
  }
</style>
