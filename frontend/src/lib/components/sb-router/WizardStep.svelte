<!--
  Источник дизайна: singbox-router/project/screens/AddRuleFlow.jsx (step container blocks)
-->

<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    n: number;
    title: string;
    hint?: string;
    active: boolean;
    children: Snippet;
  }

  let { n, title, hint, active, children }: Props = $props();
</script>

<section class="step" class:active>
  <header class="head">
    <div class="circle">{n}</div>
    <h2 class="title">{title}</h2>
    {#if hint}
      <span class="hint">{hint}</span>
    {/if}
  </header>
  <div class="body">
    {@render children()}
  </div>
</section>

<style>
  .step {
    padding: 18px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    margin-bottom: 16px;
    opacity: 0.92;
  }
  .step.active {
    border-color: var(--accent-line);
    opacity: 1;
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 8%, transparent);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 14px;
  }
  .circle {
    width: 24px;
    height: 24px;
    flex-shrink: 0;
    border-radius: 50%;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-weight: 700;
    font-size: 12px;
  }
  .step.active .circle {
    background: var(--accent);
    color: var(--color-accent-contrast, #fff);
  }
  .title {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .hint {
    font-size: 11.5px;
    color: var(--text-muted);
  }
  @media (max-width: 768px) {
    .step {
      padding: 14px 12px;
      margin-bottom: 12px;
    }
    .head {
      flex-wrap: wrap;
      align-items: flex-start;
      gap: 6px 8px;
      margin-bottom: 12px;
    }
    .title {
      flex: 1 1 0;
      min-width: 0;
      font-size: 14px;
      line-height: 1.3;
    }
    .hint {
      flex: 1 1 100%;
      margin-left: 34px;
      line-height: 1.35;
    }
  }
</style>
