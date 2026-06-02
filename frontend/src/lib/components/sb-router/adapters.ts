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

import type { SingboxRouterPreset, SingboxRouterRule, SingboxRouterOutbound } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import type {
  MatcherChip,
  OutboundDisplay,
  RuleAction,
  RuleCardData,
} from './types';
import { detectService } from './serviceDetection';

/* ─── System rule detection ─────────────────────────────────────────── */

export function isSystemRule(rule: SingboxRouterRule): boolean {
  if (rule.action === 'sniff' || rule.action === 'hijack-dns') return true;
  if (rule.ip_is_private && rule.outbound === 'direct') return true;
  return false;
}

/* ─── Action mapping ────────────────────────────────────────────────── */

function mapAction(rule: SingboxRouterRule): RuleAction {
  if (rule.action === 'reject') return 'block';
  if (rule.action === 'sniff') return 'sniff';
  if (rule.action === 'hijack-dns') return 'hijack-dns';
  if (rule.outbound === 'direct') return 'direct';
  return 'route';
}

/* ─── Outbound display ──────────────────────────────────────────────── */

const COMPOSITE_TYPES = new Set(['selector', 'urltest']);
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

export function resolveOutboundDisplay(
  name: string | undefined,
  action: RuleAction,
  outbounds: SingboxRouterOutbound[],
  outboundOptions: OutboundGroup[] = [],
): OutboundDisplay {
  // System actions — render as mono badges instead of destination tile.
  if (action === 'sniff') {
    return { name: name ?? 'sniff', label: 'SNIFF', kind: 'sniff' };
  }
  if (action === 'hijack-dns') {
    return { name: name ?? 'hijack-dns', label: 'HIJACK-DNS', kind: 'hijack-dns' };
  }

  if (action === 'block') {
    return { name: name ?? 'block', label: 'Блок', kind: 'block' };
  }

  if (!name || name === 'direct') {
    return { name: 'direct', label: 'Прямо', kind: 'direct' };
  }
  if (name === 'block' || name === 'reject') {
    return { name, label: 'Блок', kind: 'block' };
  }

  const option = findOutboundOption(name, outboundOptions);
  const ob = outbounds.find((o) => (o as { tag?: string }).tag === name);
  if (option && AWG_OPTION_GROUPS.has(option.group)) {
    return { name, label: option.label, kind: 'awg' };
  }
  if (!ob) {
    return { name, label: option?.label ?? name, kind: option ? 'tunnel' : 'unknown' };
  }
  const obType = (ob as { type?: string }).type ?? '';
  if (COMPOSITE_TYPES.has(obType)) {
    return { name, label: option?.label ?? name, kind: 'composite' };
  }
  return { name, label: option?.label ?? name, kind: 'tunnel' };
}

/* ─── Matcher chip extraction ───────────────────────────────────────── */

export function extractMatcherChips(
  rule: SingboxRouterRule,
  rulesetLabels: Record<string, string>,
): MatcherChip[] {
  const chips: MatcherChip[] = [];

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
    chips.push({ kind: 'ruleset', label: rulesetLabels[rs] ?? rs });
  }
  if (rule.protocol) {
    chips.push({ kind: 'protocol', label: rule.protocol });
  }
  if (rule.ip_is_private) {
    chips.push({ kind: 'private', label: 'Локальная сеть' });
  }

  return chips;
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
): RuleCardData {
  const detected = detectService(rule, routerPresets);
  const serviceKey = detected.iconSlug;
  const action = mapAction(rule);
  const outbound = resolveOutboundDisplay(rule.outbound, action, outbounds, outboundOptions);
  const matchers = extractMatcherChips(rule, rulesetLabels);
  const isSystem = isSystemRule(rule);
  const title = fallbackTitle(rule, serviceKey, index, detected.displayName);
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
  };
}
