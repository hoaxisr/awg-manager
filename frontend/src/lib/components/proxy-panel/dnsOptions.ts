/** DNS resolver mode for proxy clients (-dns-mode), freeturn + wdtt wt-client. */
export const dnsModeOptions = [
	{ value: 'plain', label: 'plain (UDP/53, для роутера)' },
	{ value: 'auto', label: 'auto (UDP, затем DoH)' },
	{ value: 'doh', label: 'doh (DoH)' }
];

export type ProxyDnsMode = (typeof dnsModeOptions)[number]['value'];

export function normalizeProxyDnsMode(mode: string | undefined): ProxyDnsMode {
	return mode === 'plain' || mode === 'doh' || mode === 'auto' ? mode : 'auto';
}
