import type {
	SingboxRouterDNSServer,
	SingboxRouterOutbound,
	SingboxRouterRule,
	SingboxRouterRuleSet,
} from '$lib/types';

export type OutboundUsageInput = {
	tag: string;
	rules: readonly SingboxRouterRule[];
	routeFinal: string;
	outbounds: readonly SingboxRouterOutbound[];
	dnsServers: readonly SingboxRouterDNSServer[];
	ruleSets: readonly SingboxRouterRuleSet[];
	deviceProxyOutbounds?: readonly string[];
};

function ruleOutboundTags(rule: SingboxRouterRule, acc = new Set<string>()): Set<string> {
	if (rule.outbound) acc.add(rule.outbound);
	for (const nested of rule.rules ?? []) ruleOutboundTags(nested, acc);
	return acc;
}

/** Один проход по конфигу: ссылки для всех интересующих тегов сразу. */
function collectAllOutboundReferences(
	tags: readonly string[],
	input: Omit<OutboundUsageInput, 'tag'>,
): Map<string, string[]> {
	const refs = new Map<string, string[]>(tags.map((t) => [t, []]));
	const push = (tag: string | undefined, ref: string) => {
		if (tag) refs.get(tag)?.push(ref);
	};

	input.rules.forEach((r, i) => {
		for (const tag of ruleOutboundTags(r)) push(tag, `route.rules[${i}]`);
	});
	push(input.routeFinal, 'route.final');
	for (const o of input.outbounds) {
		o.outbounds?.forEach((member, j) => push(member, `outbounds[${o.tag}].members[${j}]`));
		push(o.default, `outbounds[${o.tag}].default`);
	}
	for (const s of input.dnsServers) {
		push(s.detour, `dns.servers[${s.tag}].detour`);
	}
	for (const rs of input.ruleSets) {
		push(rs.download_detour, `route.rule_set[${rs.tag}].download_detour`);
	}
	for (const tag of new Set(input.deviceProxyOutbounds ?? [])) {
		push(tag, 'device-proxy');
	}

	return refs;
}

/** Best-effort UI delete guards; API remains source of truth. Also blocks device-proxy usage (frontend-only). */
export function collectOutboundReferences(input: OutboundUsageInput): string[] {
	return collectAllOutboundReferences([input.tag], input).get(input.tag) ?? [];
}

export function formatUsageBlockReason(noun: string, refs: readonly string[]): string | null {
	if (refs.length === 0) return null;
	const preview = refs.slice(0, 3).join(', ');
	return refs.length > 3 ? `${noun} используется (${preview}…)` : `${noun} используется (${preview})`;
}

const SUBSCRIPTION_DELETE_REASON = 'Подписку можно удалить только в разделе «Подписки»';

export function outboundDeleteBlockReason(
	outbound: SingboxRouterOutbound,
	input: Omit<OutboundUsageInput, 'tag'>,
): string | null {
	return outboundDeleteBlockReasons([outbound], input).get(outbound.tag) ?? null;
}

/** Причины блокировки удаления для всего списка за один проход по конфигу. */
export function outboundDeleteBlockReasons(
	outbounds: readonly SingboxRouterOutbound[],
	input: Omit<OutboundUsageInput, 'tag'>,
): Map<string, string | null> {
	const refs = collectAllOutboundReferences(outbounds.map((o) => o.tag), input);
	const reasons = new Map<string, string | null>();
	for (const o of outbounds) {
		reasons.set(
			o.tag,
			o.source === 'subscription'
				? SUBSCRIPTION_DELETE_REASON
				: formatUsageBlockReason('Outbound', refs.get(o.tag) ?? []),
		);
	}
	return reasons;
}
