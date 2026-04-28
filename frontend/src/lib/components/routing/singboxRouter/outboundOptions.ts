import type { AWGTagInfo, SingboxRouterOutbound, SingboxTunnel } from '$lib/types';

export interface OutboundGroup {
	group: string;
	items: Array<{ value: string; label: string }>;
}

export function buildOutboundOptions(
	awgTags: AWGTagInfo[] | undefined | null,
	phase1Tunnels: SingboxTunnel[] | undefined | null,
	composite: SingboxRouterOutbound[] | undefined | null,
	includeSpecial = true,
): OutboundGroup[] {
	// Stores may yield undefined before initial load completes; treat as empty
	// to avoid breaking the dropdown render. Same pattern as defensive `?? []`
	// elsewhere in the routing UI.
	const tags = awgTags ?? [];
	const sbTunnels = phase1Tunnels ?? [];
	const composites = composite ?? [];

	const groups: OutboundGroup[] = [];

	if (includeSpecial) {
		groups.push({
			group: 'Специальные',
			items: [{ value: 'direct', label: 'direct (мимо VPN)' }],
		});
	}

	const managed = tags.filter((t) => t.kind === 'managed');
	const system = tags.filter((t) => t.kind === 'system');

	if (managed.length > 0) {
		groups.push({
			group: 'AWG туннели',
			items: managed.map((t) => ({
				value: t.tag,
				label: `${t.label} (${t.iface})`,
			})),
		});
	}

	if (system.length > 0) {
		groups.push({
			group: 'Системные WireGuard',
			items: system.map((t) => ({
				value: t.tag,
				label: `${t.label} (${t.iface})`,
			})),
		});
	}

	if (sbTunnels.length > 0) {
		groups.push({
			group: 'Sing-box туннели',
			items: sbTunnels.map((t) => ({
				value: t.tag,
				label: t.tag,
			})),
		});
	}

	if (composites.length > 0) {
		groups.push({
			group: 'Composite outbounds',
			items: composites.map((o) => ({
				value: o.tag,
				label: `${o.tag} (${o.type})`,
			})),
		});
	}

	return groups;
}
