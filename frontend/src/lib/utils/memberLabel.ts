// Resolves an outbound member tag (awg-awgN, sub-XXXX-YYYY и т.п.) to a
// human-readable label suitable for chip display in composite outbound
// cards / dropdowns (issue #214).
//
// Resolution order:
//   1. Subscription selector: sub.label когда tag совпадает с sub.selectorTag —
//      покрывает чистое имя подписки для тэга-селектора (sub-XXXX).
//   2. Subscription member: sub.members[i].label (or server) когда tag
//      совпадает с одним из sub.members[i].tag — это покрывает
//      sub-XXX-YYY формат.
//   3. outboundOptions flat lookup (покрывает awg-awgN тэги — там label
//      уже сделан билдером как "${t.label} (${t.iface})").
//   4. Fallback: исходный тэг (не теряем информацию когда мапа неполная).

import type { Subscription } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

export function resolveMemberLabel(
	tag: string,
	subscriptions: Subscription[] | undefined | null,
	outboundOptions: OutboundGroup[] | undefined | null,
): string {
	if (!tag) return tag;

	if (subscriptions && subscriptions.length > 0) {
		const selector = subscriptions.find((s) => s.selectorTag === tag);
		if (selector) {
			return selector.label || tag;
		}
		for (const sub of subscriptions) {
			const m = sub.members?.find((x) => x.tag === tag);
			if (m) {
				return m.label || m.server || tag;
			}
		}
	}

	if (outboundOptions && outboundOptions.length > 0) {
		for (const g of outboundOptions) {
			const item = g.items?.find((x) => x.value === tag);
			if (item) {
				return item.label;
			}
		}
	}

	return tag;
}
