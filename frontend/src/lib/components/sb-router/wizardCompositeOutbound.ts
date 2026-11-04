import { m } from '$lib/i18n';
import { DEFAULT_SUBSCRIPTION_URLTEST, type SingboxRouterOutbound } from '$lib/types';
import type { WizardCompositeMode } from './addWizardStore';

const COMPOSITE_TYPES = new Set(['selector', 'urltest', 'loadbalance', 'fallback']);

export function sameOutboundMemberSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const setA = new Set(a);
  for (const x of b) {
    if (!setA.has(x)) return false;
  }
  return true;
}

export function isCompositeOutboundType(type: string): boolean {
  return COMPOSITE_TYPES.has(type);
}

/** Режим визарда для типа composite; legacy `loadbalance` и не-composite → undefined. */
export function normalizeCompositeType(type: string): WizardCompositeMode | undefined {
  if (type === 'selector' || type === 'urltest' || type === 'fallback') return type;
  return undefined;
}

/** Ищет composite outbound с ровно тем же набором участников (и типом, если передан). */
export function findMatchingComposite(
  outbounds: SingboxRouterOutbound[],
  tunnelTags: string[],
  mode?: WizardCompositeMode,
): SingboxRouterOutbound | undefined {
  return outbounds.find((o) => {
    if (!isCompositeOutboundType(o.type)) return false;
    const normalizedType = normalizeCompositeType(o.type);
    if (mode && normalizedType !== mode) return false;
    const members = o.outbounds ?? [];
    if (mode === 'fallback' || normalizedType === 'fallback') {
      if (members.length !== tunnelTags.length) return false;
      return members.every((m, idx) => m === tunnelTags[idx]);
    }
    return sameOutboundMemberSet(members, tunnelTags);
  });
}

/** Первый свободный тег custom-composite-N среди существующих outbound'ов. */
export function nextCustomCompositeTag(existing: string[]): string {
  const set = new Set(existing);
  let n = 1;
  while (set.has(`custom-composite-${n}`)) n++;
  return `custom-composite-${n}`;
}

export interface TunnelOutboundPreview {
  outboundTag: string;
  willCreate: boolean;
  tunnelCount: number;
  mode?: WizardCompositeMode;
}

export type WizardOutboundCategory = 'tunnel' | 'direct' | 'block';

function compositeModeLabel(mode?: WizardCompositeMode): string {
  switch (mode) {
    case 'fallback':
      return m.sb_router_wizard_mode_fallback_label();
    case 'selector':
      return m.sb_router_wizard_mode_selector_label();
    case 'urltest':
    default:
      return m.sb_router_wizard_mode_urltest_label();
  }
}

/** Текст предпросмотра outbound на шаге 3 визарда. */
export function formatTunnelOutboundPreview(preview: TunnelOutboundPreview): string {
  if (preview.tunnelCount === 1) {
    return m.sb_router_wizard_preview_route({ tag: preview.outboundTag });
  }
  const params = {
    tag: preview.outboundTag,
    count: preview.tunnelCount,
    mode: compositeModeLabel(preview.mode),
  };
  return preview.willCreate
    ? m.sb_router_wizard_preview_create_composite(params)
    : m.sb_router_wizard_preview_use_composite(params);
}

export function formatWizardOutboundPreview(
  category: WizardOutboundCategory | null,
  preview: TunnelOutboundPreview | null,
  directTag = 'direct',
): string | null {
  if (category === 'direct') {
    return m.sb_router_wizard_preview_direct({ tag: directTag });
  }
  if (category === 'block') {
    return m.sb_router_wizard_preview_block();
  }
  if (category === 'tunnel' && preview) {
    return formatTunnelOutboundPreview(preview);
  }
  return null;
}

/** Предпросмотр: какой outbound будет использован для выбранных туннелей. */
export function previewTunnelOutboundResolution(
  tunnelTags: string[],
  outbounds: SingboxRouterOutbound[],
  mode: WizardCompositeMode = 'urltest',
): TunnelOutboundPreview | null {
  if (tunnelTags.length === 0) return null;
  if (tunnelTags.length === 1) {
    return { outboundTag: tunnelTags[0], willCreate: false, tunnelCount: 1, mode };
  }
  const existing = findMatchingComposite(outbounds, tunnelTags, mode);
  if (existing) {
    const existingMode = normalizeCompositeType(existing.type);
    return {
      outboundTag: existing.tag,
      willCreate: false,
      tunnelCount: tunnelTags.length,
      mode: existingMode || mode,
    };
  }
  const tags = outbounds.map((o) => o.tag);
  return {
    outboundTag: nextCustomCompositeTag(tags),
    willCreate: true,
    tunnelCount: tunnelTags.length,
    mode,
  };
}

export function buildWizardCompositeOutbound(
  tag: string,
  tunnelTags: string[],
  mode: WizardCompositeMode = 'urltest',
): SingboxRouterOutbound {
  const base: SingboxRouterOutbound = {
    type: mode,
    tag,
    outbounds: [...tunnelTags],
  };
  if (mode === 'urltest') {
    base.url = DEFAULT_SUBSCRIPTION_URLTEST.url;
    base.interval = `${DEFAULT_SUBSCRIPTION_URLTEST.intervalSec}s`;
    base.tolerance = DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs;
  } else if (mode === 'fallback') {
    base.url = DEFAULT_SUBSCRIPTION_URLTEST.url;
    base.interval = `${DEFAULT_SUBSCRIPTION_URLTEST.intervalSec}s`;
    base.interrupt_exist_connections = true;
  } else if (mode === 'selector') {
    base.default = tunnelTags[0];
  }
  return base;
}
