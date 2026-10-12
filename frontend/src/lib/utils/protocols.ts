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
