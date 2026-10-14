import type { SingboxRouterDNSServer } from '$lib/types';

/** Managed final DNS server — detour must stay unset (direct WAN dial). */
export const DNS_DIRECT_SERVER_TAG = 'dns-direct';

/** Empty, null, and explicit `direct` all mean «no detour key in config». */
export function normalizeDnsServerDetour(detour?: string | null): string | undefined {
	const d = detour?.trim();
	if (!d || d === 'direct') return undefined;
	return d;
}

/** Strip unset detour so JSON/API never emit `"detour": null`. */
export function sanitizeDnsServerForApi(
	server: SingboxRouterDNSServer,
): SingboxRouterDNSServer {
	const detour = normalizeDnsServerDetour(server.detour);
	const out: SingboxRouterDNSServer = {
		tag: server.tag,
		type: server.type,
		server: server.server,
	};
	if (server.server_port != null && server.server_port > 0) {
		out.server_port = server.server_port;
	}
	if (server.path?.trim()) out.path = server.path.trim();
	if (server.domain_strategy) out.domain_strategy = server.domain_strategy;
	if (server.domain_resolver) out.domain_resolver = { ...server.domain_resolver };
	const tls = server.tls;
	if (tls) {
		const cleanTLS = {
			...(tls.server_name?.trim() ? { server_name: tls.server_name.trim() } : {}),
			...(tls.insecure ? { insecure: true } : {}),
			...(tls.alpn?.map((value) => value.trim()).filter(Boolean).length
				? { alpn: tls.alpn.map((value) => value.trim()).filter(Boolean) }
				: {}),
			...(tls.min_version ? { min_version: tls.min_version } : {}),
			...(tls.max_version ? { max_version: tls.max_version } : {}),
			...(tls.certificate_public_key_sha256?.map((value) => value.trim()).filter(Boolean).length
				? { certificate_public_key_sha256: tls.certificate_public_key_sha256.map((value) => value.trim()).filter(Boolean) }
				: {}),
		};
		if (Object.keys(cleanTLS).length) out.tls = cleanTLS;
	}
	if (detour && !shouldOmitDnsServerDetour(server.tag, detour)) {
		out.detour = detour;
	}
	// С detour sing-box адрес отправителя игнорирует, а бэкенд такую пару
	// отвергает: адрес уходит только у сервера, который ходит напрямую.
	const bind = server.inet4_bind_address?.trim();
	if (bind && !out.detour && server.tag.trim() !== DNS_DIRECT_SERVER_TAG) {
		out.inet4_bind_address = bind;
	}
	return out;
}

export function isDnsServerEmptyDetour(detour?: string | null): boolean {
	return normalizeDnsServerDetour(detour) === undefined;
}

/** Whether detour should be omitted when serializing a DNS server. */
export function shouldOmitDnsServerDetour(
	tag: string,
	detour?: string | null,
): boolean {
	if (tag.trim() === DNS_DIRECT_SERVER_TAG) return true;
	return normalizeDnsServerDetour(detour) === undefined;
}
