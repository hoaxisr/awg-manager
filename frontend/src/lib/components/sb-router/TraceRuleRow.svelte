<!--
  Источник дизайна: singbox-router/project/screens/RuleDetail.jsx
  Per-rule row в Trace results: matched (success bg + checkmark) или skipped (muted dim).
-->

<script lang="ts">
  import { Check } from 'lucide-svelte';
  import type { SingboxRouterInspectMatch } from '$lib/types';

  interface Props {
    match: SingboxRouterInspectMatch;
    /** Highlight as the winning rule (first matched terminal). */
    winner?: boolean;
  }
  let { match, winner = false }: Props = $props();

  let indexStr = $derived(`#${String(match.index).padStart(2, '0')}`);
  let actionLabel = $derived.by(() => {
    if (match.action === 'route' && match.outbound) return `→ ${match.outbound}`;
    if (match.action === 'reject') return '✕ заблокировать';
    if (match.action === 'sniff') return 'sniff';
    if (match.action === 'hijack-dns') return 'hijack-dns';
    return match.action;
  });
</script>

<div class="row" class:is-matched={match.matched} class:is-skipped={!match.matched} class:is-winner={winner}>
  <span class="index">{indexStr}</span>
  <div class="content">
    <div class="head">
      <span class="action">{actionLabel}</span>
      {#if match.matched}
        <Check class="check" size={14} strokeWidth={2.5} aria-hidden={true} />
      {/if}
    </div>
    {#if match.conditions && match.conditions.length > 0}
      <div class="conditions">
        {#each match.conditions as cond}
          <span class="cond-chip">{cond}</span>
        {/each}
      </div>
    {/if}
    {#if match.reason}
      <div class="reason">{match.reason}</div>
    {/if}
  </div>
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: 40px 1fr;
    gap: 12px;
    align-items: start;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    transition: border-color var(--t-fast);
  }
  .row.is-matched {
    background: color-mix(in srgb, var(--success) 10%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--success) 30%, var(--border));
  }
  .row.is-winner {
    border-left: 3px solid var(--success);
    padding-left: 10px;
  }
  .row.is-skipped {
    opacity: 0.6;
  }

  .index {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    text-align: right;
  }
  .content {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .action {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary);
    font-family: var(--font-mono);
  }
  .row.is-matched :global(.check) { color: var(--success); }

  .conditions {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .cond-chip {
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-secondary);
    font-family: var(--font-mono);
  }

  .reason {
    font-size: 11px;
    color: var(--text-muted);
    line-height: 1.4;
    font-style: italic;
  }
</style>
