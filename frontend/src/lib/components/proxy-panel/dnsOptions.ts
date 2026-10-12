import { m } from '$lib/i18n';

/** DNS resolver mode for proxy clients (-dns-mode), freeturn + wdtt wt-client. */
export function dnsModeOptions() {
	return [
		{ value: 'plain', label: m.proxy_option_dns_plain() },
		{ value: 'auto', label: m.proxy_option_dns_auto() },
		{ value: 'doh', label: 'doh (DoH)' }
	];
}

export type ProxyDnsMode = 'plain' | 'auto' | 'doh';

export function normalizeProxyDnsMode(mode: string | undefined): ProxyDnsMode {
	return mode === 'plain' || mode === 'doh' || mode === 'auto' ? mode : 'auto';
}
