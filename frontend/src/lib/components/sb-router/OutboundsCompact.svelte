<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (OutboundsCompact)
-->

<script lang="ts">
  import type { SingboxRouterOutbound, Subscription } from '$lib/types';
  import { Badge } from '$lib/components/ui';
  import { Edit3, Trash2 } from 'lucide-svelte';
  import { isSubscriptionOutbound, outboundDisplay } from './outboundLabel';
  import { outboundDeleteBlockReasons, type OutboundUsageInput } from './outboundUsage';

  interface Props {
    outbounds: SingboxRouterOutbound[];
    onEdit: (tag: string) => void;
    onDelete?: (tag: string) => void;
    /** Subscriptions, to resolve sub-<hash> composite tags to their names. */
    subscriptions?: Subscription[];
    usage?: Omit<OutboundUsageInput, 'tag'>;
  }

  let { outbounds, onEdit, onDelete, subscriptions = [], usage }: Props = $props();

  function toneFor(type: string): 'success' | 'accent' | 'info' | 'muted' | 'error' {
    if (type === 'direct') return 'muted';
    if (type === 'selector' || type === 'urltest' || type === 'loadbalance') return 'accent';
    return 'info';
  }

  function kindLabel(o: SingboxRouterOutbound): string {
    if (isSubscriptionOutbound(o, subscriptions)) return 'subscription';
    if (o.type === 'selector' || o.type === 'urltest' || o.type === 'loadbalance') return 'composite';
    return o.type;
  }

  // Один проход по конфигу на список вместо O(outbounds × конфиг) на строку.
  const deleteReasons = $derived(usage ? outboundDeleteBlockReasons(outbounds, usage) : null);
</script>

<div class="list">
  {#each outbounds as o (o.tag)}
    {@const d = outboundDisplay(o, subscriptions)}
    {@const deleteReason = deleteReasons?.get(o.tag) ?? null}
    <div class="row">
      <span class="dot" data-tone={toneFor(o.type)}></span>
      <button type="button" class="meta-btn" onclick={() => onEdit(o.tag)}>
        <div class="meta">
          <div class="tag">{d.title}</div>
          <div class="sub">{d.subtitle}</div>
        </div>
      </button>
      <Badge variant="default" size="sm">{kindLabel(o)}</Badge>
      <div class="actions">
        <button
          type="button"
          class="route-action-btn"
          onclick={() => onEdit(o.tag)}
          aria-label={`Редактировать outbound ${o.tag}`}
          title={`Редактировать outbound «${d.title}»`}
        >
          <Edit3 size={15} />
        </button>
        {#if onDelete}
          <button
            type="button"
            class="route-action-btn danger"
            disabled={deleteReason !== null}
            onclick={() => onDelete(o.tag)}
            aria-label={`Удалить outbound ${o.tag}`}
            title={deleteReason ?? `Удалить outbound «${d.title}»`}
          >
            <Trash2 size={15} />
          </button>
        {/if}
      </div>
    </div>
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
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: 6px minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }

  @media (hover: hover) and (pointer: fine) {
    .row:hover {
      background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
    }
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
  .meta-btn {
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
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
  .actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
</style>
