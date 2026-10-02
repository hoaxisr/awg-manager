import { m } from '$lib/i18n';

/**
 * Translates the machine-readable router reference paths returned by the
 * backend (`ErrTunnelReferenced.RouterOther`, formatted in
 * internal/singbox/router/config.go) into human-readable Russian text for
 * TunnelReferencedModal. Unknown formats fall back to the raw path so the
 * user still sees something if the backend format changes.
 */
export interface RouterReference {
	/** Human-readable description, or the raw location for unknown formats. */
	text: string;
	/** false when the location did not match a known format (render raw). */
	known: boolean;
}

const PATTERNS: Array<{ re: RegExp; label: (captured: string) => string }> = [
	{ re: /^outbounds\[\d+="(.*)"\]\.outbounds\[\d+\]$/, label: (n) => m.tunnel_refs_group_member({ name: n }) },
	{ re: /^outbounds\[\d+="(.*)"\]\.default$/, label: (n) => m.tunnel_refs_group_default({ name: n }) },
	{ re: /^dns\.servers\[\d+="(.*)"\]\.detour$/, label: (n) => m.tunnel_refs_dns_server({ name: n }) },
	{ re: /^route\.rule_set\[\d+="(.*)"\]\.download_detour$/, label: (n) => m.tunnel_refs_rule_set_download({ name: n }) },
	// Приходит только от fakeip-слота: router-слот отдаёт свои правила
	// отдельным списком индексов (RouterRules). Номер 0-based, как в таблице.
	{ re: /^route\.rules\[(\d+)\]$/, label: (n) => m.tunnel_refs_rule({ number: n }) }
];

const FAKEIP_PREFIX = '[fakeip] ';

export function describeRouterReference(loc: string): RouterReference {
	if (loc.startsWith(FAKEIP_PREFIX)) {
		const ref = describeRouterReference(loc.slice(FAKEIP_PREFIX.length));
		return { text: `FakeIP → ${ref.text}`, known: ref.known };
	}
	if (loc === 'route.final') {
		return { text: m.tunnel_refs_final(), known: true };
	}
	for (const { re, label } of PATTERNS) {
		const hit = loc.match(re);
		if (hit) {
			return { text: label(hit[1]), known: true };
		}
	}
	return { text: loc, known: false };
}
