import type { AWGTagInfo, SingboxRouterOutbound, SingboxTunnel, Subscription } from '$lib/types';
import type { DropdownOption } from '$lib/components/ui';

export interface OutboundGroup {
	group: string;
	items: Array<{ value: string; label: string }>;
}

export function buildOutboundOptions(
	awgTags: AWGTagInfo[] | undefined | null,
	phase1Tunnels: SingboxTunnel[] | undefined | null,
	composite: SingboxRouterOutbound[] | undefined | null,
	includeSpecial = true,
	subscriptions: Subscription[] | undefined | null = null,
	excludeTag: string | null = null,
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
	const awg3 = tags.filter((t) => t.kind === 'awg3');

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

	if (awg3.length > 0) {
		groups.push({
			group: 'Endpoints',
			items: awg3.map((t) => ({
				value: t.tag,
				// AWG3-эндпоинты не имеют kernel-iface — скобки печатаются только
				// когда iface непустой, иначе label остаётся без «(…)».
				label: t.iface ? `${t.label} (${t.iface})` : t.label,
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
		const subs = subscriptions ?? [];
		groups.push({
			group: 'Composite outbounds',
			items: composites.map((o) => {
				if (o.source === 'subscription' && subs.length > 0) {
					const sub = subs.find((s) => s.selectorTag === o.tag);
					if (sub) {
						return { value: o.tag, label: `${sub.label} · ${o.tag}` };
					}
				}
				return { value: o.tag, label: `${o.tag} (${o.type})` };
			}),
		});
	}

	// Exclude one tag (the outbound being edited) so a composite can never
	// be offered as a member of itself — a self-reference FATALs sing-box
	// with a circular-dependency error. Empty groups are dropped.
	const exclude = excludeTag?.trim();
	if (exclude) {
		return groups
			.map((g) => ({ ...g, items: g.items.filter((i) => i.value !== exclude) }))
			.filter((g) => g.items.length > 0);
	}

	return groups;
}

// Download detour dropdown options (для remote rule-set'ов): плоский список
// outbound'ов из OutboundGroup[] + сбрасывающий пункт со значением "" первым.
// resetLabel параметризован — RuleSetAddModal формулирует его как «применится
// автоматически», массовые bulk-detour бары — как «сбросить».
export function buildDownloadDetourOptions(
	outboundOptions: OutboundGroup[],
	resetLabel: string,
): DropdownOption[] {
	return [
		{ value: '', label: resetLabel },
		...outboundOptions.flatMap((g) =>
			g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	];
}
