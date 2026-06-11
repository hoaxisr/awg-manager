<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (DnsServersCompact)
-->

<script lang="ts">
  import type {
    SingboxProxyGroup,
    SingboxRouterDNSServer,
    SingboxRouterDNSRule,
    SingboxRouterOutbound,
    SingboxTunnel,
    Subscription,
  } from '$lib/types';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import { Badge, Button } from '$lib/components/ui';
  import { Trash2, Edit3 } from 'lucide-svelte';
  import { dnsServerDetourDisplay } from './dnsServerDetourDisplay';
  import OutboundTile from './OutboundTile.svelte';
  import { dnsRuleTarget } from './dnsRuleLabel';
  import { dnsMatcherParts, dnsMatcherSummary } from './dnsMatcherParts';
  import { dnsServerDeleteBlockReasons, type DnsServerUsageInput } from './dnsServerUsage';

  interface Props {
    servers: SingboxRouterDNSServer[];
    rules: SingboxRouterDNSRule[];
    outbounds?: SingboxRouterOutbound[];
    onEditServer: (tag: string) => void;
    onDeleteServer?: (tag: string) => void;
    onEditRule: (idx: number) => void;
    onDeleteRule?: (idx: number) => void;
    onAddRule?: () => void;
    addRuleDisabled?: boolean;
    addRuleTitle?: string;
    outboundOptions?: OutboundGroup[];
    subscriptions?: Subscription[] | null;
    proxyGroups?: SingboxProxyGroup[];
    singboxTunnels?: SingboxTunnel[];
    dnsUsage?: Omit<DnsServerUsageInput, 'tag'>;
  }

  let {
    servers,
    rules,
    outbounds = [],
    onEditServer,
    onDeleteServer,
    onEditRule,
    onDeleteRule,
    onAddRule,
    addRuleDisabled = false,
    addRuleTitle,
    outboundOptions = [],
    subscriptions = null,
    proxyGroups = [],
    singboxTunnels = [],
    dnsUsage,
  }: Props = $props();

  function subFor(s: SingboxRouterDNSServer): string {
    return `${s.type ?? 'dns'} · ${s.server}`;
  }

  // Один проход по DNS-конфигу на список вместо O(servers × конфиг) на строку.
  const serverDeleteReasons = $derived(
    dnsUsage ? dnsServerDeleteBlockReasons(servers, dnsUsage) : null,
  );
</script>

