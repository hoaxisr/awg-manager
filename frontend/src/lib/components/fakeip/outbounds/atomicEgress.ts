// Builds the ATOMIC section of the FakeIP «Outbounds» chip: the proxy egress
// pool (FE-spec §5.3, mockup page-outbounds-v3 ATOMIC cards).
//
// The atomic pool is NOT the router-catalog `direct` outbounds (there are
// usually none). It is every SELECTABLE egress that can appear as a composite
// MEMBER — i.e. AWG/NWG tunnels + subscription proxies — EXCLUDING:
//   - the «Специальные» group (direct / block — not real proxy egresses);
//   - the «Composite outbounds» group (those render in the COMPOSITE section).
//
// Spine: `singboxRouter.options` (OutboundGroup[]) is the canonical "what can
// be a member" list — we walk it and keep the non-special, non-composite
// items. Each item is then enriched by joining:
//   - SingboxTunnel (by tag) → protocol badge, server:port, sni, transport;
//   - SubscriptionMember (by tag) → protocol, server:port, sni, transport.
// resolveMemberLabel gives the display name+flag exactly as composite members
// render, so atomic cards match the member list.
//
// HONESTY (§4): only fields that EXIST for a given egress are carried; tunnels
// have protocol/endpoint but no sni/flow unless the join provides them,
// subscription proxies carry protocol/server/sni/transport. The card renders
// sni/transport lines only when present. Latency is on-demand, per-egress, and
// the only number — no throughput.

import type { SingboxTunnel, Subscription } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import { resolveMemberLabel } from '$lib/utils/memberLabel';

// Groups excluded from the atomic pool. Specials are local pseudo-egresses;
// composites belong to the COMPOSITE section. Matched by the group labels
// buildOutboundOptions emits.
const EXCLUDED_GROUPS = new Set(['Специальные', 'Composite outbounds']);

/** Origin of an atomic egress — drives the read-only link-out target. */
export type AtomicEgressSource = 'tunnel' | 'subscription' | 'unknown';

export interface AtomicEgress {
	/** Outbound tag (stable id, used for delay-check + keying). */
	tag: string;
	/** Display name (with flag) — same resolution as composite members. */
	name: string;
	/** Protocol / type badge text (vless, hysteria2, awg, …). */
	proto: string;
	/** «server:port» when known. */
	endpoint?: string;
	/** TLS SNI, when the egress has one. */
	sni?: string;
	/** Transport / flow detail line (grpc, xtls-rprx-vision, reality, …). */
	transport?: string;
	/** Where this egress is managed — for the read-only link-out. */
	source: AtomicEgressSource;
	/** Subscription id (link-out target) when source === 'subscription'. */
	subscriptionId?: string;
}

/** «server:port», or just server when port is missing/zero. */
function endpoint(server: string | undefined, port: number | undefined): string | undefined {
	if (!server) return undefined;
	return port ? `${server}:${port}` : server;
}

/**
 * Transport/flow detail for a tunnel: combine security + transport into one
 * honest line (e.g. «reality · grpc»), dropping the noise-y «none»/«tcp».
 */
function tunnelTransport(t: SingboxTunnel): string | undefined {
	const parts: string[] = [];
	if (t.security && t.security !== 'none') parts.push(t.security);
	if (t.transport && t.transport !== 'tcp') parts.push(t.transport);
	return parts.length ? parts.join(' · ') : undefined;
}

/**
 * Builds the atomic egress pool from the options spine, enriched by tunnels +
 * subscription members. Order follows the options groups (config order).
 */
export function buildAtomicEgresses(
	options: OutboundGroup[] | undefined | null,
	tunnels: SingboxTunnel[] | undefined | null,
	subscriptions: Subscription[] | undefined | null,
): AtomicEgress[] {
	const groups = options ?? [];
	const tunnelByTag = new Map((tunnels ?? []).map((t) => [t.tag, t]));

	// Map each subscription member tag → { member, subscriptionId } for join +
	// link-out. A member tag belongs to exactly one subscription.
	const subMemberByTag = new Map<
		string,
		{ member: Subscription['members'][number]; subscriptionId: string }
	>();
	for (const sub of subscriptions ?? []) {
		for (const m of sub.members ?? []) {
			subMemberByTag.set(m.tag, { member: m, subscriptionId: sub.id });
		}
	}

	const out: AtomicEgress[] = [];
	const seen = new Set<string>();

	for (const group of groups) {
		if (EXCLUDED_GROUPS.has(group.group)) continue;
		for (const item of group.items ?? []) {
			const tag = item.value;
			if (!tag || seen.has(tag)) continue;
			seen.add(tag);

			const name = resolveMemberLabel(tag, subscriptions, groups);

			const tunnel = tunnelByTag.get(tag);
			if (tunnel) {
				out.push({
					tag,
					name,
					proto: tunnel.protocol,
					endpoint: endpoint(tunnel.server, tunnel.port),
					sni: tunnel.sni || undefined,
					transport: tunnelTransport(tunnel),
					source: 'tunnel',
				});
				continue;
			}

			const sub = subMemberByTag.get(tag);
			if (sub) {
				const m = sub.member;
				out.push({
					tag,
					name,
					proto: m.protocol,
					endpoint: endpoint(m.server, m.port),
					sni: m.sni || undefined,
					transport: m.transport || undefined,
					source: 'subscription',
					subscriptionId: sub.subscriptionId,
				});
				continue;
			}

			// In the options spine but joined to neither tunnels nor subs — an
			// AWG/system egress or one whose enrichment hasn't loaded. Keep it
			// (config list is honest) with the group name as the proto badge.
			out.push({
				tag,
				name,
				proto: group.group,
				source: 'unknown',
			});
		}
	}

	return out;
}
