import { m } from '$lib/i18n';
import { DEFAULT_SUBSCRIPTION_URLTEST, type SingboxRouterOutbound } from '$lib/types';

const COMPOSITE_TYPES = new Set(['selector', 'urltest', 'loadbalance']);

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

/** Ищет composite outbound с ровно тем же набором участников (порядок не важен). */
export function findMatchingComposite(
  outbounds: SingboxRouterOutbound[],
  tunnelTags: string[],
): SingboxRouterOutbound | undefined {
  return outbounds.find((o) => {
    if (!isCompositeOutboundType(o.type)) return false;
    const members = o.outbounds ?? [];
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
}

export type WizardOutboundCategory = 'tunnel' | 'direct' | 'block';

/** Текст предпросмотра outbound на шаге 3 визарда. */
export function formatTunnelOutboundPreview(preview: TunnelOutboundPreview): string {
  if (preview.tunnelCount === 1) {
    return m.sb_router_wizard_preview_route({ tag: preview.outboundTag });
  }
  if (preview.willCreate) {
    return m.sb_router_wizard_preview_create_composite({
      tag: preview.outboundTag,
      count: preview.tunnelCount,
    });
  }
  return m.sb_router_wizard_preview_use_composite({
    tag: preview.outboundTag,
    count: preview.tunnelCount,
  });
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
): TunnelOutboundPreview | null {
  if (tunnelTags.length === 0) return null;
  if (tunnelTags.length === 1) {
    return { outboundTag: tunnelTags[0], willCreate: false, tunnelCount: 1 };
  }
  const existing = findMatchingComposite(outbounds, tunnelTags);
  if (existing) {
    return { outboundTag: existing.tag, willCreate: false, tunnelCount: tunnelTags.length };
  }
  const tags = outbounds.map((o) => o.tag);
  return {
    outboundTag: nextCustomCompositeTag(tags),
    willCreate: true,
    tunnelCount: tunnelTags.length,
  };
}

export function buildWizardCompositeOutbound(
  tag: string,
  tunnelTags: string[],
): SingboxRouterOutbound {
  return {
    type: 'urltest',
    tag,
    outbounds: [...tunnelTags],
    url: DEFAULT_SUBSCRIPTION_URLTEST.url,
    interval: `${DEFAULT_SUBSCRIPTION_URLTEST.intervalSec}s`,
    tolerance: DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs,
  };
}
