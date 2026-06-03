<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (DnsServersCompact)
-->

<script lang="ts">
  import type { SingboxRouterDNSServer, SingboxRouterDNSRule } from '$lib/types';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import { Badge, Button } from '$lib/components/ui';
  import { ArrowRight, Trash2 } from 'lucide-svelte';
  import { resolveMemberLabel } from '$lib/utils/memberLabel';
  import { dnsRuleTarget } from './dnsRuleLabel';

  const AWG_OPTION_GROUPS = new Set(['AWG туннели', 'Системные WireGuard']);

  interface Props {
    servers: SingboxRouterDNSServer[];
    rules: SingboxRouterDNSRule[];
    onEditServer: (tag: string) => void;
    onEditRule: (idx: number) => void;
    onDeleteRule?: (idx: number) => void;
    onAddRule?: () => void;
    addRuleDisabled?: boolean;
    addRuleTitle?: string;
    outboundOptions?: OutboundGroup[];
  }

  let {
    servers, rules, onEditServer, onEditRule, onDeleteRule, onAddRule, addRuleDisabled = false, addRuleTitle,
    outboundOptions = [],
  }: Props = $props();

  function subFor(s: SingboxRouterDNSServer): string {
    return `${s.type ?? 'dns'} · ${s.server}`;
  }

  function detourFor(s: SingboxRouterDNSServer): string {
    return s.detour ?? 'direct';
  }

  function detourLabelFor(s: SingboxRouterDNSServer): string {
    const detour = detourFor(s);
    if (detour === 'direct') return detour;
    return resolveMemberLabel(detour, null, outboundOptions);
  }

  function detourVariantFor(s: SingboxRouterDNSServer): 'default' | 'accent' | 'purple' {
    const detour = detourFor(s);
    if (detour === 'direct') return 'default';
    return outboundOptions.some((g) =>
      AWG_OPTION_GROUPS.has(g.group) && g.items.some((i) => i.value === detour)
    ) ? 'purple' : 'accent';
  }

  function matcherSummary(r: SingboxRouterDNSRule): string {
    const parts: string[] = [];
    if (r.rule_set?.length) parts.push(`rule_set: ${r.rule_set.join(', ')}`);
    if (r.domain_suffix?.length) parts.push(`suffix: ${r.domain_suffix[0]}${r.domain_suffix.length > 1 ? ` +${r.domain_suffix.length - 1}` : ''}`);
    if (r.domain_keyword?.length) parts.push(`keyword: ${r.domain_keyword[0]}`);
    if (r.query_type?.length) parts.push(`query_type=${r.query_type[0]}`);
    return parts.length > 0 ? parts.join(' · ') : '—';
  }
</script>

<div class="wrap">
  <div class="servers">
    {#each servers as s (s.tag)}
      <button type="button" class="row" onclick={() => onEditServer(s.tag)}>
        <span class="dot"></span>
        <div class="meta">
          <div class="tag">{s.tag}</div>
          <div class="sub">{subFor(s)}</div>
        </div>
        <Badge variant={detourVariantFor(s)} size="sm" mono title={detourFor(s)}>
          {detourLabelFor(s)}
        </Badge>
      </button>
    {/each}
    {#if servers.length === 0}
      <div class="empty">Нет серверов</div>
    {/if}
  </div>

  <div class="rules-cap">
    <span class="rules-cap-label">DNS-правила · {rules.length}</span>
    {#if onAddRule}
      <Button variant="primary" size="sm" onclick={onAddRule} disabled={addRuleDisabled}>+ Правило</Button>
    {/if}
  </div>
  {#if rules.length > 0}
    <div class="rules">
      {#each rules as r, i (i)}
        {@const tgt = dnsRuleTarget(r)}
        <div class="rule-row">
          <button type="button" class="rule-main" onclick={() => onEditRule(i)}>
            <span class="rule-matchers">{matcherSummary(r)}</span>
            <ArrowRight size={11} color="var(--text-muted)" />
            <span class="rule-target" class:block={tgt.kind === 'block'} class:none={tgt.kind === 'none'}>{tgt.label}</span>
          </button>
          {#if onDeleteRule}
            <button type="button" class="rule-del" onclick={() => onDeleteRule(i)} aria-label="Удалить правило" title="Удалить правило">
              <Trash2 size={14} />
            </button>
          {/if}
        </div>
      {/each}
    </div>
  {:else}
    <div class="rules-empty">нет правил</div>
  {/if}
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
  }
  .servers, .rules {
    display: flex;
    flex-direction: column;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    background: transparent;
    border: 0;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
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
  .meta {
    flex: 1;
    min-width: 0;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
  }
  .sub {
    font-size: 11px;
    color: var(--text-muted);
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
  .rules-empty {
    padding: 12px 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 11.5px;
    font-style: italic;
  }
  .rule-row {
    display: flex;
    align-items: stretch;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }
  .rule-row:hover {
    background: var(--bg-tertiary);
  }
  .rule-main {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 14px;
    background: transparent;
    border: 0;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: inherit;
    text-align: left;
  }
  .rule-del {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    border: 0;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition: color 0.15s, background 0.15s;
  }
  .rule-del:hover {
    color: var(--color-error, #dc2626);
    background: color-mix(in srgb, var(--color-error, #dc2626) 10%, transparent);
  }
  .rule-matchers {
    flex: 1;
    min-width: 0;
    color: var(--text-secondary);
    white-space: normal;
    overflow: hidden;
    text-overflow: initial;
    overflow-wrap: anywhere;
    word-break: break-word;
    line-height: 1.25;
    display: -webkit-box;
    line-clamp: 2;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }
  .rule-target {
    flex-shrink: 0;
    color: var(--accent);
    min-width: 0;
    max-width: 108px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* Block actions (DROP / REFUSED / NXDOMAIN) are not a server detour — render
     distinct from the accent-coloured route target so they're not mistaken
     for a DNS server tag. */
  .rule-target.block {
    color: var(--text-secondary);
    font-weight: 600;
  }
  .rule-target.none {
    color: var(--text-muted);
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
</style>
