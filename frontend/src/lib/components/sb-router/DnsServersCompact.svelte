<!--
  Источник дизайна: singbox-router/project/screens/MainExpert.jsx (DnsServersCompact)
-->

<script lang="ts">
  import type { SingboxRouterDNSServer, SingboxRouterDNSRule } from '$lib/types';
  import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
  import { Badge, Button } from '$lib/components/ui';
  import { ArrowRight } from 'lucide-svelte';
  import { resolveMemberLabel } from '$lib/utils/memberLabel';

  const AWG_OPTION_GROUPS = new Set(['AWG туннели', 'Системные WireGuard']);

  interface Props {
    servers: SingboxRouterDNSServer[];
    rules: SingboxRouterDNSRule[];
    onEditServer: (tag: string) => void;
    onEditRule: (idx: number) => void;
    onAddRule?: () => void;
    addRuleDisabled?: boolean;
    addRuleTitle?: string;
    outboundOptions?: OutboundGroup[];
  }

  let {
    servers, rules, onEditServer, onEditRule, onAddRule, addRuleDisabled = false, addRuleTitle,
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
        <button type="button" class="rule-row" onclick={() => onEditRule(i)}>
          <span class="rule-matchers">{matcherSummary(r)}</span>
          <ArrowRight size={11} color="var(--text-muted)" />
          <span class="rule-server">{r.server ?? '—'}</span>
        </button>
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
  .row, .rule-row {
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
  .row:hover, .rule-row:hover {
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
    font-family: var(--font-mono);
    font-size: 11.5px;
    align-items: flex-start;
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
  .rule-server {
    flex-shrink: 0;
    color: var(--accent);
    min-width: 0;
    max-width: 108px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
</style>
