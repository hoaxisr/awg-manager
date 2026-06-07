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

const PATTERNS: Array<{ re: RegExp; label: (name: string) => string }> = [
	{ re: /^outbounds\[\d+="(.*)"\]\.outbounds\[\d+\]$/, label: (n) => `Входит в группу маршрутов «${n}»` },
	{ re: /^outbounds\[\d+="(.*)"\]\.default$/, label: (n) => `Выбран по умолчанию в группе «${n}»` },
	{ re: /^dns\.servers\[\d+="(.*)"\]\.detour$/, label: (n) => `Используется DNS-сервером «${n}»` },
	{ re: /^route\.rule_set\[\d+="(.*)"\]\.download_detour$/, label: (n) => `Через него скачивается список «${n}»` }
];

export function describeRouterReference(loc: string): RouterReference {
	if (loc === 'route.final') {
		return { text: 'Назначен маршрутом по умолчанию', known: true };
	}
	for (const { re, label } of PATTERNS) {
		const m = loc.match(re);
		if (m) {
			return { text: label(m[1]), known: true };
		}
	}
	return { text: loc, known: false };
}
