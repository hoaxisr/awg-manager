import type { AWGTagInfo, Subscription, MihomoNativeSubscription } from '$lib/types';

export interface OutboundHumanNameContext {
  awgTags?: AWGTagInfo[] | null;
  subscriptions?: Subscription[] | null;
  mihomoSubscriptions?: MihomoNativeSubscription[] | null;
  tunnels?: Array<{ tag?: string; name?: string; id?: string }> | null;
}

/**
 * Converts internal technical tags (awg-sys-Wireguard3, awg-awg20, sub-c6bb819f...)
 * into clean, user-friendly card names (Wireguard3 / SW, NuxtAWG, ABV).
 */
export function formatOutboundHumanName(
  tag: string | null | undefined,
  context?: OutboundHumanNameContext,
): string {
  if (!tag) return '';
  const raw = tag.trim();

  // 1. Direct match in awgTags
  if (context?.awgTags) {
    const awg = context.awgTags.find(
      (t) => t.tag === raw || t.tag === `awg-${raw}` || t.tag === `awg-sys-${raw}` || `awg-${t.tag}` === raw || `awg-sys-${t.tag}` === raw,
    );
    if (awg) {
      return awg.label || raw;
    }
  }

  // 2. Direct match in mihomoSubscriptions (name on card)
  if (context?.mihomoSubscriptions) {
    const mSub = context.mihomoSubscriptions.find(
      (s) => s.name === raw || s.id === raw || raw.startsWith(`sub-${s.id?.slice(0, 8)}`) || raw.startsWith(`sub-${s.id}`),
    );
    if (mSub?.name) {
      return mSub.name;
    }
  }

  // 3. Direct match in standard subscriptions
  if (context?.subscriptions) {
    const sub = context.subscriptions.find(
      (s) => s.selectorTag === raw || s.id === raw || raw.startsWith(`sub-${s.id?.slice(0, 8)}`) || s.label === raw,
    );
    if (sub?.label) {
      return sub.label;
    }
  }

  // 4. Direct match in standalone tunnels
  if (context?.tunnels) {
    const tun = context.tunnels.find((t) => t.tag === raw || t.id === raw);
    if (tun?.name) {
      return tun.name;
    }
  }

  // 5. Intelligent fallback prefix cleanup:
  // "awg-sys-Wireguard3" -> "Wireguard3"
  if (raw.startsWith('awg-sys-')) {
    return raw.slice(8);
  }
  // "awg-awg20" -> "awg20"
  if (raw.startsWith('awg-')) {
    return raw.slice(4);
  }
  // "sub-06d59bc1" -> if there is a single subscription, use its name
  if (raw.startsWith('sub-')) {
    if (context?.mihomoSubscriptions && context.mihomoSubscriptions.length > 0) {
      const found = context.mihomoSubscriptions.find((s) => s.id && raw.includes(s.id.slice(0, 6)));
      if (found) return found.name;
      if (context.mihomoSubscriptions.length === 1) return context.mihomoSubscriptions[0].name;
    }
    if (context?.subscriptions && context.subscriptions.length > 0) {
      const found = context.subscriptions.find((s) => s.id && raw.includes(s.id.slice(0, 6)));
      if (found) return found.label;
      if (context.subscriptions.length === 1) return context.subscriptions[0].label;
    }
  }

  return raw;
}
