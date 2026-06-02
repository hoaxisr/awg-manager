<!--
  Источник дизайна: singbox-router/project/parts/RuleCard.jsx (ActionTile)
  Варианты: block / direct / tunnel / composite (показывается как tunnel)
  / sniff / hijack-dns / unknown. Spacing, цвета — повторяем точно.
-->

<script lang="ts">
  import type { OutboundDisplay } from './types';

  interface Props {
    outbound: OutboundDisplay;
  }
  let { outbound }: Props = $props();
</script>

{#if outbound.kind === 'block'}
  <div class="tile tile-block">
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="10" />
      <line x1="4.93" y1="4.93" x2="19.07" y2="19.07" />
    </svg>
    <span>Заблокировать</span>
  </div>
{:else if outbound.kind === 'direct'}
  <div class="tile tile-direct">
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <line x1="5" y1="12" x2="19" y2="12" />
      <polyline points="14 7 19 12 14 17" />
    </svg>
    <span>Напрямую</span>
  </div>
{:else if outbound.kind === 'sniff' || outbound.kind === 'hijack-dns'}
  <div class="tile tile-system">
    <span class="label-mono">{outbound.label}</span>
  </div>
{:else if outbound.kind === 'tunnel' || outbound.kind === 'awg' || outbound.kind === 'composite'}
  <div class="tile" class:tile-tunnel={outbound.kind === 'tunnel' || outbound.kind === 'composite'} class:tile-awg={outbound.kind === 'awg'}>
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M2 22V12a10 10 0 1 1 20 0v10" />
      <path d="M8 22V12a4 4 0 1 1 8 0v10" />
      <line x1="2" y1="22" x2="22" y2="22" />
    </svg>
    <span class="label-mono">{outbound.label}</span>
  </div>
{:else}
  <div class="tile tile-unknown" title="Outbound не найден в конфиге">
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
      <line x1="12" y1="9" x2="12" y2="13" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
    <span class="label-mono">{outbound.label}</span>
  </div>
{/if}

<style>
  .tile {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 10px;
    border-radius: 6px;
    font-size: 12px;
    line-height: 1;
    border: 1px solid transparent;
    white-space: nowrap;
  }

  .tile-block {
    background: rgba(181, 101, 119, 0.12);
    border-color: rgba(181, 101, 119, 0.3);
    color: var(--error);
    font-weight: 600;
  }

  .tile-direct {
    background: rgba(255, 255, 255, 0.04);
    border-color: var(--border);
    color: var(--text-secondary);
    font-weight: 500;
  }

  .tile-tunnel {
    background: var(--accent-soft);
    border-color: var(--accent-line);
    color: var(--accent);
    font-weight: 600;
    gap: 8px;
  }

  .tile-awg {
    background: color-mix(in srgb, #9c8aff 12%, transparent);
    border-color: color-mix(in srgb, #9c8aff 34%, transparent);
    color: #9c8aff;
    font-weight: 600;
    gap: 8px;
  }

  .tile-unknown {
    background: rgba(184, 147, 90, 0.12);
    border-color: rgba(184, 147, 90, 0.3);
    color: var(--warning);
    font-weight: 600;
    gap: 8px;
  }

  .tile-system {
    background: var(--bg-tertiary);
    border-color: var(--border);
    color: var(--text-muted);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-size: 11px;
  }

  .label-mono { font-family: var(--font-mono); }
</style>