<div class="wrap">
  <div class="servers">
    {#each servers as s (s.tag)}
      {@const deleteReason = serverDeleteReasons?.get(s.tag) ?? null}
      <div class="server-row">
        <span class="dot"></span>
        <button type="button" class="meta-btn" onclick={() => onEditServer(s.tag)}>
          <div class="meta">
            <div class="tag">{s.tag}</div>
            <div class="sub">{subFor(s)}</div>
          </div>
        </button>
        <span class="detour-chip">
          <OutboundTile
            outbound={dnsServerDetourDisplay(
              s,
              outbounds,
              outboundOptions,
              subscriptions,
              proxyGroups,
              singboxTunnels,
            )}
            size="compact"
          />
        </span>
        <div class="server-actions">
          <button
            type="button"
            class="route-action-btn"
            onclick={() => onEditServer(s.tag)}
            aria-label={`Редактировать DNS-сервер ${s.tag}`}
            title={`Редактировать DNS-сервер «${s.tag}»`}
          >
            <Edit3 size={15} />
          </button>
          {#if onDeleteServer}
            <button
              type="button"
              class="route-action-btn danger"
              disabled={deleteReason !== null}
              onclick={() => onDeleteServer(s.tag)}
              aria-label={`Удалить DNS-сервер ${s.tag}`}
              title={deleteReason ?? `Удалить DNS-сервер «${s.tag}»`}
            >
              <Trash2 size={15} />
            </button>
          {/if}
        </div>
      </div>
    {/each}
    {#if servers.length === 0}
      <div class="empty">Нет DNS-серверов.</div>
    {/if}
  </div>

  <div class="rules-cap">
    <span class="rules-cap-label">DNS-правила · {rules.length}</span>
    {#if onAddRule}
      <Button variant="primary" size="sm" onclick={onAddRule} disabled={addRuleDisabled}>+ Правило</Button>
    {/if}
  </div>
  {#if rules.length > 0}
    <div class="rules-table">
      <div class="rules-rows">
        {#each rules as r, i (i)}
          {@const tgt = dnsRuleTarget(r)}
          {@const matchers = dnsMatcherParts(r)}
          <div class="rule-row">
            <button
              type="button"
              class="rule-content"
              onclick={() => onEditRule(i)}
              title={`${dnsMatcherSummary(r)} → ${tgt.label}`}
            >
              <span class="rule-match">
                {#if matchers.length === 0}
                  —
                {:else}
                  {#each matchers as part, pi (part.key + pi)}
                    <span class="m-part">
                      <span class="m-head">
                        {#if pi > 0}<span class="m-sep">· </span>{/if}
                        {#if part.key === 'query_type'}
                          <span class="m-key">query_type</span><span class="m-eq">=</span>
                        {:else}
                          <span class="m-key">{part.key}</span><span class="m-colon">: </span>
                        {/if}
                      </span>
                      <span class="m-val">{part.value}</span>
                    </span>
                  {/each}
                {/if}
              </span>
              <span class="rule-arrow" aria-hidden="true">→</span>
              {#if tgt.kind === 'block'}
                <span class="rule-target">
                  <Badge variant="error" size="sm" mono>{tgt.label}</Badge>
                </span>
              {:else if tgt.kind === 'none'}
                <span class="rule-target none">{tgt.label}</span>
              {:else}
                <span class="rule-target" title={tgt.label}>
                  <Badge variant="accent" size="sm" mono>{tgt.label}</Badge>
                </span>
              {/if}
            </button>

            <div class="rule-actions">
              <button
                type="button"
                class="route-action-btn"
                onclick={() => onEditRule(i)}
                aria-label={`Редактировать DNS-правило #${i + 1}`}
                title={`Редактировать DNS-правило #${i + 1}`}
              >
                <Edit3 size={15} />
              </button>

              {#if onDeleteRule}
                <button
                  type="button"
                  class="route-action-btn danger"
                  onclick={() => onDeleteRule(i)}
                  aria-label={`Удалить DNS-правило #${i + 1}`}
                  title={`Удалить DNS-правило #${i + 1}`}
                >
                  <Trash2 size={15} />
                </button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </div>
  {:else}
    <div class="empty">Нет правил</div>
  {/if}
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
  }
  .servers {
    display: flex;
    flex-direction: column;
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
  .server-row {
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: 6px minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }
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
  .server-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  @media (hover: hover) and (pointer: fine) {
    .server-row:hover,
    .rule-row:hover {
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
  .meta {
    flex: 1;
    min-width: 0;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .sub {
    font-size: 11px;
    color: var(--text-muted);
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .detour-chip {
    flex-shrink: 1;
    min-width: 0;
    max-width: 100%;
  }
  .detour-chip :global(.tone-chip) {
    max-width: 100%;
    min-width: 0;
    overflow: hidden;
  }
  .rules-cap {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 14px;
    background: var(--bg-tertiary);
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .rules-table {
    display: grid;
    min-width: 0;
  }
  .rules-rows {
    display: grid;
    gap: 0.25rem;
    min-width: 0;
  }
  .rule-row {
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    column-gap: 0.5rem;
    min-width: 0;
    background: var(--surface-bg);
    padding: 0.55rem 0.75rem;
    border-radius: 4px;
  }
  .rule-content {
    min-width: 0;
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto minmax(4.5rem, max-content);
    align-items: center;
    column-gap: 0.35rem;
    background: transparent;
    border: 0;
    padding: 0;
    color: inherit;
    font-family: var(--font-mono);
    text-align: left;
    cursor: pointer;
  }
  .rule-match {
    grid-column: 1;
    min-width: 0;
    color: var(--text);
    font-size: 12px;
    line-height: 1.35;
  }
  .m-part {
    display: inline;
  }
  .m-head {
    white-space: nowrap;
  }
  .m-key {
    color: var(--text-muted);
    font-weight: 600;
  }
  .m-colon,
  .m-eq,
  .m-sep {
    color: var(--text-muted);
  }
  .m-val {
    color: var(--text-secondary);
    overflow-wrap: anywhere;
  }
  .rule-arrow {
    grid-column: 2;
    flex-shrink: 0;
    color: var(--muted-text);
    line-height: 1;
    opacity: 0.85;
  }
  .rule-target {
    grid-column: 3;
    justify-self: end;
    max-width: 10rem;
    min-width: 0;
    overflow: hidden;
  }
  .rule-target :global(.badge) {
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .rule-target.none {
    color: var(--text-muted);
    font-size: 12px;
  }
  .rule-actions {
    display: inline-flex;
    align-items: center;
    justify-content: flex-end;
    gap: 4px;
    flex-shrink: 0;
    white-space: nowrap;
  }
  @media (max-width: 768px) {
    .rules-rows {
      gap: 0;
    }

    .rule-row {
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: start;
      gap: 0.5rem;
      padding: 0.65rem 0.75rem;
      border: 0;
      border-radius: 0;
      border-bottom: 1px solid var(--border);
      background: transparent;
    }

    .rule-row:last-child {
      border-bottom: 0;
    }

    .rule-content {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas:
        'match'
        'target';
      row-gap: 0.35rem;
    }

    .rule-match {
      grid-area: match;
    }

    .rule-arrow {
      display: none;
    }

    .rule-target {
      grid-area: target;
      grid-column: auto;
      justify-self: start;
      max-width: 100%;
    }

    .rule-actions {
      align-self: start;
    }
  }
</style>
