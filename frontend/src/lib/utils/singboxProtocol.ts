/**
 * Human-readable display label for a sing-box outbound protocol identifier.
 */
export function singboxProtocolLabel(protocol: string): string {
	switch (protocol) {
		case 'vless':
			return 'VLESS';
		case 'trojan':
			return 'Trojan';
		case 'shadowsocks':
			return 'Shadowsocks';
		case 'hysteria2':
			return 'Hysteria2';
		case 'naive':
			return 'Naive';
		case 'mieru':
			return 'Mieru';
		case 'trusttunnel':
			return 'TrustTunnel';
		default:
			return protocol;
	}
}

/**
 * Extra search keywords associated with a protocol (for example, `hy2` -> `hysteria2`).
 */
export function singboxProtocolAliases(protocol: string): string[] {
	switch (protocol) {
		case 'hysteria2':
			return ['hy2'];
		case 'shadowsocks':
			return ['ss'];
		case 'trusttunnel':
			return ['tt'];
		default:
			return [];
	}
}
