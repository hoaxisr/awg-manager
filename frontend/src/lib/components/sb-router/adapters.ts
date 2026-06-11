/**
 * Adapters singbox routing rule → RuleCardData.
 *
 * Источник дизайна: singbox-router/project/parts/RuleCard.jsx
 * Маппинг action: дизайн использует action='reject' для блокировки,
 * 'direct' (через outbound name) для пропуска без туннеля, 'route' default.
 *
 * resolveOutboundDisplay учитывает 3 спец-случая: direct/block/reject
 * (всегда нормализуются), composite (selector/urltest type), tunnel
 * (всё остальное где outbound найден в списке).
 */

import type {
  CatalogPreset,
  SingboxProxyGroup,
  SingboxRouterPreset,
  SingboxRouterRule,
  SingboxRouterRuleSet,
  SingboxRouterOutbound,
  SingboxTunnel,
  Subscription,
} from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import type {
  MatcherChip,
  OutboundDisplay,
  RuleAction,
  RuleCardData,
} from './types';
import { detectService } from './serviceDetection';
import { resolveRuleSetDisplayType } from '$lib/utils/ruleSetType';
import { formatIpCidrForList } from '$lib/utils/singboxInlineRules';
import { COMPOSITE_OUTBOUND_TYPES, resolveCompositeOutboundView } from './compositeOutboundDisplay';
import {
  classifyRuleSimplicity,
  isCustomInlineRuleSetTag,
  isSystemRule,
  type RuleSimplicity,
} from './simpleRule';

/* ─── System rule detection ─────────────────────────────────────────── */

export { isSystemRule };

/** Пояснение для системных правил — тултип в простом и экспертном режиме. */
export function systemRuleTooltip(rule: SingboxRouterRule): string | undefined {
  if (rule.action === 'sniff') {
    return 'Анализирует протокол и извлекает домен из соединения (SNI, HTTP Host). Нужно, чтобы следующие правила могли маршрутизировать по домену, а не только по IP.';
  }
  if (rule.action === 'hijack-dns') {
    return 'Перехватывает DNS-запросы на порту 53 и обрабатывает их через DNS-модуль sing-box. Без этого DNS-маршрутизация не работает.';
  }
  if (rule.ip_is_private && rule.outbound === 'direct') {
    return 'Трафик в локальные и приватные сети (RFC1918, loopback, link-local) идёт напрямую, минуя VPN. Нужен для доступа к роутеру, NAS и устройствам в LAN.';
  }
  return undefined;
}

/* ─── Action mapping ────────────────────────────────────────────────── */

export function mapRuleAction(rule: SingboxRouterRule): RuleAction {
  if (rule.action === 'reject') return 'block';
  if (rule.action === 'sniff') return 'sniff';
  if (rule.action === 'hijack-dns') return 'hijack-dns';
  if (rule.outbound === 'direct') return 'direct';
  return 'route';
}

/* ─── Outbound display ──────────────────────────────────────────────── */

const COMPOSITE_TYPES = COMPOSITE_OUTBOUND_TYPES;
const AWG_OPTION_GROUPS = new Set(['AWG туннели', 'Системные WireGuard']);

function findOutboundOption(
  tag: string,
  outboundOptions: OutboundGroup[] | undefined,
): { label: string; group: string } | null {
  for (const group of outboundOptions ?? []) {
    const item = group.items?.find((x) => x.value === tag);
    if (item) return { label: item.label, group: group.group };
  }
  return null;
}

import { splitParenMeta } from './outboundLabelFormat';

const SINGBOX_TUNNEL_GROUP = 'Sing-box туннели';

function proxyMetaSuffix(tag: string, tunnels: SingboxTunnel[]): string | undefined {
  return tunnels.find((t) => t.tag === tag)?.proxyInterface;
}

