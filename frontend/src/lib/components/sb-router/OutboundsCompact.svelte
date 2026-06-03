<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (OutboundsCompact)
-->

<script lang="ts">
  import type { SingboxRouterOutbound, Subscription } from '$lib/types';
  import { Badge } from '$lib/components/ui';
  import { outboundDisplay } from './outboundLabel';

  interface Props {
    outbounds: SingboxRouterOutbound[];
    onEdit: (tag: string) => void;
    /** Subscriptions, to resolve sub-<hash> composite tags to their names. */
    subscriptions?: Subscription[];
  }

  let { outbounds, onEdit, subscriptions = [] }: Props = $props();

  function toneFor(type: string): 'success' | 'accent' | 'info' | 'muted' | 'error' {
    if (type === 'direct') return 'muted';
    if (type === 'selector' || type === 'urltest' || type === 'loadbalance') return 'accent';
    return 'info';
  }

  function kindLabel(type: string): string {
    if (type === 'selector' || type === 'urltest' || type === 'loadbalance') return 'composite';
    return type;
  }
</script>

<div class="list">
  {#each outbounds as o (o.tag)}
    {@const d = outboundDisplay(o, subscriptions)}
    <button type="button" class="row" onclick={() => onEdit(o.tag)}>
      <span class="dot" data-tone={toneFor(o.type)}></span>
      <div class="meta">
        <div class="tag">{d.title}</div>
        <div class="sub">{d.subtitle}</div>
      </div>
      <Badge variant="default" size="sm">{kindLabel(o.type)}</Badge>
    </button>
  {/each}
  {#if outbounds.length === 0}
    <div class="empty">Нет outbounds</div>
  {/if}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
    background: transparent;
    border-left: 0;
    border-right: 0;
    border-top: 0;
    cursor: pointer;
    font-family: inherit;
    color: inherit;
    width: 100%;
    text-align: left;
  }
  .row:hover {
    background: var(--bg-tertiary);
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }
  .dot[data-tone='success'] { background: var(--color-success, #22c55e); }
  .dot[data-tone='accent'] { background: var(--accent); }
  .dot[data-tone='info'] { background: var(--color-info, #3b82f6); }
  .dot[data-tone='error'] { background: var(--color-error, #dc2626); }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    font-size: 11px;
    color: var(--text-muted);
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
</style>
