import pako from 'pako';
import { m } from '$lib/i18n';

export type VpnLinkFlavor = 'regular' | 'premium' | 'unknown';

/**
 * Распаковывает vpn:// в JSON-объект без извлечения .conf (для классификации Premium vs клиентская ссылка).
 */
export function decodeVpnLinkPayload(input: string): Record<string, unknown> | null {
	try {
		const trimmed = input.trim();
		if (!trimmed.startsWith('vpn://')) {
			return null;
		}
		const encoded = trimmed.slice('vpn://'.length);
		if (!encoded) {
			return null;
		}
		const bytes = base64UrlDecode(encoded);
		const json = decompress(bytes);
		const data = JSON.parse(json) as Record<string, unknown>;
		return typeof data === 'object' && data !== null ? data : null;
	} catch {
		return null;
	}
}

function looksLikePremiumVpnPayload(data: Record<string, unknown>): boolean {
	if (data.api_config !== undefined && typeof data.api_config === 'object' && data.api_config !== null) {
		return true;
	}
	if (data.auth_data !== undefined && typeof data.auth_data === 'object' && data.auth_data !== null) {
		return true;
	}
	if (typeof data.api_endpoint === 'string' && typeof data.api_key === 'string') {
		return true;
	}
	const st = data.service_type;
	if (st === 'amnezia-premium') {
		return true;
	}
	const apiCfg = data.api_config as Record<string, unknown> | undefined;
	if (apiCfg && apiCfg.service_type === 'amnezia-premium') {
		return true;
	}
	return false;
}

/**
 * vpn:// с API другого портала, который текущий экран импорта не проксирует (отличие от каталога премиум-ЛК).
 */
export function vpnLinkUnsupportedPortalReason(input: string): string | null {
	const payload = decodeVpnLinkPayload(input);
	if (!payload) {
		return null;
	}
	const ac = payload.api_config;
	if (!ac || typeof ac !== 'object' || ac === null) {
		return null;
	}
	const st = (ac as Record<string, unknown>).service_type;
	if (st === 'amnezia-free') {
		return (
			m.vpnlink_amnezia_free_unsupported()
		);
	}
	return null;
}

/**
 * Обычная клиентская vpn:// (containers + last_config) vs ключ Premium / API (api_config, auth_data, …).
 */
export function classifyVpnLink(input: string): VpnLinkFlavor {
	const trimmed = input.trim();
	if (!trimmed.startsWith('vpn://')) {
		return 'unknown';
	}
	try {
		decodeVpnLink(trimmed);
		return 'regular';
	} catch {
		const payload = decodeVpnLinkPayload(trimmed);
		if (!payload) {
			return 'unknown';
		}
		if (looksLikePremiumVpnPayload(payload)) {
			return 'premium';
		}
		return 'unknown';
	}
}

export interface VpnLinkResult {
	config: string;
	name: string;
}

/**
 * Decode an AmneziaVPN vpn:// link into a WireGuard/AWG .conf string.
 *
 * Format: vpn://{base64url(blob)} — обычно 4 байта длины + zlib(JSON); zlib может начинаться с первого маркера 0x78 0x9c.
 * JSON contains containers[].awg.last_config.config with the .conf content.
 */
export function decodeVpnLink(input: string): VpnLinkResult {
	const trimmed = input.trim();
	if (!trimmed.startsWith('vpn://')) {
		throw new Error(m.vpnlink_must_start_with_scheme());
	}

	const encoded = trimmed.slice('vpn://'.length);
	if (!encoded) {
		throw new Error(m.vpnlink_invalid_format());
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
		throw new Error(m.vpnlink_invalid_format());
	}
}

function zlibHeaderOffset(bytes: Uint8Array): number {
	for (let i = 0; i + 1 < bytes.length; i++) {
		if (
			bytes[i] === 0x78 &&
			(bytes[i + 1] === 0x9c || bytes[i + 1] === 0x01 || bytes[i + 1] === 0xda || bytes[i + 1] === 0x5e)
		) {
			return i;
		}
	}
	return -1;
}

function decompress(bytes: Uint8Array): string {
	const tryInflate = (payload: Uint8Array): string | null => {
		try {
			const decompressed = pako.inflate(payload);
			return new TextDecoder().decode(decompressed);
		} catch {
			return null;
		}
	};

	if (bytes.length >= 5) {
		const fromPrefix = tryInflate(bytes.slice(4));
		if (fromPrefix !== null) {
			return fromPrefix;
		}
	}

	const z = zlibHeaderOffset(bytes);
	if (z >= 0) {
		const fromMagic = tryInflate(bytes.slice(z));
		if (fromMagic !== null) {
			return fromMagic;
		}
	}

	const whole = tryInflate(bytes);
	if (whole !== null) {
		return whole;
	}

	try {
		return new TextDecoder().decode(bytes);
	} catch {
		throw new Error(m.vpnlink_decompress_failed());
	}
}

function extractConfig(jsonStr: string): VpnLinkResult {
	let data: Record<string, unknown>;
	try {
		data = JSON.parse(jsonStr);
	} catch {
		throw new Error(m.vpnlink_decompress_failed());
	}

	const containers = data.containers;
	if (!Array.isArray(containers) || containers.length === 0) {
		throw new Error(m.vpnlink_awg_config_not_found());
	}

	// Use last container (as AmneziaVPN does)
	const container = containers[containers.length - 1];

	// Try to find AWG/WireGuard config in the container
	const proto = container.awg || container.wireguard;
	if (!proto) {
		throw new Error(m.vpnlink_container_no_config());
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
		throw new Error(m.vpnlink_no_client_config());
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
