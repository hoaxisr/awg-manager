import pako from 'pako';

export interface VpnLinkResult {
	config: string;
	name: string;
}

/**
 * Decode an AmneziaVPN vpn:// link into a WireGuard/AWG .conf string.
 *
 * Format: vpn://{base64url(4-byte-length-prefix + zlib(JSON))}
 * JSON contains containers[].awg.last_config.config with the .conf content.
 */
export function decodeVpnLink(input: string): VpnLinkResult {
	const trimmed = input.trim();
	if (!trimmed.startsWith('vpn://')) {
		throw new Error('Ссылка должна начинаться с vpn://');
	}

	const encoded = trimmed.slice('vpn://'.length);
	if (!encoded) {
		throw new Error('Неверный формат vpn:// ссылки');
	}

	const bytes = base64UrlDecode(encoded);
	const json = decompress(bytes);
	return extractConfig(json);
}

/**
 * Check if a string looks like a vpn:// link.
 */
export function isVpnLink(input: string): boolean {
	return input.trim().startsWith('vpn://');
}

function base64UrlDecode(input: string): Uint8Array {
	// Replace URL-safe chars with standard base64
	let base64 = input.replace(/-/g, '+').replace(/_/g, '/');

	// Add padding
	const pad = base64.length % 4;
	if (pad === 2) base64 += '==';
	else if (pad === 3) base64 += '=';

	try {
		const binary = atob(base64);
		const bytes = new Uint8Array(binary.length);
		for (let i = 0; i < binary.length; i++) {
			bytes[i] = binary.charCodeAt(i);
		}
		return bytes;
	} catch {
		throw new Error('Неверный формат vpn:// ссылки');
	}
}

function decompress(bytes: Uint8Array): string {
	// First 4 bytes are big-endian uint32 (expected uncompressed length)
	if (bytes.length < 5) {
		throw new Error('Неверный формат vpn:// ссылки');
	}

	const payload = bytes.slice(4);

	// Try zlib decompression first
	try {
		const decompressed = pako.inflate(payload);
		return new TextDecoder().decode(decompressed);
	} catch {
		// Fallback: try entire bytes as plain JSON (legacy format)
		try {
			return new TextDecoder().decode(bytes);
		} catch {
			throw new Error('Не удалось распаковать данные');
		}
	}
}

function extractConfig(jsonStr: string): VpnLinkResult {
	let data: Record<string, unknown>;
	try {
		data = JSON.parse(jsonStr);
	} catch {
		throw new Error('Не удалось распаковать данные');
	}

	const containers = data.containers;
	if (!Array.isArray(containers) || containers.length === 0) {
		throw new Error('Конфигурация AWG не найдена в ссылке');
	}

	// Use last container (as AmneziaVPN does)
	const container = containers[containers.length - 1];

	// Try to find AWG/WireGuard config in the container
	const proto = container.awg || container.wireguard;
	if (!proto) {
		throw new Error('Контейнер не содержит AWG или WireGuard конфигурацию');
	}

	// Extract .conf content from last_config.
	// AmneziaVPN stores last_config as a JSON string (double-encoded),
	// not as a nested object. Handle both cases.
	let config: string | undefined;

	const lastConfig = proto.last_config;
	if (lastConfig) {
		if (typeof lastConfig === 'string') {
			// Double-encoded JSON string — parse inner JSON
			try {
				const inner = JSON.parse(lastConfig);
				if (typeof inner.config === 'string') {
					config = inner.config;
				}
			} catch {
				// Maybe it's the .conf content directly
				if (lastConfig.includes('[Interface]')) {
					config = lastConfig;
				}
			}
		} else if (typeof lastConfig === 'object' && typeof lastConfig.config === 'string') {
			// Already a parsed object
			config = lastConfig.config;
		}
	}

	if (!config) {
		throw new Error(
			'Ссылка не содержит готовой конфигурации клиента. ' +
			'Вероятно, это ссылка полного доступа (Full Access). ' +
			'Экспортируйте конфигурацию конкретного клиента из AmneziaVPN.'
		);
	}

	// DNS substitution
	const dns1 = typeof data.dns1 === 'string' ? data.dns1 : '';
	const dns2 = typeof data.dns2 === 'string' ? data.dns2 : '';
	if (dns1) config = config.replace(/\$PRIMARY_DNS/g, dns1);
	if (dns2) config = config.replace(/\$SECONDARY_DNS/g, dns2);

	// Try to extract a name
	const name =
		(typeof data.description === 'string' && data.description) ||
		(typeof container.description === 'string' && container.description) ||
		'';

	return { config, name };
}