function mapCompositeOutbound(
  expanded: NonNullable<ReturnType<typeof resolveCompositeOutboundView>>,
  singboxTunnels: SingboxTunnel[] = [],
): OutboundDisplay {
  const isSubscription = expanded.isSubscription;
  return {
    name: expanded.groupTitle,
    label: expanded.groupTitle,
    metaSuffix: isSubscription ? 'sub' : undefined,
    kind: isSubscription ? 'subscription' : 'composite',
    tone: isSubscription ? 'subscription' : 'composite',
    compositeType: expanded.compositeType,
    activeMemberLabel: expanded.activeMemberLabel,
    activeMemberTitle: expanded.activeMemberTag,
    activeMemberMetaSuffix: proxyMetaSuffix(expanded.activeMemberTag, singboxTunnels),
    otherMemberLabels: expanded.otherMemberLabels,
    otherMemberTitles: expanded.otherMemberTags,
  };
}

export function resolveOutboundDisplay(
  name: string | undefined,
  action: RuleAction,
  outbounds: SingboxRouterOutbound[],
  outboundOptions: OutboundGroup[] = [],
  subscriptions: Subscription[] | null = null,
  proxyGroups: SingboxProxyGroup[] = [],
  singboxTunnels: SingboxTunnel[] = [],
): OutboundDisplay {
  // System actions — render as mono badges instead of destination tile.
  if (action === 'sniff') {
    return { name: name ?? 'sniff', label: 'SNIFF', kind: 'sniff', tone: 'system' };
  }
  if (action === 'hijack-dns') {
    return { name: name ?? 'hijack-dns', label: 'HIJACK-DNS', kind: 'hijack-dns', tone: 'system' };
  }

  if (action === 'block') {
    return { name: name ?? 'block', label: 'Блок', kind: 'block', tone: 'block' };
  }

  if (!name || name === 'direct') {
    return { name: 'direct', label: 'Прямо', kind: 'direct', tone: 'direct' };
  }
  if (name === 'block' || name === 'reject') {
    return { name, label: 'Блок', kind: 'block', tone: 'block' };
  }

  const option = findOutboundOption(name, outboundOptions);
  const ob = outbounds.find((o) => (o as { tag?: string }).tag === name);
  if (option && AWG_OPTION_GROUPS.has(option.group)) {
    const parsed = splitParenMeta(option.label);
    return { name, label: parsed.label, metaSuffix: parsed.metaSuffix, kind: 'awg', tone: 'awg' };
  }
  if (option?.group === SINGBOX_TUNNEL_GROUP) {
    return {
      name,
      label: option.label,
      metaSuffix: proxyMetaSuffix(name, singboxTunnels),
      kind: 'proxy',
      tone: 'proxy',
    };
  }
  if (!ob) {
    const expanded = resolveCompositeOutboundView(name, outbounds, outboundOptions, subscriptions, proxyGroups);
    if (expanded) {
      return { ...mapCompositeOutbound(expanded, singboxTunnels), name };
    }
    const baseLabel = option?.label ?? name;
    const isSingboxProxy = option?.group === SINGBOX_TUNNEL_GROUP
      || singboxTunnels.some((t) => t.tag === name);
    return {
      name,
      label: baseLabel,
      metaSuffix: isSingboxProxy ? proxyMetaSuffix(name, singboxTunnels) : undefined,
      kind: isSingboxProxy ? 'proxy' : option ? 'tunnel' : 'unknown',
      tone: isSingboxProxy || option ? 'proxy' : 'unknown',
    };
  }
  const obType = (ob as { type?: string }).type ?? '';
  if (COMPOSITE_TYPES.has(obType)) {
    const expanded = resolveCompositeOutboundView(name, outbounds, outboundOptions, subscriptions, proxyGroups);
    if (expanded) {
      return { ...mapCompositeOutbound(expanded, singboxTunnels), name };
    }
    return {
      name,
      label: option?.label ?? name,
      kind: 'composite',
      tone: 'composite',
      compositeType: obType as OutboundDisplay['compositeType'],
    };
  }
  const baseLabel = option?.label ?? name;
  const isSingboxProxy = option?.group === SINGBOX_TUNNEL_GROUP
    || singboxTunnels.some((t) => t.tag === name);
  return {
    name,
    label: baseLabel,
    metaSuffix: isSingboxProxy ? proxyMetaSuffix(name, singboxTunnels) : undefined,
    kind: isSingboxProxy ? 'proxy' : 'tunnel',
    tone: 'proxy',
  };
}

/* ─── Matcher chip extraction ───────────────────────────────────────── */

