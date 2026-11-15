// ---------------------------------------------------------------------------
// AWG Signature Profile Catalog
//
// Каталог профилей имитации (CONTEXT.md «Профиль имитации»). Сигнатуру
// собирает бэкенд: POST /api/signature/generate. Ключи = signature.Profiles.
//
// Суммарная ДЛИНА СТРОК I1-I5 ограничена буфером awg-tools, см.
// signature.MaxSignatureChars — там же замеры со стенда. Полезная нагрузка
// (`<r 1000>` = 1000 байт в 8 символах) на лимит не влияет.
// ---------------------------------------------------------------------------

import { m } from '$lib/i18n';

export const MAX_SIGNATURE_CHARS = 3500;

export type ProtocolKey = 'quic_initial' | 'stun' | 'dns' | 'dtls' | 'sip';

export const protocols: Record<ProtocolKey, { name: string; description: string }> = {
	quic_initial: {
		name: 'QUIC Initial',
		get description() {
			return m.protocols_desc_quic_initial();
		},
	},
	stun: {
		name: 'STUN / TURN',
		get description() {
			return m.protocols_desc_stun();
		},
	},
	dns: {
		name: 'DNS Query',
		get description() {
			return m.protocols_desc_dns();
		},
	},
	dtls: {
		name: 'DTLS (WebRTC)',
		get description() {
			return m.protocols_desc_dtls();
		},
	},
	sip: {
		name: 'SIP',
		get description() {
			return m.protocols_desc_sip();
		},
	},
};

export interface SignaturePackets {
	i1: string;
	i2: string;
	i3: string;
	i4: string;
	i5: string;
}

/** Суммарная длина строк I1-I5 — величина, которую ограничивает буфер awg-tools. */
export function calcTotalChars(packets: SignaturePackets): number {
	return (packets.i1 + packets.i2 + packets.i3 + packets.i4 + packets.i5).length;
}

export type SignatureHostPreset = 'default' | 'ru_whitelist' | 'cdn' | 'custom';

export interface SignatureGenerateOpts {
	domain?: string;
	hostPreset?: SignatureHostPreset;
	domains?: string[];
}

export function resolveSignatureGenerateOptions(
	preset: SignatureHostPreset,
	customRaw: string,
): { ok: true; opts?: SignatureGenerateOpts } | { ok: false; error: string } {
	if (preset === 'default') return { ok: true };
	if (preset === 'custom') {
		const items = customRaw
			.split(/[\s,;]+/)
			.map((s) => s.trim())
			.filter(Boolean);
		if (items.length === 0) {
			return { ok: false, error: m.protocols_custom_domain_invalid() };
		}
		return {
			ok: true,
			opts: items.length === 1 ? { hostPreset: 'custom', domain: items[0] } : { hostPreset: 'custom', domains: items },
		};
	}
	return { ok: true, opts: { hostPreset: preset } };
}

export function signatureHostPresetOptions(): {
	value: SignatureHostPreset;
	label: string;
	description: string;
}[] {
	return [
		{
			value: 'default',
			label: m.protocols_host_preset_default(),
			description: m.protocols_host_preset_default_desc(),
		},
		{
			value: 'ru_whitelist',
			label: m.protocols_host_preset_ru_whitelist(),
			description: m.protocols_host_preset_ru_whitelist_desc(),
		},
		{
			value: 'cdn',
			label: m.protocols_host_preset_cdn(),
			description: m.protocols_host_preset_cdn_desc(),
		},
		{
			value: 'custom',
			label: m.protocols_host_preset_custom(),
			description: m.protocols_host_preset_custom_desc(),
		},
	];
}
