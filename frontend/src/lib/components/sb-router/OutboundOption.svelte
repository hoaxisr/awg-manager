<!--
  Источник дизайна: singbox-router/project/screens/AddRuleFlow.jsx (OutboundOption)
-->

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Check } from 'lucide-svelte';

  type Tone = 'accent' | 'muted' | 'error';

  interface Props {
    icon?: Snippet;
    label: string;
    sub: string;
    count?: string;
    tone: Tone;
    selected: boolean;
    onclick: () => void;
  }

  let { icon, label, sub, count, tone, selected, onclick }: Props = $props();
</script>

<button type="button" class="opt tone-{tone}" class:selected aria-pressed={selected} {onclick}>
  {#if icon}<div class="icon">{@render icon()}</div>{/if}
  <div class="text">
    <div class="label">{label}</div>
    <div class="sub">{sub}</div>
  </div>
  {#if count}<div class="count">{count}</div>{/if}
  {#if selected}
    <div class="check"><Check size={10} color="#fff" /></div>
  {/if}
</button>

<style>
  .opt {
    padding: 14px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    cursor: pointer;
    text-align: left;
    font-family: inherit;
    color: inherit;
    display: flex;
    flex-direction: column;
    gap: 8px;
    position: relative;
  }
  .opt.selected.tone-accent {
    background: color-mix(in srgb, var(--accent) 10%, var(--bg-tertiary));
    border-color: var(--accent);
  }
  .opt.selected.tone-muted {
    background: color-mix(in srgb, var(--text-muted) 10%, var(--bg-tertiary));
    border-color: var(--text-muted);
  }
  .opt.selected.tone-error {
    background: color-mix(in srgb, var(--color-error, #dc2626) 10%, var(--bg-tertiary));
    border-color: var(--color-error, #dc2626);
  }
  .icon {
    color: var(--text-secondary);
  }
  .opt.tone-accent .icon { color: var(--accent); }
  .opt.tone-muted .icon { color: var(--text-muted); }
  .opt.tone-error .icon { color: var(--color-error, #dc2626); }
  .label {
    font-weight: 600;
    font-size: 13.5px;
  }
  .sub {
    font-size: 11.5px;
    color: var(--text-muted);
    margin-top: 2px;
  }
  .count {
    font-size: 10.5px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    margin-top: auto;
  }
  .check {
    position: absolute;
    top: 10px;
    right: 10px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--accent);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .opt.tone-muted .check { background: var(--text-muted); }
  .opt.tone-error .check { background: var(--color-error, #dc2626); }
</style>