export function extractMatcherChips(
  rule: SingboxRouterRule,
  rulesetLabels: Record<string, string>,
  ruleSets: SingboxRouterRuleSet[] = [],
): MatcherChip[] {
  const chips: MatcherChip[] = [];
  const rulesetTypes = new Map(
    ruleSets.filter((rs) => rs.tag).map((rs) => [rs.tag, resolveRuleSetDisplayType(rs)] as const),
  );

  for (const d of rule.domain_suffix ?? []) {
    chips.push({ kind: 'domain', label: d });
  }
  for (const c of rule.ip_cidr ?? []) {
    chips.push({ kind: 'ip', label: c, mono: true });
  }
  for (const c of rule.source_ip_cidr ?? []) {
    chips.push({ kind: 'src', label: c, mono: true });
  }
  for (const p of rule.port ?? []) {
    chips.push({ kind: 'port', label: String(p), mono: true });
  }
  for (const rs of rule.rule_set ?? []) {
    chips.push({
      kind: 'ruleset',
      label: rulesetLabels[rs] ?? rs,
      rulesetTag: rs,
      rulesetType: rulesetTypes.get(rs),
    });
  }
  if (rule.protocol) {
    chips.push({ kind: 'protocol', label: rule.protocol });
  }
  if (rule.ip_is_private) {
    chips.push({ kind: 'private', label: 'Локальная сеть' });
  }

  return chips;
}

/** Первый домен из inline rules (domain_suffix, затем domain) — для custom-N карточек. */
export function firstDomainFromInlineRules(rules: Record<string, unknown>[] | undefined): string | undefined {
  if (!Array.isArray(rules)) return undefined;
  for (const r of rules) {
    const suffixes = r['domain_suffix'];
    if (Array.isArray(suffixes)) {
      for (const s of suffixes) {
        if (typeof s === 'string' && s.trim()) return s.trim();
      }
    }
    const domains = r['domain'];
    if (Array.isArray(domains)) {
      for (const d of domains) {
        if (typeof d === 'string' && d.trim()) return d.trim();
      }
    }
  }
  return undefined;
}

function chipsFromInlineRules(rules: Record<string, unknown>[] | undefined): MatcherChip[] {
  const chips: MatcherChip[] = [];
  if (!Array.isArray(rules)) return chips;

  for (const r of rules) {
    const suffixes = r['domain_suffix'];
    if (Array.isArray(suffixes)) {
      for (const s of suffixes) {
        if (typeof s === 'string') chips.push({ kind: 'domain', label: s });
      }
    }
    const domains = r['domain'];
    if (Array.isArray(domains)) {
      for (const d of domains) {
        if (typeof d === 'string') chips.push({ kind: 'domain', label: d });
      }
    }
    const cidrs = r['ip_cidr'];
    if (Array.isArray(cidrs)) {
      for (const c of cidrs) {
        if (typeof c === 'string') {
          chips.push({ kind: 'ip', label: formatIpCidrForList(c), mono: true });
        }
      }
    }
  }
  return chips;
}

/** Matcher chips с учётом простого режима (разворот custom-N inline). */
export function extractMatchersForCard(
  rule: SingboxRouterRule,
  simplicity: RuleSimplicity,
  rulesetLabels: Record<string, string>,
  ruleSets: SingboxRouterRuleSet[] = [],
): MatcherChip[] {
  if (!simplicity.simple) {
    return extractMatcherChips(rule, rulesetLabels, ruleSets);
  }

  if (simplicity.kind === 'inline-text') {
    const chips: MatcherChip[] = [];
    for (const d of rule.domain_suffix ?? []) {
      chips.push({ kind: 'domain', label: d });
    }
    for (const c of rule.ip_cidr ?? []) {
      chips.push({ kind: 'ip', label: c, mono: true });
    }
    return chips;
  }

  if (simplicity.kind === 'inline-set' && simplicity.inlineRuleSetTag) {
    const tag = simplicity.inlineRuleSetTag;
    if (isCustomInlineRuleSetTag(tag)) {
      const rs = ruleSets.find((r) => r.tag === tag);
      if (rs?.type === 'inline' && rs.rules?.length) {
        return chipsFromInlineRules(rs.rules);
      }
    }
    return extractMatcherChips(rule, rulesetLabels, ruleSets);
  }

  return extractMatcherChips(rule, rulesetLabels, ruleSets);
}

/* ─── Title fallback ────────────────────────────────────────────────── */

function fallbackTitle(
  rule: SingboxRouterRule,
  serviceKey: string,
  index: number,
  displayName?: string,
): string {
  if (displayName) return displayName;
  if (serviceKey !== 'custom') {
    return serviceKey.charAt(0).toUpperCase() + serviceKey.slice(1).replace('_', ' ');
  }
  if (rule.ip_is_private) return 'Локальная сеть';
  if (rule.action === 'sniff') return 'Анализ протокола';
  if (rule.action === 'hijack-dns') return 'Перехват DNS';
  if (rule.domain_suffix?.length) return rule.domain_suffix[0];
  if (rule.ip_cidr?.length) return rule.ip_cidr[0];
  if (rule.rule_set?.length) return rule.rule_set[0];
  return `Правило #${index + 1}`;
}

/* ─── Subtitle (system rules show technical detail per design) ─────── */

function systemSubtitle(rule: SingboxRouterRule): string | undefined {
  if (rule.action === 'sniff') return 'sniff';
  if (rule.action === 'hijack-dns') return 'protocol=dns OR port=53';
  if (rule.ip_is_private) return 'RFC1918 · loopback · link-local · CGNAT';
  return undefined;
}

/* ─── Stable id ─────────────────────────────────────────────────────── */

function ruleId(rule: SingboxRouterRule, index: number): string {
  const sig = [
    rule.domain_suffix?.[0],
    rule.ip_cidr?.[0],
    rule.rule_set?.[0],
    rule.protocol,
    rule.ip_is_private ? 'priv' : null,
  ].filter(Boolean).join('|');
  return `${index}:${rule.outbound ?? 'no-ob'}:${sig || 'empty'}`;
}

/* ─── Main adapter ──────────────────────────────────────────────────── */

export function singboxRuleToCard(
  rule: SingboxRouterRule,
  index: number,
  outbounds: SingboxRouterOutbound[],
  rulesetLabels: Record<string, string>,
  routerPresets: SingboxRouterPreset[] = [],
  outboundOptions: OutboundGroup[] = [],
  catalog: CatalogPreset[] = [],
  ruleSets: SingboxRouterRuleSet[] = [],
  subscriptions: Subscription[] | null = null,
  proxyGroups: SingboxProxyGroup[] = [],
  singboxTunnels: SingboxTunnel[] = [],
): RuleCardData {
  const simplicity = classifyRuleSimplicity(rule, ruleSets);

  let customInlineFirstDomain: string | undefined;
  if (
    simplicity.simple
    && simplicity.kind === 'inline-set'
    && simplicity.inlineRuleSetTag
    && isCustomInlineRuleSetTag(simplicity.inlineRuleSetTag)
  ) {
    const rs = ruleSets.find((r) => r.tag === simplicity.inlineRuleSetTag);
    if (rs?.type === 'inline') {
      customInlineFirstDomain = firstDomainFromInlineRules(rs.rules);
    }
  }

  const detected = customInlineFirstDomain
    ? detectService({ domain_suffix: [customInlineFirstDomain] }, routerPresets, catalog)
    : detectService(rule, routerPresets, catalog);
  const serviceKey = detected.iconSlug;
  const action = mapRuleAction(rule);
  const outbound = resolveOutboundDisplay(
    rule.outbound,
    action,
    outbounds,
    outboundOptions,
    subscriptions,
    proxyGroups,
    singboxTunnels,
  );
  const matchers = extractMatchersForCard(rule, simplicity, rulesetLabels, ruleSets);
  const isSystem = isSystemRule(rule);
  const title = customInlineFirstDomain
    ? customInlineFirstDomain
    : fallbackTitle(rule, serviceKey, index, detected.displayName);
  const subtitle = isSystem
    ? systemSubtitle(rule)
    : matchers.length > 4
      ? `${matchers.length} матчеров`
      : undefined;

  return {
    id: ruleId(rule, index),
    serviceKey,
    title,
    subtitle,
    matchers,
    action,
    outbound,
    isSystem,
    simplicity,
    tooltip: isSystem ? systemRuleTooltip(rule) : undefined,
  };
}
